package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// v2App returns a started App whose agents (zee, coop) speak protocol 2,
// with zee welcomed into conversation C_A ("Alpha").
func v2App(t *testing.T) (*App, *fakePool, *[]emitted) {
	t.Helper()
	app, pool, sent := testApp(t)
	pool.sessions["zee"] = core.Session{ProtocolVersion: 2, ConversationID: "C_A"}
	pool.sessions["coop"] = core.Session{ProtocolVersion: 2}
	alpha, title := "C_A", "Alpha"
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Welcome{Principal: "Paul", ProtocolVersion: 2, ConversationID: &alpha, ConversationTitle: &title}})
	return app, pool, sent
}

// historyRequests returns the history requests sent to agent.
func historyRequests(pool *fakePool, agent string) []protocol.ClientMessage {
	var requests []protocol.ClientMessage
	for _, message := range pool.sent[agent] {
		if message.Type == protocol.TypeHistory {
			requests = append(requests, message)
		}
	}
	return requests
}

// entriesFor returns the entries emitted for one agent's conversation.
func entriesFor(sent []emitted, agent, convID string) []Entry {
	var entries []Entry
	for _, item := range sent {
		if entry, isEntry := item.payload.(Entry); isEntry && entry.Agent == agent && entry.ConversationID == convID {
			entries = append(entries, entry)
		}
	}
	return entries
}

func TestWelcome_SeedsTheConversationsHistory(t *testing.T) {
	_, pool, sent := v2App(t)
	requests := historyRequests(pool, "zee")
	if len(requests) != 1 || requests[0].ConversationID != "C_A" || requests[0].BeforeSequence != 0 {
		t.Fatalf("the welcome should fetch Alpha's newest page, sent=%+v", requests)
	}
	if conversation, _ := last[ConversationMsg](*sent, eventConversation); conversation.ConversationID != "C_A" {
		t.Errorf("the frontend should be told which conversation zee shows, got %+v", conversation)
	}
	if entries := entriesFor(*sent, "zee", "C_A"); len(entries) == 0 || entries[0].Kind != "welcome" {
		t.Errorf("the welcome entry belongs to Alpha, got %+v", entries)
	}
}

func TestWelcome_NoHistoryFromAProtocolOneDaemon(t *testing.T) {
	app, pool, _ := testApp(t)
	alpha := "C_A"
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Welcome{ConversationID: &alpha}})
	if len(historyRequests(pool, "zee")) != 0 {
		t.Error("a daemon without history must not be asked for it")
	}
}

func TestHistoryPage_BecomesEntriesAndPagesBack(t *testing.T) {
	app, pool, sent := v2App(t)
	oldest := 40
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.HistoryPage{
		ConversationID: "C_A", HasMore: true, OldestSequence: &oldest,
		Messages: []protocol.HistoryMessage{
			{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: "text", Text: "status?"}}},
			{Role: protocol.RoleAssistant, Model: "m", Content: []protocol.ContentBlock{{Type: "text", Text: "**all up**"}}},
		},
	}})
	page, _ := last[HistoryMsg](*sent, eventHistory)
	if page.Older || !page.HasMore || len(page.Entries) != 2 {
		t.Fatalf("a seed page: %+v", page)
	}
	if page.Entries[0].Role != "you" || page.Entries[1].Role != "agent" || !strings.Contains(page.Entries[1].HTML, "<strong>all up</strong>") {
		t.Errorf("records should become you / agent entries with HTML: %+v", page.Entries)
	}

	if err := app.LoadOlder("zee"); err != nil {
		t.Fatal(err)
	}
	requests := historyRequests(pool, "zee")
	if last := requests[len(requests)-1]; last.BeforeSequence != 40 {
		t.Errorf("the older page should be fetched from sequence 40, got %+v", last)
	}
	before := len(historyRequests(pool, "zee"))
	must(t, app.LoadOlder("zee"))
	if len(historyRequests(pool, "zee")) != before {
		t.Error("a second LoadOlder while one is on its way should do nothing")
	}

	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.HistoryPage{ConversationID: "C_A"}})
	if page, _ = last[HistoryMsg](*sent, eventHistory); !page.Older || page.HasMore {
		t.Errorf("the reply should be an older page, with nothing more: %+v", page)
	}
	must(t, app.LoadOlder("zee"))
	if len(historyRequests(pool, "zee")) != before {
		t.Error("with nothing older, LoadOlder should do nothing")
	}
}

