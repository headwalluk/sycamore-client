package main

import (
	"strings"
	"testing"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// lastSent returns the last message sent to agent.
func lastSent(pool *fakePool, agent string) protocol.ClientMessage {
	sent := pool.sent[agent]
	var message protocol.ClientMessage
	if len(sent) > 0 {
		message = sent[len(sent)-1]
	}
	return message
}

// lastNote returns the newest note emitted for agent.
func lastNote(sent []emitted, agent string) string {
	got := notes(sent, agent)
	text := ""
	if len(got) > 0 {
		text = got[len(got)-1]
	}
	return text
}

func TestCommand_WireCommands(t *testing.T) {
	cases := map[string]protocol.ClientMessage{
		"/info":         {Type: protocol.TypeInfo},
		"/models":       {Type: protocol.TypeModels},
		"/model":        {Type: protocol.TypeShowModel},
		"/model sonnet": {Type: protocol.TypeSetModel, Model: "sonnet"},
		"/rename Trip":  {Type: protocol.TypeRename, Title: "Trip"},
		"/capture on":   {Type: protocol.TypeCapture, Action: "on"},
		"/capture":      {Type: protocol.TypeCapture, Action: "status"},
		"/ping":         {Type: protocol.TypePing},
		"/new":          {Type: protocol.TypeNew},
		"/dream":        {Type: protocol.TypeDream},
	}
	for line, want := range cases {
		app, pool, _ := v2App(t)
		if err := app.Command("zee", line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		got := lastSent(pool, "zee")
		if got.Type != want.Type || got.Model != want.Model || got.Title != want.Title || got.Action != want.Action {
			t.Errorf("%s: sent %+v, want %+v", line, got, want)
		}
	}
}

func TestCommand_EchoesTheLine(t *testing.T) {
	app, _, sent := v2App(t)
	must(t, app.Command("zee", "/info"))
	if echo, _ := last[Entry](*sent, eventEntry); echo.Role != "you" || echo.Kind != "command" || echo.Text != "/info" || echo.ConversationID != "C_A" {
		t.Errorf("the command should be echoed into the conversation, got %+v", echo)
	}
}

func TestCommand_ModelWorkShowsThinkingUntilTheReply(t *testing.T) {
	app, _, sent := v2App(t)
	must(t, app.Command("zee", "/new"))
	if thinking, _ := last[ThinkingMsg](*sent, eventThinking); !thinking.Thinking {
		t.Fatal("/new should show the working state")
	}
	if err := app.Command("zee", "/info"); err == nil {
		t.Error("a second wire command should wait while /new runs")
	}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Finalizing{ConversationID: "C_A"}})
	if thinking, _ := last[ThinkingMsg](*sent, eventThinking); !thinking.Thinking {
		t.Error("finalizing isn't the end of /new")
	}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.NewConversation{ConversationID: "C_NEW"}})
	if thinking, _ := last[ThinkingMsg](*sent, eventThinking); thinking.Thinking {
		t.Error("new_conversation ends /new")
	}
	if conversation, _ := last[ConversationMsg](*sent, eventConversation); conversation.ConversationID != "C_NEW" || conversation.Adopt {
		t.Errorf("the frontend should switch to the new conversation, got %+v", conversation)
	}
	if entry, _ := last[Entry](*sent, eventEntry); entry.ConversationID != "C_NEW" || entry.Text != "new conversation" {
		t.Errorf("the note belongs to the new conversation, got %+v", entry)
	}
}

func TestCommand_AnErrorEndsTheWait(t *testing.T) {
	app, _, sent := v2App(t)
	must(t, app.Command("zee", "/dream"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Error{Message: "dream locked"}})
	if thinking, _ := last[ThinkingMsg](*sent, eventThinking); thinking.Thinking {
		t.Error("an error should end the wait")
	}
}

