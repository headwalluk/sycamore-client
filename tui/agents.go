package main

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// Attention states, the switcher, the footer summary and ctrl+r: how the
// TUI shows what every agent is doing. See dev-notes/planning-multi-agent.md
// §6 and §7.

// Row markers for agents and conversations.
const (
	markIdle         = "●"
	markWorking      = "◐"
	markUnread       = "◆"
	markError        = "!"
	markDisconnected = "○"
	markConnecting   = "…"
	markFailed       = "✗"
)

// agentNames lists every agent the switcher shows (configured instances
// plus any open agent not in the config), grouped by team, then by name.
func (m model) agentNames() []string {
	seen := map[string]bool{}
	var names []string
	if m.cfg != nil {
		for name := range m.cfg.Instances {
			seen[name] = true
			names = append(names, name)
		}
	}
	for name := range m.agents {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(left, right int) bool {
		leftTeam, rightTeam := m.teamOf(names[left]), m.teamOf(names[right])
		if leftTeam != rightTeam {
			return leftTeam < rightTeam
		}
		return names[left] < names[right]
	})
	return names
}

// teamOf returns the agent's configured team, or "".
func (m model) teamOf(name string) string {
	team := ""
	if m.cfg != nil {
		team = m.cfg.Instances[name].Team
	}
	return team
}

// displayNameOf is how an agent is named in lists: its own display name
// once known, else its config name.
func (m model) displayNameOf(name string) string {
	display := titleCase(name)
	if agent, open := m.agents[name]; open {
		if profile := agent.client.Session().Agent; profile != nil && profile.DisplayName != "" {
			display = profile.DisplayName
		}
	}
	return display
}

// attentionOf returns an agent's marker and state label, most urgent first:
// failed, down, working, a failed turn, unread replies, idle.
func (m model) attentionOf(name string) (string, string) {
	agent, open := m.agents[name]
	mark, label := markDisconnected, "disconnected"
	switch {
	case !open:
		// Never opened, or closed.
	case agent.failed:
		mark, label = markFailed, "failed — enter to retry"
	case agent.reconnecting:
		mark, label = markConnecting, fmt.Sprintf("reconnecting (attempt %d)", agent.reconnectAttempt)
	case agent.connecting:
		mark, label = markConnecting, "connecting"
	case !agent.connected:
		// Offline: retired while idle.
	case agent.turnActive || agent.thinking:
		mark, label = markWorking, "working"
		if step := agent.latestStep(); step != "" {
			label += " · " + step
		}
	case agent.errorUnseen:
		mark, label = markError, "error"
	case len(agent.unread) > 0:
		mark, label = markUnread, "unread"
	default:
		mark, label = markIdle, "idle"
	}
	return mark, label
}

// latestStep describes the running turn's latest progress step, from the
// view the turn belongs to (which may be cached), or "".
func (a *agentCtx) latestStep() string {
	steps := a.steps
	if a.turnConv != a.convID {
		if cached, found := a.cached[a.turnConv]; found {
			steps = cached.steps
		}
	}
	description := ""
	if len(steps) > 0 {
		latest := steps[len(steps)-1]
		description = fmt.Sprintf("round %d", latest.Round)
		if latest.Kind == protocol.ProgressToolStart {
			description = truncate(latest.ToolName, maxFooterToolName) + " · " + description
		}
	}
	return description
}

// agentItems builds the switcher's agent rows.
func (m model) agentItems() []pickerItem {
	names := m.agentNames()
	items := make([]pickerItem, 0, len(names))
	for _, name := range names {
		mark, label := m.attentionOf(name)
		detail := []string{label}
		if team := m.teamOf(name); team != "" {
			detail = append([]string{team}, detail...)
		}
		item := pickerItem{id: name, title: m.displayNameOf(name), mark: mark}
		if agent, open := m.agents[name]; open {
			if profile := agent.client.Session().Agent; profile != nil {
				if profile.Tagline != "" {
					detail = append(detail, profile.Tagline)
				}
				item.accent = lipgloss.Color(profile.AccentColor)
			}
		}
		item.desc = strings.Join(detail, " · ")
		items = append(items, item)
	}
	return items
}