func TestSwitchConversation_FollowsAndFetches(t *testing.T) {
	app, pool, sent := v2App(t)
	app.agents["zee"].unread["C_B"] = true
	if err := app.SwitchConversation("zee", "C_B", "Bravo"); err != nil {
		t.Fatal(err)
	}
	if session, _ := pool.Session("zee"); session.ConversationID != "C_B" {
		t.Errorf("the session should follow, got %q", session.ConversationID)
	}
	if requests := historyRequests(pool, "zee"); requests[len(requests)-1].ConversationID != "C_B" {
		t.Errorf("Bravo's history should be fetched, got %+v", requests)
	}
	if info, _ := last[SessionInfo](*sent, eventSession); info.ConversationTitle != "Bravo" {
		t.Errorf("the status bar should show Bravo, got %+v", info)
	}
	if unread, _ := last[UnreadMsg](*sent, eventUnread); len(unread.Conversations) != 0 {
		t.Errorf("showing Bravo should clear its unread mark, got %+v", unread)
	}
}

func TestReply_ForAConversationLeftIsTaggedAndUnread(t *testing.T) {
	app, _, sent := v2App(t)
	must(t, app.SendChat("zee", "long job"))
	must(t, app.SwitchConversation("zee", "C_B", "Bravo"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Response{ConversationID: "C_A", Content: []protocol.ContentBlock{{Type: "text", Text: "done"}}}})

	alphaEntries := entriesFor(*sent, "zee", "C_A")
	if replyEntry := alphaEntries[len(alphaEntries)-1]; replyEntry.Kind != "response" {
		t.Errorf("the reply belongs to Alpha, got %+v", replyEntry)
	}
	if unread, _ := last[UnreadMsg](*sent, eventUnread); !slices.Equal(unread.Conversations, []string{"C_A"}) {
		t.Errorf("Alpha should be unread, got %+v", unread)
	}
	if err := app.SendChat("zee", "next"); err != nil {
		t.Errorf("the reply should free zee: %v", err)
	}
	if you, _ := last[Entry](*sent, eventEntry); you.ConversationID != "C_B" {
		t.Errorf("a new line goes in the conversation on screen, got %+v", you)
	}
}

func TestReply_OnScreenIsNotUnread(t *testing.T) {
	app, _, sent := v2App(t)
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Response{ConversationID: "C_A"}})
	if _, found := last[UnreadMsg](*sent, eventUnread); found {
		t.Error("a reply to the conversation on screen isn't unread")
	}
}

func TestReply_OnABackgroundAgentIsUnreadUntilShown(t *testing.T) {
	app, _, sent := v2App(t)
	must(t, app.Connect("coop"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Response{ConversationID: "C_A"}})
	if unread, _ := last[UnreadMsg](*sent, eventUnread); unread.Agent != "zee" || !slices.Equal(unread.Conversations, []string{"C_A"}) {
		t.Fatalf("zee's reply should be unread while coop is shown, got %+v", unread)
	}
	must(t, app.Connect("zee"))
	if unread, _ := last[UnreadMsg](*sent, eventUnread); len(unread.Conversations) != 0 {
		t.Errorf("showing zee should clear it, got %+v", unread)
	}
}

func TestError_DuringATurnLeftBehindGoesToItsConversation(t *testing.T) {
	app, _, sent := v2App(t)
	must(t, app.SendChat("zee", "risky"))
	must(t, app.SwitchConversation("zee", "C_B", "Bravo"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Error{Message: "provider down"}})
	if errorEntry, _ := last[Entry](*sent, eventEntry); errorEntry.ConversationID != "C_A" || errorEntry.Kind != "error" {
		t.Errorf("the error belongs to Alpha, got %+v", errorEntry)
	}
}

