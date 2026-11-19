package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/config"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// dialTimeout bounds each dial (SSH tunnel + connect), matching the TUI.
const dialTimeout = 25 * time.Second

// agentPool is the part of core.Pool the App drives; tests substitute a fake.
type agentPool interface {
	Events() <-chan core.Event
	Open(name string) error
	Close(name string)
	CloseAll()
	Pin(name string, pinned bool)
	Send(name string, msg protocol.ClientMessage) error
	Session(name string) (core.Session, bool)
	SetConversation(name, id string)
	State(name string) core.AgentState
}

// App is the Wails-bound application struct. Its exported methods are
// generated into TypeScript bindings the Svelte frontend calls; the pool's
// events go the other way as runtime events (see handleEvent). The design is
// in dev-notes/planning-gui-chat.md; the multi-agent parts follow
// dev-notes/planning-multi-agent.md §9.
type App struct {
	ctx context.Context

	// emit delivers an event to the frontend. loadConfig and newPool are
	// swapped out in tests.
	emit       func(event string, payload any)
	loadConfig func() (*config.File, error)
	newPool    func(cfg *config.File) agentPool
	sweep      func(ctx context.Context) ([]string, error) // stale tunnels; nil skips

	mu     sync.Mutex
	cfg    *config.File
	pool   agentPool
	active string                  // the agent on screen
	agents map[string]*agentStatus // every open agent

	// avatars holds each agent's avatar as a data URI; avatarWanted the
	// digest of one requested and not yet received. avatarDir is the disk
	// cache ("" for none); avatarDirErr why there's none, not yet shown.
	avatars      map[string]string
	avatarWanted map[string]string
	avatarDir    string
	avatarDirErr error
}

// agentStatus is the App's per-agent bookkeeping.
type agentStatus struct {
	turnActive   bool   // a chat is waiting for its reply
	reconnecting bool   // down after a drop
	resumeConv   string // conversation to re-pin when the welcome lands

	// status-bar fields, sent whole as a SessionInfo on every change.
	principal         string
	conversationTitle string
	model             string
	protocolVersion   int

	// steps are the running turn's progress messages; turnConv is the
	// conversation the turn was sent in.
	steps    []protocol.Progress
	turnConv string

	// convID is the conversation the agent shows. The hist* fields are its
	// history cursor: a page is on its way (histLoading, for older pages
	// when histOlder), older turns exist (histHasMore) before histOldest.
	convID      string
	histLoading bool
	histOlder   bool
	histHasMore bool
	histOldest  *int

	// unread marks conversations with a reply not yet seen; errorUnseen a
	// failed turn not yet seen; listPending a conversations request whose
	// reply is on its way; titles the conversation titles seen, by id.
	unread      map[string]bool
	errorUnseen bool
	listPending bool
	titles      map[string]string

	// commands: commandPending is the reply type a command the model is
	// working on waits for; listIntent and switchArg say what a pending
	// conversations list is for; modelsForPicker that a models list is for
	// the model picker.
	commandPending  string
	listIntent      listIntent
	switchArg       string
	modelsForPicker bool
}

// newAgentStatus returns empty bookkeeping for an agent just opened.
func newAgentStatus() *agentStatus {
	return &agentStatus{unread: map[string]bool{}, titles: map[string]string{}}
}

// NewApp creates the App with its production wiring.
func NewApp() *App {
	app := newApp(config.LoadDefault, buildPool, nil)
	app.emit = func(event string, payload any) { runtime.EventsEmit(app.ctx, event, payload) }
	app.avatarDir, app.avatarDirErr = avatarCacheDir()
	app.sweep = core.SweepStaleTunnels
	return app
}

// newApp creates an App with the given wiring and no avatar cache.
func newApp(loadConfig func() (*config.File, error), newPool func(*config.File) agentPool, emit func(string, any)) *App {
	return &App{
		loadConfig:   loadConfig,
		newPool:      newPool,
		emit:         emit,
		agents:       map[string]*agentStatus{},
		avatars:      map[string]string{},
		avatarWanted: map[string]string{},
	}
}

// buildPool returns a pool with every configured instance registered.
func buildPool(cfg *config.File) agentPool {
	pool := core.NewPool(core.PoolOptions{DialTimeout: dialTimeout, IdleTimeout: cfg.Connections.IdleTimeout})
	for name, inst := range cfg.Instances {
		pool.Add(name, core.InstanceDialer(inst))
	}
	return pool
}

