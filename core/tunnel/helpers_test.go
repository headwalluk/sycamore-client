package tunnel

// Test helpers for handling errors in tests rather than discarding them.

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
)

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
