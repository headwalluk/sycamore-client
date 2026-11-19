package main

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/config"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// link is how the model reaches the agent on screen: a *core.AgentLink in
// production, a fake in tests.
type link interface {
	Name() string
	Send(protocol.ClientMessage) error
	Session() core.Session
	SetConversation(id string)
}

// agentPool is the part of core.Pool the model drives. poolAdapter wraps
// the real pool; tests substitute a fake.
type agentPool interface {
	Events() <-chan core.Event
	Open(name string) error
	Close(name string)
	Pin(name string, pinned bool)
	Link(name string) link
}

// poolAdapter satisfies agentPool with a *core.Pool.
type poolAdapter struct{ *core.Pool }

// Link returns the pool's handle for one agent, as a link.
func (adapter poolAdapter) Link(name string) link { return adapter.Pool.Link(name) }

// ── messages from the pool / send Cmd ───────────────────────────────

// recvMsg is one server message from the named agent.
type recvMsg struct {
	agent string
	msg   protocol.ServerMessage
}

// agentStateMsg is a connection-state change for the named agent: the
// pool connecting, reconnecting (attempt counts up from 1), failing for
// good, or going offline.
type agentStateMsg struct {
	agent   string
	state   core.AgentState
	attempt int
	err     error
}

// poolClosedMsg signals that the pool has shut down; no more events come.
type poolClosedMsg struct{}

// sendErrMsg signals that a send to the named agent failed. The pool drops
// and reconnects the connection itself; the model only reports it.
type sendErrMsg struct {
	agent string
	err   error
}

// ── styles ──────────────────────────────────────────────────────────

var (
	headerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("63")).Bold(true)
	ruleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	userLabelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("44")).Bold(true)  // cyan
	agentLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true) // magenta
	metaStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("180")).Bold(true) // command-reply titles
	dimStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	systemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
	errorStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	attachmentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("110")) // light blue

	connOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))  // green
	connDown = lipgloss.NewStyle().Foreground(lipgloss.Color("203")) // red
)

// fixedChromeRows is the number of non-viewport, non-input rows in the
// layout: the header, three rules (under header, above input, under
// input), and the footer. The input is now a variable-height textarea —
// see relayout.
const fixedChromeRows = 5

// maxInputRows caps how tall the input grows before it scrolls internally.
const maxInputRows = 6

// entryKind tags a transcript entry by origin.
type entryKind int

const (
	entryUser    entryKind = iota // a line the operator typed (chat or command)
	entryServer                   // a decoded server message
	entryNote                     // client-side text: /help output, command errors
	entryHistory                  // a backfilled historic turn (one user/assistant record)
)

// entry is one item in the transcript. Stored raw (not pre-rendered) so
// the whole transcript can re-wrap on a terminal resize.
type entry struct {
	kind    entryKind
	text    string                  // entryUser / entryNote
	msg     protocol.ServerMessage  // entryServer
	histMsg protocol.HistoryMessage // entryHistory
}

// historyPageLimit is the turn count requested per history page — the
// seed-on-connect page and each scroll-back page. The daemon clamps to
// its max (200); 50 is a sane default seed.
const historyPageLimit = 50

// convView is the conversation-scoped slice of UI state: the transcript
// and everything tied to the conversation currently on screen (its title,
// model label, in-flight indicator, and history-pagination cursor). It is
// grouped — and embedded in model — so re-attaching to a different
// conversation (switch, M17) or a fresh connection (reconnect, M14) is a
// single reset (see model.resetView), while connection-scoped state
// (client, instance, principal, connected) survives untouched. Embedded,
// so existing field access (m.entries, m.convTitle, …) is unchanged.
type convView struct {
	convID  string // the conversation shown; "" until one exists
	entries []entry

	// status of the on-screen conversation.
	convTitle  string
	modelLabel string
	thinking   bool

	// steps are the in-flight chat turn's progress messages, drawn as a
	// live block below the transcript and dropped when the turn ends (the
	// response carries the same content).
	steps []protocol.Progress

	// history backfill (protocol v2). histConvID is the conversation the
	// loaded pages belong to; histOldest is the next-older-page cursor
	// (nil when nothing more to load); histHasMore reports older turns
	// exist; histLoading guards against overlapping requests;
	// histScrollbackPending marks the in-flight request as a scroll-back
	// (so its reply preserves the scroll anchor) vs. the initial seed
	// (which pins to the bottom). histSeeded ensures we seed only once.
	histConvID            string
	histOldest            *int
	histHasMore           bool
	histLoading           bool
	histScrollbackPending bool
	histSeeded            bool
}

// agentCtx is one agent's slice of UI state: its link, its conversation
// view, and its connection and pending-request status. The model keeps one
// per open agent and embeds a pointer to the one on screen, so field access
// (m.entries, m.thinking, …) reads the active agent; withAgent points it at
// another agent to apply that agent's events. See
// dev-notes/planning-multi-agent.md §4.
type agentCtx struct {
	instance string // the agent's name in the pool and config (e.g. "prod")
	client   link

	convView // conversation-scoped state (transcript, status, history cursor)

	// connection-scoped status — survives a conversation switch, reset
	// only on (re)connect.
	principal string
	connected bool

	// connection state, driven by the pool's state events. connecting is a
	// first dial in progress; reconnecting is the agent being down after a
	// drop, with reconnectAttempt the pool's attempt count. The resume*
	// fields carry the conversation to re-pin once the reconnect welcome
	// lands, so a drop returns you to the conversation you were in rather
	// than wherever the daemon's welcome resumes.
	connecting       bool
	reconnecting     bool
	reconnectAttempt int
	resumeConvID     string
	resumeConvTitle  string
	resumePending    bool

	// pending requests. convAction records what a pending
	// conversations_list reply is for, pendingSwitchArg the argument to
	// resolve for a direct /switch <arg>; modelPickerPending marks a
	// models request whose reply should open the picker (ctrl+y) rather
	// than dump the list into the transcript (/models).
	convAction         convAction
	pendingSwitchArg   string
	modelPickerPending bool

	// input history: history holds the lines sent to this agent, oldest
	// first; historyIndex is the one shown while browsing with ↑ / ↓
	// (len(history) when not browsing), and historyDraft the draft to
	// return to.
	history      []string
	historyIndex int
	historyDraft string

	// fetchOnWelcome sends the pending conversations request once the
	// agent's welcome arrives, for a request made while it was offline.
	fetchOnWelcome bool

	// conversations is the agent's last conversations_list, shown by the
	// switcher while a fresh one is fetched.
	conversations []protocol.ConversationSummary

	// cached holds recently shown conversations' views by id, least
	// recently shown first in cacheOrder, at most cacheLimit of them.
	// viewingOther is set while withConversation has one in place.
	cached       map[string]*convView
	cacheOrder   []string
	cacheLimit   int
	viewingOther bool

	// turnActive marks the agent's one chat turn in flight; turnConv is the
	// conversation it was sent in.
	turnActive bool
	turnConv   string

	// attention: unread marks conversations with a reply not yet seen;
	// errorUnseen a failed turn not yet seen; failed a connection the pool
	// gave up on.
	unread      map[string]bool
	errorUnseen bool
	failed      bool
}

// newAgentCtx returns empty state for one agent, linked through pool.
func newAgentCtx(pool agentPool, name string, cacheLimit int) *agentCtx {
	return &agentCtx{
		instance:   name,
		client:     pool.Link(name),
		cached:     map[string]*convView{},
		cacheLimit: cacheLimit,
		unread:     map[string]bool{},
	}
}

// model is the TUI's Bubble Tea root: a full-screen layout of a status
// header, a scrollable transcript viewport, a single-line input, and a
// footer carrying key hints + connection/working state.
type model struct {
	pool agentPool // every agent's connection; see core.Pool

	*agentCtx                      // the agent on screen, except inside withAgent
	agents    map[string]*agentCtx // every open agent, by name
	active    string               // the agent on screen

	// cfg is the loaded config, kept at runtime so /connect <name> can
	// resolve an instance and dial it. Nil when started with --socket and
	// no config file was found — /connect then reports it's unavailable.
	cfg *config.File

	// cacheLimit is how many conversation views each agent keeps besides
	// the one shown ([ui] conversation_cache).
	cacheLimit int

	// editor is the command /edit runs ([ui] editor, $VISUAL or $EDITOR).
	editor string

	// emojiChoices is the most matches the emoji chooser shows ([ui]
	// emoji_choices); emoji is the chooser, closed when it has no choices.
	emojiChoices int
	emoji        emojiChooser

	vp    viewport.Model
	input textarea.Model
	spin  spinner.Model
	md    *mdRenderer // glamour Markdown renderer for agent text blocks

	width  int
	height int
	ready  bool

	// mouseCapture mirrors whether the program is currently capturing the
	// mouse (wheel-scroll on). Toggled at runtime with ctrl+o; its initial
	// value comes from [ui] mouse_capture. When off, the terminal's native
	// click-drag text selection works again.
	mouseCapture bool

	// picker is the modal overlay (nil when closed): conversation switch,
	// agent connect, or model choice, per pickerAction.
	picker       *picker
	pickerAction pickerAction // what an open picker's selection does

	// switcher is the /switch overlay (nil when closed).
	switcher *switcher

	err      error
	quitting bool
}