// conversationItems builds the switcher's rows for an agent's last
// conversation list, marking the one it shows, unread replies, and the
// conversation its turn is running in.
func (a *agentCtx) conversationItems() []pickerItem {
	items := make([]pickerItem, 0, len(a.conversations))
	for _, summary := range a.conversations {
		mark := " "
		var detail []string
		switch {
		case a.turnActive && summary.ID == a.turnConv:
			mark = markWorking
			detail = append(detail, "working")
		case a.unread[summary.ID]:
			mark = markUnread
			detail = append(detail, "unread")
		default:
			// Nothing to flag.
		}
		if summary.ID == a.convID {
			detail = append(detail, "(current)")
		}
		detail = append(detail, fmt.Sprintf("%d turns · %s", summary.TurnCount, shortTime(summary.UpdatedAt)))
		items = append(items, pickerItem{
			id:    summary.ID,
			title: convTitleOrPlaceholder(summary.Title),
			desc:  strings.Join(detail, " · "),
			mark:  mark,
		})
	}
	return items
}

// openSwitcher opens the /switch overlay on the agent shown, with its
// conversations focused, and fetches a fresh list.
func (m model) openSwitcher() (tea.Model, tea.Cmd) {
	overlay := newSwitcher(m.agentItems(), m.active)
	overlay.setSize(m.width, m.height)
	m.switcher = &overlay
	return m, m.loadSwitcherConversations(m.active)
}

// loadSwitcherConversations shows an agent's conversations in the
// switcher: its last list at once, and a fresh one when it arrives. An
// agent that isn't connected gets a note instead, and nothing is dialled.
func (m *model) loadSwitcherConversations(name string) tea.Cmd {
	if note, blocked := m.switcherNoteFor(name); blocked {
		m.switcher.showNote(name, m.displayNameOf(name), note)
		return nil
	}
	agent := m.agents[name]
	if agent.conversations != nil {
		m.switcher.showConversations(name, m.displayNameOf(name), agent.conversationItems(), agent.convID)
	} else {
		m.switcher.showNote(name, m.displayNameOf(name), "loading…")
	}
	var cmd tea.Cmd
	if agent.convAction != convActionPicker {
		agent.convAction = convActionPicker
		cmd = sendConversations(agent.client)
	}
	return cmd
}

// switcherNoteFor explains why an agent's conversations can't be listed
// yet, when they can't.
func (m model) switcherNoteFor(name string) (string, bool) {
	agent, open := m.agents[name]
	note := ""
	switch {
	case !open:
		note = "disconnected — enter to connect"
	case agent.failed:
		note = "couldn't connect — enter to retry"
	case agent.connecting || agent.reconnecting:
		note = "connecting…"
	case !agent.connected:
		note = "disconnected — enter to connect"
	case !agent.client.Session().Has(protocol.CapConversations):
		note = "listing conversations needs a protocol v2 daemon"
	default:
		// Listable.
	}
	return note, note != ""
}

// updateSwitcher routes a key press to the open switcher and acts on it.
func (m model) updateSwitcher(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	next, outcome := m.switcher.Update(msg)
	m.switcher = &next
	var cmd tea.Cmd
	switch outcome {
	case switcherCancelled:
		m.switcher = nil
	case switcherHighlighted:
		cmd = m.loadSwitcherConversations(next.highlightedAgent())
	case switcherAgentChosen:
		return m.chooseSwitcherAgent(next.highlightedAgent())
	case switcherConversationChosen:
		item, _ := next.conversations.selected()
		m.switcher = nil
		if item.id == newConversationID {
			return m.startNewConversation(next.shownAgent)
		}
		return m.openConversation(next.shownAgent, item.id, item.title)
	default:
		// Still browsing.
	}
	return m, cmd
}

// chooseSwitcherAgent moves to an agent's conversations, connecting it
// first when needed; its list is fetched once its welcome arrives.
func (m model) chooseSwitcherAgent(name string) (tea.Model, tea.Cmd) {
	if _, blocked := m.switcherNoteFor(name); !blocked {
		m.switcher.focusBox(boxConversations)
		return m, nil
	}
	if agent, open := m.agents[name]; open && agent.connected {
		return m, nil // connected but can't list: the note already says why
	}
	if err := m.pool.Open(name); err != nil {
		m.switcher.showNote(name, m.displayNameOf(name), fmt.Sprintf("could not connect: %v", err))
		return m, nil
	}
	agent, open := m.agents[name]
	if !open {
		agent = newAgentCtx(m.pool, name, m.cacheLimit)
		m.agents[name] = agent
	}
	agent.failed = false
	agent.convAction = convActionPicker
	agent.fetchOnWelcome = true
	m.switcher.showNote(name, m.displayNameOf(name), "connecting…")
	m.switcher.focusBox(boxConversations)
	return m, m.spin.Tick
}

// openConversation puts an agent and one of its conversations on screen.
func (m model) openConversation(agent, id, title string) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if agent != m.active {
		next, cmd := m.focusAgent(agent)
		m = next.(model)
		cmds = append(cmds, cmd)
	}
	cmds = append(cmds, m.switchTo(id, title))
	return m, tea.Batch(cmds...)
}

