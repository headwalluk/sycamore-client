package tunnel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// deadPid returns the pid of a process that has just exited.
func deadPid(t *testing.T) int {
	t.Helper()
	command := exec.Command("true")
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	return command.Process.Pid
}

// strayTunnel creates a tunnel's socket files for a client pid, and returns
// the control socket's path.
func strayTunnel(t *testing.T, dir, name string, pid int) string {
	t.Helper()
	base := filepath.Join(dir, "sycamore-"+name+"-"+strconv.FormatInt(int64(pid), 36)+"-2")
	for _, path := range []string{base + ".ctl", base + ".sock"} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return base + ".ctl"
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSweepStale_StopsTunnelsOfExitedClients(t *testing.T) {
	logPath := installFakeSSH(t)
	dir := t.TempDir()
	stray := strayTunnel(t, dir, "dev-host.example", deadPid(t))
	mine := strayTunnel(t, dir, "zee", os.Getpid())

	cleaned, err := SweepStale(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 1 || cleaned[0] != stray {
		t.Errorf("only the exited client's tunnel should be cleaned, got %v", cleaned)
	}
	if exists(stray) || exists(strings.TrimSuffix(stray, ".ctl")+".sock") {
		t.Error("the stray tunnel's files should be removed")
	}
	if !exists(mine) {
		t.Error("a live client's tunnel must be left alone")
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "-S "+stray+" -O exit") {
		t.Errorf("the stray master should be asked to exit, ssh saw:\n%s", log)
	}
}

func TestSweepStale_MasterAlreadyGoneJustRemovesFiles(t *testing.T) {
	installFakeSSH(t)
	t.Setenv("FAKE_SSH_EXIT_CODE", "255")
	t.Setenv("FAKE_SSH_EXIT_STDERR", "Control socket connect(/x.ctl): Connection refused")
	dir := t.TempDir()
	stray := strayTunnel(t, dir, "zee", deadPid(t))
	cleaned, err := SweepStale(context.Background(), dir)
	if err != nil || len(cleaned) != 1 || exists(stray) {
		t.Errorf("a dead master's files should just be removed: cleaned=%v err=%v", cleaned, err)
	}
}

func TestSweepStale_ReportsAMasterThatWontStop(t *testing.T) {
	installFakeSSH(t)
	t.Setenv("FAKE_SSH_EXIT_CODE", "255")
	t.Setenv("FAKE_SSH_EXIT_STDERR", "mux_client_request_session: permission denied")
	dir := t.TempDir()
	strayTunnel(t, dir, "zee", deadPid(t))
	if _, err := SweepStale(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "stop stray master") {
		t.Errorf("a master that won't stop should be reported, got %v", err)
	}
}

func TestSweepStale_IgnoresNamesItCantRead(t *testing.T) {
	installFakeSSH(t)
	dir := t.TempDir()
	odd := filepath.Join(dir, "sycamore-something.ctl")
	if err := os.WriteFile(odd, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if cleaned, err := SweepStale(context.Background(), dir); err != nil || len(cleaned) != 0 || !exists(odd) {
		t.Errorf("an unrecognised name must be left alone: cleaned=%v err=%v", cleaned, err)
	}
}

func TestOwnerPid_ReadsTheTokenFromTheEnd(t *testing.T) {
	cases := map[string]int{
		"/run/sycamore-zee-blr7-2.ctl":              541411,
		"/run/sycamore-dev-host.example-blr7-a.ctl": 541411,
		"/run/sycamore-zee.ctl":                     0,
	}
	for path, want := range cases {
		got, parsed := ownerPid(path)
		if (want == 0 && parsed) || (want != 0 && (!parsed || got != want)) {
			t.Errorf("ownerPid(%q) = %d, %v; want %d", path, got, parsed, want)
		}
	}
}