// withAgent runs handle with the named agent's state in place of the one on
// screen, then puts the active agent back. Handlers redraw through refresh,
// which does nothing for a background agent, so the screen (and its scroll
// position) is left alone. Handlers must not change which agent is active.
func (m *model) withAgent(name string, handle func(*model) tea.Cmd) tea.Cmd {
	onScreen := m.agentCtx
	m.agentCtx = m.agents[name]
	cmd := handle(m)
	m.agentCtx = onScreen
	return cmd
}

// isOnScreen reports whether the agent and conversation in place are the
// ones being shown.
func (m *model) isOnScreen() bool {
	return m.instance == m.active && !m.viewingOther
}

// resetView clears all conversation-scoped state — the transcript, the
// status fields, and the history-pagination cursor — returning the view
// to its just-attached empty state. Connection-scoped state (client,
// instance, principal, connected) is left intact. This is the single
// primitive behind re-attaching to a different conversation (switch, M17)
// or, together with a client swap, a new connection (reconnect, M14).
func (m *model) resetView() {
	m.convView = convView{}
}

func newModel(pool agentPool, instance, mdStyle string, mouseCapture bool, cfg *config.File) model {
	ta := textarea.New()
	ta.Prompt = "› "
	ta.Placeholder = "message… (/help · ctrl+j or \\↵ = newline)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0 // no limit — chat turns can be long
	ta.SetHeight(1)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	cacheLimit := config.DefaultConversationCache
	if cfg != nil && cfg.UI.ConversationCache != nil {
		cacheLimit = *cfg.UI.ConversationCache
	}
	configuredEditor := ""
	if cfg != nil {
		configuredEditor = cfg.UI.Editor
	}
	emojiChoices := config.DefaultEmojiChoices
	if cfg != nil && cfg.UI.EmojiChoices != nil {
		emojiChoices = *cfg.UI.EmojiChoices
	}
	onScreen := newAgentCtx(pool, instance, cacheLimit)
	return model{
		cacheLimit:   cacheLimit,
		editor:       config.ResolveEditor(configuredEditor),
		emojiChoices: emojiChoices,
		pool:         pool,
		agentCtx:     onScreen,
		agents:       map[string]*agentCtx{instance: onScreen},
		active:       instance,
		cfg:          cfg,
		input:        ta,
		spin:         sp,
		md:           newMDRenderer(mdStyle),
		mouseCapture: mouseCapture,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.input.Focus(), waitForEvent(m.pool.Events()))
}

// relayout sizes the input to its content (1 row, growing with newlines up
// to maxInputRows, then scrolling) and gives the transcript viewport the
// remaining height. Called on resize and after every input edit.
func (m *model) relayout() {
	if !m.ready {
		return
	}
	rows := m.input.LineCount()
	if rows < 1 {
		rows = 1
	}
	if rows > maxInputRows {
		rows = maxInputRows
	}
	m.input.SetHeight(rows)
	m.input.SetWidth(m.width)

	vpH := m.height - fixedChromeRows - rows - len(m.emoji.choices)
	if vpH < 1 {
		vpH = 1
	}
	m.vp.Width = m.width
	m.vp.Height = vpH
}

// waitForEvent is the read-loop Cmd: it delivers the pool's next event as a
// recvMsg or agentStateMsg. Re-issued after every event, so exactly one
// read is pending while the pool is open.
func waitForEvent(events <-chan core.Event) tea.Cmd {
	return func() tea.Msg {
		event, open := <-events
		if !open {
			return poolClosedMsg{}
		}
		if event.Kind == core.EventMessage {
			sanitizeStrings(event.Message)
			return recvMsg{agent: event.Agent, msg: event.Message}
		}
		return agentStateMsg{agent: event.Agent, state: event.State, attempt: event.Attempt, err: event.Err}
	}
}

// sendMessage writes any client message to the wire, reporting a send
// failure as the fatal sendErrMsg.
func sendMessage(c link, msg protocol.ClientMessage) tea.Cmd {
	return func() tea.Msg {
		if err := c.Send(msg); err != nil {
			return sendErrMsg{agent: c.Name(), err: err}
		}
		return nil
	}
}

// sendChat sends a chat turn, asking for progress steps when the agent
// advertises them.
func sendChat(c link, text string) tea.Cmd {
	return sendMessage(c, protocol.ClientMessage{
		Type:     protocol.TypeChat,
		Message:  text,
		Progress: c.Session().Has(protocol.CapProgress),
	})
}

// fetchHistory requests one page of conversation history. A zero
// beforeSeq asks for the newest page (the seed); otherwise it's the
// backward-pagination cursor (a prior page's oldest sequence).
func fetchHistory(c link, convID string, beforeSeq, limit int) tea.Cmd {
	return sendMessage(c, protocol.ClientMessage{
		Type:           protocol.TypeHistory,
		ConversationID: convID,
		BeforeSequence: beforeSeq,
		Limit:          limit,
	})
}

// seedHistoryCmd requests the newest page of history for the resumed
// conversation, once per connection. Returns nil — backfilling nothing —
// when the daemon predates protocol v2 (the feature gate) or there is no
// conversation to seed.
func (m *model) seedHistoryCmd(welcome *protocol.Welcome) tea.Cmd {
	if m.histSeeded || !m.client.Session().Has(protocol.CapHistory) {
		return nil
	}
	if welcome.ConversationID == nil || *welcome.ConversationID == "" {
		return nil
	}
	m.histSeeded = true
	m.histLoading = true
	return fetchHistory(m.client, *welcome.ConversationID, 0, historyPageLimit)
}

// welcomeHistoryCmd handles history on a welcome. Normally it seeds the
// resumed conversation (seedHistoryCmd). After an auto-reconnect, though, a
// resume is pending: if the daemon's welcome resumed a different conversation
// than the one we were in, re-pin ours (switchTo) so the drop returns you
// exactly where you were; otherwise the welcome already landed us there and a
// plain seed suffices.
func (m *model) welcomeHistoryCmd(welcome *protocol.Welcome) tea.Cmd {
	if !m.resumePending {
		return m.seedHistoryCmd(welcome)
	}
	m.resumePending = false
	target, title := m.resumeConvID, m.resumeConvTitle
	m.resumeConvID, m.resumeConvTitle = "", ""

	resumeElsewhere := target != "" &&
		m.client.Session().Has(protocol.CapHistory) &&
		(welcome.ConversationID == nil || *welcome.ConversationID != target)
	if resumeElsewhere {
		m.convID = "" // the daemon's pick isn't worth caching
		return m.switchTo(target, title)
	}
	return m.seedHistoryCmd(welcome)
}

// maybeLoadOlder fires a scroll-back request when the viewport is parked
// at the top and older turns remain — the infinite-scroll trigger.
// Returns nil when there's nothing to load or a request is already in
// flight.
func (m *model) maybeLoadOlder() tea.Cmd {
	if !m.ready || m.histLoading || !m.histHasMore || m.histOldest == nil {
		return nil
	}
	if !m.vp.AtTop() {
		return nil
	}
	m.histLoading = true
	m.histScrollbackPending = true
	return fetchHistory(m.client, m.histConvID, *m.histOldest, historyPageLimit)
}

// ── conversation list / switch (protocol v2) ────────────────────────

// sendConversations requests the caller's conversation list.
func sendConversations(c link) tea.Cmd {
	return sendMessage(c, protocol.ClientMessage{Type: protocol.TypeConversations})
}

// startConvFetch records the intent behind a conversation-list request
// and sends it, gated on protocol v2 (an older daemon can't serve it).
func (m model) startConvFetch(action convAction, arg string) (tea.Model, tea.Cmd) {
	if action == convActionPicker {
		return m.openSwitcher()
	}
	if agentName, rest, targeted := m.splitAgentArg(arg); action == convActionSwitchArg && targeted {
		return m.switchOnAgent(agentName, rest)
	}
	if !m.client.Session().Has(protocol.CapConversations) {
		m.entries = append(m.entries, entry{kind: entryNote, text: "conversation listing/switching needs a protocol v2 daemon"})
		m.refresh()
		return m, nil
	}
	m.convAction = action
	m.pendingSwitchArg = arg
	m.refresh()
	return m, sendConversations(m.client)
}

