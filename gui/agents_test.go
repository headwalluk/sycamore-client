package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// infoFor returns one agent's sidebar row.
func infoFor(app *App, name string) AgentInfo {
	var found AgentInfo
	for _, info := range app.Agents() {
		if info.Name == name {
			found = info
		}
	}
	return found
}

func TestSidebar_AttentionStates(t *testing.T) {
	app, pool, _ := v2App(t)
	if info := infoFor(app, "coop"); info.Mark != markDisconnected || info.Label != "disconnected" || info.Open {
		t.Errorf("coop isn't open yet: %+v", info)
	}

	pool.states["zee"] = core.StateOnline
	if info := infoFor(app, "zee"); info.Mark != markIdle || !info.Active {
		t.Errorf("zee is on screen and idle: %+v", info)
	}

	must(t, app.SendChat("zee", "job"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Progress{ConversationID: "C_A", TurnID: "T_1", Round: 3, Kind: protocol.ProgressToolStart, ToolName: "shell_exec"}})
	if info := infoFor(app, "zee"); info.Mark != markWorking || info.Label != "working · shell_exec · round 3" || !info.Working {
		t.Errorf("zee is working on a tool: %+v", info)
	}

	pool.states["zee"] = core.StateReconnecting
	if info := infoFor(app, "zee"); info.Mark != markConnecting || info.Label != "reconnecting" {
		t.Errorf("being down outranks working: %+v", info)
	}
	pool.states["zee"] = core.StateFailed
	if info := infoFor(app, "zee"); info.Mark != markFailed {
		t.Errorf("failed outranks everything: %+v", info)
	}
}

func TestSidebar_UnreadAndErrorOnABackgroundAgent(t *testing.T) {
	app, pool, _ := v2App(t)
	pool.states["zee"] = core.StateOnline
	must(t, app.Connect("coop"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Response{ConversationID: "C_A"}})
	if info := infoFor(app, "zee"); info.Mark != markUnread || info.Unread != 1 {
		t.Errorf("zee has a reply waiting: %+v", info)
	}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Error{Message: "boom"}})
	if info := infoFor(app, "zee"); info.Mark != markError {
		t.Errorf("an unseen error outranks unread: %+v", info)
	}
	must(t, app.Connect("zee"))
	if info := infoFor(app, "zee"); info.Mark != markIdle || info.Unread != 0 {
		t.Errorf("showing zee clears both: %+v", info)
	}
}

func TestSidebar_ErrorOnScreenIsNotFlagged(t *testing.T) {
	app, pool, _ := v2App(t)
	pool.states["zee"] = core.StateOnline
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Error{Message: "boom"}})
	if info := infoFor(app, "zee"); info.Mark == markError {
		t.Errorf("an error you're looking at isn't waiting: %+v", info)
	}
}

func TestSidebar_ListArrivesWithChanges(t *testing.T) {
	app, _, sent := v2App(t)
	must(t, app.Connect("coop"))
	infos, found := last[[]AgentInfo](*sent, eventAgents)
	if !found || len(infos) != 2 {
		t.Fatalf("a focus change should send the agent list, got %+v", infos)
	}
	for _, info := range infos {
		if info.Name == "coop" && (!info.Active || !info.Open) {
			t.Errorf("coop is open and on screen: %+v", info)
		}
	}
}

func TestNextUnread_GoesToTheReplyWaiting(t *testing.T) {
	app, pool, _ := v2App(t)
	must(t, app.Connect("coop"))
	app.agents["zee"].unread["C_B"] = true
	app.agents["zee"].titles["C_B"] = "Bravo"
	moved, err := app.NextUnread()
	if err != nil || !moved {
		t.Fatalf("NextUnread: moved=%v err=%v", moved, err)
	}
	if app.active != "zee" || app.agents["zee"].convID != "C_B" {
		t.Errorf("should show zee's Bravo, active=%q conv=%q", app.active, app.agents["zee"].convID)
	}
	if session, _ := pool.Session("zee"); session.ConversationID != "C_B" {
		t.Errorf("the session should follow, got %q", session.ConversationID)
	}
	moved, err = app.NextUnread()
	must(t, err)
	if moved {
		t.Error("with nothing waiting, NextUnread should report so")
	}
}

func TestNextUnread_AnErrorOnAnotherAgent(t *testing.T) {
	app, _, _ := v2App(t)
	must(t, app.Connect("coop"))
	must(t, app.Connect("zee"))
	app.agents["coop"].errorUnseen = true
	moved, err := app.NextUnread()
	must(t, err)
	if !moved || app.active != "coop" {
		t.Errorf("an unseen error on coop should be the next stop, active=%q", app.active)
	}
}

// fakePNG is enough of a PNG to pass the format check.
var fakePNG = []byte("\x89PNG\r\n\x1a\n" + "rest of the image")

func digestOf(image []byte) string {
	digest := sha256.Sum256(image)
	return hex.EncodeToString(digest[:])
}

// welcomeWithAvatar is a protocol 3 welcome announcing an avatar.
func welcomeWithAvatar(sha string) *protocol.Welcome {
	conv := "C_A"
	return &protocol.Welcome{ProtocolVersion: 3, ConversationID: &conv, Agent: &protocol.AgentProfile{
		Name: "zee", Capabilities: []string{protocol.CapAvatar},
		Avatar: &protocol.AvatarInfo{SHA256: sha, MIME: "image/png", Width: 64, Height: 64, Bytes: len(fakePNG)},
	}}
}

