package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// Slash commands in the GUI, matching the TUI's (tui/chat.go parseCommand),
// and the replies they bring back, worded as the TUI words them.

// listIntent is why a conversations request was sent, so its reply can go
// to the right place.
type listIntent int

const (
	listForPanel      listIntent = iota // the conversations panel
	listForTranscript                   // /list
	listForSwitch                       // /switch <text>, resolved on arrival
)

// ModelRow is one configured model alias.
type ModelRow struct {
	Name    string `json:"name"`
	Target  string `json:"target"`
	Current bool   `json:"current"`
}

// ModelsMsg is an agent's model aliases, for the model picker.
type ModelsMsg struct {
	Agent  string     `json:"agent"`
	Models []ModelRow `json:"models"`
}

// Frontend event names for commands.
const (
	eventModels = "chat:models"
	eventPanel  = "app:panel" // open the conversations panel
)

// guiHelp is /help's text.
const guiHelp = `Commands:
  /model [alias]      show the current model, or switch to one (or click the model above)
  /models             list the configured models
  /new                start a new conversation
  /info               show conversation stats
  /rename [title]     set a title (blank = let the agent title it)
  /park               fold this conversation into memory, leaving it open
  /dream              run memory consolidation
  /capture <on|off|flush|status>   control request capture
  /ping               liveness check
  /list               list your conversations
  /switch [n|text]    switch conversation; agent:text on another agent (no arg = the panel)
  /connect <name>     switch to an agent, opening it if needed
  /close [name]       disconnect an agent
Keys: Enter sends · Shift+Enter inserts a newline · ctrl+r jumps to the next reply waiting.`

// Command runs a slash command typed into an agent's input. The line is
// echoed into the transcript; the reply, if any, arrives as entries.
func (a *App) Command(agent, line string) error {
	status, err := a.statusOf(agent)
	if err != nil {
		return err
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	rest := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))

	a.mu.Lock()
	convID, busy := status.convID, status.turnActive || status.commandPending != ""
	a.mu.Unlock()
	a.emit(eventEntry, Entry{Agent: agent, ConversationID: convID, Role: "you", Kind: "command", Text: line})

	var wire *protocol.ClientMessage
	awaits := "" // the reply type that ends a command the model has to work on
	needs := ""  // a capability the agent must advertise
	switch name {
	case "help":
		a.emitNote(agent, guiHelp)
	case "new":
		wire, awaits = &protocol.ClientMessage{Type: protocol.TypeNew}, protocol.TypeNewConversation
	case "info":
		wire = &protocol.ClientMessage{Type: protocol.TypeInfo}
	case "models":
		wire = &protocol.ClientMessage{Type: protocol.TypeModels}
	case "model":
		wire = &protocol.ClientMessage{Type: protocol.TypeShowModel}
		if rest != "" {
			wire = &protocol.ClientMessage{Type: protocol.TypeSetModel, Model: rest}
		}
	case "rename":
		wire = &protocol.ClientMessage{Type: protocol.TypeRename, Title: rest}
		if rest == "" {
			awaits = protocol.TypeConversationRenamed
		}
	case "dream":
		wire, awaits = &protocol.ClientMessage{Type: protocol.TypeDream}, protocol.TypeDreamResult
	case "park":
		wire, awaits, needs = &protocol.ClientMessage{Type: protocol.TypePark}, protocol.TypeConversationParked, protocol.CapPark
	case "capture":
		action := "status"
		if parts := strings.Fields(rest); len(parts) > 0 {
			action = strings.ToLower(parts[0])
		}
		switch action {
		case "on", "off", "flush", "status":
			wire = &protocol.ClientMessage{Type: protocol.TypeCapture, Action: action}
		default:
			a.emitNote(agent, "usage: /capture on | off | flush | status")
		}
	case "ping":
		wire = &protocol.ClientMessage{Type: protocol.TypePing}
	case "list", "conversations":
		return a.requestList(agent, status, listForTranscript, "")
	case "switch":
		return a.switchCommand(agent, status, rest)
	case "connect":
		if rest == "" {
			a.emitNote(agent, "pick an agent in the sidebar, or /connect <name>")
			return nil
		}
		return a.Connect(rest)
	case "close":
		_, err = a.Close(rest)
		return err
	default:
		a.emitNote(agent, fmt.Sprintf("unknown command: /%s — try /help", name))
	}
	if wire == nil {
		return nil
	}

	session, _ := a.pool.Session(agent)
	switch {
	case needs != "" && !session.Has(needs):
		a.emitNote(agent, fmt.Sprintf("this agent doesn't support that command (it needs the %q capability)", needs))
		return nil
	case busy:
		return fmt.Errorf("%s is still working on a reply; that command can wait until it lands", agent)
	default:
		// Send it.
	}
	if awaits != "" {
		a.mu.Lock()
		status.commandPending = awaits
		a.mu.Unlock()
		a.emit(eventThinking, ThinkingMsg{Agent: agent, Thinking: true})
	}
	sendErr := a.pool.Send(agent, *wire)
	if sendErr != nil && awaits != "" {
		a.endCommand(agent, status)
	}
	return sendErr
}

