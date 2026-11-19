package core

// Test helpers for handling errors in tests rather than discarding them.

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
)

// must fails the test on a setup error.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// writeTo writes to a test peer, reporting a failure. Safe from a
// goroutine; a peer already closed at cleanup is not a failure.
func writeTo(t *testing.T, peer io.Writer, data []byte) {
	t.Helper()
	if _, err := peer.Write(data); err != nil && !alreadyClosed(err) {
		t.Errorf("write to the test peer: %v", err)
	}
}

// closeChecked closes a test resource, reporting a failure; one already
// closed is fine.
func closeChecked(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil && !alreadyClosed(err) {
		t.Errorf("close: %v", err)
	}
}

// alreadyClosed reports whether err only says the resource was closed.
func alreadyClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.EOF)
}
