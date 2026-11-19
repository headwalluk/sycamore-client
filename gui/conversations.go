package main

import (
	"fmt"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// Conversations: history, the conversation list, switching, and unread
// marks. The frontend keeps a transcript per conversation; everything sent
// to it is tagged with the conversation it belongs to. See
// dev-notes/planning-multi-agent.md §4 (the TUI's version of the same).

// historyPageLimit is the turn count requested per history page.
const historyPageLimit = 50

// HistoryMsg is one page of a conversation's history. Older pages go above
// what the frontend has; a seed (Older false) replaces it.
type HistoryMsg struct {
	Agent          string  `json:"agent"`
	ConversationID string  `json:"conversationId"`
	Entries        []Entry `json:"entries"`
	HasMore        bool    `json:"hasMore"`
	Older          bool    `json:"older"`
}

// ConversationRow is one conversation in an agent's list.
type ConversationRow struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	TurnCount int    `json:"turnCount"`
	UpdatedAt string `json:"updatedAt"`
	Current   bool   `json:"current"`
	Unread    bool   `json:"unread"`
	Working   bool   `json:"working"`
}

// ConversationsMsg is an agent's conversation list.
type ConversationsMsg struct {
	Agent         string            `json:"agent"`
	Conversations []ConversationRow `json:"conversations"`
}

// ConversationMsg says which conversation an agent now shows. Adopt means a
// first chat just created it: the frontend moves its "no conversation"
// transcript under the new id.
type ConversationMsg struct {
	Agent          string `json:"agent"`
	ConversationID string `json:"conversationId"`
	Adopt          bool   `json:"adopt,omitempty"`
}

// UnreadMsg lists an agent's conversations with a reply not yet seen.
type UnreadMsg struct {
	Agent         string   `json:"agent"`
	Conversations []string `json:"conversations"`
}

// Frontend event names for conversations.
const (
	eventHistory       = "chat:history"
	eventConversations = "chat:conversations"
	eventConversation  = "chat:conversation"
	eventUnread        = "chat:unread"
)

// ── bound methods ───────────────────────────────────────────────────────

// Conversations asks an agent for its conversation list; the reply arrives
// as a chat:conversations event.
func (a *App) Conversations(agent string) error {
	status, err := a.statusOf(agent)
	if err != nil {
		return err
	}
	session, _ := a.pool.Session(agent)
	if !session.Has(protocol.CapConversations) {
		return fmt.Errorf("%s can't list conversations (it needs a protocol v2 daemon)", agent)
	}
	a.mu.Lock()
	status.listPending = true
	status.listIntent, status.switchArg = listForPanel, ""
	a.mu.Unlock()
	return a.pool.Send(agent, protocol.ClientMessage{Type: protocol.TypeConversations})
}

// SwitchConversation shows one of an agent's conversations: the session
// follows, the frontend shows what it has for it at once, and a fresh page
// of history replaces that when it arrives. A reply still running elsewhere
// lands in its own conversation.
func (a *App) SwitchConversation(agent, id, title string) error {
	status, err := a.statusOf(agent)
	if err != nil {
		return err
	}
	a.pool.SetConversation(agent, id)
	a.mu.Lock()
	status.convID = id
	status.conversationTitle = title
	a.mu.Unlock()
	a.emit(eventConversation, ConversationMsg{Agent: agent, ConversationID: id})
	a.emit(eventSession, a.sessionSnapshot(agent, status))
	a.markSeen(agent)
	return a.fetchHistory(agent, status, id, 0)
}

// LoadOlder asks for the page of history before what the agent's current
// conversation shows. It does nothing when there's no more, or a page is
// already on its way.
func (a *App) LoadOlder(agent string) error {
	status, err := a.statusOf(agent)
	if err != nil {
		return err
	}
	a.mu.Lock()
	convID, oldest, more, loading := status.convID, status.histOldest, status.histHasMore, status.histLoading
	a.mu.Unlock()
	if convID == "" || !more || loading || oldest == nil {
		return nil
	}
	return a.fetchHistory(agent, status, convID, *oldest)
}

// statusOf returns an open agent's bookkeeping.
func (a *App) statusOf(agent string) (*agentStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	status, open := a.agents[agent]
	if !open {
		return nil, fmt.Errorf("%s isn't open", agent)
	}
	return status, nil
}

// fetchHistory requests a page of history; beforeSequence 0 is the newest.
func (a *App) fetchHistory(agent string, status *agentStatus, convID string, beforeSequence int) error {
	session, _ := a.pool.Session(agent)
	if !session.Has(protocol.CapHistory) {
		return nil
	}
	a.mu.Lock()
	status.histLoading = true
	status.histOlder = beforeSequence != 0
	a.mu.Unlock()
	return a.pool.Send(agent, protocol.ClientMessage{Type: protocol.TypeHistory, ConversationID: convID, BeforeSequence: beforeSequence, Limit: historyPageLimit})
}

// ── handling ────────────────────────────────────────────────────────────

