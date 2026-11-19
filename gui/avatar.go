package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// Agent avatars: fetched once per image (keyed by the digest the welcome
// announces), checked, cached on disk, and handed to the frontend as data
// URIs. See docs/sycamore-protocol/server-messages.md#avatar_image in the
// daemon repo.

// maxAvatarBytes is the protocol's size limit for an avatar.
const maxAvatarBytes = 256 * 1024

// avatarCacheDir returns where avatars are cached (under the user's cache
// directory, e.g. ~/.cache/sycamore-client/avatars).
func avatarCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sycamore-client", "avatars"), nil
}

// sniffImage returns an image's MIME type from its first bytes, and false
// for anything but PNG, JPEG or WebP.
func sniffImage(image []byte) (string, bool) {
	mime := ""
	switch {
	case bytes.HasPrefix(image, []byte("\x89PNG\r\n\x1a\n")):
		mime = "image/png"
	case bytes.HasPrefix(image, []byte{0xFF, 0xD8, 0xFF}):
		mime = "image/jpeg"
	case len(image) >= 12 && bytes.Equal(image[0:4], []byte("RIFF")) && bytes.Equal(image[8:12], []byte("WEBP")):
		mime = "image/webp"
	default:
		// Not an image type avatars may use.
	}
	return mime, mime != ""
}

// checkAvatar validates an image against the digest it should have, and
// returns it as a data URI.
func checkAvatar(image []byte, wantSHA256 string) (string, error) {
	if len(image) > maxAvatarBytes {
		return "", fmt.Errorf("%d bytes is over the %d-byte limit", len(image), maxAvatarBytes)
	}
	digest := sha256.Sum256(image)
	if hex.EncodeToString(digest[:]) != wantSHA256 {
		return "", errors.New("its sha256 doesn't match the one the agent announced")
	}
	mime, known := sniffImage(image)
	if !known {
		return "", errors.New("it isn't a PNG, JPEG or WebP image")
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(image), nil
}

// validDigest reports whether sha is a lowercase hex SHA-256, and so safe
// as a cache filename.
func validDigest(sha string) bool {
	valid := len(sha) == sha256.Size*2
	for _, character := range sha {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			valid = false
			break
		}
	}
	return valid
}

// loadCachedAvatar returns a cached avatar's data URI, and false when it
// isn't cached (or there's no cache). A cached file that can't be read or
// fails its check comes back as an error, so it can be fetched again.
func loadCachedAvatar(cacheDir, sha string) (string, bool, error) {
	if cacheDir == "" || !validDigest(sha) {
		return "", false, nil
	}
	image, err := readCachedFile(filepath.Join(cacheDir, sha))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", false, nil
	case err != nil:
		return "", false, err
	default:
		uri, checkErr := checkAvatar(image, sha)
		return uri, checkErr == nil, checkErr
	}
}

// readCachedFile reads a regular file of at most maxAvatarBytes. Lstat comes
// first so a symlink, FIFO or device is refused before it's opened.
func readCachedFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("the cached copy isn't a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	image, readErr := readOpenedFile(file, before)
	return image, errors.Join(readErr, file.Close())
}

// readOpenedFile reads an opened cache file, checking it's still the file
// that was inspected and within the size limit.
func readOpenedFile(file *os.File, before os.FileInfo) ([]byte, error) {
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) {
		return nil, errors.New("the cached copy changed while it was opened")
	}
	image, err := io.ReadAll(io.LimitReader(file, maxAvatarBytes+1))
	if err == nil && len(image) > maxAvatarBytes {
		err = fmt.Errorf("the cached copy is over the %d-byte limit", maxAvatarBytes)
	}
	return image, err
}

// handleWelcomeAvatar shows a cached avatar for the agent's announced
// digest, or asks the agent for the image.
func (a *App) handleWelcomeAvatar(agent string, welcome *protocol.Welcome) {
	profile := welcome.Agent
	if profile == nil || profile.Avatar == nil {
		return
	}
	sha := profile.Avatar.SHA256
	if !validDigest(sha) {
		a.emitNote(agent, describeAgentError("ignored", errors.New("the announced digest isn't a SHA-256")))
		return
	}
	a.mu.Lock()
	cacheDir, dirErr := a.avatarDir, a.avatarDirErr
	a.avatarDirErr = nil // said once
	a.mu.Unlock()
	if dirErr != nil {
		a.emitNote(agent, describeAgentError("no cache directory, so it's fetched each time", dirErr))
	}
	uri, cached, err := loadCachedAvatar(cacheDir, sha)
	if err != nil {
		a.emitNote(agent, describeAgentError("the cached copy is unusable, fetching it again", err))
	}
	if cached {
		a.setAvatar(agent, uri)
		return
	}
	a.mu.Lock()
	a.avatarWanted[agent] = sha
	a.mu.Unlock()
	if err := a.pool.Send(agent, protocol.ClientMessage{Type: protocol.TypeAvatar}); err != nil {
		a.emitNote(agent, describeAgentError("couldn't request it", err))
	}
}

// handleAvatarImage checks an avatar the agent sent, caches it, and shows it.
func (a *App) handleAvatarImage(agent string, reply *protocol.AvatarImage) {
	a.mu.Lock()
	want := a.avatarWanted[agent]
	delete(a.avatarWanted, agent)
	cacheDir := a.avatarDir
	a.mu.Unlock()
	if reply.Avatar == nil || want == "" {
		return // none configured, or not asked for
	}
	if reply.Avatar.SHA256 != want {
		a.emitNote(agent, describeAgentError("discarded", errors.New("the image isn't the one the welcome announced")))
		return
	}
	image, err := base64.StdEncoding.DecodeString(reply.Avatar.Data)
	if err != nil {
		a.emitNote(agent, describeAgentError("discarded", err))
		return
	}
	uri, err := checkAvatar(image, want)
	if err != nil {
		a.emitNote(agent, describeAgentError("discarded", err))
		return
	}
	a.setAvatar(agent, uri)
	if cacheDir == "" {
		return
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		a.emitNote(agent, describeAgentError("couldn't cache it", err))
		return
	}
	if err := os.WriteFile(filepath.Join(cacheDir, want), image, 0o600); err != nil {
		a.emitNote(agent, describeAgentError("couldn't cache it", err))
	}
}

// setAvatar records an agent's avatar and refreshes the sidebar.
func (a *App) setAvatar(agent, uri string) {
	a.mu.Lock()
	a.avatars[agent] = uri
	a.mu.Unlock()
	a.emitAgents()
}

// emitNote adds a note to the transcript of the conversation an agent shows.
func (a *App) emitNote(agent, text string) {
	a.mu.Lock()
	convID := ""
	if status, open := a.agents[agent]; open {
		convID = status.convID
	}
	a.mu.Unlock()
	a.emit(eventEntry, Entry{Agent: agent, ConversationID: convID, Role: "note", Kind: "note", Text: text})
}
