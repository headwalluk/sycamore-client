package tunnel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── pure unit tests ─────────────────────────────────────────────────

func TestOpenArgs(t *testing.T) {
	cfg := Config{
		Name:         "prod",
		Host:         "prod-host.example",
		User:         "sycamore",
		Port:         2222,
		IdentityFile: "/home/me/.ssh/id_ed25519",
		RemoteSocket: "/var/sycamore/prod.sock",
		ExtraOpts:    []string{"ProxyJump=bastion"},
	}
	args := cfg.openArgs("/run/local.sock", "/run/prod.ctl")
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-M", "-S /run/prod.ctl", "-fNT",
		"-o ExitOnForwardFailure=yes",
		"-p 2222",
		"-i /home/me/.ssh/id_ed25519",
		"-o BatchMode=yes",
		"-o ConnectTimeout=10",
		"-o ProxyJump=bastion",
		"-L /run/local.sock:/var/sycamore/prod.sock",
		"sycamore@prod-host.example",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("openArgs missing %q\n  got: %s", want, joined)
		}
	}
}

func TestBatchModeAndTimeoutDefaults(t *testing.T) {
	t.Run("batch on by default", func(t *testing.T) {
		args := Config{Host: "h", RemoteSocket: "/s"}.openArgs("/l", "/c")
		if !strings.Contains(strings.Join(args, " "), "-o BatchMode=yes") {
			t.Error("expected BatchMode=yes by default")
		}
	})
	t.Run("batch suppressed when disabled", func(t *testing.T) {
		args := Config{Host: "h", RemoteSocket: "/s", DisableBatchMode: true}.openArgs("/l", "/c")
		if strings.Contains(strings.Join(args, " "), "BatchMode") {
			t.Error("expected no BatchMode flag when DisableBatchMode is set")
		}
	})
	t.Run("custom timeout", func(t *testing.T) {
		args := Config{Host: "h", RemoteSocket: "/s", ConnectTimeout: 3 * time.Second}.openArgs("/l", "/c")
		if !strings.Contains(strings.Join(args, " "), "-o ConnectTimeout=3") {
			t.Error("expected ConnectTimeout=3")
		}
	})
}

func TestDest(t *testing.T) {
	if got := (Config{Host: "h", User: "u"}).dest(); got != "u@h" {
		t.Errorf("dest = %q, want u@h", got)
	}
	if got := (Config{Host: "h"}).dest(); got != "h" {
		t.Errorf("dest = %q, want h (no user → defer to ssh_config)", got)
	}
}

