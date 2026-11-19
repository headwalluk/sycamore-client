package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/config"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

func keyOf(keyType tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: keyType} }

func typed(text string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)} }

// sampleSwitcher is a laid-out switcher over three agents, showing two of
// zee's conversations.
func sampleSwitcher() switcher {
	overlay := newSwitcher([]pickerItem{{id: "coop", title: "Coop"}, {id: "ops", title: "Ops"}, {id: "zee", title: "Zee"}}, "zee")
	overlay.setSize(60, 20)
	overlay.showConversations("zee", "Zee", []pickerItem{{id: "C1", title: "Deploy checklist"}, {id: "C2", title: "Q3 review"}}, "")
	return overlay
}

func TestSwitcherWidget_OpensOnTheShownAgentsConversations(t *testing.T) {
	overlay := sampleSwitcher()
	if overlay.focus != boxConversations || overlay.highlightedAgent() != "zee" {
		t.Errorf("should open on zee's conversations, focus=%v agent=%q", overlay.focus, overlay.highlightedAgent())
	}
}

func TestSwitcherWidget_TabMovesFocusAndEachBoxKeepsItsFilter(t *testing.T) {
	overlay := sampleSwitcher()
	overlay, _ = overlay.Update(typed("q3"))
	if len(overlay.conversations.filtered) != 2 { // the new-conversation row, then the match
		t.Fatalf("typing should filter the conversations, got %d", len(overlay.conversations.filtered))
	}
	overlay, _ = overlay.Update(keyOf(tea.KeyTab))
	if overlay.focus != boxAgents || overlay.filter.Value() != "" {
		t.Errorf("tab should focus the agents with their own (empty) filter, value=%q", overlay.filter.Value())
	}
	overlay, _ = overlay.Update(typed("co"))
	if len(overlay.agents.filtered) != 1 || len(overlay.conversations.filtered) != 2 {
		t.Errorf("the agents filter must not touch the conversations: agents=%d conversations=%d", len(overlay.agents.filtered), len(overlay.conversations.filtered))
	}
	overlay, _ = overlay.Update(keyOf(tea.KeyShiftTab))
	if overlay.focus != boxConversations || overlay.filter.Value() != "q3" {
		t.Errorf("tabbing back should restore the conversations filter, got %q", overlay.filter.Value())
	}
}

func TestSwitcherWidget_OutcomesFromKeys(t *testing.T) {
	overlay := sampleSwitcher()
	if _, outcome := overlay.Update(keyOf(tea.KeyEnter)); outcome != switcherConversationChosen {
		t.Errorf("enter on a conversation: got %v", outcome)
	}
	overlay, _ = overlay.Update(keyOf(tea.KeyTab))
	overlay, outcome := overlay.Update(keyOf(tea.KeyUp))
	if outcome != switcherHighlighted || overlay.highlightedAgent() != "ops" {
		t.Errorf("moving in the agents box should highlight ops, got %v / %q", outcome, overlay.highlightedAgent())
	}
	if _, outcome = overlay.Update(keyOf(tea.KeyEnter)); outcome != switcherAgentChosen {
		t.Errorf("enter on an agent: got %v", outcome)
	}
	if _, outcome = overlay.Update(keyOf(tea.KeyEsc)); outcome != switcherCancelled {
		t.Errorf("esc: got %v", outcome)
	}
}

func TestSwitcherWidget_OneAgentHasNoAgentsBox(t *testing.T) {
	overlay := newSwitcher([]pickerItem{{id: "zee", title: "Zee"}}, "zee")
	overlay.setSize(60, 20)
	overlay, _ = overlay.Update(keyOf(tea.KeyTab))
	if overlay.showAgents || overlay.focus != boxConversations {
		t.Error("with one agent there's no agents box to tab to")
	}
	if strings.Contains(ansi.Strip(overlay.View()), "Agents") {
		t.Error("the agents box should be left out")
	}
}

