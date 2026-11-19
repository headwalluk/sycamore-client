// Package tunnel forwards a remote Unix socket to a local one over SSH,
// so the rest of the client can dial a local path even when the agent
// lives on another host.
//
// It shells out to the host's OpenSSH rather than reimplementing SSH in
// Go (decision #5 — see dev-notes/planning-ssh-tunnel.md), so the
// operator's ~/.ssh/config, keys, agent, known_hosts, and Host aliases
// all apply for free. A ControlMaster connection underpins the tunnel:
// Open starts it, Alive checks it, RemoteSocketExists probes the far
// side over it, and Close tears it down.
//
// This package is transport-only — it knows nothing of the chat
// protocol. The full staged "test connection" (SSH → Socket → Connect)
// composes Open + RemoteSocketExists here with a protocol-level connect
// one layer up, where the Client lives.
package tunnel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// sshBinary is the ssh executable, resolved via PATH. A package var so
// tests can point it at a stub.
var sshBinary = "ssh"

// tunnelSeq makes each Open's local/control socket paths unique within a
// process. Without it, two live tunnels to the *same* instance (e.g. an
// auto-reconnect dialing a fresh tunnel before the dropped one is torn down)
// would share paths — and closing the old one would `ssh -O exit` the new
// one's master and unlink its socket. Combined with the pid, the token is
// also unique across concurrent client processes.
var tunnelSeq atomic.Uint64

// uniqueToken returns a short per-Open token for disambiguating socket
// paths between concurrent tunnels. base36(pid) gives cross-process
// uniqueness; the process-local counter guarantees per-Open uniqueness
// within a process (the auto-reconnect case). base36 keeps it compact so it
// barely dents the sun_path budget.
func uniqueToken() string {
	pid := strconv.FormatInt(int64(os.Getpid()), 36)
	seq := strconv.FormatUint(tunnelSeq.Add(1), 36)
	return pid + "-" + seq
}

// defaultConnectTimeout bounds the initial network wait when none is set.
const defaultConnectTimeout = 10 * time.Second

// maxSocketPathLen is a conservative cap on a Unix socket path. The
// kernel's sun_path is 108 bytes on Linux and 104 on macOS (both
// including the NUL terminator); 104 stays safe on either. Both the
// forwarded socket and the control socket are subject to it.
const maxSocketPathLen = 104

// Config describes the SSH connection and the remote socket to expose.
// It maps from a config.Instance, but stays decoupled from that package
// so tunnel has no dependency on config.
type Config struct {
	// Name labels the local socket files (sycamore-<name>.sock / .ctl).
	// Falls back to a sanitised Host when empty.
	Name string

	// Host is the ssh destination — a hostname or a ~/.ssh/config Host
	// alias. Required.
	Host string

	// User is the SSH login user. Empty defers to ssh_config. This is
	// the identity the daemon authenticates via SO_PEERCRED on the
	// target host.
	User string

	// Port is the SSH port. 0 defers to ssh_config.
	Port int

	// IdentityFile is the private-key path. Empty defers to
	// ssh_config / the agent.
	IdentityFile string

	// RemoteSocket is the absolute path to sycamore.sock on the target
	// host. Required.
	RemoteSocket string

	// DisableBatchMode allows interactive password/passphrase prompts.
	// The zero value (false) keeps BatchMode=yes, so a missing key
	// fails fast instead of hanging.
	DisableBatchMode bool

	// ConnectTimeout bounds the initial connection. Zero uses
	// defaultConnectTimeout.
	ConnectTimeout time.Duration

	// SocketDir is where the local forwarded and control sockets go
	// (normally $XDG_RUNTIME_DIR, via config.RuntimeDir). Empty uses the
	// system temporary directory.
	SocketDir string

	// ExtraOpts are passed through as `-o KEY=VALUE` escape hatches.
	ExtraOpts []string
}

// Tunnel is a live SSH tunnel. LocalSocket is the path to dial; Close
// tears the tunnel down and removes the local socket files.
type Tunnel struct {
	localSocket string
	controlPath string
	cfg         Config
	closeOnce   sync.Once
}

// LocalSocket returns the local Unix socket path to dial.
func (t *Tunnel) LocalSocket() string { return t.localSocket }

// Open establishes the ControlMaster and the forward, returning once
// the local socket is bound (`-f` + ExitOnForwardFailure mean a clean
// exit implies the forward is up — no polling needed). On failure the
// ssh stderr is mapped to an operator-facing error.
func Open(ctx context.Context, cfg Config) (*Tunnel, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	local, control, err := cfg.socketPaths(uniqueToken())
	if err != nil {
		return nil, err
	}

	// Clear stale files from a prior crash — ssh refuses to bind over
	// an existing socket.
	var staleErr error
	for _, stale := range []string{local, control} {
		if removeErr := os.Remove(stale); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			staleErr = errors.Join(staleErr, fmt.Errorf("tunnel: clear stale socket %s: %w", stale, removeErr))
		}
	}
	if staleErr != nil {
		return nil, staleErr
	}

	stderr, runErr := runSSH(ctx, cfg.openArgs(local, control))
	if runErr != nil {
		return nil, classifyOpenError(stderr, runErr)
	}

	return &Tunnel{localSocket: local, controlPath: control, cfg: cfg}, nil
}