func TestFirstChat_AdoptsTheConversationItCreates(t *testing.T) {
	app, pool, sent := testApp(t)
	pool.sessions["zee"] = core.Session{ProtocolVersion: 2}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Welcome{Principal: "Paul"}})
	must(t, app.SendChat("zee", "hello"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Response{ConversationID: "C_NEW"}})
	conversation, _ := last[ConversationMsg](*sent, eventConversation)
	if conversation.ConversationID != "C_NEW" || !conversation.Adopt {
		t.Errorf("the frontend should move the new transcript under C_NEW, got %+v", conversation)
	}
	if app.agents["zee"].convID != "C_NEW" {
		t.Errorf("zee now shows C_NEW, got %q", app.agents["zee"].convID)
	}
}

func TestConversationsList_RowsCarryMarkers(t *testing.T) {
	app, pool, sent := v2App(t)
	if err := app.Conversations("zee"); err != nil {
		t.Fatal(err)
	}
	if last := pool.sent["zee"][len(pool.sent["zee"])-1]; last.Type != protocol.TypeConversations {
		t.Errorf("a conversations request should be sent, got %+v", last)
	}
	must(t, app.SendChat("zee", "job"))
	app.agents["zee"].unread["C_B"] = true
	bravo := "Bravo"
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.ConversationsList{Conversations: []protocol.ConversationSummary{
		{ID: "C_A", TurnCount: 3}, {ID: "C_B", Title: &bravo, TurnCount: 1},
	}}})
	list, _ := last[ConversationsMsg](*sent, eventConversations)
	if len(list.Conversations) != 2 {
		t.Fatalf("rows: %+v", list)
	}
	alpha, bravoRow := list.Conversations[0], list.Conversations[1]
	if !alpha.Current || !alpha.Working || alpha.Title != "(untitled)" {
		t.Errorf("Alpha is current, working and untitled: %+v", alpha)
	}
	if !bravoRow.Unread || bravoRow.Current || bravoRow.Title != "Bravo" {
		t.Errorf("Bravo is unread: %+v", bravoRow)
	}
}

func TestConversations_NeedsProtocolTwo(t *testing.T) {
	app, _, _ := testApp(t)
	if err := app.Conversations("zee"); err == nil || !strings.Contains(err.Error(), "protocol v2") {
		t.Errorf("a v1 daemon should be refused, got %v", err)
	}
}

func TestRename_OfAnotherConversationKeepsTheTitle(t *testing.T) {
	app, _, sent := v2App(t)
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.ConversationRenamed{ConversationID: "C_B", Title: "Bravo"}})
	if info, _ := last[SessionInfo](*sent, eventSession); info.ConversationTitle != "Alpha" {
		t.Errorf("renaming Bravo must not change the title on screen, got %q", info.ConversationTitle)
	}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.ConversationRenamed{ConversationID: "C_A", Title: "Alpha, renamed"}})
	if info, _ := last[SessionInfo](*sent, eventSession); info.ConversationTitle != "Alpha, renamed" {
		t.Errorf("renaming Alpha should, got %q", info.ConversationTitle)
	}
}

func TestIdleRetire_ReopensTheSameConversation(t *testing.T) {
	app, pool, _ := v2App(t)
	must(t, app.SwitchConversation("zee", "C_OLD", "Old"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateOffline})
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateConnecting})
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventState, State: core.StateOnline})
	newer := "C_NEWER"
	pool.sessions["zee"] = core.Session{ProtocolVersion: 2, ConversationID: newer}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Welcome{ConversationID: &newer}})
	if session, _ := pool.Session("zee"); session.ConversationID != "C_OLD" {
		t.Errorf("reopening after an idle close should return to C_OLD, got %q", session.ConversationID)
	}
}

func TestSendChat_NamesTheConversationItEchoesInto(t *testing.T) {
	app, pool, sent := testApp(t)
	conversation := "C_SHOWN"
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Welcome{Principal: "Paul", ConversationID: &conversation}})
	must(t, app.SendChat("zee", "hello"))
	echo, _ := last[Entry](*sent, eventEntry)
	wire := pool.sent["zee"][len(pool.sent["zee"])-1]
	if echo.ConversationID != "C_SHOWN" || wire.ConversationID != "C_SHOWN" {
		t.Errorf("echo into %q, sent to %q; both should be C_SHOWN", echo.ConversationID, wire.ConversationID)
	}
}