func TestSwitcherWidget_FitsANarrowPane(t *testing.T) {
	var agents, conversations []pickerItem
	for _, name := range []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9", "a10", "a11", "a12"} {
		agents = append(agents, pickerItem{id: name, title: strings.Repeat("Agent ", 6) + name, desc: "working · shell_exec · round 12"})
		conversations = append(conversations, pickerItem{id: name, title: strings.Repeat("Conversation ", 5), desc: "12 turns"})
	}
	overlay := newSwitcher(agents, "a1")
	overlay.setSize(40, 12)
	overlay.showConversations("a1", "Agent a1", conversations, "")
	lines := strings.Split(overlay.View(), "\n")
	if len(lines) > 12 {
		t.Errorf("the overlay is %d rows in a 12-row pane", len(lines))
	}
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 40 {
			t.Errorf("a row is %d wide in a 40-column pane: %q", width, ansi.Strip(line))
		}
	}
	if overlay.agents.rows < 2 || overlay.conversations.rows < 1 {
		t.Errorf("both boxes need rows: agents=%d conversations=%d", overlay.agents.rows, overlay.conversations.rows)
	}
}

// switcherModel returns a connected model on "prod" (conversation C_A,
// protocol 2) with coop and zee configured, and prod's conversations
// already listed once.
func switcherModel(t *testing.T) (model, *fakePool, *fakeConn) {
	t.Helper()
	m, pool, prodLink := connectedModel(t)
	prodLink.sess.ProtocolVersion = 2
	m.convID = "C_A"
	m.conversations = []protocol.ConversationSummary{{ID: "C_A", TurnCount: 3}, {ID: "C_B", TurnCount: 1}}
	return m, pool, prodLink
}

func TestSwitch_OpensOnTheAgentShownAndFetches(t *testing.T) {
	ui, _, prodLink := switcherModel(t)
	next, cmd := ui.openSwitcher()
	ui = next.(model)
	runCmd(cmd)
	if ui.switcher == nil || ui.switcher.shownAgent != "prod" || len(ui.switcher.conversations.filtered) != 3 {
		t.Fatalf("should open on prod's last list at once")
	}
	if len(prodLink.sent) != 1 || prodLink.sent[0].Type != protocol.TypeConversations {
		t.Errorf("a fresh list should be fetched, sent=%+v", prodLink.sent)
	}
	var got []string
	for _, item := range ui.switcher.agents.items {
		got = append(got, item.id)
	}
	if !slices.Equal(got, []string{"coop", "prod", "zee"}) {
		t.Errorf("every configured and open agent should be listed, got %v", got)
	}
	if selected, _ := ui.switcher.conversations.selected(); selected.id != "C_A" {
		t.Errorf("the cursor should start on the conversation shown, got %q", selected.id)
	}
}

func TestSwitch_HighlightingAnOfflineAgentNeverDials(t *testing.T) {
	ui, pool, _ := switcherModel(t)
	next, _ := ui.openSwitcher()
	ui = next.(model)
	next, _ = ui.Update(keyOf(tea.KeyTab))
	ui = next.(model)
	next, _ = ui.Update(keyOf(tea.KeyUp)) // prod → coop
	ui = next.(model)
	if ui.switcher.shownAgent != "coop" || !strings.Contains(ui.switcher.note, "enter to connect") {
		t.Errorf("an offline agent should show a note, shown=%q note=%q", ui.switcher.shownAgent, ui.switcher.note)
	}
	if len(pool.opened) != 0 {
		t.Errorf("highlighting must not open a connection, opened=%v", pool.opened)
	}
}

func TestSwitch_EnterOnAnOfflineAgentConnectsThenLists(t *testing.T) {
	ui, pool, _ := switcherModel(t)
	next, _ := ui.openSwitcher()
	ui = next.(model)
	for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyUp, tea.KeyEnter} {
		next, _ = ui.Update(keyOf(key))
		ui = next.(model)
	}
	if !slices.Equal(pool.opened, []string{"coop"}) || ui.switcher.note != "connecting…" || ui.switcher.focus != boxConversations {
		t.Fatalf("enter should open coop and wait on its list: opened=%v note=%q", pool.opened, ui.switcher.note)
	}
	if ui.active != "prod" {
		t.Error("choosing an agent in the switcher must not change the screen yet")
	}

	coopLink := pool.Link("coop").(*fakeConn)
	coopLink.sess.ProtocolVersion = 2
	ui = stateChange(ui, "coop", core.StateOnline, 0, nil)
	next, cmd := ui.Update(recvMsg{agent: "coop", msg: &protocol.Welcome{Principal: "Paul"}})
	ui = next.(model)
	runCmd(cmd)
	if len(coopLink.sent) == 0 || coopLink.sent[len(coopLink.sent)-1].Type != protocol.TypeConversations {
		t.Fatalf("coop's welcome should trigger the list fetch, sent=%+v", coopLink.sent)
	}
	title := "Coop's work"
	next, _ = ui.Update(recvMsg{agent: "coop", msg: &protocol.ConversationsList{Conversations: []protocol.ConversationSummary{{ID: "C_COOP", Title: &title}}}})
	ui = next.(model)
	if ui.switcher.note != "" || len(ui.switcher.conversations.filtered) != 2 {
		t.Errorf("coop's list should replace the note, note=%q rows=%d", ui.switcher.note, len(ui.switcher.conversations.filtered))
	}

	next, cmd = ui.Update(keyOf(tea.KeyEnter))
	ui = next.(model)
	runCmd(cmd)
	if ui.switcher != nil || ui.active != "coop" || ui.convID != "C_COOP" || ui.convTitle != title {
		t.Errorf("enter on coop's conversation should show it: active=%q conv=%q title=%q", ui.active, ui.convID, ui.convTitle)
	}
}

