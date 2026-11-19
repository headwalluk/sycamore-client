// Package core is the shared client logic for the Sycamore AI agent:
// socket dialing, the message protocol, and SSH tunnelling. It contains
// no UI code so it can be imported by both the TUI and the GUI.
//
// Conn is the low-level wire primitive: dial the socket, Send one
// ClientMessage, Recv one server message at a time. The session-aware
// Client (which owns the read goroutine, tracks principal_id and
// conversation_id, and exposes a typed channel of decoded messages)
// is layered on top of Conn — see M2.
package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// recvBufferMax caps the bufio.Scanner buffer at 8 MB. The default
// 64 KB truncates any meaningful image-bearing response — see the
// daemon cookbook §9. Anything larger than 8 MB is almost certainly
// a bug.
const recvBufferMax = 8 * 1024 * 1024

// sendTimeout bounds one write, so a peer that stops reading fails the send
// instead of blocking it. A variable so tests can shorten it.
var sendTimeout = 30 * time.Second

// Conn is a single connection to a Sycamore agent's chat socket. It
// wraps a net.Conn and a line-buffered scanner sized for image-bearing
// responses. It is not safe for concurrent Send or concurrent Recv;
// callers expecting parallel I/O should serialise each direction or
// (more commonly) wrap a Conn in a higher-level Client that owns the
// read goroutine.
type Conn struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

// Dial opens a Unix-domain socket connection to the agent. The socket
// is a filesystem path on Linux/macOS and an AF_UNIX path on Windows
// (supported natively since Windows 10 1803).
func Dial(ctx context.Context, socketPath string) (*Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}
	return newConn(conn), nil
}

// newConn wraps an arbitrary net.Conn. Exposed via Dial for production
// use; tests use it directly with net.Pipe().
func newConn(c net.Conn) *Conn {
	scanner := bufio.NewScanner(c)
	scanner.Buffer(make([]byte, 0, 64*1024), recvBufferMax)
	return &Conn{conn: c, scanner: scanner}
}

// Send marshals msg and writes it as one newline-delimited JSON line.
// One Send produces exactly one line on the wire; the trailing newline
// is added automatically.
func (c *Conn) Send(msg protocol.ClientMessage) error {
	encoded, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal client message: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := c.conn.SetWriteDeadline(time.Now().Add(sendTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := c.conn.Write(encoded); err != nil {
		return fmt.Errorf("write client message: %w", err)
	}
	return nil
}

// Recv reads the next server message from the socket. It returns the
// "type" discriminator and the raw JSON line so the caller can
// unmarshal into the appropriate protocol.* struct. Blank lines are
// skipped. When the peer closes the connection cleanly, Recv returns
// io.EOF.
func (c *Conn) Recv() (string, json.RawMessage, error) {
	var line []byte
	scanned := true
	for scanned && len(line) == 0 {
		scanned = c.scanner.Scan()
		if scanned {
			line = c.scanner.Bytes()
		}
	}
	if !scanned {
		err := io.EOF
		if scanErr := c.scanner.Err(); scanErr != nil {
			err = fmt.Errorf("scan server message: %w", scanErr)
		}
		return "", nil, err
	}

	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return "", nil, fmt.Errorf("malformed server message: %w", err)
	}

	// scanner.Bytes() is invalidated by the next Scan, so copy.
	raw := make(json.RawMessage, len(line))
	copy(raw, line)
	return head.Type, raw, nil
}

// Close shuts down the connection. Subsequent Send / Recv calls will
// return an error.
func (c *Conn) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