// startup is called when the app starts. The context is saved so we can emit
// runtime events to the frontend.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	// A signal (ctrl+c where it was started, the session closing) quits the
	// normal way, so shutdown closes the tunnels rather than leaving them.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-signals
		runtime.Quit(ctx)
	}()
}

// shutdown is called on window close: close every connection.
func (a *App) shutdown(context.Context) {
	a.mu.Lock()
	pool := a.pool
	a.mu.Unlock()
	if pool != nil {
		pool.CloseAll()
	}
}

// CoreVersion returns the shared core library version.
func (a *App) CoreVersion() string {
	return core.Version
}

// ── view-model DTOs (emitted to the frontend; never the wire types) ─────

// Entry is one transcript line, already shaped for rendering. Role drives the
// label/styling ("you" | "agent" | "system" | "note"); Kind is the message
// origin ("chat" | "command" | "response" | "history" | "welcome" | "error" |
// "info" | "note").
// Agent and ConversationID say whose transcript it belongs to.
type Entry struct {
	Agent          string  `json:"agent"`
	ConversationID string  `json:"conversationId"`
	Role           string  `json:"role"`
	Kind           string  `json:"kind"`
	Title          string  `json:"title,omitempty"` // an info block's heading
	Text           string  `json:"text"`
	HTML           string  `json:"html,omitempty"` // an agent reply, rendered from Markdown
	Model          string  `json:"model,omitempty"`
	Elapsed        int     `json:"elapsedMs,omitempty"`
	Tokens         *Tokens `json:"tokens,omitempty"`
}

// Tokens is the response token accounting shown on an agent reply.
type Tokens struct {
	In  int `json:"in"`
	Out int `json:"out"`
}

// SessionInfo is an agent's whole status bar: who you are, the conversation,
// the model, and the agent's profile once it has sent one. It is sent whole
// on every change, so the frontend replaces rather than merges it.
type SessionInfo struct {
	Agent             string `json:"agent"`
	Principal         string `json:"principal"`
	ConversationTitle string `json:"conversationTitle"`
	Model             string `json:"model,omitempty"`
	ProtocolVersion   int    `json:"protocolVersion"`
	DisplayName       string `json:"displayName,omitempty"`
	AccentColor       string `json:"accentColor,omitempty"`
}

// ProgressMsg is a running turn's progress, folded into rows, with Status a
// one-line summary of the latest step. No rows means the turn has ended.
type ProgressMsg struct {
	Agent          string             `json:"agent"`
	ConversationID string             `json:"conversationId,omitempty"`
	Rows           []core.ProgressRow `json:"rows"`
	Status         string             `json:"status,omitempty"`
}

// StateMsg is an agent's connection state: connecting | connected |
// reconnecting | failed | disconnected. Attempt counts reconnect attempts;
// Error says why it dropped or failed.
type StateMsg struct {
	Agent   string `json:"agent"`
	State   string `json:"state"`
	Attempt int    `json:"attempt,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ThinkingMsg says whether an agent is working on a reply.
type ThinkingMsg struct {
	Agent    string `json:"agent"`
	Thinking bool   `json:"thinking"`
}

// AgentInfo is one configured agent for the sidebar: who it is, whether it's
// open and on screen, and its attention (Mark and Label, most urgent first:
// failed, connecting, disconnected, working, error, unread, idle). Avatar is
// a data URI, or "" for the initial on the accent colour.
type AgentInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Team        string `json:"team,omitempty"`
	Tagline     string `json:"tagline,omitempty"`
	AccentColor string `json:"accentColor,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
	State       string `json:"state"`
	Open        bool   `json:"open"`
	Active      bool   `json:"active"`
	Mark        string `json:"mark"`
	Label       string `json:"label"`
	Unread      int    `json:"unread"`
	Working     bool   `json:"working"`
}

// schemaAnchor exists only so Wails generates TypeScript types for the DTOs
// above: EventsEmit payloads alone don't trigger type generation, so each
// emitted type must appear in a bound method signature.
type schemaAnchor struct {
	Entry    Entry       `json:"entry"`
	Session  SessionInfo `json:"session"`
	State    StateMsg    `json:"state"`
	Thinking ThinkingMsg `json:"thinking"`
	Agent    AgentInfo   `json:"agent"`
	Progress ProgressMsg `json:"progress"`

	Models        ModelsMsg        `json:"models"`
	History       HistoryMsg       `json:"history"`
	Conversations ConversationsMsg `json:"conversations"`
	Conversation  ConversationMsg  `json:"conversation"`
	Unread        UnreadMsg        `json:"unread"`
}