// splitAgentArg splits a "/switch agent:text" argument, when the part
// before the first colon names an agent.
func (m model) splitAgentArg(arg string) (string, string, bool) {
	name, rest, found := strings.Cut(arg, ":")
	targeted := found && slices.Contains(m.agentNames(), strings.TrimSpace(name))
	return strings.TrimSpace(name), strings.TrimSpace(rest), targeted
}

// switchOnAgent handles "/switch agent:text": put the agent on screen and
// switch it to the conversation matching text, fetching its list once it's
// connected.
func (m model) switchOnAgent(name, arg string) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if name != m.active {
		next, cmd := m.focusAgent(name)
		m = next.(model)
		cmds = append(cmds, cmd)
		if m.active != name {
			return m, tea.Batch(cmds...) // focusAgent explained why
		}
	}
	m.convAction = convActionSwitchArg
	m.pendingSwitchArg = arg
	switch {
	case !m.connected:
		m.fetchOnWelcome = true
	case !m.client.Session().Has(protocol.CapConversations):
		m.convAction = convActionNone
		m.note("conversation listing/switching needs a protocol v2 daemon")
	default:
		cmds = append(cmds, sendConversations(m.client))
	}
	return m, tea.Batch(cmds...)
}

// handleConversationsList dispatches a conversations_list reply by the
// recorded intent: render it, open the switcher, or resolve an argument
// and switch. Returns a Cmd when the action triggers a switch (a history
// re-seed), else nil.
func (m *model) handleConversationsList(cl *protocol.ConversationsList) tea.Cmd {
	action := m.convAction
	arg := m.pendingSwitchArg
	m.convAction = convActionNone
	m.pendingSwitchArg = ""
	m.conversations = cl.Conversations

	switch action {
	case convActionPicker:
		m.showSwitcherList()
		return nil
	case convActionSwitchArg:
		return m.switchByArg(cl, arg)
	default: // convActionList or unsolicited
		m.entries = append(m.entries, entry{kind: entryServer, msg: cl})
		m.refresh()
		return nil
	}
}

// openModelPicker requests the model list and marks the reply for the
// picker (ctrl+y). The overlay opens when models_list lands
// (handleModelsList) — the list is async, so we can't open it inline.
func (m model) openModelPicker() (tea.Model, tea.Cmd) {
	m.modelPickerPending = true
	return m, sendMessage(m.client, protocol.ClientMessage{Type: protocol.TypeModels})
}

// handleModelsList dispatches a models_list reply: open the picker when a
// ctrl+y request is pending, else dump the list into the transcript (the
// /models path). Returns nil — neither path needs a follow-up Cmd.
func (m *model) handleModelsList(ml *protocol.ModelsList) tea.Cmd {
	if m.modelPickerPending && m.isOnScreen() {
		m.modelPickerPending = false
		p := newPicker("Switch model", modelsToItems(ml))
		p.setSize(m.width, m.height)
		m.picker = &p
		m.pickerAction = pickerSetModel
		return nil
	}
	m.modelPickerPending = false
	m.entries = append(m.entries, entry{kind: entryServer, msg: ml})
	m.refresh()
	return nil
}

// modelsToItems maps the alias list onto picker rows: the alias name is
// the id handed to set_model, with the resolved target as dim desc and the
// current model marked.
func modelsToItems(ml *protocol.ModelsList) []pickerItem {
	cur := ""
	if ml.Current != nil {
		cur = *ml.Current
	}
	items := make([]pickerItem, 0, len(ml.Aliases))
	for _, a := range ml.Aliases {
		title := a.Name
		if a.Name == cur {
			title += "  (current)"
		}
		items = append(items, pickerItem{id: a.Name, title: title, desc: a.Target})
	}
	return items
}

// switchByArg resolves a /switch argument — a 1-based index from /list or
// a case-insensitive title substring — and switches, or notes why it
// couldn't.
func (m *model) switchByArg(cl *protocol.ConversationsList, arg string) tea.Cmd {
	cs := cl.Conversations
	if idx, err := strconv.Atoi(arg); err == nil {
		if idx >= 1 && idx <= len(cs) {
			return m.switchTo(cs[idx-1].ID, convTitleOrPlaceholder(cs[idx-1].Title))
		}
		m.note(fmt.Sprintf("no conversation at index %d (have %d)", idx, len(cs)))
		return nil
	}

	q := strings.ToLower(arg)
	var matched []protocol.ConversationSummary
	for _, c := range cs {
		if c.Title != nil && strings.Contains(strings.ToLower(*c.Title), q) {
			matched = append(matched, c)
		}
	}
	switch len(matched) {
	case 0:
		m.note(fmt.Sprintf("no conversation matching %q", arg))
		return nil
	case 1:
		return m.switchTo(matched[0].ID, convTitleOrPlaceholder(matched[0].Title))
	default:
		m.note(fmt.Sprintf("%d conversations match %q — be more specific, or use /switch with no argument", len(matched), arg))
		return nil
	}
}

// switchTo performs the switch: point the session at the new conversation
// (so chats follow), reset the view (M13), show the title immediately,
// and re-seed the transcript with that conversation's history (M15).
func (m *model) switchTo(id, title string) tea.Cmd {
	m.client.SetConversation(id)
	if id == m.convID {
		m.markSeen()
		m.refresh()
		return nil
	}
	m.parkView()
	if cached, found := m.takeCached(id); found {
		m.convView = cached
		m.markSeen()
		m.refresh()
		return nil
	}
	m.resetView()
	m.convID = id
	m.markSeen()
	m.convTitle = title
	m.histSeeded = true // seeded explicitly here, not via a welcome
	m.histLoading = true
	m.entries = append(m.entries, entry{kind: entryNote, text: fmt.Sprintf("— switched to %q —", title)})
	m.refresh()
	return fetchHistory(m.client, id, 0, historyPageLimit)
}

// note appends a client-side note line and refreshes.
func (m *model) note(text string) {
	m.entries = append(m.entries, entry{kind: entryNote, text: text})
	m.refresh()
}

// convTitleOrPlaceholder renders a possibly-null/empty title.
func convTitleOrPlaceholder(title *string) string {
	if title == nil || *title == "" {
		return "(untitled)"
	}
	return *title
}

// shortTime trims an ISO-8601 timestamp to "YYYY-MM-DD HH:MM" for compact
// display; anything shorter is shown verbatim.
func shortTime(iso string) string {
	if len(iso) >= 16 {
		return strings.Replace(iso[:16], "T", " ", 1)
	}
	return iso
}

// ── user-initiated reconnect (M14) ──────────────────────────────────

// pickerAction records what an open picker's selection should do, since
// one widget serves both conversation switch (M17) and instance connect
// (M14, bare /connect). The zero value is the conversation switch, the
// original consumer.
type pickerAction int

const (
	pickerConnect  pickerAction = iota // selection connects to an instance
	pickerSetModel                     // selection switches the active model
)

// startConnect handles /connect: put a named agent on screen, or open the
// agent picker for a bare /connect. Both need a loaded config.
func (m model) startConnect(action connectAction, name string) (tea.Model, tea.Cmd) {
	if m.cfg == nil || len(m.cfg.Instances) == 0 {
		m.note("no instances configured — /connect needs a config file (run with --init)")
		return m, nil
	}
	if action == connectActionPicker {
		m.openInstancePicker()
		return m, nil
	}
	return m.focusAgent(name)
}

// focusAgent puts the named agent on screen, opening it in the pool if it
// isn't connected. The agent it replaces stays open in the background.
func (m model) focusAgent(name string) (tea.Model, tea.Cmd) {
	if name == m.active {
		if m.connected || m.connecting || m.reconnecting {
			m.note(fmt.Sprintf("already on %s", name))
			return m, nil
		}
		// Offline or failed: reopen it where it is.
		if err := m.pool.Open(name); err != nil {
			m.note(fmt.Sprintf("could not connect to %q: %v", name, err))
		}
		return m, nil
	}
	if err := m.pool.Open(name); err != nil {
		m.note(fmt.Sprintf("could not connect to %q: %v", name, err))
		return m, nil
	}
	if _, open := m.agents[name]; !open {
		m.agents[name] = newAgentCtx(m.pool, name, m.cacheLimit)
	}
	m.pool.Pin(m.active, false)
	m.pool.Pin(name, true)
	m.active = name
	m.agentCtx = m.agents[name]
	m.markSeen()
	m.refresh()
	return m, m.spin.Tick
}

