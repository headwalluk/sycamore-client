package core

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/headwalluk/sycamore-client/core/config"
)

func TestTunnelConfigMapping(t *testing.T) {
	inst := config.Instance{
		Name: "prod", Host: "prod-host.example", User: "sycamore",
		Port: 2222, Identity: "/home/me/.ssh/id_ed25519",
		Socket: "/var/sycamore/prod.sock",
	}
	tc := tunnelConfig(inst)
	if tc.Name != "prod" || tc.Host != "prod-host.example" || tc.User != "sycamore" ||
		tc.Port != 2222 || tc.IdentityFile != "/home/me/.ssh/id_ed25519" ||
		tc.RemoteSocket != "/var/sycamore/prod.sock" {
		t.Errorf("tunnelConfig mapping wrong: %+v", tc)
	}
}

// unixServer starts a one-shot Unix socket listener that, on the first
// connection, optionally writes line (newline appended) and then either
// closes immediately or holds the connection open until cleanup.
func unixServer(t *testing.T, line string, holdOpen bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeChecked(t, ln) })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			if !alreadyClosed(err) {
				t.Errorf("fake sshd accept: %v", err)
			}
			return
		}
		if line != "" {
			writeTo(t, conn, []byte(line+"\n"))
		}
		if holdOpen {
			// Block until the listener is closed at cleanup.
			buf := make([]byte, 1)
			if _, readErr := conn.Read(buf); readErr != nil && !alreadyClosed(readErr) {
				t.Errorf("fake sshd read: %v", readErr)
			}
		} else {
			closeChecked(t, conn)
		}
	}()
	return path
}

func TestProbeConnect(t *testing.T) {
	t.Run("welcome → ok", func(t *testing.T) {
		path := unixServer(t, `{"type":"welcome","principal":"Paul","principal_id":"p1","conversation_id":null,"conversation_title":null}`, false)
		st := probeConnect(context.Background(), path)
		if !st.OK || !strings.Contains(st.Detail, "Paul") {
			t.Errorf("stage = %+v, want OK with principal", st)
		}
	})

	t.Run("error → rejected with message", func(t *testing.T) {
		path := unixServer(t, `{"type":"error","message":"Connection rejected: no contact"}`, false)
		st := probeConnect(context.Background(), path)
		if st.OK || !strings.Contains(st.Detail, "no contact") {
			t.Errorf("stage = %+v, want rejection carrying the daemon message", st)
		}
	})

	t.Run("immediate close → permission hint", func(t *testing.T) {
		path := unixServer(t, "", false) // accept then close, no bytes
		st := probeConnect(context.Background(), path)
		if st.OK || !strings.Contains(st.Detail, "permission") {
			t.Errorf("stage = %+v, want the EACCES/permission hint", st)
		}
	})

	t.Run("silence → timeout", func(t *testing.T) {
		path := unixServer(t, "", true) // accept, write nothing, hold open
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		st := probeConnect(ctx, path)
		if st.OK || !strings.Contains(st.Detail, "timed out") {
			t.Errorf("stage = %+v, want a timeout", st)
		}
	})

	t.Run("dial failure → reported", func(t *testing.T) {
		st := probeConnect(context.Background(), filepath.Join(t.TempDir(), "nope.sock"))
		if st.OK || !strings.Contains(st.Detail, "could not open") {
			t.Errorf("stage = %+v, want a dial failure", st)
		}
	})
}

// ── ProbeInstance / DialInstance via fake ssh ───────────────────────

const fakeSSH = `#!/bin/sh
echo "$@" >> "$FAKE_SSH_LOG"
args="$*"
case "$args" in
  *"-O check"*) exit "${FAKE_SSH_CHECK:-0}" ;;
  *"-O exit"*) exit 0 ;;
  *" test -S "*) exit "${FAKE_SSH_SOCKET:-0}" ;;
  *"-M "*)
    if [ -n "$FAKE_SSH_OPEN_STDERR" ]; then printf '%s\n' "$FAKE_SSH_OPEN_STDERR" >&2; fi
    code="${FAKE_SSH_OPEN_CODE:-0}"
    if [ "$code" -eq 0 ]; then
      prev=""
      for a in "$@"; do
        case "$prev" in
          -S) ctl="$a" ;;
          -L) fwd="$a" ;;
        esac
        prev="$a"
      done
      [ -n "$ctl" ] && : > "$ctl"
      [ -n "$fwd" ] && : > "${fwd%%:*}"
    fi
    exit "$code" ;;
  *) exit 0 ;;
esac
`

