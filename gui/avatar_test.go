package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// pngAvatar returns a tiny PNG-signed image and its digest.
func pngAvatar() ([]byte, string) {
	image := []byte("\x89PNG\r\n\x1a\nrest-of-image")
	digest := sha256.Sum256(image)
	return image, hex.EncodeToString(digest[:])
}

func TestValidDigest(t *testing.T) {
	_, good := pngAvatar()
	cases := map[string]bool{
		good:                          true,
		strings.ToUpper(good):         false,
		good[:63]:                     false,
		"../../../../etc/passwd":      false,
		good[:62] + "/x":              false,
		"":                            false,
		strings.Repeat("g", 64):       false,
		"/" + strings.Repeat("a", 63): false,
	}
	for digest, want := range cases {
		if got := validDigest(digest); got != want {
			t.Errorf("validDigest(%q) = %v, want %v", digest, got, want)
		}
	}
}

func TestLoadCachedAvatar(t *testing.T) {
	image, digest := pngAvatar()

	t.Run("reads a valid cached image", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, digest), image, 0o600); err != nil {
			t.Fatal(err)
		}
		uri, cached, err := loadCachedAvatar(dir, digest)
		if err != nil || !cached || !strings.HasPrefix(uri, "data:image/png;base64,") {
			t.Fatalf("got (%q, %v, %v)", uri, cached, err)
		}
	})

	t.Run("never builds a path from a malformed digest", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(filepath.Dir(dir), "outside")
		if err := os.WriteFile(outside, image, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, cached, err := loadCachedAvatar(dir, "../outside"); cached || err != nil {
			t.Fatalf("got (%v, %v), want not cached", cached, err)
		}
	})

	t.Run("refuses a symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "image")
		if err := os.WriteFile(target, image, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, digest)); err != nil {
			t.Fatal(err)
		}
		if _, cached, err := loadCachedAvatar(dir, digest); cached || err == nil {
			t.Fatalf("got (%v, %v), want an error", cached, err)
		}
	})

	t.Run("refuses a FIFO without blocking", func(t *testing.T) {
		dir := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(dir, digest), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, cached, err := loadCachedAvatar(dir, digest); cached || err == nil {
			t.Fatalf("got (%v, %v), want an error", cached, err)
		}
	})

	t.Run("refuses an oversized file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, digest), make([]byte, maxAvatarBytes+10), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, cached, err := loadCachedAvatar(dir, digest); cached || err == nil || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("got (%v, %v), want a size error", cached, err)
		}
	})
}
