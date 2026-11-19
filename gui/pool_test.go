package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/config"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// fakePool is an in-memory agentPool that records calls.
type fakePool struct {
	mu        sync.Mutex
	events    chan core.Event
	opened    []string
	closed    []string
	pinned    map[string]bool
	sent      map[string][]protocol.ClientMessage
	sessions  map[string]core.Session
	states    map[string]core.AgentState
	sendErr   error
	closedAll bool
}

func newFakePool() *fakePool {
	return &fakePool{
		events:   make(chan core.Event),
		pinned:   map[string]bool{},
		sent:     map[string][]protocol.ClientMessage{},
		sessions: map[string]core.Session{},
		states:   map[string]core.AgentState{},
	}
}

func (p *fakePool) Events() <-chan core.Event { return p.events }
func (p *fakePool) Open(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if name == "nobody" {
		return core.ErrUnknownAgent
	}
	p.opened = append(p.opened, name)
	return nil
}
func (p *fakePool) Close(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = append(p.closed, name)
}
func (p *fakePool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closedAll = true
}
func (p *fakePool) Pin(name string, pinned bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pinned[name] = pinned
}
func (p *fakePool) Send(name string, msg protocol.ClientMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sendErr != nil {
		return p.sendErr
	}
	p.sent[name] = append(p.sent[name], msg)
	return nil
}
func (p *fakePool) Session(name string) (core.Session, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session, found := p.sessions[name]
	return session, found
}
func (p *fakePool) SetConversation(name, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.sessions[name]
	session.ConversationID = id
	p.sessions[name] = session
}
func (p *fakePool) State(name string) core.AgentState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.states[name]
}

// emitted is one event the App sent to the frontend.
type emitted struct {
	event   string
	payload any
}

// testApp returns a started App over a fake pool with zee (the default) and
// coop configured, recording what it emits. The event pump isn't running;
// tests call handleEvent directly.
func testApp(t *testing.T) (*App, *fakePool, *[]emitted) {
	t.Helper()
	pool := newFakePool()
	var mu sync.Mutex
	var sent []emitted
	cfg := &config.File{Default: "zee", Instances: map[string]config.Instance{
		"zee":  {Name: "zee", Host: "h", Socket: "/s"},
		"coop": {Name: "coop", Host: "h", Socket: "/s", Team: "web"},
	}}
	app := newApp(
		func() (*config.File, error) { return cfg, nil },
		func(*config.File) agentPool { return pool },
		func(event string, payload any) {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, emitted{event, payload})
		},
	)
	app.avatarDir = t.TempDir()
	active, err := app.Start()
	if err != nil || active != "zee" {
		t.Fatalf("Start: active=%q err=%v", active, err)
	}
	t.Cleanup(func() { close(pool.events) })
	return app, pool, &sent
}

// last returns the newest payload emitted under event, and false if none.
func last[T any](sent []emitted, event string) (T, bool) {
	var found T
	ok := false
	for _, item := range sent {
		if payload, matches := item.payload.(T); matches && item.event == event {
			found, ok = payload, true
		}
	}
	return found, ok
}

// notes returns the note texts emitted for agent.
func notes(sent []emitted, agent string) []string {
	var texts []string
	for _, item := range sent {
		if entry, isEntry := item.payload.(Entry); isEntry && entry.Agent == agent && entry.Kind == "note" {
			texts = append(texts, entry.Text)
		}
	}
	return texts
}

func TestStart_OpensAndPinsTheDefaultAgent(t *testing.T) {
	app, pool, _ := testApp(t)
	if !slices.Equal(pool.opened, []string{"zee"}) || !pool.pinned["zee"] {
		t.Errorf("zee should be opened and pinned: opened=%v pinned=%v", pool.opened, pool.pinned)
	}
	again, err := app.Start()
	must(t, err)
	if again != "zee" || len(pool.opened) != 1 {
		t.Error("a second Start should just report the agent on screen")
	}
}

