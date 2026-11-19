package main

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

func TestLoopReturns_FindsReturnsInLoopsButNotInClosures(t *testing.T) {
	source := `package sample

func first(values []int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func counted(limit int) {
	for index := 0; index < limit; index++ {
		visit := func() bool { return index%2 == 0 }
		_ = visit
	}
}

func fine() int {
	return 1
}
`
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := loopReturns(token.NewFileSet(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Line != 6 {
		t.Errorf("want exactly the return on line 6, got %v", found)
	}
}