func installFakeSSH(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	logPath = filepath.Join(dir, "args.log")
	t.Setenv("FAKE_SSH_LOG", logPath)
	// Use a SHORT runtime dir: t.TempDir() embeds the (long) test name, which
	// can push the derived socket path past the sun_path cap once the unique
	// per-tunnel token is appended.
	rt, err := os.MkdirTemp("", "syrt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(rt); err != nil {
			t.Errorf("remove the test runtime dir: %v", err)
		}
	})
	t.Setenv("XDG_RUNTIME_DIR", rt)
	return logPath
}

func TestProbeInstanceStaging(t *testing.T) {
	inst := config.Instance{Name: "prod", Host: "prod-host.example", Socket: "/var/sycamore/prod.sock"}

	t.Run("ssh stage fails → later stages not attempted", func(t *testing.T) {
		installFakeSSH(t)
		t.Setenv("FAKE_SSH_OPEN_CODE", "255")
		t.Setenv("FAKE_SSH_OPEN_STDERR", "Permission denied (publickey).")

		res := ProbeInstance(context.Background(), inst)
		if res.SSH.OK || !res.SSH.Attempted {
			t.Errorf("SSH = %+v, want attempted+failed", res.SSH)
		}
		if res.Socket.Attempted || res.Connect.Attempted {
			t.Error("later stages should not be attempted after SSH fails")
		}
		if !strings.Contains(res.SSH.Detail, "authentication failed") {
			t.Errorf("SSH detail = %q, want mapped auth error", res.SSH.Detail)
		}
	})

	t.Run("socket absent → connect not attempted", func(t *testing.T) {
		installFakeSSH(t)
		t.Setenv("FAKE_SSH_SOCKET", "1") // test -S → false

		res := ProbeInstance(context.Background(), inst)
		if !res.SSH.OK {
			t.Fatalf("SSH = %+v, want OK", res.SSH)
		}
		if res.Socket.OK || !res.Socket.Attempted {
			t.Errorf("Socket = %+v, want attempted+not-found", res.Socket)
		}
		if res.Connect.Attempted {
			t.Error("Connect should not be attempted when the socket is absent")
		}
	})

	t.Run("socket present → connect attempted", func(t *testing.T) {
		installFakeSSH(t)
		t.Setenv("FAKE_SSH_SOCKET", "0") // test -S → true

		res := ProbeInstance(context.Background(), inst)
		if !res.SSH.OK || !res.Socket.OK {
			t.Fatalf("SSH=%+v Socket=%+v, want both OK", res.SSH, res.Socket)
		}
		// The fake forwards to a plain file, not a real socket, so the
		// Connect dial fails — but the staging reached it, which is what
		// this test pins. The happy Connect path is covered by
		// TestProbeConnect against a real listener.
		if !res.Connect.Attempted {
			t.Error("Connect should be attempted once the socket is found")
		}
	})
}

func TestDialInstanceTearsDownTunnelOnConnectFailure(t *testing.T) {
	logPath := installFakeSSH(t)
	// Tunnel opens fine, but the fake forwards to a plain file, so the
	// client dial fails — DialInstance must close the tunnel.
	_, err := DialInstance(context.Background(),
		config.Instance{Name: "prod", Host: "h", Socket: "/var/sycamore/prod.sock"})
	if err == nil {
		t.Fatal("expected DialInstance to fail when the forwarded socket isn't dialable")
	}
	if !strings.Contains(readFile(t, logPath), "-O exit") {
		t.Error("DialInstance did not tear the tunnel down (`-O exit`) on failure")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