func TestControlAndProbeArgs(t *testing.T) {
	cfg := Config{Host: "h", User: "u", RemoteSocket: "/var/x.sock"}
	if got := strings.Join(cfg.controlArgs("/c", "check"), " "); got != "-S /c -O check -- u@h" {
		t.Errorf("controlArgs = %q", got)
	}
	probe := cfg.socketProbeArgs("/c")
	if got := strings.Join(probe[:len(probe)-1], " "); got != "-S /c -- u@h" {
		t.Errorf("socketProbeArgs = %q", got)
	}
	if command := probe[len(probe)-1]; command != "test -S '/var/x.sock'" {
		t.Errorf("probe command = %q", command)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/var/x.sock":         "'/var/x.sock'",
		"/with space/s.sock":  "'/with space/s.sock'",
		"/x; rm -rf ~":        "'/x; rm -rf ~'",
		"/it's/here":          `'/it'\''s/here'`,
		"$(touch /tmp/owned)": "'$(touch /tmp/owned)'",
	}
	for input, want := range cases {
		if got := shellQuote(input); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOpenArgsEndOptionsBeforeDestination(t *testing.T) {
	args := Config{Host: "h", User: "u", RemoteSocket: "/s"}.openArgs("/l", "/c")
	if got := strings.Join(args[len(args)-2:], " "); got != "-- u@h" {
		t.Errorf("openArgs should end with -- u@h, got %q", got)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]Config{
		"missing host":   {RemoteSocket: "/s"},
		"missing socket": {Host: "h"},
		"bad port":       {Host: "h", RemoteSocket: "/s", Port: 70000},
		"option host":    {Host: "-oProxyCommand=x", RemoteSocket: "/s"},
		"option user":    {Host: "h", User: "-oX", RemoteSocket: "/s"},
	}
	for name, cfg := range cases {
		if err := cfg.validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	if err := (Config{Host: "h", RemoteSocket: "/s"}).validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestSocketPaths(t *testing.T) {
	t.Run("uses the socket directory and name", func(t *testing.T) {
		local, control, err := Config{Name: "prod", Host: "h", RemoteSocket: "/s", SocketDir: "/run/user/1000"}.socketPaths("")
		if err != nil {
			t.Fatal(err)
		}
		if local != "/run/user/1000/sycamore-prod.sock" {
			t.Errorf("local = %q", local)
		}
		if control != "/run/user/1000/sycamore-prod.ctl" {
			t.Errorf("control = %q", control)
		}
	})

	t.Run("falls back to sanitised host when unnamed", func(t *testing.T) {
		local, _, err := Config{Host: "dev-host.example", RemoteSocket: "/s", SocketDir: "/run/user/1000"}.socketPaths("")
		if err != nil {
			t.Fatal(err)
		}
		if local != "/run/user/1000/sycamore-dev-host.example.sock" {
			t.Errorf("local = %q", local)
		}
	})

	t.Run("token makes paths unique per tunnel", func(t *testing.T) {
		cfg := Config{Name: "prod", Host: "h", RemoteSocket: "/s", SocketDir: "/run/user/1000"}
		l1, c1, firstErr := cfg.socketPaths("123-1")
		l2, c2, secondErr := cfg.socketPaths("123-2")
		if err := errors.Join(firstErr, secondErr); err != nil {
			t.Fatal(err)
		}
		if l1 == l2 || c1 == c2 {
			t.Errorf("distinct tokens must yield distinct paths: %q/%q vs %q/%q", l1, c1, l2, c2)
		}
		if l1 != "/run/user/1000/sycamore-prod-123-1.sock" {
			t.Errorf("local = %q", l1)
		}
	})

	t.Run("rejects over-long path", func(t *testing.T) {
		if _, _, err := (Config{Name: "x", Host: "h", RemoteSocket: "/s", SocketDir: "/" + strings.Repeat("d", 120)}).socketPaths(""); err == nil {
			t.Error("expected length-cap error for an over-long socket path")
		}
	})

	t.Run("no socket directory uses a private one under the temporary one", func(t *testing.T) {
		t.Setenv("TMPDIR", shortTempDir(t))
		local, _, err := Config{Name: "prod", Host: "h", RemoteSocket: "/s"}.socketPaths("")
		if err != nil {
			t.Fatal(err)
		}
		private := filepath.Join(os.TempDir(), privateDirName())
		if local != filepath.Join(private, "sycamore-prod.sock") {
			t.Errorf("local = %q", local)
		}
		info, statErr := os.Stat(private)
		if statErr != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("private directory: %v, mode %v", statErr, info.Mode().Perm())
		}
	})

	t.Run("refuses a fallback directory open to others", func(t *testing.T) {
		t.Setenv("TMPDIR", shortTempDir(t))
		if err := os.Mkdir(filepath.Join(os.TempDir(), privateDirName()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(os.TempDir(), privateDirName()), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (Config{Name: "prod", Host: "h", RemoteSocket: "/s"}).socketPaths(""); err == nil {
			t.Error("expected a 0755 fallback directory to be refused")
		}
	})

	t.Run("refuses a symlinked fallback directory", func(t *testing.T) {
		t.Setenv("TMPDIR", shortTempDir(t))
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(os.TempDir(), privateDirName())); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (Config{Name: "prod", Host: "h", RemoteSocket: "/s"}).socketPaths(""); err == nil {
			t.Error("expected a symlinked fallback directory to be refused")
		}
	})

	t.Run("instance names are made filename-safe", func(t *testing.T) {
		local, _, err := Config{Name: "../../etc/x", Host: "h", RemoteSocket: "/s", SocketDir: "/run/user/1000"}.socketPaths("")
		if err != nil {
			t.Fatal(err)
		}
		if local != "/run/user/1000/sycamore-..-..-etc-x.sock" {
			t.Errorf("local = %q", local)
		}
	})
}

func TestUniqueToken_Distinct(t *testing.T) {
	if first, second := uniqueToken(), uniqueToken(); first == second {
		t.Error("uniqueToken should return a different value on each call")
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"prod-host.example": "prod-host.example",
		"a b/c:d":           "a-b-c-d",
		"":                  "agent",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapSSHStderr(t *testing.T) {
	cases := []struct {
		stderr string
		want   string // substring expected in the mapped message; "" means no match
	}{
		{"Permission denied (publickey).", "authentication failed"},
		{"ssh: Could not resolve hostname nope: Name or service not known", "could not resolve"},
		{"connect to host h port 22: Connection refused", "connection refused"},
		{"ssh: connect to host h port 22: Connection timed out", "timed out"},
		{"Host key verification failed.", "host-key verification"},
		{"@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@", "host-key verification"},
		{"bind: Address already in use\nchannel_setup_fwd_listener", "stale socket"},
		{"some unrecognised noise", ""},
	}
	for _, testCase := range cases {
		got := mapSSHStderr(testCase.stderr)
		if testCase.want == "" {
			if got != "" {
				t.Errorf("mapSSHStderr(%q) = %q, want no match", testCase.stderr, got)
			}
			continue
		}
		if !strings.Contains(strings.ToLower(got), testCase.want) {
			t.Errorf("mapSSHStderr(%q) = %q, want substring %q", testCase.stderr, got, testCase.want)
		}
	}
}

func TestClassifyOpenErrorFallsBackToLastLine(t *testing.T) {
	err := classifyOpenError("first line\nsomething odd happened\n", os.ErrClosed)
	if err == nil || !strings.Contains(err.Error(), "something odd happened") {
		t.Errorf("err = %v, want it to surface the last stderr line", err)
	}
}

// ── integration tests via a fake ssh on PATH ────────────────────────

// fakeSSH is a stand-in for the ssh binary. It logs its args and reacts
// to FAKE_SSH_* env vars: open code/stderr, control-check code, and the
// socket-probe exit code. On a successful master open it creates the
// control + forwarded socket files so teardown is observable.
const fakeSSH = `#!/bin/sh
echo "$@" >> "$FAKE_SSH_LOG"
args="$*"
case "$args" in
  *"-O check"*) exit "${FAKE_SSH_CHECK:-0}" ;;
  *"-O exit"*)
    if [ -n "$FAKE_SSH_EXIT_STDERR" ]; then printf '%s\n' "$FAKE_SSH_EXIT_STDERR" >&2; fi
    exit "${FAKE_SSH_EXIT_CODE:-0}" ;;
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

// installFakeSSH writes the fake ssh to a temp dir, puts it first on
// PATH, points XDG_RUNTIME_DIR at a short writable dir, and returns the
// arg-log path.
func installFakeSSH(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	sshPath := filepath.Join(dir, "ssh")
	if err := os.WriteFile(sshPath, []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	logPath = filepath.Join(dir, "args.log")
	t.Setenv("FAKE_SSH_LOG", logPath)

	// A short, writable runtime dir for the local sockets.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return logPath
}

func TestOpenAndClose(t *testing.T) {
	logPath := installFakeSSH(t)

	tun, err := Open(context.Background(), Config{
		Name: "prod", Host: "prod-host.example", User: "sycamore",
		RemoteSocket: "/var/sycamore/prod.sock",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// The master invocation should carry -M and the forward spec.
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "-M") ||
		!strings.Contains(string(log), tun.LocalSocket()+":/var/sycamore/prod.sock") {
		t.Errorf("open invocation looks wrong:\n%s", log)
	}

	// The fake created both socket files on open.
	if _, err := os.Stat(tun.LocalSocket()); err != nil {
		t.Errorf("local socket not created: %v", err)
	}

	if err := tun.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// Close ran `-O exit` and removed the local files.
	if !strings.Contains(readLog(t, logPath), "-O exit") {
		t.Error("Close did not run `-O exit`")
	}
	if _, err := os.Stat(tun.LocalSocket()); !os.IsNotExist(err) {
		t.Errorf("local socket not cleaned up: %v", err)
	}

	// Close is idempotent.
	if err := tun.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestOpenFailureMapsError(t *testing.T) {
	installFakeSSH(t)
	t.Setenv("FAKE_SSH_OPEN_CODE", "255")
	t.Setenv("FAKE_SSH_OPEN_STDERR", "Permission denied (publickey).")

	_, err := Open(context.Background(), Config{
		Name: "prod", Host: "prod-host.example", RemoteSocket: "/var/sycamore/prod.sock",
	})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("err = %v, want a mapped authentication error", err)
	}
}

func TestAlive(t *testing.T) {
	installFakeSSH(t)
	tun, err := Open(context.Background(), Config{Name: "z", Host: "h", RemoteSocket: "/s"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeChecked(t, tun)

	if !tun.Alive(context.Background()) {
		t.Error("Alive = false, want true when `-O check` exits 0")
	}

	t.Setenv("FAKE_SSH_CHECK", "255")
	if tun.Alive(context.Background()) {
		t.Error("Alive = true, want false when `-O check` exits non-zero")
	}
}

func TestRemoteSocketExists(t *testing.T) {
	installFakeSSH(t)
	tun, err := Open(context.Background(), Config{Name: "z", Host: "h", RemoteSocket: "/var/x.sock"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeChecked(t, tun)

	t.Run("found", func(t *testing.T) {
		t.Setenv("FAKE_SSH_SOCKET", "0")
		ok, err := tun.RemoteSocketExists(context.Background())
		if err != nil || !ok {
			t.Fatalf("got (%v, %v), want (true, nil)", ok, err)
		}
	})

	t.Run("absent", func(t *testing.T) {
		t.Setenv("FAKE_SSH_SOCKET", "1")
		ok, err := tun.RemoteSocketExists(context.Background())
		if err != nil || ok {
			t.Fatalf("got (%v, %v), want (false, nil)", ok, err)
		}
	})

	t.Run("ssh failure surfaces as error", func(t *testing.T) {
		t.Setenv("FAKE_SSH_SOCKET", "255")
		ok, err := tun.RemoteSocketExists(context.Background())
		if err == nil || ok {
			t.Fatalf("got (%v, %v), want (false, error)", ok, err)
		}
	})
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClassifyOpenError_PermanentFailuresCarrySentinels(t *testing.T) {
	cases := []struct {
		stderr string
		want   error
	}{
		{"user@host: Permission denied (publickey).", ErrAuthentication},
		{"Host key verification failed.", ErrHostKey},
		{"WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!", ErrHostKey},
	}
	for _, testCase := range cases {
		err := classifyOpenError(testCase.stderr, os.ErrClosed)
		if !errors.Is(err, testCase.want) {
			t.Errorf("classifyOpenError(%q) = %v, want it to match %v", testCase.stderr, err, testCase.want)
		}
	}
}

func TestClassifyOpenError_TransientFailuresCarryNoSentinel(t *testing.T) {
	for _, stderr := range []string{"ssh: connect to host h port 22: Connection refused", "Connection timed out", "something odd"} {
		err := classifyOpenError(stderr, os.ErrClosed)
		if errors.Is(err, ErrAuthentication) || errors.Is(err, ErrHostKey) {
			t.Errorf("classifyOpenError(%q) = %v should be retryable", stderr, err)
		}
	}
}

// shortTempDir makes a temporary directory with a short path, so socket
// paths under it stay inside the sun_path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "syc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			t.Errorf("remove %s: %v", dir, removeErr)
		}
	})
	return dir
}