// closeAgent handles /close [name]: disconnect an agent and forget its
// view. Closing the agent on screen moves to another open one; the last
// open agent can't be closed.
func (m model) closeAgent(name string) (tea.Model, tea.Cmd) {
	if name == "" {
		name = m.active
	}
	if _, open := m.agents[name]; !open {
		m.note(fmt.Sprintf("%s isn't open", name))
		return m, nil
	}
	if len(m.agents) == 1 {
		m.note("that's the only open agent — /quit to exit")
		return m, nil
	}
	m.pool.Pin(name, false)
	m.pool.Close(name)
	delete(m.agents, name)
	if name == m.active {
		remaining := make([]string, 0, len(m.agents))
		for open := range m.agents {
			remaining = append(remaining, open)
		}
		sort.Strings(remaining)
		m.active = remaining[0]
		m.agentCtx = m.agents[m.active]
		m.pool.Pin(m.active, true)
	}
	m.note(fmt.Sprintf("— closed %s —", name))
	return m, m.spin.Tick
}

// tracksAgent reports whether events for name belong to an open agent,
// adopting one the pool has just started opening (such as an
// open_on_start agent). Events for agents closed with /close are dropped.
func (m *model) tracksAgent(name string, change *agentStateMsg) bool {
	if _, open := m.agents[name]; open {
		return true
	}
	starting := change != nil && (change.state == core.StateConnecting || change.state == core.StateOnline)
	if starting {
		m.agents[name] = newAgentCtx(m.pool, name, m.cacheLimit)
	}
	return starting
}

// applyAgentState records a pool state change on the agent in place: a
// drop starts the reconnecting state, coming back online re-attaches the
// view, and a permanent failure stops and says why.
func (m *model) applyAgentState(change agentStateMsg) tea.Cmd {
	var cmd tea.Cmd
	switch change.state {
	case core.StateConnecting:
		m.connecting = true
		m.failed = false
		cmd = m.spin.Tick
	case core.StateReconnecting:
		m.connecting = false
		if m.reconnecting {
			m.reconnectAttempt = change.attempt
		} else {
			cmd = m.handleDrop(change)
		}
	case core.StateOnline:
		m.connecting = false
		m.failed = false
		if m.reconnecting || m.resumeConvID != "" {
			m.handleReconnected()
		}
	case core.StateFailed:
		m.connecting = false
		m.reconnecting = false
		m.connected = false
		m.failed = true
		m.note(fmt.Sprintf("— can't reconnect to %s: %v — fix that, then /connect %s —", change.agent, change.err, change.agent))
	case core.StateOffline:
		// Retired while idle (or closed): remember the conversation, so
		// reopening returns to it.
		m.rememberConversation()
		m.connecting = false
		m.reconnecting = false
		m.connected = false
	default:
		// No other states exist.
	}
	return cmd
}

// rememberConversation records the conversation on screen as the one to
// resume when the agent next comes online.
func (m *model) rememberConversation() {
	if session := m.client.Session(); session.ConversationID != "" {
		m.resumeConvID = session.ConversationID
		m.resumeConvTitle = m.convTitle
	}
}

// handleDrop enters the reconnecting state after the agent dropped. It
// remembers the conversation to resume and clears any in-flight turn; the
// pool does the redialling. The transcript stays on screen throughout.
func (m *model) handleDrop(change agentStateMsg) tea.Cmd {
	m.rememberConversation()
	m.connected = false
	m.steps = nil
	m.endTurn()
	m.clearCachedTurns()
	if m.thinking {
		m.thinking = false
		m.note("— connection lost mid-turn; that reply may not have completed (it'll appear in the transcript after reconnect if it did) —")
	}
	if change.err != nil {
		m.note(fmt.Sprintf("— connection lost: %v — reconnecting… —", change.err))
	} else {
		m.note("— connection lost — reconnecting… —")
	}
	m.reconnecting = true
	m.reconnectAttempt = change.attempt
	return m.spin.Tick
}

// handleReconnected re-attaches the view once the agent is back: clear the
// conversation view and connection status, and arrange for the welcome to
// re-pin the conversation we were in.
func (m *model) handleReconnected() {
	m.reconnecting = false
	m.reconnectAttempt = 0
	m.resumePending = m.resumeConvID != ""

	m.dropCache()
	m.resetView()
	m.principal = ""
	m.connected = false
	m.note("— reconnected —")
}

// openInstancePicker opens the picker over the configured instances; a
// selection dials that instance — the same path as /connect <name>.
func (m *model) openInstancePicker() {
	p := newPicker("Connect to instance", instancesToItems(m.cfg))
	p.setSize(m.width, m.height)
	m.picker = &p
	m.pickerAction = pickerConnect
}

// instancesToItems maps configured instances onto picker rows, sorted by
// name for a stable order, with the host (and a default marker) as the
// secondary text. The row id is the instance name passed to beginDial.
func instancesToItems(cfg *config.File) []pickerItem {
	names := make([]string, 0, len(cfg.Instances))
	for name := range cfg.Instances {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]pickerItem, 0, len(names))
	for _, name := range names {
		desc := cfg.Instances[name].Host
		if name == cfg.Default {
			desc += " · default"
		}
		items = append(items, pickerItem{id: name, title: name, desc: desc})
	}
	return items
}

// handleServerMessage applies one server message to the agent in place.
func (m *model) handleServerMessage(message protocol.ServerMessage) tea.Cmd {
	if id, carries := protocol.ConversationOf(message); carries && m.convID != "" && id != m.convID {
		return m.handleOffScreenConversation(id, message)
	}
	if failure, isError := message.(*protocol.Error); isError {
		return m.handleError(failure)
	}

	var cmds []tea.Cmd
	switch typed := message.(type) {
	case *protocol.HistoryPage:
		// History pages are folded into the transcript specially
		// (expanded per record, prepended); they don't touch the
		// status bar or appear as a single server entry.
		scrollback := m.histScrollbackPending && m.isOnScreen()
		m.histScrollbackPending = false
		m.applyHistoryPage(typed, scrollback)
	case *protocol.ConversationsList:
		// Dispatched by the recorded intent (list / picker / switch),
		// which may trigger a history re-seed.
		cmds = append(cmds, m.handleConversationsList(typed))
	case *protocol.ModelsList:
		// Open the picker (ctrl+y) or dump the list (/models), per the
		// pending intent.
		cmds = append(cmds, m.handleModelsList(typed))
	case *protocol.Progress:
		// Kept out of default, which would end the turn.
		m.applyProgress(typed)
	case *protocol.AvatarImage:
		// GUI-only; the TUI never requests it.
	case *protocol.Undecodable:
		m.note(fmt.Sprintf("couldn't read a %s message from the agent: %s", typed.Kind, typed.Err))
		if typed.EndsTurn() {
			m.thinking = false
			m.steps = nil
			m.endTurn()
		}
	default:
		m.applyStatus(message)
		m.entries = append(m.entries, entry{kind: entryServer, msg: message})
		m.refresh()
		if reply, isReply := message.(*protocol.Response); isReply {
			m.endTurn()
			if !m.isOnScreen() {
				m.unread[reply.ConversationID] = true
			}
		}
		if w, ok := message.(*protocol.Welcome); ok {
			cmds = append(cmds, m.welcomeHistoryCmd(w))
			if m.fetchOnWelcome {
				m.fetchOnWelcome = false
				cmds = append(cmds, sendConversations(m.client))
			}
		}
	}
	return tea.Batch(cmds...)
}

// handleError shows a daemon error. With no request ids, any error ends
// the agent's chat turn; when that turn was in a conversation since left,
// the error goes into that conversation's view.
func (m *model) handleError(failure *protocol.Error) tea.Cmd {
	turnConv := m.turnConv
	turnElsewhere := m.turnActive && turnConv != "" && turnConv != m.convID
	m.endTurn()
	_, cached := m.cached[turnConv]

	var cmd tea.Cmd
	if turnElsewhere && cached {
		m.unread[turnConv] = true
		m.errorUnseen = true
		cmd = m.withConversation(turnConv, func(m *model) tea.Cmd {
			m.showError(failure)
			return nil
		})
	} else {
		m.showError(failure)
		if !m.isOnScreen() {
			m.errorUnseen = true
		}
	}
	return cmd
}