func avatarRequests(pool *fakePool) int {
	count := 0
	for _, message := range pool.sent["zee"] {
		if message.Type == protocol.TypeAvatar {
			count++
		}
	}
	return count
}

func TestAvatar_FetchedCheckedAndCached(t *testing.T) {
	app, pool, _ := testApp(t)
	sha := digestOf(fakePNG)
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: welcomeWithAvatar(sha)})
	if avatarRequests(pool) != 1 {
		t.Fatal("an uncached avatar should be requested")
	}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.AvatarImage{Avatar: &protocol.AvatarData{
		SHA256: sha, MIME: "image/png", Width: 64, Height: 64, Data: base64.StdEncoding.EncodeToString(fakePNG),
	}}})
	if avatar := infoFor(app, "zee").Avatar; !strings.HasPrefix(avatar, "data:image/png;base64,") {
		t.Errorf("the sidebar should get the image, got %.40q", avatar)
	}
	if cached, err := os.ReadFile(filepath.Join(app.avatarDir, sha)); err != nil || string(cached) != string(fakePNG) {
		t.Errorf("the image should be cached on disk: %v", err)
	}

	app.avatars = map[string]string{}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: welcomeWithAvatar(sha)})
	if avatarRequests(pool) != 1 || infoFor(app, "zee").Avatar == "" {
		t.Error("a cached avatar should be loaded from disk, not requested again")
	}
}

func TestAvatar_RejectsABadImage(t *testing.T) {
	cases := map[string][]byte{
		"not an allowed format": []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"),
		"over the size limit":   append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, maxAvatarBytes)...),
	}
	for name, image := range cases {
		app, _, sent := testApp(t)
		sha := digestOf(image)
		app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: welcomeWithAvatar(sha)})
		app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.AvatarImage{Avatar: &protocol.AvatarData{
			SHA256: sha, Data: base64.StdEncoding.EncodeToString(image),
		}}})
		if infoFor(app, "zee").Avatar != "" {
			t.Errorf("%s: the image must be discarded", name)
		}
		if got := notes(*sent, "zee"); len(got) == 0 || !strings.Contains(got[len(got)-1], "avatar: discarded") {
			t.Errorf("%s: the discard should be noted, got %v", name, got)
		}
	}
}

func TestAvatar_DigestMismatchIsDiscarded(t *testing.T) {
	app, _, sent := testApp(t)
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: welcomeWithAvatar(digestOf(fakePNG))})
	tampered := append([]byte(nil), fakePNG...)
	tampered[len(tampered)-1] ^= 0xFF
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.AvatarImage{Avatar: &protocol.AvatarData{
		SHA256: digestOf(fakePNG), Data: base64.StdEncoding.EncodeToString(tampered),
	}}})
	if infoFor(app, "zee").Avatar != "" {
		t.Error("an image whose bytes don't match the digest must be discarded")
	}
	if got := notes(*sent, "zee"); !strings.Contains(got[len(got)-1], "sha256") {
		t.Errorf("the note should say why, got %v", got)
	}
}

func TestAvatar_UnaskedOrNoneIsIgnored(t *testing.T) {
	app, _, sent := testApp(t)
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.AvatarImage{Avatar: &protocol.AvatarData{SHA256: "x", Data: "eA=="}}})
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.AvatarImage{}})
	if infoFor(app, "zee").Avatar != "" || len(notes(*sent, "zee")) != 0 {
		t.Error("an avatar nobody asked for, or none at all, changes nothing")
	}
}

func TestAvatar_UnusableCacheIsFetchedAgainWithANote(t *testing.T) {
	app, pool, sent := testApp(t)
	sha := digestOf(fakePNG)
	if err := os.WriteFile(filepath.Join(app.avatarDir, sha), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: welcomeWithAvatar(sha)})
	if avatarRequests(pool) != 1 {
		t.Error("a corrupt cached copy should be fetched again")
	}
	if got := notes(*sent, "zee"); len(got) == 0 || !strings.Contains(got[0], "cached copy is unusable") {
		t.Errorf("the corrupt cache should be noted, got %v", got)
	}
}

func TestSniffImage(t *testing.T) {
	cases := map[string]string{
		"\x89PNG\r\n\x1a\nxxxx":        "image/png",
		"\xFF\xD8\xFF\xE0xxxx":         "image/jpeg",
		"RIFF\x00\x00\x00\x00WEBPVP8 ": "image/webp",
	}
	for header, want := range cases {
		if got, known := sniffImage([]byte(header)); !known || got != want {
			t.Errorf("sniffImage(%q) = %q, %v; want %q", header, got, known, want)
		}
	}
	if _, known := sniffImage([]byte("GIF89a")); known {
		t.Error("GIF isn't an allowed avatar format")
	}
}

func TestAvatarCacheDir_IsUnderTheUserCache(t *testing.T) {
	dir, err := avatarCacheDir()
	if err != nil {
		t.Skipf("no user cache directory here: %v", err)
	}
	if !strings.HasSuffix(dir, filepath.Join("sycamore-client", "avatars")) {
		t.Errorf("unexpected cache dir %q", dir)
	}
}