func TestStart_OpensOpenOnStartAgents(t *testing.T) {
	pool := newFakePool()
	t.Cleanup(func() { close(pool.events) })
	cfg := &config.File{Default: "zee", Instances: map[string]config.Instance{"zee": {Name: "zee"}, "coop": {Name: "coop"}}}
	cfg.Connections.OpenOnStart = []string{"coop", "zee"}
	app := newApp(func() (*config.File, error) { return cfg, nil }, func(*config.File) agentPool { return pool }, func(string, any) {})
	if _, err := app.Start(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pool.opened, []string{"zee", "coop"}) || pool.pinned["coop"] {
		t.Errorf("coop should open in the background, once each: opened=%v pinned=%v", pool.opened, pool.pinned)
	}
}

func TestStart_ReportsAConfigError(t *testing.T) {
	app := newApp(func() (*config.File, error) { return nil, errors.New("no config file") }, nil, func(string, any) {})
	if _, err := app.Start(); err == nil || !strings.Contains(err.Error(), "no config file") {
		t.Errorf("want the config error, got %v", err)
	}
}

func TestConnect_FocusesAndKeepsTheOldAgentOpen(t *testing.T) {
	app, pool, sent := testApp(t)
	if err := app.Connect("coop"); err != nil {
		t.Fatal(err)
	}
	if app.active != "coop" || pool.pinned["zee"] || !pool.pinned["coop"] || len(pool.closed) != 0 {
		t.Errorf("coop on screen, zee still open: active=%q pinned=%v closed=%v", app.active, pool.pinned, pool.closed)
	}
	if focus, _ := last[string](*sent, eventFocus); focus != "coop" {
		t.Errorf("the frontend should be told, got %q", focus)
	}
	if err := app.Connect("nobody"); err == nil || app.active != "coop" {
		t.Error("an unknown agent should fail and leave the screen alone")
	}
}

func TestClose_MovesToAnotherAgentAndRefusesTheLast(t *testing.T) {
	app, pool, _ := testApp(t)
	if _, err := app.Close(""); err == nil {
		t.Error("closing the only open agent should be refused")
	}
	must(t, app.Connect("coop"))
	active, err := app.Close("")
	if err != nil || active != "zee" || !slices.Equal(pool.closed, []string{"coop"}) {
		t.Errorf("closing coop should return to zee: active=%q closed=%v err=%v", active, pool.closed, err)
	}
	app.handleEvent(core.Event{Agent: "coop", Kind: core.EventMessage, Message: &protocol.Pong{}})
	if _, tracked := app.agents["coop"]; tracked {
		t.Error("a closed agent's late events must not bring it back")
	}
}

func TestSendChat_OneReplyPerAgent(t *testing.T) {
	app, pool, sent := testApp(t)
	if err := app.SendChat("zee", "long job"); err != nil {
		t.Fatal(err)
	}
	if err := app.SendChat("zee", "another"); err == nil || !strings.Contains(err.Error(), "still working") {
		t.Errorf("a second chat to a busy agent should be refused, got %v", err)
	}
	must(t, app.Connect("coop"))
	if err := app.SendChat("coop", "quick one"); err != nil {
		t.Errorf("another agent should take a chat meanwhile: %v", err)
	}
	if len(pool.sent["zee"]) != 1 || len(pool.sent["coop"]) != 1 {
		t.Errorf("sent: %+v", pool.sent)
	}
	if thinking, _ := last[ThinkingMsg](*sent, eventThinking); thinking.Agent != "coop" || !thinking.Thinking {
		t.Errorf("thinking should be tagged with its agent, got %+v", thinking)
	}

	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Response{ConversationID: "C_1"}})
	if err := app.SendChat("zee", "another"); err != nil {
		t.Errorf("zee's reply should free it for the next chat: %v", err)
	}
}

func TestSendChat_FailureFreesTheAgent(t *testing.T) {
	app, pool, _ := testApp(t)
	pool.sendErr = errors.New("broken pipe")
	if err := app.SendChat("zee", "hello"); err == nil {
		t.Fatal("the send error should be returned")
	}
	pool.sendErr = nil
	if err := app.SendChat("zee", "hello again"); err != nil {
		t.Errorf("a failed send must not leave the agent busy: %v", err)
	}
}

