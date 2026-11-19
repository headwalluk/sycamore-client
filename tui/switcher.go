package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The /switch overlay: a filter over an agents box and the highlighted
// agent's conversations. See dev-notes/planning-multi-agent.md §7.

// switcherBox names the switcher's two lists.
type switcherBox int

const (
	boxConversations switcherBox = iota
	boxAgents
)

// switcherOutcome is what one key press in the switcher resolved to.
type switcherOutcome int

const (
	switcherNone               switcherOutcome = iota // still open, nothing to do
	switcherCancelled                                 // esc
	switcherHighlighted                               // a different agent is highlighted
	switcherAgentChosen                               // enter on an agent
	switcherConversationChosen                        // enter on a conversation
)

// listBox is one filterable, scrolling list in the switcher. Each box keeps
// its own filter text.
type listBox struct {
	pinned   []pickerItem // shown first, whatever the filter
	items    []pickerItem
	filtered []pickerItem
	query    string
	cursor   int
	offset   int
	rows     int // visible rows, set by the layout
}

// setItems replaces the rows, keeping the cursor on keepID when it's still
// listed.
func (b *listBox) setItems(items []pickerItem, keepID string) {
	b.items = items
	b.refilter(keepID)
}

// setQuery changes the filter text; the cursor goes back to the top.
func (b *listBox) setQuery(query string) {
	b.query = query
	b.refilter("")
}

// refilter applies the filter and places the cursor on keepID, or the top.
func (b *listBox) refilter(keepID string) {
	b.filtered = append(append([]pickerItem(nil), b.pinned...), filterItems(b.items, b.query)...)
	// Start on the first real row; a pinned row only when it's all there is.
	b.cursor = 0
	if len(b.filtered) > len(b.pinned) {
		b.cursor = len(b.pinned)
	}
	for index, item := range b.filtered {
		if item.id == keepID {
			b.cursor = index
			break
		}
	}
	b.clamp()
}

// move shifts the cursor by delta rows.
func (b *listBox) move(delta int) {
	b.cursor += delta
	b.clamp()
}

// selected returns the highlighted row, and false when the list is empty.
func (b listBox) selected() (pickerItem, bool) {
	var item pickerItem
	found := b.cursor >= 0 && b.cursor < len(b.filtered)
	if found {
		item = b.filtered[b.cursor]
	}
	return item, found
}

// clamp keeps the cursor in range and visible.
func (b *listBox) clamp() {
	b.cursor = max(min(b.cursor, len(b.filtered)-1), 0)
	rows := max(b.rows, 1)
	if b.cursor < b.offset {
		b.offset = b.cursor
	}
	if b.cursor >= b.offset+rows {
		b.offset = b.cursor - rows + 1
	}
	// Scroll back up when there's room, so the box is never part empty
	// while rows above are hidden.
	b.offset = max(min(b.offset, len(b.filtered)-rows), 0)
}

// switcher is the two-box overlay state.
type switcher struct {
	filter        textinput.Model
	agents        listBox
	conversations listBox
	focus         switcherBox
	showAgents    bool

	// shownAgent is whose conversations the lower box lists, and
	// shownAgentName its display name for the box's heading. note, when
	// set, stands in for the conversation rows (offline, loading, …).
	shownAgent     string
	shownAgentName string
	note           string

	width  int
	height int
}

// newSwitcher builds the overlay over the agents, focused on the
// conversations of shownAgent. With one agent the agents box is left out.
func newSwitcher(agentItems []pickerItem, shownAgent string) switcher {
	filter := textinput.New()
	filter.Prompt = "filter› "
	filter.Focus()
	overlay := switcher{filter: filter, shownAgent: shownAgent, showAgents: len(agentItems) > 1}
	overlay.agents.setItems(agentItems, shownAgent)
	return overlay
}

// setSize fits the overlay to the screen and lays out the two boxes.
func (s *switcher) setSize(width, height int) {
	s.width, s.height = width, height
	s.filter.Width = width - lipgloss.Width(s.filter.Prompt) - 1
	s.layout()
}

// layout splits the rows: the agents box takes up to a third, at least two
// rows, and the conversations get the rest.
func (s *switcher) layout() {
	fixed := 4 // title, filter, conversations heading, key hints
	if s.showAgents {
		fixed++ // agents heading
	}
	available := max(s.height-fixed, 2)
	agentRows := 0
	if s.showAgents {
		agentRows = min(max(len(s.agents.filtered), 1), max(available/3, 2))
		agentRows = min(agentRows, available-1)
	}
	s.agents.rows = agentRows
	s.conversations.rows = max(available-agentRows, 1)
	s.agents.clamp()
	s.conversations.clamp()
}

// focused returns the box with focus.
func (s *switcher) focused() *listBox {
	box := &s.conversations
	if s.focus == boxAgents {
		box = &s.agents
	}
	return box
}

// focusBox moves focus to box, swapping the filter text to that box's own.
func (s *switcher) focusBox(box switcherBox) {
	if box == s.focus || (box == boxAgents && !s.showAgents) {
		return
	}
	s.focused().query = s.filter.Value()
	s.focus = box
	s.filter.SetValue(s.focused().query)
	s.filter.CursorEnd()
}

// showConversations lists an agent's conversations under its heading,
// keeping the cursor on keepID when it's listed.
func (s *switcher) showConversations(agent, agentName string, items []pickerItem, keepID string) {
	if agent != s.shownAgent {
		s.conversations.query = ""
		if s.focus == boxConversations {
			s.filter.SetValue("")
		}
	}
	s.shownAgent, s.shownAgentName, s.note = agent, agentName, ""
	s.conversations.pinned = []pickerItem{newConversationItem}
	s.conversations.setItems(items, keepID)
	s.layout()
}