// startNewConversation puts an agent on screen and starts a new
// conversation there, as /new does.
func (m model) startNewConversation(agent string) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if agent != m.active {
		next, cmd := m.focusAgent(agent)
		m = next.(model)
		cmds = append(cmds, cmd)
	}
	if m.thinking || m.turnActive {
		m.note(fmt.Sprintf("%s is still working on a reply — start the new conversation once it lands", m.agentName()))
		return m, tea.Batch(cmds...)
	}
	m.entries = append(m.entries, entry{kind: entryUser, text: "/new"})
	m.thinking = true
	m.refresh()
	cmds = append(cmds, sendMessage(m.client, protocol.ClientMessage{Type: protocol.TypeNew}), m.spin.Tick)
	return m, tea.Batch(cmds...)
}

// syncSwitcher refreshes an open switcher after an event: agent states,
// and the note for an agent whose conversations couldn't be listed.
func (m *model) syncSwitcher() {
	if m.switcher == nil {
		return
	}
	m.switcher.setAgents(m.agentItems())
	shown := m.switcher.shownAgent
	if m.switcher.note == "" {
		return
	}
	if note, blocked := m.switcherNoteFor(shown); blocked {
		m.switcher.showNote(shown, m.displayNameOf(shown), note)
	} else if agent := m.agents[shown]; agent.conversations == nil {
		m.switcher.showNote(shown, m.displayNameOf(shown), "loading…")
	}
}

// showSwitcherList puts a freshly fetched list in the switcher, when the
// switcher is showing the agent in place.
func (m *model) showSwitcherList() {
	if m.switcher != nil && m.switcher.shownAgent == m.instance {
		keep, _ := m.switcher.conversations.selected()
		if keep.id == "" {
			keep.id = m.convID
		}
		m.switcher.showConversations(m.instance, m.agentName(), m.conversationItems(), keep.id)
	}
}

// attentionSummary is the footer's note on everything not on screen: other
// agents working, and replies waiting (on any agent, in any conversation
// but the one shown). Empty when there's nothing to say.
func (m model) attentionSummary() string {
	working, unread := 0, 0
	for name, agent := range m.agents {
		waiting := len(agent.unread)
		if name == m.active {
			if agent.unread[agent.convID] {
				waiting--
			}
		} else {
			if agent.turnActive || agent.thinking {
				working++
			}
			if agent.errorUnseen && waiting == 0 {
				waiting = 1
			}
		}
		unread += waiting
	}
	var parts []string
	if working > 0 {
		parts = append(parts, fmt.Sprintf("%d working", working))
	}
	if unread > 0 {
		parts = append(parts, fmt.Sprintf("%d unread", unread))
	}
	return strings.Join(parts, " · ")
}

// nextUnread handles ctrl+r: show the next reply waiting, looking at the
// agents after the one on screen first, then the rest of its own
// conversations.
func (m model) nextUnread() (tea.Model, tea.Cmd) {
	names := m.agentNames()
	start := 0
	for index, name := range names {
		if name == m.active {
			start = index + 1
			break
		}
	}
	targetAgent, targetConv := "", ""
	for offset := range names {
		name := names[(start+offset)%len(names)]
		agent, open := m.agents[name]
		if !open || targetAgent != "" {
			continue
		}
		waiting := make([]string, 0, len(agent.unread))
		for id := range agent.unread {
			if !(name == m.active && id == agent.convID) {
				waiting = append(waiting, id)
			}
		}
		sort.Strings(waiting)
		switch {
		case len(waiting) > 0:
			targetAgent, targetConv = name, waiting[0]
		case agent.errorUnseen && name != m.active:
			targetAgent = name
		default:
			// Nothing waiting on this agent.
		}
	}

	if targetAgent == "" {
		m.note("nothing unread")
		return m, nil
	}
	if targetConv == "" || targetConv == m.agents[targetAgent].convID {
		return m.focusAgent(targetAgent)
	}
	return m.openConversation(targetAgent, targetConv, m.agents[targetAgent].titleOf(targetConv))
}

// titleOf finds a conversation's title from the agent's cached view or
// last list.
func (a *agentCtx) titleOf(id string) string {
	title := ""
	if cached, found := a.cached[id]; found {
		title = cached.convTitle
	}
	for _, summary := range a.conversations {
		if title == "" && summary.ID == id {
			title = convTitleOrPlaceholder(summary.Title)
		}
	}
	return title
}
