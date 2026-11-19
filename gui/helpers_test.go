package main

// Test helpers for handling errors in tests rather than discarding them.

import "testing"

// must fails the test on a setup error.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
