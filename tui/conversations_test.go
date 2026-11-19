package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// viewingModel returns a connected model on agent "prod" showing
// conversation C_A, with one line in its transcript.
func viewingModel(t *testing.T) (model, *fakeConn) {
	t.Helper()
	m, _, prodLink := connectedModel(t)
	prodLink.sess.ProtocolVersion = 2
	m.convID = "C_A"
	m.convTitle = "Alpha"
	m.entries = append(m.entries, entry{kind: entryNote, text: "alpha line"})
	return m, prodLink
}

// reply is a chat response for a conversation.
func reply(conversationID, text string) *protocol.Response {
	return &protocol.Response{ConversationID: conversationID, TurnID: "T_" + conversationID,
		Content: []protocol.ContentBlock{{Type: "text", Text: text}}}
}

// historySends counts history requests sent on a link.
func historySends(link *fakeConn) int {
	count := 0
	for _, sent := range link.sent {
		if sent.Type == protocol.TypeHistory {
			count++
		}
	}
	return count
}

func TestSwitch_BackToACachedConversationIsInstant(t *testing.T) {
	ui, prodLink := viewingModel(t)
	runCmd(ui.switchTo("C_B", "Bravo"))
	if ui.convID != "C_B" || len(ui.entries) != 1 || historySends(prodLink) != 1 {
		t.Fatalf("switching to a new conversation should start a fresh view and fetch it, entries=%+v", ui.entries)
	}
	if _, cached := ui.cached["C_A"]; !cached {
		t.Fatal("the view left should be cached")
	}

	runCmd(ui.switchTo("C_A", "Alpha"))
	if ui.convID != "C_A" || ui.entries[0].text != "alpha line" {
		t.Errorf("switching back should restore Alpha's view, got %+v", ui.entries)
	}
	if historySends(prodLink) != 1 {
		t.Error("a cached conversation must not be fetched again")
	}
	if prodLink.convID != "C_A" {
		t.Errorf("the session should follow the switch, got %q", prodLink.convID)
	}
}

func TestSwitch_MidTurnIsAllowedAndTheReplyLandsInItsConversation(t *testing.T) {
	ui, prodLink := viewingModel(t)
	ui.input.SetValue("long job")
	next, cmd := ui.submit()
	ui = next.(model)
	runCmd(cmd)
	if !ui.turnActive || ui.turnConv != "C_A" {
		t.Fatal("the chat should start a turn in C_A")
	}

	ui.input.SetValue("/switch")
	next, _ = ui.submit()
	ui = next.(model)
	if ui.convAction != convActionPicker {
		t.Fatal("/switch should be allowed mid-turn")
	}
	runCmd(ui.switchTo("C_B", "Bravo"))

	ui.input.SetValue("and another thing")
	next, _ = ui.submit()
	ui = next.(model)
	if chats := len(prodLink.sent); prodLink.sent[chats-1].Type == protocol.TypeChat && prodLink.sent[chats-1].Message == "and another thing" {
		t.Error("a second chat must wait while the agent's turn runs in another conversation")
	}

	next, _ = ui.Update(recvMsg{agent: "prod", msg: reply("C_A", "alpha's answer")})
	ui = next.(model)
	if strings.Contains(ansi.Strip(ui.vp.View()), "alpha's answer") {
		t.Error("Alpha's reply must not appear in Bravo")
	}
	if ui.turnActive || !ui.unread["C_A"] {
		t.Errorf("the reply should end the turn and mark Alpha unread (turn=%v unread=%v)", ui.turnActive, ui.unread)
	}
	alpha := ui.cached["C_A"]
	if alpha.thinking || !strings.Contains(renderAll(ui, alpha.entries), "alpha's answer") {
		t.Errorf("the reply should be in Alpha's cached view, thinking=%v", alpha.thinking)
	}

	runCmd(ui.switchTo("C_A", "Alpha"))
	if ui.unread["C_A"] {
		t.Error("showing Alpha should clear its unread mark")
	}
	if !strings.Contains(ansi.Strip(ui.vp.View()), "alpha's answer") {
		t.Error("Alpha's reply should show once it's back on screen")
	}
}

func TestSwitch_ErrorForATurnLeftBehindLandsInItsConversation(t *testing.T) {
	ui, _ := viewingModel(t)
	ui.input.SetValue("risky job")
	next, _ := ui.submit()
	ui = next.(model)
	runCmd(ui.switchTo("C_B", "Bravo"))

	next, _ = ui.Update(recvMsg{agent: "prod", msg: &protocol.Error{Message: "provider down"}})
	ui = next.(model)
	if ui.turnActive || !ui.unread["C_A"] || !ui.errorUnseen {
		t.Errorf("the error should end the turn and flag Alpha (turn=%v unread=%v error=%v)", ui.turnActive, ui.unread, ui.errorUnseen)
	}
	if strings.Contains(renderAll(ui, ui.entries), "provider down") {
		t.Error("the error must not land in Bravo")
	}
	if !strings.Contains(renderAll(ui, ui.cached["C_A"].entries), "provider down") {
		t.Error("the error should be in Alpha's view")
	}
}