// Schema is a binding-generation anchor (see schemaAnchor); it returns zero
// values and is not meant to be called.
func (a *App) Schema() schemaAnchor { return schemaAnchor{} }

// Frontend event names.
const (
	eventEntry    = "chat:entry"
	eventSession  = "chat:session"
	eventState    = "chat:state"
	eventThinking = "chat:thinking"
	eventProgress = "chat:progress"
	eventFocus    = "app:focus"
)

// ── bound methods ───────────────────────────────────────────────────────

// Start loads the config, builds the connection pool, and opens the default
// agent (plus any open_on_start ones) in the background, returning the
// default agent's name. Calling it again returns the agent on screen.
func (a *App) Start() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pool != nil {
		return a.active, nil
	}
	if a.sweep != nil {
		sweepCtx, cancelSweep := context.WithTimeout(context.Background(), 10*time.Second)
		if _, sweepErr := a.sweep(sweepCtx); sweepErr != nil {
			fmt.Fprintf(os.Stderr, "sycamore-gui: cleaning up tunnels left by an earlier client: %v\n", sweepErr)
		}
		cancelSweep()
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	inst, err := cfg.Resolve("")
	if err != nil {
		return "", err
	}
	a.cfg = cfg
	a.pool = a.newPool(cfg)
	go a.pump(a.pool.Events())

	a.active = inst.Name
	a.pool.Pin(inst.Name, true)
	var openErrs []error
	for _, name := range append([]string{inst.Name}, cfg.Connections.OpenOnStart...) {
		if _, open := a.agents[name]; open {
			continue
		}
		if openErr := a.pool.Open(name); openErr != nil {
			openErrs = append(openErrs, openErr)
			continue
		}
		a.agents[name] = newAgentStatus()
	}
	return inst.Name, errors.Join(openErrs...)
}

// Connect puts an agent on screen, opening it if it isn't connected. The
// agent left stays connected in the background.
func (a *App) Connect(name string) error {
	a.mu.Lock()
	if a.pool == nil {
		a.mu.Unlock()
		return errors.New("not started")
	}
	if err := a.pool.Open(name); err != nil {
		a.mu.Unlock()
		return err
	}
	if _, open := a.agents[name]; !open {
		a.agents[name] = newAgentStatus()
	}
	a.pool.Pin(a.active, false)
	a.pool.Pin(name, true)
	a.active = name
	a.mu.Unlock()
	a.emit(eventFocus, name)
	a.markSeen(name)
	a.emitAgents()
	return nil
}

// Close disconnects an agent and returns the agent now on screen. Closing
// the one on screen moves to another open agent; the last can't be closed.
func (a *App) Close(name string) (string, error) {
	a.mu.Lock()
	if name == "" {
		name = a.active
	}
	var refusal error
	if _, open := a.agents[name]; !open {
		refusal = fmt.Errorf("%s isn't open", name)
	} else if len(a.agents) == 1 {
		refusal = errors.New("that's the only open agent")
	}
	if refusal != nil {
		active := a.active
		a.mu.Unlock()
		return active, refusal
	}

	a.pool.Pin(name, false)
	a.pool.Close(name)
	delete(a.agents, name)
	delete(a.avatarWanted, name)
	moved := name == a.active
	if moved {
		remaining := make([]string, 0, len(a.agents))
		for open := range a.agents {
			remaining = append(remaining, open)
		}
		sort.Strings(remaining)
		a.active = remaining[0]
		a.pool.Pin(a.active, true)
	}
	active := a.active
	a.mu.Unlock()

	if moved {
		a.emit(eventFocus, active)
	}
	a.emitAgents()
	return active, nil
}

// SendChat echoes the operator's line into the agent's transcript and sends
// it. The reply arrives through the pool's events. Each agent works on one
// reply at a time.
func (a *App) SendChat(agent, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	a.mu.Lock()
	status, open := a.agents[agent]
	switch {
	case !open:
		a.mu.Unlock()
		return fmt.Errorf("%s isn't open", agent)
	case status.turnActive:
		a.mu.Unlock()
		return fmt.Errorf("%s is still working on a reply", agent)
	default:
		status.turnActive = true
		status.turnConv = status.convID
	}
	pool := a.pool
	convID := status.convID
	a.mu.Unlock()

	a.emit(eventEntry, Entry{Agent: agent, ConversationID: convID, Role: "you", Kind: "chat", Text: text})
	a.emit(eventThinking, ThinkingMsg{Agent: agent, Thinking: true})
	session, _ := pool.Session(agent)
	// The echoed conversation is the one sent to; empty lets the daemon start one.
	err := pool.Send(agent, protocol.ClientMessage{Type: protocol.TypeChat, ConversationID: convID, Message: text, Progress: session.Has(protocol.CapProgress)})
	if err != nil {
		a.endTurn(agent, status)
	}
	a.emitAgents()
	return err
}