// showError adds an error to the view in place.
func (m *model) showError(failure *protocol.Error) {
	m.applyStatus(failure)
	m.entries = append(m.entries, entry{kind: entryServer, msg: failure})
	m.refresh()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.ready {
			m.vp = viewport.New(m.width, 1) // height set by relayout
			m.ready = true
		}
		m.relayout()
		if m.picker != nil {
			m.picker.setSize(m.width, m.height)
		}
		if m.switcher != nil {
			m.switcher.setSize(m.width, m.height)
		}
		m.refresh()
		return m, nil

	case recvMsg:
		var cmd tea.Cmd
		if m.tracksAgent(msg.agent, nil) {
			cmd = m.withAgent(msg.agent, func(m *model) tea.Cmd { return m.handleServerMessage(msg.msg) })
			m.syncSwitcher()
		}
		return m, tea.Batch(cmd, waitForEvent(m.pool.Events()))

	case agentStateMsg:
		var cmd tea.Cmd
		if m.tracksAgent(msg.agent, &msg) {
			cmd = m.withAgent(msg.agent, func(m *model) tea.Cmd { return m.applyAgentState(msg) })
			m.syncSwitcher()
		}
		return m, tea.Batch(cmd, waitForEvent(m.pool.Events()))

	case editorDoneMsg:
		return m.finishEditing(msg)

	case tempRemoveFailedMsg:
		m.note(fmt.Sprintf("/edit: couldn't remove the temporary file %s: %v", msg.path, msg.err))
		return m, nil

	case poolClosedMsg:
		m.quitting = true
		return m, tea.Quit

	case sendErrMsg:
		// The pool drops the broken connection and reconnects it; the pool's
		// state events drive the rest. The turn won't be answered.
		if _, open := m.agents[msg.agent]; open {
			m.withAgent(msg.agent, func(m *model) tea.Cmd {
				m.thinking = false
				m.steps = nil
				m.endTurn()
				m.note(fmt.Sprintf("— send failed: %v —", msg.err))
				return nil
			})
		}
		return m, nil

	case spinner.TickMsg:
		if !m.thinking && !m.connecting && !m.reconnecting {
			return m, nil // stop ticking once the turn lands / the (re)dial resolves
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, tea.Batch(cmd, m.maybeLoadOlder())

	case tea.FocusMsg:
		return m, m.input.Focus()

	case tea.BlurMsg:
		// A blurred textarea draws no cursor, so the focused pane is the
		// only one showing one.
		m.input.Blur()
		return m, nil

	case tea.KeyMsg:
		// A key means the pane has focus, even if the focus event was missed;
		// a blurred textarea ignores typing.
		if !m.input.Focused() {
			focusCmd := m.input.Focus()
			next, cmd := m.Update(msg)
			return next, tea.Batch(focusCmd, cmd)
		}
		if m.switcher != nil {
			if msg.Type == tea.KeyCtrlC {
				m.quitting = true
				return m, tea.Quit
			}
			return m.updateSwitcher(msg)
		}
		// While the picker overlay is open it captures keys (ctrl+c, or
		// ctrl+d on an empty input, quit; esc cancels the picker, handled
		// within it).
		if m.picker != nil {
			if msg.Type == tea.KeyCtrlC || (msg.Type == tea.KeyCtrlD && m.input.Value() == "") {
				m.quitting = true
				return m, tea.Quit
			}
			np, outcome := m.picker.Update(msg)
			m.picker = &np
			switch outcome {
			case pickerSelected:
				it, ok := np.Selected()
				m.picker = nil
				if !ok {
					return m, nil
				}
				switch m.pickerAction {
				case pickerConnect:
					return m.focusAgent(it.id)
				case pickerSetModel:
					return m, sendMessage(m.client, protocol.ClientMessage{Type: protocol.TypeSetModel, Model: it.id})
				default:
					return m, nil
				}
			case pickerCancelled:
				m.picker = nil
				return m, nil
			default:
				return m, nil
			}
		}

		if len(m.emoji.choices) > 0 && m.chooseEmoji(msg) {
			return m, nil
		}

		// Soft-newline: ctrl+j inserts a newline instead of submitting.
		// It's the portable baseline (the literal newline control code,
		// decoded by every terminal); a trailing "\" before Enter is the
		// other newline idiom, handled in submit().
		if msg.String() == "ctrl+j" {
			m.input.InsertString("\n")
			m.relayout()
			return m, nil
		}

		switch msg.Type {
		case tea.KeyCtrlC:
			// A draft is cleared first; ctrl+c on an empty input quits.
			if m.input.Value() != "" {
				m.input.Reset()
				m.historyIndex = len(m.history)
				m.historyDraft = ""
				m.relayout()
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit

		case tea.KeyEsc:
			// Esc closes overlays (handled above); on the chat it does nothing,
			// so a stray press can't quit.
			return m, nil

		case tea.KeyUp:
			if m.onFirstRow() && m.recallPrevious() {
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd

		case tea.KeyDown:
			if m.onLastRow() && m.recallNext() {
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd

		case tea.KeyCtrlD:
			// Shell EOF idiom: ctrl+d on an empty input quits; with text in
			// the box it falls through to the textarea, whose default keymap
			// binds ctrl+d to delete-character-forward (alt+d = delete word
			// forward). "/quit" always quits; ctrl+c does on an empty input.
			if m.input.Value() == "" {
				m.quitting = true
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.relayout()
			return m, cmd

		case tea.KeyCtrlO:
			return m, m.toggleMouse()

		case tea.KeyCtrlY:
			return m.openModelPicker()

		case tea.KeyCtrlR:
			return m.nextUnread()

		case tea.KeyEnter:
			return m.submit()

		case tea.KeyPgUp, tea.KeyPgDown:
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, tea.Batch(cmd, m.maybeLoadOlder())

		default:
			// Everything else (runes, backspace, ←/→/↑/↓, ctrl+a/e/k/…)
			// drives the textarea: typing, multi-line cursor motion, and
			// the readline editing bindings it ships with.
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			if (msg.Type == tea.KeyRunes && !msg.Paste) || msg.Type == tea.KeyBackspace {
				m.afterTyping(msg)
			}
			m.relayout()
			return m, cmd
		}
	}

	return m, nil
}

// toggleMouse flips mouse capture and returns the bubbletea command that
// enables or disables mouse reporting to match. With capture off, the
// terminal's native click-drag text selection works again (wheel-scroll
// stops; keyboard scroll is unaffected). The pointer receiver mutates the
// model in place — the caller returns m unchanged alongside the command.
func (m *model) toggleMouse() tea.Cmd {
	m.mouseCapture = !m.mouseCapture
	if m.mouseCapture {
		return tea.EnableMouseCellMotion
	}
	return tea.DisableMouse
}

// submit handles the Enter key: dispatch the input as a chat message or
// a slash command. Suppressed while a turn is already in flight.
func (m model) submit() (tea.Model, tea.Cmd) {
	raw := m.input.Value()

	// Trailing backslash → soft newline (the WhatsApp idiom): drop the
	// backslash, start a new line, keep composing instead of sending.
	if strings.HasSuffix(raw, "\\") {
		m.input.SetValue(strings.TrimSuffix(raw, "\\") + "\n")
		m.relayout()
		return m, nil
	}

	text := strings.TrimSpace(raw)
	if text == "" {
		return m, nil
	}
	if text == "/edit" || strings.HasPrefix(text, "/edit ") {
		m.input.Reset()
		m.relayout()
		return m.openEditor(strings.TrimSpace(strings.TrimPrefix(text, "/edit")))
	}
	if (m.thinking || m.turnActive) && !strings.HasPrefix(text, "/") {
		m.note(fmt.Sprintf("%s is still working on a reply — your message is kept; send it once that lands, or /connect to another agent", m.agentName()))
		return m, nil
	}

	// While auto-reconnecting the wire is dead. Keep a plain-chat draft (don't
	// drop the message), but let local commands — /quit, /help, /connect —
	// still run; wire-bound commands are refused below.
	if m.reconnecting && !strings.HasPrefix(text, "/") {
		m.note("reconnecting… — your message is kept; send it once you're back")
		return m, nil
	}

	m.recordHistory(text)
	m.input.Reset()
	m.relayout()
	m.entries = append(m.entries, entry{kind: entryUser, text: text})

	// Plain chat.
	if !strings.HasPrefix(text, "/") {
		m.thinking = true
		m.steps = nil
		m.startTurn()
		m.refresh()
		return m, tea.Batch(sendChat(m.client, text), m.spin.Tick)
	}

	// Slash command.
	parsed := parseCommand(text)
	if parsed.quit {
		m.quitting = true
		return m, tea.Quit
	}
	if parsed.wire != nil && (m.thinking || m.turnActive) {
		m.note(fmt.Sprintf("%s is still working on a reply — that command can wait until it lands", m.agentName()))
		return m, nil
	}
	if parsed.convFetch != convActionNone {
		if m.reconnecting {
			m.note("reconnecting… — conversation commands need a live connection")
			m.refresh()
			return m, nil
		}
		return m.startConvFetch(parsed.convFetch, parsed.switchArg)
	}
	if parsed.connect == connectActionClose {
		return m.closeAgent(parsed.connectArg)
	}
	if parsed.connect != connectActionNone {
		return m.startConnect(parsed.connect, parsed.connectArg)
	}
	if parsed.note != "" {
		m.entries = append(m.entries, entry{kind: entryNote, text: parsed.note})
	}
	if parsed.wire != nil && m.reconnecting {
		m.note("reconnecting… — that command needs a live connection")
		m.refresh()
		return m, nil
	}
	if parsed.needs != "" && !m.client.Session().Has(parsed.needs) {
		m.note(fmt.Sprintf("this agent doesn't support that command (it needs the %q capability)", parsed.needs))
		return m, nil
	}
	var cmds []tea.Cmd
	if parsed.wire != nil {
		m.thinking = parsed.thinking
		cmds = append(cmds, sendMessage(m.client, *parsed.wire))
		if parsed.thinking {
			cmds = append(cmds, m.spin.Tick)
		}
	}
	m.refresh()
	return m, tea.Batch(cmds...)
}

// ── slash commands ──────────────────────────────────────────────────

// convAction records why a `conversations` request was sent, so its
// conversations_list reply can be dispatched: render into the transcript
// (/list), open the switcher picker (/switch), or resolve an argument and
// switch directly (/switch <arg>).
type convAction int

const (
	convActionNone      convAction = iota
	convActionList                 // dump the list into the transcript
	convActionPicker               // open the switcher overlay
	convActionSwitchArg            // resolve pendingSwitchArg and switch
)

// connectAction records how /connect was invoked: not at all, with an
// instance name to dial, or bare (open the instance picker).
type connectAction int

const (
	connectActionNone   connectAction = iota
	connectActionName                 // /connect <name>
	connectActionPicker               // bare /connect → instance picker
	connectActionClose                // /close [name]
)

// command is the parsed result of a slash-command line. Exactly one of
// {wire, note, quit, convFetch, connect} drives the outcome: send a wire
// message, print a client-side note, quit, fetch the conversation list for
// a list/switch action, or (re)connect to an instance. thinking marks a
// wire command whose reply involves an LLM call (so the spinner should run).
type command struct {
	wire       *protocol.ClientMessage
	thinking   bool
	note       string
	quit       bool
	convFetch  convAction // /list, /switch
	switchArg  string     // /switch <arg>
	connect    connectAction
	connectArg string // /connect <name>
	needs      string // capability the daemon must advertise, e.g. protocol.CapPark
}

const helpText = `Commands:
  /model [alias]   show the current model, or switch to one
  /models          list available models (ctrl+y = picker)
  /new             start a new conversation
  /info            show conversation stats
  /rename [title]  set a title (blank = let the agent title it)
  /park            fold this conversation into memory, leaving it open
  /dream           run memory consolidation
  /capture <on|off|flush|status>   control request capture
  /ping            liveness check
  /list            list your conversations
  /switch [n|text] switch conversation; agent:text on another agent
                   (no arg = the agents and conversations picker)
  /connect [name]  switch to an agent, opening it if needed (no arg = picker)
  /close [name]    disconnect an agent (no arg = the one on screen)
  /edit [text]     compose a message in your editor ([ui] editor, $VISUAL, $EDITOR)
  /help            this list
  /quit            exit (also ctrl+c)

Editing: enter sends · ctrl+j (or end a line with \) inserts a newline ·
  ↑ / ↓ recall what you've sent (from the first / last line) · ctrl+c clears
  the input, and quits when it's empty.
  ctrl+d deletes the next char (quits on an empty line) · alt+d deletes
  the next word · ctrl+w / alt+backspace deletes the previous word.
Emoji: :thumbsup: becomes 👍 on the closing colon; after two letters a list
  of up to 5 matches opens (tab/enter pick, ↑/↓ move, esc close).
Unread: ctrl+r jumps to the next reply waiting, on any agent.
Mouse: ctrl+o toggles capture — turn it off to select/copy text with the
mouse (wheel-scroll pauses; PgUp/PgDn and the arrows still scroll).
Connection: drops reconnect automatically (backing off, resuming the same
conversation). Agents you switch away from stay connected and keep working;
each can run one reply at a time.`

// parseCommand maps a "/..." line to a command. Pure — no I/O — so the
// dispatch table is straightforward to test.
func parseCommand(line string) command {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return command{note: "empty command — try /help"}
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	rest := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))

	switch name {
	case "help":
		return command{note: helpText}
	case "quit", "exit":
		return command{quit: true}
	case "new":
		return command{wire: &protocol.ClientMessage{Type: protocol.TypeNew}, thinking: true}
	case "info":
		return command{wire: &protocol.ClientMessage{Type: protocol.TypeInfo}}
	case "models":
		return command{wire: &protocol.ClientMessage{Type: protocol.TypeModels}}
	case "model":
		if rest == "" {
			return command{wire: &protocol.ClientMessage{Type: protocol.TypeShowModel}}
		}
		return command{wire: &protocol.ClientMessage{Type: protocol.TypeSetModel, Model: rest}}
	case "rename":
		// A blank title asks the daemon to auto-title (an LLM call).
		return command{wire: &protocol.ClientMessage{Type: protocol.TypeRename, Title: rest}, thinking: rest == ""}
	case "dream":
		return command{wire: &protocol.ClientMessage{Type: protocol.TypeDream}, thinking: true}
	case "park":
		// The id is left empty on purpose: core.Client.Send fills it from
		// the session, which tracks /switch. thinking — the drain is an
		// LLM call, and finalizing arrives first only if there's work.
		return command{wire: &protocol.ClientMessage{Type: protocol.TypePark}, thinking: true, needs: protocol.CapPark}
	case "capture":
		action := ""
		if f := strings.Fields(rest); len(f) > 0 {
			action = strings.ToLower(f[0])
		}
		switch action {
		case "":
			return command{wire: &protocol.ClientMessage{Type: protocol.TypeCapture, Action: "status"}}
		case "on", "off", "flush", "status":
			return command{wire: &protocol.ClientMessage{Type: protocol.TypeCapture, Action: action}}
		default:
			return command{note: "usage: /capture on | off | flush | status"}
		}
	case "ping":
		return command{wire: &protocol.ClientMessage{Type: protocol.TypePing}}
	case "list", "conversations":
		return command{convFetch: convActionList}
	case "switch":
		if rest == "" {
			return command{convFetch: convActionPicker}
		}
		return command{convFetch: convActionSwitchArg, switchArg: rest}
	case "connect":
		if rest == "" {
			return command{connect: connectActionPicker}
		}
		return command{connect: connectActionName, connectArg: rest}
	case "close":
		return command{connect: connectActionClose, connectArg: rest}
	default:
		return command{note: "unknown command: /" + name + " — try /help"}
	}
}

// applyStatus folds a server message into the status-bar state. Every
// reply clears the working flag except Finalizing, which is an
// intermediate signal during /new (more is still coming).
func (m *model) applyStatus(msg protocol.ServerMessage) {
	if _, ok := msg.(*protocol.Finalizing); !ok {
		m.thinking = false
		m.steps = nil
	}
	switch typed := msg.(type) {
	case *protocol.Welcome:
		m.connected = true
		m.principal = typed.Principal
		if typed.ConversationTitle != nil {
			m.convTitle = *typed.ConversationTitle
		}
		if typed.ConversationID != nil {
			m.convID = *typed.ConversationID
		}
	case *protocol.Response:
		m.modelLabel = typed.Model
		if m.convID == "" {
			// A first chat created this conversation.
			m.convID = typed.ConversationID
		}
	case *protocol.NewConversation:
		m.convID = typed.ConversationID
		m.convTitle = ""
		// A fresh conversation has no backfill — drop the scroll-back
		// cursor so scrolling up doesn't pull the previous one's pages.
		m.histHasMore = false
		m.histOldest = nil
	case *protocol.ConversationRenamed:
		m.convTitle = typed.Title
	case *protocol.ModelShow:
		if typed.Resolved != nil {
			m.modelLabel = *typed.Resolved
		}
	case *protocol.ModelSet:
		m.modelLabel = typed.Resolved
	case *protocol.InfoResponse:
		if typed.ConversationTitle != nil {
			m.convTitle = *typed.ConversationTitle
		}
		if typed.ModelResolved != nil {
			m.modelLabel = *typed.ModelResolved
		}
	}
}

// applyHistoryPage folds a backfilled page into the transcript. History
// is always older than the live content, so a page is prepended to the
// front, expanded one entry per record (already oldest-first). The seed
// page pins the view to the bottom (ready to chat); a scroll-back page
// preserves the scroll anchor by nudging the offset down by the height
// of the freshly-prepended content, so the reader's place doesn't jump.
func (m *model) applyHistoryPage(hp *protocol.HistoryPage, scrollback bool) {
	m.histLoading = false
	m.histHasMore = hp.HasMore
	m.histOldest = hp.OldestSequence
	if hp.ConversationID != "" {
		m.histConvID = hp.ConversationID
	}

	if len(hp.Messages) == 0 {
		if !scrollback {
			m.refresh()
		}
		return
	}

	older := make([]entry, 0, len(hp.Messages))
	for _, hm := range hp.Messages {
		older = append(older, entry{kind: entryHistory, histMsg: hm})
	}

	if scrollback && m.ready {
		delta := renderedHeight(m.renderEntries(older))
		if delta > 0 {
			delta++ // the blank separator line before the existing content
		}
		m.entries = append(older, m.entries...)
		m.vp.SetContent(strings.Join(m.renderEntries(m.entries), "\n\n"))
		m.vp.SetYOffset(m.vp.YOffset + delta)
		return
	}

	m.entries = append(older, m.entries...)
	m.refresh()
}

// refresh re-renders the whole transcript at the current width and pins
// the viewport to the bottom.
func (m *model) refresh() {
	if !m.ready || !m.isOnScreen() {
		return
	}
	blocks := m.renderEntries(m.entries)
	if live := m.renderProgress(); live != "" {
		blocks = append(blocks, live)
	}
	m.vp.SetContent(strings.Join(blocks, "\n\n"))
	m.vp.GotoBottom()
}

// renderEntries renders each entry to its block string, dropping empties.
func (m *model) renderEntries(entries []entry) []string {
	blocks := make([]string, 0, len(entries))
	for _, e := range entries {
		if s := m.renderEntry(e); s != "" {
			blocks = append(blocks, s)
		}
	}
	return blocks
}

func (m *model) renderEntry(item entry) string {
	switch item.kind {
	case entryUser:
		return m.renderUser(terminalSafe(item.text))
	case entryNote:
		return m.renderNote(terminalSafe(item.text))
	case entryServer:
		return m.renderServer(item.msg)
	case entryHistory:
		return m.renderHistory(item.histMsg)
	}
	return ""
}

// renderedHeight is the display-line count of a set of rendered blocks
// once joined for the viewport (blocks separated by a blank line).
func renderedHeight(blocks []string) int {
	if len(blocks) == 0 {
		return 0
	}
	return strings.Count(strings.Join(blocks, "\n\n"), "\n") + 1
}

func (m model) View() string {
	if !m.ready {
		return "connecting…"
	}
	if m.quitting {
		if m.err != nil {
			return "disconnected: " + terminalSafe(m.err.Error()) + "\n"
		}
		return ""
	}
	if m.switcher != nil {
		return m.switcher.View()
	}
	if m.picker != nil {
		return m.picker.View()
	}

	rule := ruleStyle.Render(strings.Repeat("─", m.width))
	parts := []string{m.renderHeader(), rule, m.vp.View(), rule}
	if len(m.emoji.choices) > 0 {
		parts = append(parts, m.renderEmojiChooser())
	}
	parts = append(parts, m.input.View(), rule, m.renderFooter())
	return strings.Join(parts, "\n")
}

// agentName is how the agent in place is shown: its own display name once
// its welcome has arrived, else its config name.
func (m model) agentName() string {
	name := titleCase(m.instance)
	if profile := m.client.Session().Agent; profile != nil && profile.DisplayName != "" {
		name = profile.DisplayName
	}
	return name
}

// agentAccent is the agent's accent colour, or "" when it has none.
func (m model) agentAccent() lipgloss.Color {
	var accent lipgloss.Color
	if profile := m.client.Session().Agent; profile != nil && profile.AccentColor != "" {
		accent = lipgloss.Color(profile.AccentColor)
	}
	return accent
}

// agentLabel is the transcript label for the agent's turns, styled and
// plain: its name, in its accent colour when it has one.
func (m model) agentLabel() (string, string) {
	name := m.agentName()
	style := agentLabelStyle
	if accent := m.agentAccent(); accent != "" {
		style = style.Foreground(accent)
	}
	return style.Render(name), name
}

func (m model) renderHeader() string {
	name := " " + m.agentName() + " "
	nameStyle := headerStyle
	if accent := m.agentAccent(); accent != "" {
		nameStyle = nameStyle.Background(accent)
	}
	var parts []string
	if m.principal != "" {
		parts = append(parts, m.principal)
	}
	if m.modelLabel != "" {
		parts = append(parts, m.modelLabel)
	}
	title := m.convTitle
	if title == "" {
		title = "(no conversation)"
	}
	parts = append(parts, "\""+title+"\"")

	// The name segment is clipped first so the rest always has its width.
	name = truncate(name, m.width)
	rest := " " + strings.Join(parts, " · ")
	restWidth := max(m.width-lipgloss.Width(name), 0)
	return nameStyle.Render(name) + headerStyle.Width(restWidth).Render(truncate(rest, restWidth))
}

func (m model) renderFooter() string {
	left := footerStyle.Render("enter send · /help · ctrl+y model")
	if len(m.emoji.choices) > 0 {
		left = footerStyle.Render("tab/enter pick · ↑/↓ move · esc close")
	}

	var right string
	switch {
	case m.reconnecting:
		right = m.spin.View() + connDown.Render(fmt.Sprintf(" reconnecting… (attempt %d)", m.reconnectAttempt))
	case m.connecting:
		right = m.spin.View() + dimStyle.Render(" connecting to "+m.instance+"…")
	case m.thinking:
		right = m.spin.View() + dimStyle.Render(" "+m.progressStatus())
	case m.connected:
		right = connOK.Render("●") + dimStyle.Render(" connected")
	default:
		right = connDown.Render("○") + dimStyle.Render(" disconnected")
	}

	// What the agents off screen are doing.
	if summary := m.attentionSummary(); summary != "" {
		right += dimStyle.Render(" · " + summary)
	}

	// Mouse-capture state, so the operator can tell whether the wheel
	// scrolls (on) or native text selection is available (off). ctrl+o
	// toggles it; the hint lives in /help.
	if m.mouseCapture {
		right += dimStyle.Render(" · mouse:on")
	} else {
		right += dimStyle.Render(" · mouse:off")
	}

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// Too narrow for both: the key hints go before the live status.
		left = ""
		gap = max(m.width-lipgloss.Width(right), 0)
	}
	return left + strings.Repeat(" ", gap) + right
}

// ── transcript rendering ────────────────────────────────────────────

func (m model) renderUser(text string) string {
	w := m.bodyWidth("you")
	var lines []string
	for _, ln := range strings.Split(text, "\n") {
		lines = append(lines, wrap(ln, w)...)
	}
	return labelBlock(userLabelStyle.Render("you"), "you", lines)
}

func (m model) renderNote(text string) string {
	return dimStyle.Render(text)
}

// renderHistory renders one backfilled turn record. It reuses the same
// content-block formatting and label layout as live turns — a user
// record under the "you" label, an assistant record under the instance
// label — but carries no token-stats line (history has no usage data).
func (m model) renderHistory(hm protocol.HistoryMessage) string {
	if hm.Role == protocol.RoleUser {
		w := m.bodyWidth("you")
		return labelBlock(userLabelStyle.Render("you"), "you", m.renderContentBlocks(hm.Content, w))
	}
	label, plainLabel := m.agentLabel()
	w := m.bodyWidth(plainLabel)
	lines := m.renderContentBlocks(hm.Content, w)
	lines = append(lines, renderAttachments(hm.Attachments, w)...)
	return labelBlock(label, plainLabel, lines)
}

// renderContentBlocks formats a ContentBlock slice (shared by live
// responses and backfilled assistant turns) into wrapped body lines.
func (m model) renderContentBlocks(content []protocol.ContentBlock, w int) []string {
	var lines []string
	for _, blk := range content {
		switch blk.Type {
		case "text":
			lines = append(lines, m.renderText(blk.Text, w)...)
		case "tool_use":
			lines = append(lines, dimStyle.Render("[tool: "+blk.Name+"]"))
		case "tool_result":
			lines = append(lines, dimStyle.Render("[tool result for "+blk.ToolUseID+"]"))
		case "image":
			lines = append(lines, dimStyle.Render(fmt.Sprintf("[image: %s, %d bytes]", blk.MediaType, len(blk.Data))))
		}
	}
	return lines
}

// renderAttachments lists a turn's attached files, one per line, wrapped to width.
func renderAttachments(attachments []protocol.Attachment, width int) []string {
	var lines []string
	for _, attachment := range attachments {
		details := fmt.Sprintf("%s · %s · %s", attachment.Filename, attachment.MIME, formatBytes(attachment.SizeBytes))
		style := attachmentStyle
		if attachment.Expired {
			details += " · expired"
			style = dimStyle
		}
		// Continuation lines indent under the filename, past the glyph.
		for index, line := range wrap(details, width-2) {
			prefix := "  "
			if index == 0 {
				prefix = "⎘ "
			}
			lines = append(lines, style.Render(prefix+strings.TrimRight(line, " ")))
		}
	}
	return lines
}

// renderText renders an agent text block as Markdown (glamour) wrapped to
// width w, falling back to plain wrap() if the renderer is absent. The
// renderer itself degrades to wrap() on a parse error (see mdRenderer), so
// content is never lost.
func (m model) renderText(text string, w int) []string {
	if m.md == nil {
		return wrap(text, w)
	}
	return m.md.render(text, w)
}

func (m model) renderServer(msg protocol.ServerMessage) string {
	switch typed := msg.(type) {
	case *protocol.Welcome:
		if typed.ConversationTitle != nil {
			return systemStyle.Render(fmt.Sprintf("— connected as %s · resumed “%s” —", typed.Principal, *typed.ConversationTitle))
		}
		return systemStyle.Render(fmt.Sprintf("— connected as %s · no active conversation —", typed.Principal))

	case *protocol.Response:
		label, plainLabel := m.agentLabel()
		w := m.bodyWidth(plainLabel)
		lines := m.renderContentBlocks(typed.Content, w)
		lines = append(lines, renderAttachments(typed.Attachments, w)...)
		lines = append(lines, dimStyle.Render(fmt.Sprintf("%s in · %s out · %s",
			formatTokens(typed.Usage.InputTokens), formatTokens(typed.Usage.OutputTokens), formatDuration(typed.ElapsedMS))))
		return labelBlock(label, plainLabel, lines)

	case *protocol.Error:
		return errorStyle.Render("Error: ") + typed.Message

	case *protocol.NewConversation:
		return systemStyle.Render("— new conversation —")

	case *protocol.Finalizing:
		// Precedes both /new and /park, so the wording names the drain
		// rather than either command's effect on the thread.
		return systemStyle.Render("— folding conversation into memory… —")

	case *protocol.ConversationParked:
		title := convTitleOrPlaceholder(typed.ConversationTitle)
		// parked=false is a successful no-op, never a failed fold — a
		// failure arrives as an Error. Said differently so the two can't
		// be mistaken for each other.
		if !typed.Parked {
			return systemStyle.Render(fmt.Sprintf("— %s was already consolidated — nothing to park —", title))
		}
		turns := "turns"
		if typed.TurnsParked == 1 {
			turns = "turn"
		}
		return systemStyle.Render(fmt.Sprintf("— parked %s: %d %s folded into memory · still open —",
			title, typed.TurnsParked, turns))

	case *protocol.ConversationRenamed:
		return systemStyle.Render(fmt.Sprintf("— %s-titled: %s —", typed.Source, typed.Title))

	case *protocol.ModelsList:
		cur := ""
		if typed.Current != nil {
			cur = *typed.Current
		}
		lines := make([]string, 0, len(typed.Aliases))
		for _, a := range typed.Aliases {
			marker := "  "
			if a.Name == cur {
				marker = "* "
			}
			lines = append(lines, fmt.Sprintf("%s%-14s → %s", marker, a.Name, a.Target))
		}
		return infoBlock("Models", lines)

	case *protocol.ModelSet:
		return systemStyle.Render(fmt.Sprintf("— model set: %s → %s —", typed.Model, typed.Resolved))

	case *protocol.ModelShow:
		if typed.Alias == nil {
			return systemStyle.Render("— no active conversation —")
		}
		line := *typed.Alias
		if typed.Resolved != nil {
			line += " → " + *typed.Resolved
		}
		return infoBlock("Model", []string{line})

	case *protocol.InfoResponse:
		title := "(none)"
		if typed.ConversationTitle != nil {
			title = *typed.ConversationTitle
		}
		modelLine := "(unset)"
		if typed.ModelAlias != nil {
			modelLine = *typed.ModelAlias
			if typed.ModelResolved != nil {
				modelLine += " → " + *typed.ModelResolved
			}
		}
		return infoBlock("Info", []string{
			"title:   " + title,
			"model:   " + modelLine,
			fmt.Sprintf("turns:   %d", typed.TurnCount),
			fmt.Sprintf("tokens:  ~%s (watermark %s / threshold %s)",
				formatTokens(typed.EstimatedTokens), formatTokens(typed.ConsolidationWatermark), formatTokens(typed.ConsolidationThreshold)),
			fmt.Sprintf("locale:  %s (%s)", typed.Locale, typed.LocaleSource),
		})

	case *protocol.CaptureStatus:
		state := "off"
		if typed.Enabled {
			state = "on"
		}
		return infoBlock("Capture", []string{
			"state:   " + state,
			fmt.Sprintf("files:   %d", typed.FileCount),
			"dir:     " + typed.Directory,
		})

	case *protocol.DreamResult:
		lines := []string{
			fmt.Sprintf("dreamed: %t", typed.Dreamed),
			fmt.Sprintf("files:   %d", typed.FilesProcessed),
			fmt.Sprintf("updated: long_term=%t · core=%t", typed.LongTermUpdated, typed.CoreUpdated),
		}
		if len(typed.BioUpdates) > 0 {
			lines = append(lines, "bios:    "+strings.Join(typed.BioUpdates, ", "))
		}
		lines = append(lines, "took:    "+formatDuration(typed.DurationMS))
		return infoBlock("Dream", lines)

	case *protocol.ConversationsList:
		if len(typed.Conversations) == 0 {
			return infoBlock("Conversations", []string{"(none)"})
		}
		lines := make([]string, 0, len(typed.Conversations))
		for i, c := range typed.Conversations {
			lines = append(lines, fmt.Sprintf("%2d. %s  %s",
				i+1, convTitleOrPlaceholder(c.Title),
				dimStyle.Render(fmt.Sprintf("· %d turns · %s", c.TurnCount, shortTime(c.UpdatedAt)))))
		}
		return infoBlock("Conversations  (/switch <n> or /switch <text>)", lines)

	case *protocol.Pong:
		return systemStyle.Render("— pong —")

	default:
		return systemStyle.Render(fmt.Sprintf("(%T)", typed))
	}
}

// infoBlock renders a command reply: a coloured title with indented
// detail lines beneath.
func infoBlock(title string, lines []string) string {
	var b strings.Builder
	b.WriteString(metaStyle.Render(title))
	for _, ln := range lines {
		b.WriteString("\n  " + ln)
	}
	return b.String()
}

// labelBlock lays out a labelled entry: the (styled) label and the first
// body line on row one, with continuation lines indented to align under
// the body. plainLabel is the label without styling, used to size the
// indent.
func labelBlock(label, plainLabel string, lines []string) string {
	indent := lipgloss.Width(plainLabel) + 2
	pad := strings.Repeat(" ", indent)

	if len(lines) == 0 {
		return label
	}
	var builder strings.Builder
	for i, ln := range lines {
		if i == 0 {
			builder.WriteString(label + "  " + ln)
		} else {
			builder.WriteString("\n" + pad + ln)
		}
	}
	return builder.String()
}

// bodyWidth is the wrap width for an entry body, accounting for the
// label indent. Falls back to a sane minimum before the first resize.
func (m model) bodyWidth(label string) int {
	w := m.vp.Width - (len(label) + 2)
	if w < 20 {
		w = 20
	}
	return w
}

// wrap soft-wraps plain text to width w, returning the lines.
func wrap(s string, w int) []string {
	if w < 1 {
		w = 1
	}
	wrapped := lipgloss.NewStyle().Width(w).Render(s)
	return strings.Split(wrapped, "\n")
}

// truncate clips s (assumed ANSI-free) to a display width of max,
// appending an ellipsis when it overflows.
func truncate(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= max {
		return text
	}
	r := []rune(text)
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// formatTokens renders a token count compactly: 1234 → "1.2k", 800 → "800".
func formatTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// formatBytes renders a file size: 800 → "800 B", 421888 → "412.0 KB".
func formatBytes(size int) string {
	const kibibyte = 1024
	switch {
	case size >= kibibyte*kibibyte:
		return fmt.Sprintf("%.1f MB", float64(size)/(kibibyte*kibibyte))
	case size >= kibibyte:
		return fmt.Sprintf("%.1f KB", float64(size)/kibibyte)
	}
	return fmt.Sprintf("%d B", size)
}

// formatDuration renders elapsed milliseconds: 4123 → "4.1s", 820 → "820ms".
func formatDuration(ms int) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}