func TestHandleEvent_TagsEntriesAndSessionWithTheAgent(t *testing.T) {
	app, pool, sent := testApp(t)
	pool.sessions["zee"] = core.Session{Agent: &protocol.AgentProfile{Name: "zee", DisplayName: "Zee", AccentColor: "#2E7D32"}}
	title := "Deploy notes"
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Welcome{Principal: "Paul", ConversationTitle: &title}})

	entry, _ := last[Entry](*sent, eventEntry)
	if entry.Agent != "zee" || entry.Kind != "welcome" {
		t.Errorf("the welcome entry should be tagged zee, got %+v", entry)
	}
	info, _ := last[SessionInfo](*sent, eventSession)
	if info.Agent != "zee" || info.DisplayName != "Zee" || info.AccentColor != "#2E7D32" || info.ConversationTitle != title {
		t.Errorf("session info should carry the agent and its profile, got %+v", info)
	}
}

func TestHandleEvent_StateChangesAndNotes(t *testing.T) {
	app, _, sent := testApp(t)
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateConnecting})
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateOnline})
	if len(notes(*sent, "zee")) != 0 {
		t.Errorf("a first connect needs no notes, got %v", notes(*sent, "zee"))
	}

	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateReconnecting, Attempt: 1, Err: errors.New("EOF")})
	state, _ := last[StateMsg](*sent, eventState)
	if state.Agent != "zee" || state.State != "reconnecting" || state.Attempt != 1 || state.Error != "EOF" {
		t.Errorf("reconnecting state: %+v", state)
	}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateReconnecting, Attempt: 2})
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateOnline})
	if got := notes(*sent, "zee"); !slices.Equal(got, []string{"connection lost — reconnecting…", "reconnected"}) {
		t.Errorf("one note for the drop and one for the return, got %v", got)
	}

	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateFailed, Err: errors.New("SSH authentication failed")})
	if got := notes(*sent, "zee"); !strings.Contains(got[len(got)-1], "SSH authentication failed") {
		t.Errorf("a failure should say why, got %v", got)
	}
}

func TestHandleEvent_DropMidTurnEndsTheTurn(t *testing.T) {
	app, _, sent := testApp(t)
	must(t, app.SendChat("zee", "long job"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateReconnecting, Attempt: 1})
	if thinking, _ := last[ThinkingMsg](*sent, eventThinking); thinking.Thinking {
		t.Error("the drop should stop the thinking indicator")
	}
	if got := notes(*sent, "zee"); len(got) != 1 || !strings.Contains(got[0], "mid-turn") {
		t.Errorf("expected the mid-turn note, got %v", got)
	}
	if err := app.SendChat("zee", "again"); err != nil {
		t.Errorf("the agent should be free once reconnected: %v", err)
	}
}

func TestHandleEvent_ReconnectReturnsToTheConversation(t *testing.T) {
	app, pool, sent := testApp(t)
	pool.sessions["zee"] = core.Session{ConversationID: "C_OLD"}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateReconnecting, Attempt: 1})
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateOnline})

	newer := "C_NEWER"
	pool.sessions["zee"] = core.Session{ConversationID: newer}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Welcome{ConversationID: &newer}})
	if session, _ := pool.Session("zee"); session.ConversationID != "C_OLD" {
		t.Errorf("the session should be back on C_OLD, got %q", session.ConversationID)
	}
	if got := notes(*sent, "zee"); !strings.Contains(got[len(got)-1], "back in the conversation") {
		t.Errorf("expected a resume note, got %v", got)
	}
}

func TestAgents_ListsEveryConfiguredAgentByTeam(t *testing.T) {
	app, pool, _ := testApp(t)
	pool.states["zee"] = core.StateOnline
	infos := app.Agents()
	if len(infos) != 2 || infos[0].Name != "zee" || infos[1].Name != "coop" {
		t.Fatalf("zee (no team) sorts before coop (web): %+v", infos)
	}
	if !infos[0].Open || infos[0].State != "connected" || infos[1].Open || infos[1].State != "disconnected" {
		t.Errorf("states: %+v", infos)
	}
}

func TestShutdown_ClosesEveryConnection(t *testing.T) {
	app, pool, _ := testApp(t)
	app.shutdown(context.Background())
	if !pool.closedAll {
		t.Error("closing the window should close every connection")
	}
}

// progressStep is a progress message for zee's turn T_1.
func progressStep(kind, toolUseID, toolName string, round int) *protocol.Progress {
	return &protocol.Progress{ConversationID: "C_1", TurnID: "T_1", Round: round, Kind: kind, ToolUseID: toolUseID, ToolName: toolName}
}

