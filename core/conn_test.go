package core

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// pipePair returns a Conn (client side) paired with a raw net.Conn
// (server side) over net.Pipe. The server side is intentionally raw
// so tests can write hand-crafted bytes directly.
func pipePair(t *testing.T) (*Conn, net.Conn) {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() {
		closeChecked(t, srv)
		closeChecked(t, cli)
	})
	return newConn(cli), srv
}

// writeLine writes one newline-terminated JSON line to srv. net.Pipe
// is synchronous (writes block until read), so callers run this in a
// goroutine — except when followed immediately by Close, in which
// case the server-side write will still succeed once the client side
// reads.
func writeLine(t *testing.T, srv net.Conn, line string) {
	t.Helper()
	if _, err := srv.Write([]byte(line + "\n")); err != nil {
		t.Errorf("server write: %v", err)
	}
}

func TestConn_RecvWelcome(t *testing.T) {
	conn, srv := pipePair(t)

	go writeLine(t, srv, `{"type":"welcome","principal":"Test User","principal_id":"01H_PRINCIPAL","conversation_id":null,"conversation_title":null}`)

	kind, raw, err := conn.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if kind != protocol.TypeWelcome {
		t.Fatalf("kind: want %q, got %q", protocol.TypeWelcome, kind)
	}

	var welcome protocol.Welcome
	if err := json.Unmarshal(raw, &welcome); err != nil {
		t.Fatalf("unmarshal welcome: %v", err)
	}
	if welcome.Principal != "Test User" {
		t.Errorf("Principal: want %q, got %q", "Test User", welcome.Principal)
	}
	if welcome.PrincipalID != "01H_PRINCIPAL" {
		t.Errorf("PrincipalID: want %q, got %q", "01H_PRINCIPAL", welcome.PrincipalID)
	}
	if welcome.ConversationID != nil {
		t.Errorf("ConversationID: want nil, got %v", *welcome.ConversationID)
	}
}

func TestConn_RecvResponse(t *testing.T) {
	conn, srv := pipePair(t)

	line := `{"type":"response","conversation_id":"conv_1","turn_id":"turn_42","content":[{"type":"text","text":"Stockholm is 4°C."}],"stop_reason":"end_turn","model":"sonnet","provider":"anthropic","usage":{"inputTokens":12,"outputTokens":7},"elapsed_ms":1234}`
	go writeLine(t, srv, line)

	kind, raw, err := conn.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if kind != protocol.TypeResponse {
		t.Fatalf("kind: want %q, got %q", protocol.TypeResponse, kind)
	}

	var response protocol.Response
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.ConversationID != "conv_1" || response.TurnID != "turn_42" {
		t.Errorf("ids: got conv=%q turn=%q", response.ConversationID, response.TurnID)
	}
	if len(response.Content) != 1 || response.Content[0].Type != "text" || response.Content[0].Text != "Stockholm is 4°C." {
		t.Errorf("content: %+v", response.Content)
	}
	if response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 7 {
		t.Errorf("usage: %+v", response.Usage)
	}
}

func TestConn_RecvSkipsBlankLines(t *testing.T) {
	conn, srv := pipePair(t)

	go func() {
		writeLine(t, srv, "")
		writeLine(t, srv, "")
		writeLine(t, srv, `{"type":"pong"}`)
	}()

	kind, _, err := conn.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if kind != protocol.TypePong {
		t.Errorf("kind: want %q, got %q", protocol.TypePong, kind)
	}
}

func TestConn_RecvMalformedJSON(t *testing.T) {
	conn, srv := pipePair(t)

	go writeLine(t, srv, `{not valid json`)

	_, _, err := conn.Recv()
	if err == nil {
		t.Fatal("Recv: want error, got nil")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error: want substring \"malformed\", got %q", err.Error())
	}
}

func TestConn_RecvEOF(t *testing.T) {
	conn, srv := pipePair(t)

	go func() { closeChecked(t, srv) }()

	_, _, err := conn.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Recv: want io.EOF, got %v", err)
	}
}