// handleHistoryPage sends a page of history, as entries, for its
// conversation.
func (a *App) handleHistoryPage(agent string, status *agentStatus, page *protocol.HistoryPage) {
	a.mu.Lock()
	older := status.histOlder
	if page.ConversationID == status.convID {
		status.histLoading = false
		status.histHasMore = page.HasMore
		status.histOldest = page.OldestSequence
	}
	a.mu.Unlock()

	entries := make([]Entry, 0, len(page.Messages))
	for _, record := range page.Messages {
		entry := Entry{Agent: agent, ConversationID: page.ConversationID, Role: "agent", Kind: "history", Text: blocksToText(record.Content)}
		if record.Role == protocol.RoleUser {
			entry.Role = "you"
		} else {
			entry.HTML = blocksToHTML(record.Content)
			entry.Model = record.Model
		}
		entries = append(entries, entry)
	}
	a.emit(eventHistory, HistoryMsg{Agent: agent, ConversationID: page.ConversationID, Entries: entries, HasMore: page.HasMore, Older: older})
}

// handleConversationsList sends an agent's list, with its markers.
func (a *App) handleConversationsList(agent string, status *agentStatus, list *protocol.ConversationsList) {
	a.mu.Lock()
	status.listPending = false
	rememberTitle(status, list)
	rows := make([]ConversationRow, 0, len(list.Conversations))
	for _, summary := range list.Conversations {
		title := "(untitled)"
		if summary.Title != nil && *summary.Title != "" {
			title = *summary.Title
		}
		rows = append(rows, ConversationRow{
			ID:        summary.ID,
			Title:     title,
			TurnCount: summary.TurnCount,
			UpdatedAt: summary.UpdatedAt,
			Current:   summary.ID == status.convID,
			Unread:    status.unread[summary.ID],
			Working:   status.turnActive && summary.ID == status.turnConv,
		})
	}
	intent, arg, convID := status.listIntent, status.switchArg, status.convID
	status.listIntent, status.switchArg = listForPanel, ""
	a.mu.Unlock()
	a.emit(eventConversations, ConversationsMsg{Agent: agent, Conversations: rows})

	switch intent {
	case listForTranscript:
		for _, entry := range conversationListEntry(list) {
			entry.Agent, entry.ConversationID = agent, convID
			a.emit(eventEntry, entry)
		}
	case listForSwitch:
		found, problem := resolveSwitch(list.Conversations, arg)
		if problem != "" {
			a.emitNote(agent, problem)
			return
		}
		if err := a.SwitchConversation(agent, found.ID, titleOrUntitled(found.Title)); err != nil {
			a.emitNote(agent, fmt.Sprintf("couldn't switch: %v", err))
		}
	default:
		// The panel has what it needs.
	}
}

// trackConversation follows which conversation the agent shows: a welcome
// or a new conversation sets it (/new switches the frontend to it), and the
// first chat's reply names the one it created. It returns the conversation a message belongs to, for
// tagging.
func (a *App) trackConversation(agent string, status *agentStatus, msg protocol.ServerMessage) string {
	a.mu.Lock()
	var adopted, fresh string
	switch typed := msg.(type) {
	case *protocol.Welcome:
		status.convID = ""
		if typed.ConversationID != nil {
			status.convID = *typed.ConversationID
		}
	case *protocol.NewConversation:
		status.convID = typed.ConversationID
		fresh = typed.ConversationID
	case *protocol.Response:
		if status.convID == "" {
			status.convID = typed.ConversationID
			adopted = typed.ConversationID
		}
	default:
		// Other messages don't move the conversation.
	}
	belongsTo := status.convID
	if named, carries := protocol.ConversationOf(msg); carries {
		belongsTo = named
	}
	a.mu.Unlock()
	if adopted != "" {
		a.emit(eventConversation, ConversationMsg{Agent: agent, ConversationID: adopted, Adopt: true})
	}
	if fresh != "" {
		a.emit(eventConversation, ConversationMsg{Agent: agent, ConversationID: fresh})
	}
	return belongsTo
}

// markReply records a reply's conversation as unread unless it's the one on
// screen.
func (a *App) markReply(agent string, status *agentStatus, convID string) {
	a.mu.Lock()
	onScreen := agent == a.active && convID == status.convID
	if !onScreen {
		status.unread[convID] = true
	}
	a.mu.Unlock()
	if !onScreen {
		a.emitUnread(agent, status)
	}
}

// markSeen clears the unread mark of the conversation the agent shows, and
// its unseen-error flag.
func (a *App) markSeen(agent string) {
	a.mu.Lock()
	status, open := a.agents[agent]
	changed := open && status.unread[status.convID]
	if changed {
		delete(status.unread, status.convID)
	}
	if open {
		status.errorUnseen = false
	}
	a.mu.Unlock()
	if changed {
		a.emitUnread(agent, status)
	}
}

// emitUnread sends an agent's unread conversations.
func (a *App) emitUnread(agent string, status *agentStatus) {
	a.mu.Lock()
	ids := make([]string, 0, len(status.unread))
	for id := range status.unread {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	a.emit(eventUnread, UnreadMsg{Agent: agent, Conversations: ids})
}