// Agents lists every configured agent for the sidebar, grouped by team then
// name. The same list arrives as app:agents whenever it changes.
func (a *App) Agents() []AgentInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	var infos []AgentInfo
	if a.cfg != nil && a.pool != nil {
		infos = a.agentList()
	}
	return infos
}

// ── event bridge ────────────────────────────────────────────────────────

// pump delivers the pool's events until it shuts down, refreshing the
// sidebar after each.
func (a *App) pump(events <-chan core.Event) {
	for event := range events {
		a.handleEvent(event)
		a.emitAgents()
	}
}

// handleEvent turns one pool event into frontend events for its agent.
func (a *App) handleEvent(event core.Event) {
	a.mu.Lock()
	status, open := a.agents[event.Agent]
	a.mu.Unlock()
	if !open {
		return // closed with Close; its trailing events don't matter
	}
	if event.Kind == core.EventMessage {
		a.handleMessage(event.Agent, status, event.Message)
		return
	}
	a.handleState(event, status)
}

// handleMessage emits a server message's transcript entries, status-bar
// update and end of turn, re-pinning the conversation after a reconnect.
func (a *App) handleMessage(agent string, status *agentStatus, msg protocol.ServerMessage) {
	switch typed := msg.(type) {
	case *protocol.Progress:
		a.handleProgress(agent, status, typed)
		return
	case *protocol.HistoryPage:
		a.handleHistoryPage(agent, status, typed)
		return
	case *protocol.ConversationsList:
		a.handleConversationsList(agent, status, typed)
		return
	case *protocol.AvatarImage:
		a.handleAvatarImage(agent, typed)
		return
	case *protocol.ModelsList:
		a.mu.Lock()
		forPicker := status.modelsForPicker
		status.modelsForPicker = false
		a.mu.Unlock()
		if forPicker {
			a.emit(eventModels, ModelsMsg{Agent: agent, Models: modelRows(typed)})
			return
		}
	default:
		// Everything else becomes transcript entries below.
	}

	convID := a.trackConversation(agent, status, msg)
	if _, isError := msg.(*protocol.Error); isError {
		// With no request ids, an error during a turn belongs to the turn's
		// conversation, which may have been left since.
		a.mu.Lock()
		if status.turnActive && status.turnConv != "" {
			convID = status.turnConv
		}
		a.mu.Unlock()
	}
	for _, entry := range translate(msg) {
		entry.Agent = agent
		entry.ConversationID = convID
		a.emit(eventEntry, entry)
	}
	if _, isError := msg.(*protocol.Error); isError {
		a.noteError(agent, status, convID)
	}
	a.settleCommand(agent, status, msg)

	a.mu.Lock()
	rememberTitle(status, msg)
	shown := convID == status.convID
	changed := shown && applySession(status, msg)
	a.mu.Unlock()
	if changed {
		a.emit(eventSession, a.sessionSnapshot(agent, status))
	}
	if endsTurn(msg) {
		a.endTurn(agent, status)
		a.markReply(agent, status, convID)
	}
	if welcome, isWelcome := msg.(*protocol.Welcome); isWelcome {
		a.afterWelcome(agent, status, welcome)
		a.handleWelcomeAvatar(agent, welcome)
	}
}

// handleProgress records a step of the agent's running turn and sends the
// folded rows. Steps outside a turn are ignored; a new turn id starts afresh.
func (a *App) handleProgress(agent string, status *agentStatus, step *protocol.Progress) {
	a.mu.Lock()
	if !status.turnActive {
		a.mu.Unlock()
		return
	}
	if len(status.steps) > 0 && status.steps[0].TurnID != step.TurnID {
		status.steps = nil
	}
	status.steps = append(status.steps, *step)
	steps := append([]protocol.Progress(nil), status.steps...)
	a.mu.Unlock()
	a.emit(eventProgress, ProgressMsg{Agent: agent, ConversationID: step.ConversationID, Rows: core.FoldProgress(steps), Status: progressStatus(steps)})
}