func TestSendChat_AsksForProgressWhenAdvertised(t *testing.T) {
	app, pool, _ := testApp(t)
	pool.sessions["zee"] = core.Session{ProtocolVersion: 3, Agent: &protocol.AgentProfile{Name: "zee", Capabilities: []string{protocol.CapProgress}}}
	must(t, app.SendChat("zee", "hello"))
	pool.sessions["coop"] = core.Session{ProtocolVersion: 2}
	must(t, app.Connect("coop"))
	must(t, app.SendChat("coop", "hello"))
	if !pool.sent["zee"][0].Progress || pool.sent["coop"][0].Progress {
		t.Errorf("progress only for an agent that advertises it: zee=%v coop=%v", pool.sent["zee"][0].Progress, pool.sent["coop"][0].Progress)
	}
}

func TestProgress_RowsDuringATurnAndClearedAfter(t *testing.T) {
	app, _, sent := testApp(t)
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: progressStep(protocol.ProgressToolStart, "tu_0", "early", 1)})
	if _, found := last[ProgressMsg](*sent, eventProgress); found {
		t.Fatal("progress outside a turn should be ignored")
	}

	must(t, app.SendChat("zee", "long job"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: progressStep(protocol.ProgressToolStart, "tu_1", "shell_exec", 2)})
	progress, _ := last[ProgressMsg](*sent, eventProgress)
	if progress.Agent != "zee" || len(progress.Rows) != 1 || progress.Rows[0].ToolName != "shell_exec" || progress.Status != "shell_exec · round 2" {
		t.Errorf("a running tool should show as a row and in the status: %+v", progress)
	}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Progress{TurnID: "T_1", Round: 2, Kind: protocol.ProgressToolEnd, ToolUseID: "tu_1", ElapsedMS: 412}})
	progress, _ = last[ProgressMsg](*sent, eventProgress)
	if len(progress.Rows) != 1 || !progress.Rows[0].Finished || progress.Status != "thinking · round 2" {
		t.Errorf("the end should merge into its row: %+v", progress)
	}

	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Response{ConversationID: "C_1"}})
	if progress, _ = last[ProgressMsg](*sent, eventProgress); len(progress.Rows) != 0 {
		t.Errorf("the reply should clear the progress, got %+v", progress)
	}
}

func TestProgress_NewTurnStartsAfresh(t *testing.T) {
	app, _, sent := testApp(t)
	must(t, app.SendChat("zee", "job"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: progressStep(protocol.ProgressToolStart, "tu_1", "shell_exec", 1)})
	second := progressStep(protocol.ProgressToolStart, "tu_2", "read_file", 1)
	second.TurnID = "T_2"
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: second})
	if progress, _ := last[ProgressMsg](*sent, eventProgress); len(progress.Rows) != 1 || progress.Rows[0].ToolName != "read_file" {
		t.Errorf("a new turn id should replace the old rows: %+v", progress)
	}
}

func TestSession_SnapshotCarriesModelAndKeepsTitle(t *testing.T) {
	app, _, sent := testApp(t)
	title := "Deploy notes"
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Welcome{Principal: "Paul", ConversationTitle: &title}})
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Response{ConversationID: "C_1", Model: "claude-sonnet-5"}})
	info, _ := last[SessionInfo](*sent, eventSession)
	if info.Model != "claude-sonnet-5" || info.ConversationTitle != title || info.Principal != "Paul" {
		t.Errorf("the whole status bar should be sent: %+v", info)
	}
}

func TestStart_SweepsStaleTunnelsFirst(t *testing.T) {
	pool := newFakePool()
	t.Cleanup(func() { close(pool.events) })
	cfg := &config.File{Default: "zee", Instances: map[string]config.Instance{"zee": {Name: "zee"}}}
	app := newApp(func() (*config.File, error) { return cfg, nil }, func(*config.File) agentPool { return pool }, func(string, any) {})
	swept := false
	app.sweep = func(context.Context) ([]string, error) {
		swept = len(pool.opened) == 0
		return nil, nil
	}
	if _, err := app.Start(); err != nil {
		t.Fatal(err)
	}
	if !swept {
		t.Error("Start should sweep stale tunnels before opening any")
	}
}
