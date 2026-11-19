package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// SweepStale stops tunnel masters left behind by client processes that have
// exited without closing them (a crash, or a kill that skipped cleanup), and
// removes their socket files. A tunnel's files are named
// sycamore-<name>-<pid>-<seq>.{ctl,sock}, with the pid in base 36 (see
// uniqueToken); a tunnel whose pid is still running, or can't be checked, is
// left alone. It returns the control sockets it cleaned. socketDir is where
// tunnels are made ("" for the private fallback; see socketDirectory).
func SweepStale(ctx context.Context, socketDir string) ([]string, error) {
	socketDir, dirErr := socketDirectory(socketDir)
	if dirErr != nil {
		return nil, dirErr
	}
	controls, err := filepath.Glob(filepath.Join(socketDir, "sycamore-*.ctl"))
	if err != nil {
		return nil, err
	}
	var cleaned []string
	var problems []error
	for _, control := range controls {
		pid, parsed := ownerPid(control)
		if !parsed || pid == os.Getpid() || !processGone(pid) {
			continue
		}
		if stopErr := stopMaster(ctx, control); stopErr != nil {
			problems = append(problems, stopErr)
			continue
		}
		cleaned = append(cleaned, control)
	}
	return cleaned, errors.Join(problems...)
}

// ownerPid reads the client pid from a control socket's name.
func ownerPid(control string) (int, bool) {
	parts := strings.Split(strings.TrimSuffix(filepath.Base(control), ".ctl"), "-")
	pid := 0
	parsed := false
	if len(parts) >= 4 {
		value, err := strconv.ParseInt(parts[len(parts)-2], 36, 64)
		pid, parsed = int(value), err == nil && value > 0
	}
	return pid, parsed
}

// processGone reports whether no process with pid exists. Anything short of
// a clear "no such process" counts as still running.
func processGone(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	signalErr := process.Signal(syscall.Signal(0))
	return errors.Is(signalErr, os.ErrProcessDone) || errors.Is(signalErr, syscall.ESRCH)
}

// stopMaster asks a stray master to exit and removes its socket files. A
// master already gone (its control socket refuses) just has its files
// removed.
func stopMaster(ctx context.Context, control string) error {
	stderr, exitErr := runSSH(ctx, []string{"-S", control, "-O", "exit", "sycamore-stale-tunnel"})
	masterGone := strings.Contains(stderr, "Connection refused") || strings.Contains(stderr, "No such file")
	var problems []error
	if exitErr != nil && !masterGone {
		problems = append(problems, fmt.Errorf("tunnel: stop stray master %s: %w (%s)", control, exitErr, strings.TrimSpace(stderr)))
	}
	for _, path := range []string{control, strings.TrimSuffix(control, ".ctl") + ".sock"} {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			problems = append(problems, removeErr)
		}
	}
	return errors.Join(problems...)
}