// endTurn frees the agent for its next chat and clears its working state.
func (a *App) endTurn(agent string, status *agentStatus) {
	a.mu.Lock()
	status.turnActive = false
	status.steps = nil
	a.mu.Unlock()
	a.emit(eventThinking, ThinkingMsg{Agent: agent, Thinking: false})
	a.emit(eventProgress, ProgressMsg{Agent: agent})
}

// sessionSnapshot builds the agent's whole status bar.
func (a *App) sessionSnapshot(agent string, status *agentStatus) SessionInfo {
	a.mu.Lock()
	info := SessionInfo{
		Agent:             agent,
		Principal:         status.principal,
		ConversationTitle: status.conversationTitle,
		Model:             status.model,
		ProtocolVersion:   status.protocolVersion,
	}
	a.mu.Unlock()
	if session, _ := a.pool.Session(agent); session.Agent != nil {
		info.DisplayName = session.Agent.DisplayName
		info.AccentColor = session.Agent.AccentColor
	}
	return info
}

// afterWelcome loads the conversation's history. After a reconnect whose
// welcome resumed a different conversation, it goes back to the one the
// agent was in before the drop.
func (a *App) afterWelcome(agent string, status *agentStatus, welcome *protocol.Welcome) {
	a.mu.Lock()
	resume := status.resumeConv
	status.resumeConv = ""
	convID := status.convID
	title := status.conversationTitle
	a.mu.Unlock()

	resumeElsewhere := resume != "" && (welcome.ConversationID == nil || *welcome.ConversationID != resume)
	if resumeElsewhere {
		a.emit(eventEntry, Entry{Agent: agent, ConversationID: resume, Role: "note", Kind: "note", Text: "back in the conversation you were in before the drop"})
		if err := a.SwitchConversation(agent, resume, ""); err != nil {
			a.emit(eventEntry, Entry{Agent: agent, ConversationID: resume, Role: "note", Kind: "note", Text: fmt.Sprintf("couldn't reload it: %v", err)})
		}
		return
	}
	if convID == "" {
		return
	}
	a.emit(eventConversation, ConversationMsg{Agent: agent, ConversationID: convID})
	if err := a.fetchHistory(agent, status, convID, 0); err != nil {
		a.emit(eventEntry, Entry{Agent: agent, ConversationID: convID, Role: "note", Kind: "note", Text: fmt.Sprintf("couldn't load the history of %q: %v", title, err)})
	}
}

// handleState emits an agent's connection state, with notes for a drop, a
// reconnect and a permanent failure.
func (a *App) handleState(event core.Event, status *agentStatus) {
	message := StateMsg{Agent: event.Agent, State: stateName(event.State), Attempt: event.Attempt}
	if event.Err != nil {
		message.Error = event.Err.Error()
	}

	a.mu.Lock()
	wasDown := status.reconnecting
	midTurn := status.turnActive
	switch event.State {
	case core.StateReconnecting:
		if !wasDown {
			session, _ := a.pool.Session(event.Agent)
			status.resumeConv = session.ConversationID
		}
		status.reconnecting = true
		status.turnActive = false
	case core.StateOffline:
		// Retired while idle: reopening returns to this conversation.
		session, _ := a.pool.Session(event.Agent)
		status.resumeConv = session.ConversationID
		status.reconnecting = false
	case core.StateOnline, core.StateFailed:
		status.reconnecting = false
	default:
		// Connecting changes nothing here.
	}
	a.mu.Unlock()

	a.emit(eventState, message)
	note := ""
	switch {
	case event.State == core.StateReconnecting && !wasDown && midTurn:
		note = "connection lost mid-turn; that reply may not have completed — reconnecting…"
	case event.State == core.StateReconnecting && !wasDown:
		note = "connection lost — reconnecting…"
	case event.State == core.StateOnline && wasDown:
		note = "reconnected"
	case event.State == core.StateFailed:
		note = fmt.Sprintf("can't connect: %s", message.Error)
	default:
		// No note.
	}
	if midTurn && event.State == core.StateReconnecting {
		a.endTurn(event.Agent, status)
	}
	if note != "" {
		a.emit(eventEntry, Entry{Agent: event.Agent, Role: "note", Kind: "note", Text: note})
	}
}