func TestSwitch_RowsShowAttention(t *testing.T) {
	ui, _, _ := switcherModel(t)
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	coop := ui.agents["coop"]
	coop.connected = true
	coop.turnActive = true
	coop.steps = []protocol.Progress{{Kind: protocol.ProgressToolStart, ToolName: "shell_exec", Round: 3}}
	back, _ := ui.focusAgent("prod")
	ui = back.(model)
	ui.unread["C_B"] = true

	items := map[string]pickerItem{}
	for _, item := range ui.agentItems() {
		items[item.id] = item
	}
	if items["coop"].mark != markWorking || !strings.Contains(items["coop"].desc, "working · shell_exec · round 3") {
		t.Errorf("coop should read as working on its tool: %+v", items["coop"])
	}
	if items["zee"].mark != markDisconnected || !strings.Contains(items["zee"].desc, "disconnected") {
		t.Errorf("zee should read as disconnected: %+v", items["zee"])
	}
	if items["prod"].mark != markUnread {
		t.Errorf("prod has an unread conversation: %+v", items["prod"])
	}

	rows := ui.conversationItems()
	if !strings.Contains(rows[0].desc, "(current)") || rows[1].mark != markUnread {
		t.Errorf("conversation rows should mark the current and unread ones: %+v", rows)
	}
}

func TestAttention_OrderOfUrgency(t *testing.T) {
	ui, _, _ := switcherModel(t)
	agent := ui.agents["prod"]
	agent.errorUnseen = true
	agent.unread["C_B"] = true
	if _, label := ui.attentionOf("prod"); label != "error" {
		t.Errorf("an unseen error outranks unread, got %q", label)
	}
	agent.turnActive = true
	if _, label := ui.attentionOf("prod"); !strings.HasPrefix(label, "working") {
		t.Errorf("working outranks both, got %q", label)
	}
	agent.reconnecting, agent.reconnectAttempt = true, 4
	if _, label := ui.attentionOf("prod"); label != "reconnecting (attempt 4)" {
		t.Errorf("being down outranks working, got %q", label)
	}
	agent.failed = true
	if mark, _ := ui.attentionOf("prod"); mark != markFailed {
		t.Errorf("failed outranks everything, got %q", mark)
	}
}

func TestSwitch_SyncsAgentStatesWhileOpen(t *testing.T) {
	m, _, _ := switcherModel(t)
	next, _ := m.openSwitcher()
	m = next.(model)
	m = stateChange(m, "zee", core.StateConnecting, 0, nil)
	for _, item := range m.switcher.agents.items {
		if item.id == "zee" && item.mark != markConnecting {
			t.Errorf("zee's row should update as it connects: %+v", item)
		}
	}
}

func TestFooter_SummarisesOtherAgents(t *testing.T) {
	ui, _, _ := switcherModel(t)
	if got := ui.attentionSummary(); got != "" {
		t.Errorf("nothing to report yet, got %q", got)
	}
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	ui.agents["prod"].turnActive = true
	ui.agents["prod"].unread["C_B"] = true
	ui.agents["zee"] = newAgentCtx(newFakePool(), "zee", 8)
	ui.agents["zee"].errorUnseen = true
	if got := ui.attentionSummary(); got != "1 working · 2 unread" {
		t.Errorf("summary: got %q", got)
	}
	if footer := ansi.Strip(ui.renderFooter()); !strings.Contains(footer, "1 working · 2 unread") {
		t.Errorf("the footer should carry the summary:\n%s", footer)
	}
}