// Alive reports whether the ControlMaster is still up (`ssh -O check`).
func (t *Tunnel) Alive(ctx context.Context) bool {
	_, err := runSSH(ctx, t.cfg.controlArgs(t.controlPath, "check"))
	return err == nil
}

// RemoteSocketExists probes the far side over the master with
// `test -S <remote>`. Returns (true, nil) when the remote path is a
// socket, (false, nil) when it isn't (or is missing), and a mapped
// error when the master itself is unreachable.
//
// This is the "Socket" stage of the staged test-connection: it answers
// "is the daemon's socket actually there?" without yet attempting to
// open it as this user — that permission question is the "Connect"
// stage, one layer up.
func (t *Tunnel) RemoteSocketExists(ctx context.Context) (bool, error) {
	stderr, err := runSSH(ctx, t.cfg.socketProbeArgs(t.controlPath))
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		// `test -S` exited non-zero: the path isn't a socket. This is a
		// clean "not found", not an ssh failure.
		return false, nil
	}
	// 255 (or anything else) means ssh itself failed — master gone, etc.
	return false, classifyOpenError(stderr, err)
}

// Close tears down the ControlMaster and removes the local socket files.
// Idempotent.
func (t *Tunnel) Close() error {
	var err error
	t.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// A master that already died (the usual case after a drop) leaves
		// no control socket and needs no exit; one that's still there must
		// go, and failing to stop it is an error.
		if _, statErr := os.Stat(t.controlPath); statErr == nil {
			if stderr, exitErr := runSSH(ctx, t.cfg.controlArgs(t.controlPath, "exit")); exitErr != nil {
				err = fmt.Errorf("tunnel: stop ssh master: %w (%s)", exitErr, strings.TrimSpace(stderr))
			}
		}

		for _, path := range []string{t.localSocket, t.controlPath} {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
	})
	return err
}

// ── command construction (pure) ─────────────────────────────────────

func (cfg Config) validate() error {
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("tunnel: host is required")
	}
	if strings.TrimSpace(cfg.RemoteSocket) == "" {
		return errors.New("tunnel: remote socket is required")
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return fmt.Errorf("tunnel: port %d out of range (1-65535)", cfg.Port)
	}
	if strings.HasPrefix(cfg.Host, "-") || strings.HasPrefix(cfg.User, "-") {
		return errors.New("tunnel: host and user can't start with '-'")
	}
	return nil
}

// dest is the ssh destination argument: user@host, or just host when no
// user is configured (deferring to ssh_config).
func (cfg Config) dest() string {
	if cfg.User != "" {
		return cfg.User + "@" + cfg.Host
	}
	return cfg.Host
}

// connectionOpts are the per-connection flags used when establishing the
// master. Control commands reuse the established master, so they don't
// repeat these.
func (cfg Config) connectionOpts() []string {
	var args []string
	if cfg.Port != 0 {
		args = append(args, "-p", strconv.Itoa(cfg.Port))
	}
	if cfg.IdentityFile != "" {
		args = append(args, "-i", cfg.IdentityFile)
	}
	if !cfg.DisableBatchMode {
		args = append(args, "-o", "BatchMode=yes")
	}
	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	args = append(args, "-o", fmt.Sprintf("ConnectTimeout=%d", int(timeout.Seconds())))
	for _, o := range cfg.ExtraOpts {
		args = append(args, "-o", o)
	}
	return args
}

// openArgs builds the master+forward invocation:
//
//	ssh -M -S <ctl> -fNT -o ExitOnForwardFailure=yes [conn opts] -L <local>:<remote> -- <dest>
func (cfg Config) openArgs(local, control string) []string {
	args := []string{"-M", "-S", control, "-fNT", "-o", "ExitOnForwardFailure=yes"}
	args = append(args, cfg.connectionOpts()...)
	args = append(args, "-L", local+":"+cfg.RemoteSocket, "--", cfg.dest())
	return args
}

// controlArgs builds a control command (`-O check` / `-O exit`) against
// the established master.
func (cfg Config) controlArgs(control, op string) []string {
	return []string{"-S", control, "-O", op, "--", cfg.dest()}
}

// socketProbeArgs builds the far-side socket existence check, run over
// the master so it needs no fresh authentication. OpenSSH hands the remote
// command to the login shell as one string, so the path is quoted.
func (cfg Config) socketProbeArgs(control string) []string {
	return []string{"-S", control, "--", cfg.dest(), "test -S " + shellQuote(cfg.RemoteSocket)}
}