// Models asks an agent for its model aliases, for the model picker.
func (a *App) Models(agent string) error {
	status, err := a.statusOf(agent)
	if err != nil {
		return err
	}
	a.mu.Lock()
	status.modelsForPicker = true
	a.mu.Unlock()
	return a.pool.Send(agent, protocol.ClientMessage{Type: protocol.TypeModels})
}

// SetModel switches the agent's conversation to a model alias.
func (a *App) SetModel(agent, alias string) error {
	if _, err := a.statusOf(agent); err != nil {
		return err
	}
	return a.pool.Send(agent, protocol.ClientMessage{Type: protocol.TypeSetModel, Model: alias})
}

// requestList sends a conversations request, recording why.
func (a *App) requestList(agent string, status *agentStatus, intent listIntent, arg string) error {
	session, _ := a.pool.Session(agent)
	if !session.Has(protocol.CapConversations) {
		a.emitNote(agent, "conversation listing/switching needs a protocol v2 daemon")
		return nil
	}
	a.mu.Lock()
	status.listIntent, status.switchArg = intent, arg
	a.mu.Unlock()
	return a.pool.Send(agent, protocol.ClientMessage{Type: protocol.TypeConversations})
}

// switchCommand handles /switch: no argument opens the panel; "agent:text"
// moves to that agent first; otherwise the text is a /list number or a
// title to match.
func (a *App) switchCommand(agent string, status *agentStatus, arg string) error {
	if arg == "" {
		a.emit(eventPanel, agent)
		return nil
	}
	if target, text, found := strings.Cut(arg, ":"); found {
		target = strings.TrimSpace(target)
		a.mu.Lock()
		_, configured := a.cfg.Instances[target]
		a.mu.Unlock()
		if configured {
			if err := a.Connect(target); err != nil {
				return err
			}
			targetStatus, err := a.statusOf(target)
			if err != nil {
				return err
			}
			return a.requestList(target, targetStatus, listForSwitch, strings.TrimSpace(text))
		}
	}
	return a.requestList(agent, status, listForSwitch, arg)
}

// resolveSwitch finds the conversation a /switch argument names: a 1-based
// /list number, or a unique case-insensitive title match.
func resolveSwitch(conversations []protocol.ConversationSummary, arg string) (protocol.ConversationSummary, string) {
	var found protocol.ConversationSummary
	problem := ""
	if index, err := strconv.Atoi(arg); err == nil {
		if index >= 1 && index <= len(conversations) {
			found = conversations[index-1]
		} else {
			problem = fmt.Sprintf("no conversation %d — /list shows %d", index, len(conversations))
		}
		return found, problem
	}
	var matches []protocol.ConversationSummary
	for _, summary := range conversations {
		if summary.Title != nil && strings.Contains(strings.ToLower(*summary.Title), strings.ToLower(arg)) {
			matches = append(matches, summary)
		}
	}
	switch len(matches) {
	case 0:
		problem = fmt.Sprintf("no conversation matches %q", arg)
	case 1:
		found = matches[0]
	default:
		problem = fmt.Sprintf("%d conversations match %q — be more specific, or use the panel", len(matches), arg)
	}
	return found, problem
}

// endCommand clears a pending command and, unless a chat turn is still
// running, the working state.
func (a *App) endCommand(agent string, status *agentStatus) {
	a.mu.Lock()
	status.commandPending = ""
	chatRunning := status.turnActive
	a.mu.Unlock()
	if !chatRunning {
		a.emit(eventThinking, ThinkingMsg{Agent: agent, Thinking: false})
	}
}

// settleCommand ends a pending command when its reply (or an error)
// arrives.
func (a *App) settleCommand(agent string, status *agentStatus, msg protocol.ServerMessage) {
	a.mu.Lock()
	pending := status.commandPending
	a.mu.Unlock()
	if pending == "" {
		return
	}
	_, isError := msg.(*protocol.Error)
	if isError || messageType(msg) == pending {
		a.endCommand(agent, status)
	}
}

// messageType is a server message's wire type, for the replies commands
// wait on.
func messageType(msg protocol.ServerMessage) string {
	kind := ""
	switch msg.(type) {
	case *protocol.NewConversation:
		kind = protocol.TypeNewConversation
	case *protocol.ConversationRenamed:
		kind = protocol.TypeConversationRenamed
	case *protocol.DreamResult:
		kind = protocol.TypeDreamResult
	case *protocol.ConversationParked:
		kind = protocol.TypeConversationParked
	default:
		// Not a reply any command waits on.
	}
	return kind
}

// modelRows builds the model picker's rows.
func modelRows(list *protocol.ModelsList) []ModelRow {
	current := ""
	if list.Current != nil {
		current = *list.Current
	}
	rows := make([]ModelRow, 0, len(list.Aliases))
	for _, alias := range list.Aliases {
		rows = append(rows, ModelRow{Name: alias.Name, Target: alias.Target, Current: alias.Name == current})
	}
	return rows
}

