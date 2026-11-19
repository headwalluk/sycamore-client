package main

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// Each agent shows one conversation at a time and keeps the views of the
// ones it recently showed, so switching back is instant and replies for a
// conversation you've left land in its own view. See
// dev-notes/planning-multi-agent.md §4.

// handleOffScreenConversation applies a message for a conversation other
// than the one the agent shows: into its cached view when there is one. A
// reply always ends the agent's turn and marks the conversation unread.
func (m *model) handleOffScreenConversation(id string, message protocol.ServerMessage) tea.Cmd {
	if _, isReply := message.(*protocol.Response); isReply {
		m.endTurn()
		m.unread[id] = true
	}
	var cmd tea.Cmd
	if _, cached := m.cached[id]; cached {
		cmd = m.withConversation(id, func(m *model) tea.Cmd { return m.handleServerMessage(message) })
	}
	return cmd
}

// withConversation runs handle with a cached conversation's view in place
// of the one shown, then swaps them back. Nothing is drawn meanwhile.
func (m *model) withConversation(id string, handle func(*model) tea.Cmd) tea.Cmd {
	shown := m.convView
	m.convView = *m.cached[id]
	m.viewingOther = true
	cmd := handle(m)
	m.viewingOther = false
	updated := m.convView
	m.cached[id] = &updated
	m.convView = shown
	return cmd
}

// parkView moves the view shown into the cache, evicting the least
// recently shown views beyond the cache limit. A view with a turn in
// flight is never evicted.
func (m *model) parkView() {
	if m.convID == "" {
		return
	}
	parked := m.convView
	m.cached[m.convID] = &parked
	m.cacheOrder = append(slices.DeleteFunc(m.cacheOrder, func(id string) bool { return id == parked.convID }), parked.convID)

	var kept []string
	excess := len(m.cacheOrder) - m.cacheLimit
	for _, id := range m.cacheOrder {
		evict := excess > 0 && !(m.turnActive && id == m.turnConv)
		if evict {
			delete(m.cached, id)
			excess--
			continue
		}
		kept = append(kept, id)
	}
	m.cacheOrder = kept
}

// takeCached removes and returns a cached view.
func (m *model) takeCached(id string) (convView, bool) {
	cached, found := m.cached[id]
	var view convView
	if found {
		view = *cached
		delete(m.cached, id)
		m.cacheOrder = slices.DeleteFunc(m.cacheOrder, func(cachedID string) bool { return cachedID == id })
	}
	return view, found
}

// dropCache forgets every cached view, as after a reconnect, when they may
// be missing turns. Unread marks are kept.
func (m *model) dropCache() {
	m.cached = map[string]*convView{}
	m.cacheOrder = nil
}

// markSeen clears the unread and error marks for the conversation shown,
// when the agent is on screen.
func (m *model) markSeen() {
	if !m.isOnScreen() {
		return
	}
	delete(m.unread, m.convID)
	m.errorUnseen = false
}

// startTurn records a chat turn in flight in the conversation shown.
func (m *model) startTurn() {
	m.turnActive = true
	m.turnConv = m.convID
}

// endTurn records that the agent's chat turn is over.
func (m *model) endTurn() {
	m.turnActive = false
	m.turnConv = ""
}

// clearCachedTurns stops the working state in every cached view, as when
// the connection drops and no reply will come.
func (m *model) clearCachedTurns() {
	for _, view := range m.cached {
		view.thinking = false
		view.steps = nil
	}
}