func TestCommand_ParkNeedsTheCapability(t *testing.T) {
	app, pool, sent := v2App(t)
	must(t, app.Command("zee", "/park"))
	if lastSent(pool, "zee").Type == protocol.TypePark || !strings.Contains(lastNote(*sent, "zee"), `"park"`) {
		t.Errorf("a v2 daemon can't be assumed to park: note=%q", lastNote(*sent, "zee"))
	}
	pool.sessions["zee"] = core.Session{ProtocolVersion: 3, Agent: &protocol.AgentProfile{Name: "zee", Capabilities: []string{protocol.CapPark}}}
	must(t, app.Command("zee", "/park"))
	if lastSent(pool, "zee").Type != protocol.TypePark {
		t.Error("an agent advertising park should get it")
	}
}

func TestCommand_WaitsForAChatTurn(t *testing.T) {
	app, _, _ := v2App(t)
	must(t, app.SendChat("zee", "long job"))
	if err := app.Command("zee", "/info"); err == nil || !strings.Contains(err.Error(), "still working") {
		t.Errorf("a wire command should wait for the running turn, got %v", err)
	}
	if err := app.Command("zee", "/help"); err != nil {
		t.Errorf("local commands still work: %v", err)
	}
}

func TestCommand_LocalOnes(t *testing.T) {
	app, _, sent := v2App(t)
	must(t, app.Command("zee", "/help"))
	if !strings.Contains(lastNote(*sent, "zee"), "/switch") {
		t.Error("/help should list the commands")
	}
	must(t, app.Command("zee", "/frobnicate"))
	if !strings.Contains(lastNote(*sent, "zee"), "unknown command: /frobnicate") {
		t.Errorf("unknown: %q", lastNote(*sent, "zee"))
	}
	must(t, app.Command("zee", "/capture sideways"))
	if !strings.Contains(lastNote(*sent, "zee"), "usage: /capture") {
		t.Errorf("bad capture action: %q", lastNote(*sent, "zee"))
	}
	must(t, app.Command("zee", "/switch"))
	if panel, _ := last[string](*sent, eventPanel); panel != "zee" {
		t.Errorf("bare /switch should open the panel, got %q", panel)
	}
	must(t, app.Command("zee", "/connect coop"))
	if app.active != "coop" {
		t.Error("/connect coop should put coop on screen")
	}
	must(t, app.Command("coop", "/close"))
	if app.active != "zee" {
		t.Error("/close should close coop")
	}
}

// deployList is a conversations list with two deploy threads and a trip.
func deployList() *protocol.ConversationsList {
	checklist, review, trip := "Deploy checklist", "Deploy review", "Trip"
	return &protocol.ConversationsList{Conversations: []protocol.ConversationSummary{
		{ID: "C_1", Title: &checklist, TurnCount: 12, UpdatedAt: "2026-09-26T14:05:00Z"},
		{ID: "C_2", Title: &review, TurnCount: 4, UpdatedAt: "2026-09-25T09:30:00Z"},
		{ID: "C_3", Title: &trip, TurnCount: 2, UpdatedAt: "2026-09-24T08:00:00Z"},
	}}
}

func TestCommand_ListGoesIntoTheTranscript(t *testing.T) {
	app, pool, sent := v2App(t)
	must(t, app.Command("zee", "/list"))
	if lastSent(pool, "zee").Type != protocol.TypeConversations {
		t.Fatal("/list should ask for the conversations")
	}
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: deployList()})
	listing, _ := last[Entry](*sent, eventEntry)
	if listing.Kind != "info" || !strings.Contains(listing.Text, " 1. Deploy checklist · 12 turns · 2026-09-26 14:05") {
		t.Errorf("the list should be an info block, got %+v", listing)
	}
}

func TestCommand_SwitchByNumberAndTitle(t *testing.T) {
	app, pool, sent := v2App(t)
	must(t, app.Command("zee", "/switch 3"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: deployList()})
	if session, _ := pool.Session("zee"); session.ConversationID != "C_3" {
		t.Errorf("/switch 3 should show Trip, got %q", session.ConversationID)
	}

	must(t, app.Command("zee", "/switch deploy"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: deployList()})
	if !strings.Contains(lastNote(*sent, "zee"), `2 conversations match "deploy"`) {
		t.Errorf("an ambiguous title should say so, got %q", lastNote(*sent, "zee"))
	}

	must(t, app.Command("zee", "/switch review"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: deployList()})
	if session, _ := pool.Session("zee"); session.ConversationID != "C_2" {
		t.Errorf("/switch review should show the review, got %q", session.ConversationID)
	}
	if info, _ := last[SessionInfo](*sent, eventSession); info.ConversationTitle != "Deploy review" {
		t.Errorf("the title should follow, got %q", info.ConversationTitle)
	}
}