// shellQuote quotes text as one POSIX shell word.
func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// socketPaths derives the local data + control socket paths. They live
// under $XDG_RUNTIME_DIR (tmpfs, user-owned, auto-cleaned, and short)
// when available, falling back to a private directory under the temp dir
// (see socketDirectory). Both are length-checked
// against the sun_path limit. token, when non-empty, is appended to make
// the paths unique per live tunnel so concurrent tunnels to the same
// instance don't collide (see uniqueToken).
func (cfg Config) socketPaths(token string) (local, control string, err error) {
	base := sanitizeName(cfg.Name)
	if cfg.Name == "" {
		base = sanitizeName(cfg.Host)
	}
	if token != "" {
		base += "-" + token
	}
	dir, dirErr := socketDirectory(cfg.SocketDir)
	if dirErr != nil {
		return "", "", dirErr
	}
	local = filepath.Join(dir, "sycamore-"+base+".sock")
	control = filepath.Join(dir, "sycamore-"+base+".ctl")

	tooLong := ""
	for _, path := range []string{local, control} {
		if tooLong == "" && len(path) > maxSocketPathLen {
			tooLong = path
		}
	}
	if tooLong != "" {
		return "", "", fmt.Errorf(
			"tunnel: socket path %q exceeds %d bytes; set XDG_RUNTIME_DIR to a shorter directory",
			tooLong, maxSocketPathLen)
	}
	return local, control, nil
}

// sanitizeName reduces a host to a filesystem-safe token for the socket
// filename. Anything outside [A-Za-z0-9._-] becomes '-'.
func sanitizeName(s string) string {
	var builder strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('-')
		}
	}
	if builder.Len() == 0 {
		return "agent"
	}
	return builder.String()
}

// ── execution + error mapping ───────────────────────────────────────

// runSSH runs ssh with args, discarding stdout and capturing stderr.
func runSSH(ctx context.Context, args []string) (string, error) {
	cmd := exec.CommandContext(ctx, sshBinary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

// Sentinels for ssh failures that retrying can't fix. Match them with
// errors.Is; the returned error's text is the operator-facing message.
var (
	ErrAuthentication = errors.New("ssh authentication failed")
	ErrHostKey        = errors.New("ssh host-key verification failed")
)

// Operator-facing messages for the failures that carry a sentinel.
const (
	authFailedMessage = "SSH authentication failed — check your key / agent for this host."
	hostKeyMessage    = "SSH host-key verification failed — review ~/.ssh/known_hosts."
)

// classifiedError is a recognised ssh failure: the friendly message, plus
// the sentinel it matches (nil when retrying might help).
type classifiedError struct {
	message string
	kind    error
}

func (e *classifiedError) Error() string { return e.message }
func (e *classifiedError) Unwrap() error { return e.kind }

// classifyOpenError turns an ssh failure into an operator-facing error,
// mapping common stderr patterns and otherwise surfacing ssh's own last
// line.
func classifyOpenError(stderr string, runErr error) error {
	if msg := mapSSHStderr(stderr); msg != "" {
		var kind error
		switch msg {
		case authFailedMessage:
			kind = ErrAuthentication
		case hostKeyMessage:
			kind = ErrHostKey
		default:
			// Retrying may help (network, DNS, a stale socket).
		}
		return &classifiedError{message: msg, kind: kind}
	}
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("ssh: %s", lastLine(s))
	}
	return fmt.Errorf("ssh: %w", runErr)
}

// mapSSHStderr returns a friendly message for a recognised ssh failure,
// or "" when nothing matches. Patterns are matched case-insensitively.
// See dev-notes/planning-ssh-tunnel.md §6.
func mapSSHStderr(stderr string) string {
	lowered := strings.ToLower(stderr)
	switch {
	case strings.Contains(lowered, "permission denied") &&
		(strings.Contains(lowered, "publickey") || strings.Contains(lowered, "password")):
		return authFailedMessage
	case strings.Contains(lowered, "could not resolve hostname"):
		return "could not resolve the host — check the hostname and your network."
	case strings.Contains(lowered, "connection refused"):
		return "connection refused — is sshd running and reachable on that host/port?"
	case strings.Contains(lowered, "timed out"):
		return "connection timed out reaching the host."
	case strings.Contains(lowered, "host key verification failed"),
		strings.Contains(lowered, "remote host identification has changed"):
		return hostKeyMessage
	case strings.Contains(lowered, "address already in use"),
		strings.Contains(lowered, "cannot listen to port"),
		strings.Contains(lowered, "forwarding") && strings.Contains(lowered, "failed"):
		return "could not bind the local tunnel socket — a stale socket file may be in the way."
	default:
		return ""
	}
}

// lastLine returns the last non-empty line of ssh's output, the most
// informative part.
func lastLine(output string) string {
	last := output
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			last = trimmed
		}
	}
	return last
}