// ── reply wording (pure; the TUI's, as plain text) ──────────────────────

// info is a titled block of lines, the form command replies take.
func info(title string, lines ...string) []Entry {
	return []Entry{{Role: "system", Kind: "info", Title: title, Text: strings.Join(lines, "\n")}}
}

// note is a one-line system note.
func note(text string) []Entry {
	return []Entry{{Role: "note", Kind: "note", Text: text}}
}

// translateCommandReply words the replies to slash commands, and returns
// nil for anything else.
func translateCommandReply(msg protocol.ServerMessage) []Entry {
	var entries []Entry
	switch typed := msg.(type) {
	case *protocol.NewConversation:
		entries = note("new conversation")
	case *protocol.Finalizing:
		entries = note("folding conversation into memory…")
	case *protocol.ConversationParked:
		title := titleOrUntitled(typed.ConversationTitle)
		switch {
		case !typed.Parked:
			entries = note(fmt.Sprintf("%s was already consolidated — nothing to park", title))
		case typed.TurnsParked == 1:
			entries = note(fmt.Sprintf("parked %s: 1 turn folded into memory · still open", title))
		default:
			entries = note(fmt.Sprintf("parked %s: %d turns folded into memory · still open", title, typed.TurnsParked))
		}
	case *protocol.ModelsList:
		current := ""
		if typed.Current != nil {
			current = *typed.Current
		}
		lines := make([]string, 0, len(typed.Aliases))
		for _, alias := range typed.Aliases {
			marker := "  "
			if alias.Name == current {
				marker = "* "
			}
			lines = append(lines, fmt.Sprintf("%s%-14s → %s", marker, alias.Name, alias.Target))
		}
		entries = info("Models", lines...)
	case *protocol.ModelSet:
		entries = note(fmt.Sprintf("model set: %s → %s", typed.Model, typed.Resolved))
	case *protocol.ModelShow:
		if typed.Alias == nil {
			entries = note("no active conversation")
		} else {
			line := *typed.Alias
			if typed.Resolved != nil {
				line += " → " + *typed.Resolved
			}
			entries = info("Model", line)
		}
	case *protocol.InfoResponse:
		modelLine := "(unset)"
		if typed.ModelAlias != nil {
			modelLine = *typed.ModelAlias
			if typed.ModelResolved != nil {
				modelLine += " → " + *typed.ModelResolved
			}
		}
		entries = info("Info",
			"title:   "+titleOrNone(typed.ConversationTitle),
			"model:   "+modelLine,
			fmt.Sprintf("turns:   %d", typed.TurnCount),
			fmt.Sprintf("tokens:  ~%d (watermark %d / threshold %d)", typed.EstimatedTokens, typed.ConsolidationWatermark, typed.ConsolidationThreshold),
			fmt.Sprintf("locale:  %s (%s)", typed.Locale, typed.LocaleSource))
	case *protocol.CaptureStatus:
		state := "off"
		if typed.Enabled {
			state = "on"
		}
		entries = info("Capture", "state:   "+state, fmt.Sprintf("files:   %d", typed.FileCount), "dir:     "+typed.Directory)
	case *protocol.DreamResult:
		lines := []string{
			fmt.Sprintf("dreamed: %t", typed.Dreamed),
			fmt.Sprintf("files:   %d", typed.FilesProcessed),
			fmt.Sprintf("updated: long_term=%t · core=%t", typed.LongTermUpdated, typed.CoreUpdated),
		}
		if len(typed.BioUpdates) > 0 {
			lines = append(lines, "bios:    "+strings.Join(typed.BioUpdates, ", "))
		}
		lines = append(lines, fmt.Sprintf("took:    %.1fs", float64(typed.DurationMS)/1000))
		entries = info("Dream", lines...)
	default:
		// Not a command reply.
	}
	return entries
}

// conversationListEntry words a conversation list for /list.
func conversationListEntry(list *protocol.ConversationsList) []Entry {
	if len(list.Conversations) == 0 {
		return info("Conversations", "(none)")
	}
	lines := make([]string, 0, len(list.Conversations))
	for index, summary := range list.Conversations {
		lines = append(lines, fmt.Sprintf("%2d. %s · %d turns · %s", index+1, titleOrUntitled(summary.Title), summary.TurnCount, shortTime(summary.UpdatedAt)))
	}
	return info("Conversations (/switch <n> or /switch <text>)", lines...)
}

// titleOrUntitled renders a possibly missing title.
func titleOrUntitled(title *string) string {
	text := "(untitled)"
	if title != nil && *title != "" {
		text = *title
	}
	return text
}

// titleOrNone renders a possibly missing title for /info.
func titleOrNone(title *string) string {
	text := "(none)"
	if title != nil {
		text = *title
	}
	return text
}

// shortTime trims an ISO-8601 timestamp to "YYYY-MM-DD HH:MM".
func shortTime(iso string) string {
	short := iso
	if len(iso) >= 16 {
		short = strings.Replace(iso[:16], "T", " ", 1)
	}
	return short
}
