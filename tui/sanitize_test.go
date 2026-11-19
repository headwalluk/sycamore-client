package main

import (
	"testing"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

func TestTerminalSafe(t *testing.T) {
	cases := map[string]string{
		"plain text":                  "plain text",
		"keeps\nnewlines\tand tabs":   "keeps\nnewlines\tand tabs",
		"\x1b]52;c;aGk=\x07clipboard": "]52;c;aGk=clipboard",
		"\x1b[2Jcleared":              "[2Jcleared",
		"carriage\rreturn":            "carriagereturn",
		"c1\u009b31mred":              "c131mred",
		"bidi\u202eevil":              "bidievil",
		"bad \xff byte":               "bad � byte",
		"emoji 🌳 and accents café":    "emoji 🌳 and accents café",
	}
	for input, want := range cases {
		if got := terminalSafe(input); got != want {
			t.Errorf("terminalSafe(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSanitizeStringsWalksMessages(t *testing.T) {
	title := "title\x1b[31m"
	response := &protocol.Response{
		ConversationID: "conv\x07",
		Content: []protocol.ContentBlock{
			{Type: "text", Text: "hello\x1b]8;;http://x\x07"},
			{Type: "tool_use", Name: "tool\x1b", Input: map[string]any{
				"key\x1b": "value\x1b",
				"nested":  []any{"deep\x1b", 3.0, map[string]any{"inner": "x\x1b"}},
			}},
		},
	}
	sanitizeStrings(response)
	if response.ConversationID != "conv" || response.Content[0].Text != "hello]8;;http://x" || response.Content[1].Name != "tool" {
		t.Errorf("strings not cleaned: %+v", response)
	}
	input := response.Content[1].Input
	if input["key"] != "value" {
		t.Errorf("map entry not cleaned: %#v", input)
	}
	nested := input["nested"].([]any)
	if nested[0] != "deep" || nested[1] != 3.0 || nested[2].(map[string]any)["inner"] != "x" {
		t.Errorf("nested values not cleaned: %#v", nested)
	}

	welcome := &protocol.Welcome{ConversationTitle: &title, Agent: &protocol.AgentProfile{DisplayName: "Zee\x1b[5m"}}
	sanitizeStrings(welcome)
	if *welcome.ConversationTitle != "title[31m" || welcome.Agent.DisplayName != "Zee[5m" {
		t.Errorf("pointer fields not cleaned: %q %q", *welcome.ConversationTitle, welcome.Agent.DisplayName)
	}
}