func TestNextUnread_JumpsToTheWaitingReply(t *testing.T) {
	ui, _, prodLink := switcherModel(t)
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	ui.agents["prod"].unread["C_B"] = true
	ui.agents["prod"].conversations = []protocol.ConversationSummary{{ID: "C_B", Title: ptr("Bravo")}}

	next, cmd := ui.Update(keyOf(tea.KeyCtrlR))
	ui = next.(model)
	runCmd(cmd)
	if ui.active != "prod" || ui.convID != "C_B" || ui.convTitle != "Bravo" {
		t.Errorf("ctrl+r should show prod's Bravo, active=%q conv=%q title=%q", ui.active, ui.convID, ui.convTitle)
	}
	if prodLink.convID != "C_B" || ui.unread["C_B"] {
		t.Errorf("the session should follow and the mark clear, session=%q unread=%v", prodLink.convID, ui.unread)
	}

	next, _ = ui.Update(keyOf(tea.KeyCtrlR))
	if !strings.Contains(lastNote(t, next.(model)), "nothing unread") {
		t.Error("with nothing waiting, ctrl+r should say so")
	}
}

func TestNextUnread_ErrorOnAnotherAgentFocusesIt(t *testing.T) {
	m, _, _ := switcherModel(t)
	m.agents["zee"] = newAgentCtx(newFakePool(), "zee", 8)
	m.agents["zee"].errorUnseen = true
	next, _ := m.Update(keyOf(tea.KeyCtrlR))
	if next.(model).active != "zee" {
		t.Error("an unseen error on another agent should be the next stop")
	}
}

func TestSwitchArg_OnAnotherAgent(t *testing.T) {
	ui, pool, _ := switcherModel(t)
	if c := parseCommand("/switch coop:deploy"); c.convFetch != convActionSwitchArg || c.switchArg != "coop:deploy" {
		t.Fatalf("parse: %+v", c)
	}
	next, _ := ui.startConvFetch(convActionSwitchArg, "coop:deploy")
	ui = next.(model)
	if ui.active != "coop" || !slices.Equal(pool.opened, []string{"coop"}) {
		t.Fatalf("coop should be opened and shown, active=%q", ui.active)
	}
	if ui.convAction != convActionSwitchArg || ui.pendingSwitchArg != "deploy" || !ui.fetchOnWelcome {
		t.Errorf("the switch should wait for coop's welcome: action=%v arg=%q", ui.convAction, ui.pendingSwitchArg)
	}

	coopLink := ui.client.(*fakeConn)
	coopLink.sess.ProtocolVersion = 2
	next, cmd := ui.Update(recvMsg{agent: "coop", msg: &protocol.Welcome{Principal: "Paul"}})
	ui = next.(model)
	runCmd(cmd)
	title := "Deploy checklist"
	next, cmd = ui.Update(recvMsg{agent: "coop", msg: &protocol.ConversationsList{Conversations: []protocol.ConversationSummary{{ID: "C_D", Title: &title}}}})
	ui = next.(model)
	runCmd(cmd)
	if ui.convID != "C_D" || coopLink.convID != "C_D" {
		t.Errorf("coop should switch to the matching conversation, conv=%q", ui.convID)
	}
}

func TestSwitchArg_ColonInATitleIsNotAnAgent(t *testing.T) {
	m, _, _ := switcherModel(t)
	if _, _, targeted := m.splitAgentArg("Q2: planning"); targeted {
		t.Error("a colon in a title must not be read as an agent")
	}
}

func TestAgentNames_GroupedByTeam(t *testing.T) {
	ui, _, _ := switcherModel(t)
	ui.cfg = &config.File{Instances: map[string]config.Instance{
		"zee":  {Name: "zee", Team: "ops"},
		"coop": {Name: "coop", Team: "web"},
		"able": {Name: "able", Team: "web"},
	}}
	if got := ui.agentNames(); !slices.Equal(got, []string{"prod", "zee", "able", "coop"}) {
		t.Errorf("agents should sort by team, then name, got %v", got)
	}
	for _, item := range ui.agentItems() {
		if item.id == "coop" && !strings.HasPrefix(item.desc, "web · ") {
			t.Errorf("the team should lead the detail: %+v", item)
		}
	}
}

func TestSwitch_CtrlCQuitsFromTheSwitcher(t *testing.T) {
	m, _, _ := switcherModel(t)
	next, _ := m.openSwitcher()
	m = next.(model)
	next, _ = m.Update(keyOf(tea.KeyCtrlC))
	if !next.(model).quitting {
		t.Error("ctrl+c should quit even with the switcher open")
	}
}