// newConversationID marks the switcher's "new conversation" row; it can't
// clash with a real conversation id, nor with "" (no conversation yet).
const newConversationID = "\x00new"

// newConversationItem is the row, first in every agent's conversations,
// that starts a new conversation there (as /new does).
var newConversationItem = pickerItem{id: newConversationID, title: "New conversation", desc: "as /new", mark: "+"}

// showNote replaces the conversation rows with a note for agent.
func (s *switcher) showNote(agent, agentName, note string) {
	s.shownAgent, s.shownAgentName, s.note = agent, agentName, note
	s.conversations.pinned = nil
	s.conversations.setItems(nil, "")
	s.layout()
}

// setAgents refreshes the agent rows (their states change while open),
// keeping the cursor where it was.
func (s *switcher) setAgents(items []pickerItem) {
	keep, _ := s.agents.selected()
	s.agents.setItems(items, keep.id)
	s.layout()
}

// highlightedAgent is the agent under the agents box's cursor.
func (s switcher) highlightedAgent() string {
	item, _ := s.agents.selected()
	return item.id
}

// Update folds one key press into the switcher.
func (s switcher) Update(msg tea.KeyMsg) (switcher, switcherOutcome) {
	before := s.highlightedAgent()
	outcome := switcherNone
	switch msg.Type {
	case tea.KeyEsc:
		outcome = switcherCancelled
	case tea.KeyTab, tea.KeyShiftTab:
		next := boxAgents
		if s.focus == boxAgents {
			next = boxConversations
		}
		s.focusBox(next)
	case tea.KeyEnter:
		if _, found := s.focused().selected(); found {
			outcome = switcherConversationChosen
			if s.focus == boxAgents {
				outcome = switcherAgentChosen
			}
		}
	case tea.KeyUp, tea.KeyCtrlP:
		s.focused().move(-1)
	case tea.KeyDown, tea.KeyCtrlN:
		s.focused().move(1)
	default:
		// Any other key edits the filter of the box with focus.
		s.filter, _ = s.filter.Update(msg)
		s.focused().setQuery(s.filter.Value())
		s.layout()
	}
	if outcome == switcherNone && s.highlightedAgent() != before {
		outcome = switcherHighlighted
	}
	return s, outcome
}

var (
	switcherHeadingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("180")).Bold(true)
	switcherIdleHeading  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// View renders the overlay full-screen.
func (s switcher) View() string {
	lines := []string{pickerTitleStyle.Width(s.width).Render(" Switch"), s.filter.View()}
	if s.showAgents {
		lines = append(lines, s.heading("Agents", s.focus == boxAgents))
		lines = append(lines, s.renderBox(s.agents, s.focus == boxAgents, "(no matching agents)")...)
	}
	conversationsTitle := "Conversations"
	if s.showAgents && s.shownAgentName != "" {
		conversationsTitle += " — " + s.shownAgentName
	}
	lines = append(lines, s.heading(conversationsTitle, s.focus == boxConversations))
	if s.note != "" {
		lines = append(lines, dimStyle.Render(truncate("  "+terminalSafe(s.note), s.width)))
		lines = append(lines, blankRows(s.conversations.rows-1)...)
	} else {
		lines = append(lines, s.renderBox(s.conversations, s.focus == boxConversations, "(no matching conversations)")...)
	}
	hints := "  ↑/↓ move · enter open · esc close"
	if s.showAgents {
		hints = "  tab agents/conversations · ↑/↓ move · enter open · esc close"
	}
	lines = append(lines, dimStyle.Render(truncate(hints, s.width)))
	return strings.Join(lines, "\n")
}

// heading renders a box's title line, brighter when the box has focus.
func (s switcher) heading(title string, focused bool) string {
	style := switcherIdleHeading
	if focused {
		style = switcherHeadingStyle
	}
	return style.Render(truncate(" "+title, s.width))
}

// renderBox renders a box's visible rows, padded to its height.
func (s switcher) renderBox(box listBox, focused bool, empty string) []string {
	var lines []string
	if len(box.filtered) == 0 {
		lines = append(lines, dimStyle.Render("  "+empty))
	}
	end := min(box.offset+box.rows, len(box.filtered))
	for index := box.offset; index < end; index++ {
		lines = append(lines, s.renderRow(box.filtered[index], index == box.cursor, focused))
	}
	return append(lines, blankRows(box.rows-len(lines))...)
}

// renderRow renders one row: a marker, the title (in its accent colour),
// and dim detail. The cursor row is highlighted in the box with focus and
// marked with ▸ in the other.
func (s switcher) renderRow(item pickerItem, atCursor, focused bool) string {
	mark := item.mark
	if mark == "" {
		mark = " "
	}
	if atCursor && focused {
		body := mark + " " + item.title
		if item.desc != "" {
			body += "  " + item.desc
		}
		return pickerSelStyle.Width(s.width).Render(truncate("▌ "+body, s.width))
	}
	pointer := "  "
	if atCursor {
		pointer = "▸ "
	}
	available := s.width - lipgloss.Width(pointer) - lipgloss.Width(mark) - 1
	title := truncate(item.title, available)
	titleStyle := lipgloss.NewStyle()
	if item.accent != "" {
		titleStyle = titleStyle.Foreground(item.accent)
	}
	row := pointer + mark + " " + titleStyle.Render(title)
	if remaining := available - lipgloss.Width(title) - 2; remaining > 0 && item.desc != "" {
		row += "  " + dimStyle.Render(truncate(item.desc, remaining))
	}
	return row
}

// blankRows returns count empty lines (none for count <= 0).
func blankRows(count int) []string {
	return make([]string, max(count, 0))
}