// stateName is the frontend's name for a pool state.
func stateName(state core.AgentState) string {
	names := map[core.AgentState]string{
		core.StateConnecting:   "connecting",
		core.StateOnline:       "connected",
		core.StateReconnecting: "reconnecting",
		core.StateFailed:       "failed",
		core.StateOffline:      "disconnected",
	}
	name, known := names[state]
	if !known {
		name = state.String()
	}
	return name
}

// ── translation (pure; unit-tested) ─────────────────────────────────────

// translate maps a server message into zero or more transcript entries. Pure
// and runtime-free so it can be tested directly. Unknown/handled-elsewhere
// kinds return nil (later milestones add history, conversation lists, etc.).
func translate(msg protocol.ServerMessage) []Entry {
	switch typed := msg.(type) {
	case *protocol.Welcome:
		title := "(no conversation)"
		if typed.ConversationTitle != nil && *typed.ConversationTitle != "" {
			title = *typed.ConversationTitle
		}
		return []Entry{{Role: "system", Kind: "welcome", Text: fmt.Sprintf("connected as %s · %s", typed.Principal, title)}}
	case *protocol.Response:
		return []Entry{{
			Role:    "agent",
			Kind:    "response",
			Text:    blocksToText(typed.Content),
			HTML:    blocksToHTML(typed.Content),
			Model:   typed.Model,
			Elapsed: typed.ElapsedMS,
			Tokens:  &Tokens{In: typed.Usage.InputTokens, Out: typed.Usage.OutputTokens},
		}}
	case *protocol.Error:
		return []Entry{{Role: "system", Kind: "error", Text: typed.Message}}
	case *protocol.ConversationRenamed:
		return []Entry{{Role: "note", Kind: "note", Text: fmt.Sprintf("%s-titled: %s", typed.Source, typed.Title)}}
	case *protocol.Pong:
		return []Entry{{Role: "note", Kind: "note", Text: "pong"}}
	case *protocol.Undecodable:
		return []Entry{{Role: "system", Kind: "error", Text: fmt.Sprintf("couldn't read a %s message from the agent: %s", typed.Kind, typed.Err)}}
	default:
		return translateCommandReply(msg)
	}
}

// applySession folds a message into the agent's status-bar fields,
// reporting whether anything the status bar shows changed. Callers hold a.mu.
func applySession(status *agentStatus, msg protocol.ServerMessage) bool {
	changed := true
	switch typed := msg.(type) {
	case *protocol.Welcome:
		status.principal = typed.Principal
		status.protocolVersion = typed.ProtocolVersion
		status.conversationTitle = ""
		if typed.ConversationTitle != nil {
			status.conversationTitle = *typed.ConversationTitle
		}
	case *protocol.ConversationRenamed:
		status.conversationTitle = typed.Title
	case *protocol.NewConversation:
		status.conversationTitle = ""
	case *protocol.Response:
		changed = typed.Model != status.model
		status.model = typed.Model
	case *protocol.ModelSet:
		status.model = typed.Resolved
	default:
		changed = false
	}
	return changed
}

// progressStatus summarises the latest step: the tool running, or the
// round the model is on.
func progressStatus(steps []protocol.Progress) string {
	summary := ""
	if len(steps) > 0 {
		latest := steps[len(steps)-1]
		summary = fmt.Sprintf("thinking · round %d", latest.Round)
		if latest.Kind == protocol.ProgressToolStart {
			summary = fmt.Sprintf("%s · round %d", latest.ToolName, latest.Round)
		}
	}
	return summary
}

// endsTurn reports whether a message ends the "thinking" state (a reply landed
// or the turn errored).
func endsTurn(msg protocol.ServerMessage) bool {
	ends := false
	switch typed := msg.(type) {
	case *protocol.Response, *protocol.Error:
		ends = true
	case *protocol.Undecodable:
		ends = typed.EndsTurn()
	default:
		// Nothing else ends a turn.
	}
	return ends
}

// blocksToText flattens response content blocks into plain text; tool and
// image blocks become short placeholders. The HTML form is blocksToHTML.
func blocksToText(blocks []protocol.ContentBlock) string {
	var builder strings.Builder
	for _, blk := range blocks {
		switch blk.Type {
		case "text":
			builder.WriteString(blk.Text)
		case "tool_use":
			fmt.Fprintf(&builder, "[tool: %s]", blk.Name)
		case "tool_result":
			builder.WriteString("[tool result]")
		case "image":
			fmt.Fprintf(&builder, "[image: %s]", blk.MediaType)
		}
	}
	return builder.String()
}