func TestConn_Send(t *testing.T) {
	conn, srv := pipePair(t)

	// Read what the client writes on the server side.
	type result struct {
		line string
		err  error
	}
	out := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if !sc.Scan() {
			out <- result{err: sc.Err()}
			return
		}
		out <- result{line: sc.Text()}
	}()

	msg := protocol.ClientMessage{
		Type:    protocol.TypeChat,
		Message: "hello",
	}
	if err := conn.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-out:
		if got.err != nil {
			t.Fatalf("server read: %v", got.err)
		}
		var decoded protocol.ClientMessage
		if err := json.Unmarshal([]byte(got.line), &decoded); err != nil {
			t.Fatalf("server unmarshal: %v", err)
		}
		if decoded.Type != protocol.TypeChat || decoded.Message != "hello" {
			t.Errorf("decoded: %+v", decoded)
		}
		// omitempty should keep optional fields out of the wire form.
		if strings.Contains(got.line, "conversation_id") {
			t.Errorf("unexpected conversation_id in wire form: %s", got.line)
		}
	case <-time.After(time.Second):
		t.Fatal("server-side read timed out")
	}
}

func TestConn_RoundTrip(t *testing.T) {
	conn, srv := pipePair(t)

	// Server: read one ClientMessage, then write one Response.
	done := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if !sc.Scan() {
			done <- sc.Err()
			return
		}
		var cm protocol.ClientMessage
		if err := json.Unmarshal(sc.Bytes(), &cm); err != nil {
			done <- err
			return
		}
		if cm.Type != protocol.TypeChat {
			done <- errors.New("server: expected chat")
			return
		}
		resp := `{"type":"response","conversation_id":"c","turn_id":"t","content":[{"type":"text","text":"ack: ` + cm.Message + `"}],"stop_reason":null,"model":"m","provider":"p","usage":{"inputTokens":1,"outputTokens":1},"elapsed_ms":1}`
		if _, err := srv.Write([]byte(resp + "\n")); err != nil {
			done <- err
			return
		}
		done <- nil
	}()

	if err := conn.Send(protocol.ClientMessage{Type: protocol.TypeChat, Message: "ping"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	kind, raw, err := conn.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if kind != protocol.TypeResponse {
		t.Fatalf("kind: want response, got %q", kind)
	}

	var r protocol.Response
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Content) != 1 || r.Content[0].Text != "ack: ping" {
		t.Errorf("response content: %+v", r.Content)
	}

	if err := <-done; err != nil {
		t.Fatalf("server goroutine: %v", err)
	}
}

func TestConn_RecvLargeLine(t *testing.T) {
	// Verify the 8 MB buffer ceiling — anything over the default 64 KB
	// would fail with bufio.ErrTooLong without the explicit Buffer call
	// in newConn. Use a 256 KB text block to comfortably clear 64 KB.
	conn, srv := pipePair(t)

	big := strings.Repeat("x", 256*1024)
	line := `{"type":"response","conversation_id":"c","turn_id":"t","content":[{"type":"text","text":"` + big + `"}],"stop_reason":null,"model":"m","provider":"p","usage":{"inputTokens":0,"outputTokens":0},"elapsed_ms":0}`

	go writeLine(t, srv, line)

	kind, raw, err := conn.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if kind != protocol.TypeResponse {
		t.Fatalf("kind: want response, got %q", kind)
	}

	var r protocol.Response
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Content) != 1 || len(r.Content[0].Text) != len(big) {
		t.Errorf("content length: want %d, got %d", len(big), len(r.Content[0].Text))
	}
}

func TestConnSend_TimesOutWhenThePeerStopsReading(t *testing.T) {
	original := sendTimeout
	sendTimeout = 20 * time.Millisecond
	t.Cleanup(func() { sendTimeout = original })

	client, server := net.Pipe() // unbuffered: with no reader, a write waits
	defer closeChecked(t, server)
	conn := newConn(client)
	defer closeChecked(t, conn)

	result := make(chan error, 1)
	go func() { result <- conn.Send(protocol.ClientMessage{Type: protocol.TypePing}) }()
	select {
	case err := <-result:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("send should fail on its deadline, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("send blocked past its deadline")
	}
}