func TestCommand_SwitchOnAnotherAgent(t *testing.T) {
	app, pool, _ := v2App(t)
	must(t, app.Command("zee", "/switch coop:trip"))
	if app.active != "coop" || lastSent(pool, "coop").Type != protocol.TypeConversations {
		t.Fatalf("coop should be shown and asked for its list, active=%q", app.active)
	}
	app.handleEvent(core.Event{Agent: "coop", Kind: core.EventMessage, Message: deployList()})
	if session, _ := pool.Session("coop"); session.ConversationID != "C_3" {
		t.Errorf("coop should switch to its Trip, got %q", session.ConversationID)
	}
}

func TestModels_PickerGetsRowsNotTranscript(t *testing.T) {
	app, pool, sent := v2App(t)
	if err := app.Models("zee"); err != nil {
		t.Fatal(err)
	}
	before := len(entriesFor(*sent, "zee", "C_A"))
	current := "sonnet"
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.ModelsList{Current: &current,
		Aliases: []protocol.Alias{{Name: "sonnet", Target: "anthropic/claude-sonnet-5"}, {Name: "opus", Target: "anthropic/claude-opus-5-5"}}}})
	models, _ := last[ModelsMsg](*sent, eventModels)
	if len(models.Models) != 2 || !models.Models[0].Current || models.Models[1].Current {
		t.Errorf("the picker should get the aliases with the current one marked: %+v", models)
	}
	if len(entriesFor(*sent, "zee", "C_A")) != before {
		t.Error("a picker's list must not go into the transcript")
	}
	must(t, app.SetModel("zee", "opus"))
	if got := lastSent(pool, "zee"); got.Type != protocol.TypeSetModel || got.Model != "opus" {
		t.Errorf("SetModel: %+v", got)
	}

	must(t, app.Command("zee", "/models"))
	app.handleEvent(core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.ModelsList{Current: &current, Aliases: []protocol.Alias{{Name: "sonnet", Target: "t"}}}})
	if listing, _ := last[Entry](*sent, eventEntry); listing.Kind != "info" || !strings.Contains(listing.Text, "* sonnet") {
		t.Errorf("/models should list into the transcript, got %+v", listing)
	}
}

func TestTranslateCommandReply_Wording(t *testing.T) {
	title := "Trip"
	cases := map[string]protocol.ServerMessage{
		"parked Trip: 7 turns folded into memory · still open": &protocol.ConversationParked{ConversationTitle: &title, Parked: true, TurnsParked: 7},
		"parked Trip: 1 turn folded into memory · still open":  &protocol.ConversationParked{ConversationTitle: &title, Parked: true, TurnsParked: 1},
		"(untitled) was already consolidated":                  &protocol.ConversationParked{Parked: false},
		"folding conversation into memory…":                    &protocol.Finalizing{},
		"model set: opus → anthropic/claude-opus-5-5":          &protocol.ModelSet{Model: "opus", Resolved: "anthropic/claude-opus-5-5"},
		"no active conversation":                               &protocol.ModelShow{},
	}
	for want, message := range cases {
		entries := translateCommandReply(message)
		if len(entries) != 1 || !strings.Contains(entries[0].Text, want) {
			t.Errorf("want %q, got %+v", want, entries)
		}
	}
	dream := translateCommandReply(&protocol.DreamResult{Dreamed: true, FilesProcessed: 3, BioUpdates: []string{"paul"}, DurationMS: 2500})
	if dream[0].Title != "Dream" || !strings.Contains(dream[0].Text, "bios:    paul") || !strings.Contains(dream[0].Text, "took:    2.5s") {
		t.Errorf("dream: %+v", dream)
	}
	if translateCommandReply(&protocol.Pong{}) != nil {
		t.Error("a non-command reply should come back nil")
	}
}