func TestCache_EvictsTheLeastRecentlyShown(t *testing.T) {
	ui, _ := viewingModel(t)
	ui.cacheLimit = 2
	for _, id := range []string{"C_B", "C_C", "C_D"} {
		runCmd(ui.switchTo(id, id))
	}
	if _, kept := ui.cached["C_A"]; kept {
		t.Error("the oldest view should be evicted past the limit")
	}
	for _, id := range []string{"C_B", "C_C"} {
		if _, kept := ui.cached[id]; !kept {
			t.Errorf("%s should still be cached", id)
		}
	}
}

func TestCache_NeverEvictsTheConversationWithATurnRunning(t *testing.T) {
	ui, _ := viewingModel(t)
	ui.cacheLimit = 1
	ui.input.SetValue("long job")
	next, _ := ui.submit()
	ui = next.(model)
	for _, id := range []string{"C_B", "C_C"} {
		runCmd(ui.switchTo(id, id))
	}
	if _, kept := ui.cached["C_A"]; !kept {
		t.Error("the view waiting for a reply must stay cached")
	}
}

func TestCache_SizeZeroStillMarksUnread(t *testing.T) {
	ui, _ := viewingModel(t)
	ui.cacheLimit = 0
	ui.input.SetValue("long job")
	next, _ := ui.submit()
	ui = next.(model)
	runCmd(ui.switchTo("C_B", "Bravo"))
	ui.cacheLimit = 0
	next, _ = ui.Update(recvMsg{agent: "prod", msg: reply("C_A", "answer")})
	ui = next.(model)
	if !ui.unread["C_A"] || ui.turnActive {
		t.Errorf("with no cache the reply should still end the turn and mark unread, unread=%v", ui.unread)
	}
}

func TestReconnect_DropsTheCacheButKeepsUnreadMarks(t *testing.T) {
	ui, _ := viewingModel(t)
	runCmd(ui.switchTo("C_B", "Bravo"))
	ui.unread["C_A"] = true
	ui = stateChange(ui, "prod", core.StateReconnecting, 1, errors.New("EOF"))
	ui = stateChange(ui, "prod", core.StateOnline, 0, nil)
	if len(ui.cached) != 0 || len(ui.cacheOrder) != 0 {
		t.Errorf("a reconnect should drop cached views, cached=%v", ui.cacheOrder)
	}
	if !ui.unread["C_A"] {
		t.Error("unread marks should survive a reconnect")
	}
}

func TestDrop_EndsATurnInACachedConversation(t *testing.T) {
	ui, _ := viewingModel(t)
	ui.input.SetValue("long job")
	next, _ := ui.submit()
	ui = next.(model)
	runCmd(ui.switchTo("C_B", "Bravo"))
	ui = stateChange(ui, "prod", core.StateReconnecting, 1, nil)
	if ui.turnActive || ui.cached["C_A"].thinking {
		t.Error("a drop should end the turn, including in its cached view")
	}
}

func TestBackgroundReply_MarksTheAgentsConversationUnreadUntilShown(t *testing.T) {
	ui, _ := viewingModel(t)
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	next, _ = ui.Update(recvMsg{agent: "prod", msg: reply("C_A", "done")})
	ui = next.(model)
	if !ui.agents["prod"].unread["C_A"] {
		t.Fatal("a reply for a background agent should be unread")
	}
	back, _ := ui.focusAgent("prod")
	ui = back.(model)
	if ui.unread["C_A"] {
		t.Error("showing the agent should mark its conversation seen")
	}
}

func TestBackgroundError_IsFlaggedUntilShown(t *testing.T) {
	ui, _ := viewingModel(t)
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	next, _ = ui.Update(recvMsg{agent: "prod", msg: &protocol.Error{Message: "boom"}})
	ui = next.(model)
	if !ui.agents["prod"].errorUnseen {
		t.Fatal("an error on a background agent should be flagged")
	}
	back, _ := ui.focusAgent("prod")
	if back.(model).errorUnseen {
		t.Error("showing the agent should clear the error flag")
	}
}

// renderAll renders entries to plain text for assertions.
func renderAll(m model, entries []entry) string {
	return ansi.Strip(strings.Join(m.renderEntries(entries), "\n"))
}
