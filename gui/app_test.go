package main

import (
	"strings"
	"testing"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

func ptr(s string) *string { return &s }

func TestTranslate_Welcome(t *testing.T) {
	got := translate(&protocol.Welcome{Principal: "Paul", ConversationTitle: ptr("Trip planning")})
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	e := got[0]
	if e.Role != "system" || e.Kind != "welcome" {
		t.Errorf("role/kind = %q/%q", e.Role, e.Kind)
	}
	if !strings.Contains(e.Text, "Paul") || !strings.Contains(e.Text, "Trip planning") {
		t.Errorf("welcome text = %q", e.Text)
	}
}

func TestTranslate_WelcomeNoConversation(t *testing.T) {
	got := translate(&protocol.Welcome{Principal: "Paul"})
	if !strings.Contains(got[0].Text, "no conversation") {
		t.Errorf("want a no-conversation placeholder, got %q", got[0].Text)
	}
}

func TestTranslate_Response(t *testing.T) {
	got := translate(&protocol.Response{
		Content:   []protocol.ContentBlock{{Type: "text", Text: "Hello there"}},
		Model:     "claude-haiku-4-5",
		ElapsedMS: 1342,
		Usage:     protocol.Usage{InputTokens: 10, OutputTokens: 20},
	})
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	entry := got[0]
	if entry.Role != "agent" || entry.Kind != "response" {
		t.Errorf("role/kind = %q/%q", entry.Role, entry.Kind)
	}
	if entry.Text != "Hello there" {
		t.Errorf("text = %q", entry.Text)
	}
	if entry.Model != "claude-haiku-4-5" || entry.Elapsed != 1342 {
		t.Errorf("model/elapsed = %q/%d", entry.Model, entry.Elapsed)
	}
	if entry.Tokens == nil || entry.Tokens.In != 10 || entry.Tokens.Out != 20 {
		t.Errorf("tokens = %+v", entry.Tokens)
	}
}

func TestTranslate_Error(t *testing.T) {
	got := translate(&protocol.Error{Message: "boom"})
	if len(got) != 1 || got[0].Kind != "error" || got[0].Text != "boom" {
		t.Errorf("error entry = %+v", got)
	}
}

func TestTranslate_UnknownIsIgnored(t *testing.T) {
	if got := translate(&protocol.HistoryPage{}); got != nil {
		t.Errorf("unhandled kinds should translate to nil for the skeleton, got %+v", got)
	}
}

func TestBlocksToText_FlattensAndPlaceholders(t *testing.T) {
	out := blocksToText([]protocol.ContentBlock{
		{Type: "text", Text: "see "},
		{Type: "tool_use", Name: "web_search"},
		{Type: "text", Text: " done"},
		{Type: "image", MediaType: "image/png"},
	})
	if !strings.Contains(out, "see ") || !strings.Contains(out, " done") {
		t.Errorf("text blocks not concatenated: %q", out)
	}
	if !strings.Contains(out, "[tool: web_search]") || !strings.Contains(out, "[image: image/png]") {
		t.Errorf("tool/image placeholders missing: %q", out)
	}
}

func TestApplySession_FromWelcome(t *testing.T) {
	status := &agentStatus{model: "kept"}
	if !applySession(status, &protocol.Welcome{Principal: "Paul", ProtocolVersion: 2, ConversationTitle: ptr("Trip")}) {
		t.Fatal("a welcome should change the status bar")
	}
	if status.principal != "Paul" || status.protocolVersion != 2 || status.conversationTitle != "Trip" || status.model != "kept" {
		t.Errorf("status after welcome = %+v", status)
	}
}

func TestApplySession_KeepsTheTitleThroughAReply(t *testing.T) {
	status := &agentStatus{conversationTitle: "Trip"}
	if !applySession(status, &protocol.Response{Model: "claude-sonnet-5"}) || status.model != "claude-sonnet-5" {
		t.Errorf("a reply with a new model should update it, got %+v", status)
	}
	if status.conversationTitle != "Trip" {
		t.Errorf("a reply must not clear the title, got %q", status.conversationTitle)
	}
	if applySession(status, &protocol.Response{Model: "claude-sonnet-5"}) {
		t.Error("the same model again is no change")
	}
}

func TestApplySession_RenameNewAndModelSet(t *testing.T) {
	status := &agentStatus{conversationTitle: "Old"}
	applySession(status, &protocol.ConversationRenamed{Title: "New title"})
	if status.conversationTitle != "New title" {
		t.Errorf("rename: %q", status.conversationTitle)
	}
	applySession(status, &protocol.NewConversation{ConversationID: "C_2"})
	if status.conversationTitle != "" {
		t.Errorf("a new conversation has no title yet, got %q", status.conversationTitle)
	}
	applySession(status, &protocol.ModelSet{Resolved: "anthropic/claude-opus-5-5"})
	if status.model != "anthropic/claude-opus-5-5" {
		t.Errorf("set_model: %q", status.model)
	}
	if applySession(status, &protocol.Pong{}) {
		t.Error("a pong changes nothing")
	}
}

func TestEndsTurn(t *testing.T) {
	if !endsTurn(&protocol.Response{}) || !endsTurn(&protocol.Error{}) {
		t.Error("response and error should end the thinking state")
	}
	if endsTurn(&protocol.Welcome{}) || endsTurn(&protocol.Pong{}) {
		t.Error("welcome/pong should not end a turn")
	}
}

func TestTranslate_UndecodableIsShown(t *testing.T) {
	entries := translate(&protocol.Undecodable{Kind: protocol.TypeResponse, Err: "bad content"})
	if len(entries) != 1 || entries[0].Kind != "error" || !strings.Contains(entries[0].Text, "couldn't read a response message") {
		t.Errorf("got %+v", entries)
	}
	if !endsTurn(&protocol.Undecodable{Kind: protocol.TypeResponse}) || endsTurn(&protocol.Undecodable{Kind: protocol.TypeModelsList}) {
		t.Error("only an unreadable response or error ends a turn")
	}
}