func TestSwitch_FailedAgentNoteUpdates(t *testing.T) {
	ui, _, _ := switcherModel(t)
	next, _ := ui.openSwitcher()
	ui = next.(model)
	for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyUp, tea.KeyEnter} {
		next, _ = ui.Update(keyOf(key))
		ui = next.(model)
	}
	ui = stateChange(ui, "coop", core.StateConnecting, 0, nil)
	ui = stateChange(ui, "coop", core.StateFailed, 0, errors.New("SSH authentication failed"))
	if !strings.Contains(ui.switcher.note, "couldn't connect") {
		t.Errorf("the note should follow the failure, got %q", ui.switcher.note)
	}
}

func ptr(text string) *string { return &text }

func TestListBox_FillsTheBoxWhenItGrows(t *testing.T) {
	box := listBox{}
	box.setItems([]pickerItem{{id: "a"}, {id: "b"}, {id: "c"}}, "b") // cursor placed before any height
	box.rows = 3
	box.clamp()
	if box.offset != 0 {
		t.Errorf("with room for every row nothing should be scrolled off, offset=%d", box.offset)
	}
}

func TestSwitch_NewConversationOnABrandNewAgent(t *testing.T) {
	ui, pool, _ := switcherModel(t)
	next, _ := ui.openSwitcher()
	ui = next.(model)
	for _, key := range []tea.KeyType{tea.KeyTab, tea.KeyUp, tea.KeyEnter} { // to coop, and connect it
		next, _ = ui.Update(keyOf(key))
		ui = next.(model)
	}
	coopLink := pool.Link("coop").(*fakeConn)
	coopLink.sess.ProtocolVersion = 2
	ui = stateChange(ui, "coop", core.StateOnline, 0, nil)
	next, cmd := ui.Update(recvMsg{agent: "coop", msg: &protocol.Welcome{Principal: "Paul"}})
	ui = next.(model)
	runCmd(cmd)
	next, _ = ui.Update(recvMsg{agent: "coop", msg: &protocol.ConversationsList{Conversations: []protocol.ConversationSummary{}}})
	ui = next.(model)

	if rows := ui.switcher.conversations.filtered; len(rows) != 1 || rows[0].id != newConversationID {
		t.Fatalf("a new agent's list should offer just the new-conversation row, got %+v", rows)
	}
	next, cmd = ui.Update(keyOf(tea.KeyEnter))
	ui = next.(model)
	runCmd(cmd)
	if ui.switcher != nil || ui.active != "coop" || !ui.thinking {
		t.Errorf("the row should put coop on screen and start a conversation: active=%q thinking=%v", ui.active, ui.thinking)
	}
	if sent := coopLink.sent; len(sent) == 0 || sent[len(sent)-1].Type != protocol.TypeNew {
		t.Errorf("a new message should go to coop, sent=%+v", sent)
	}
}

func TestSwitch_NewConversationRowSurvivesTheFilter(t *testing.T) {
	overlay := sampleSwitcher()
	overlay, _ = overlay.Update(typed("nothing matches this"))
	if rows := overlay.conversations.filtered; len(rows) != 1 || rows[0].id != newConversationID {
		t.Errorf("the row should stay whatever the filter, got %+v", rows)
	}
	overlay = sampleSwitcher()
	overlay, _ = overlay.Update(typed("q3"))
	if selected, _ := overlay.conversations.selected(); selected.id != "C2" {
		t.Errorf("filtering should put the cursor on the first match, not the row, got %q", selected.id)
	}
}

func TestSwitch_CursorStartsOnARealConversation(t *testing.T) {
	overlay := sampleSwitcher()
	if selected, _ := overlay.conversations.selected(); selected.id != "C1" {
		t.Errorf("with no current conversation the cursor starts on the first real one, got %q", selected.id)
	}
	overlay.showConversations("zee", "Zee", []pickerItem{{id: "C1"}, {id: "C2"}}, "C2")
	if selected, _ := overlay.conversations.selected(); selected.id != "C2" {
		t.Errorf("the cursor starts on the current conversation, got %q", selected.id)
	}
}

func TestSwitch_NewConversationWaitsForABusyAgent(t *testing.T) {
	ui, _, prodLink := switcherModel(t)
	ui.turnActive = true
	next, _ := ui.startNewConversation("prod")
	ui = next.(model)
	if len(prodLink.sent) != 0 || !strings.Contains(lastNote(t, ui), "still working") {
		t.Errorf("a busy agent should say so and send nothing, sent=%+v", prodLink.sent)
	}
}
