package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// typedKeys sends text to the model one key at a time, as if typed.
func typedKeys(ui model, text string) model {
	for _, typedRune := range text {
		keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{typedRune}}
		if typedRune == ' ' {
			keyMsg.Type = tea.KeySpace
		}
		next, _ := ui.Update(keyMsg)
		ui = next.(model)
	}
	return ui
}

// pressed sends one key to the model.
func pressed(ui model, keyType tea.KeyType) model {
	next, _ := ui.Update(key(keyType))
	return next.(model)
}

// choiceCodes lists the open chooser's codes.
func choiceCodes(ui model) []string {
	codes := make([]string, 0, len(ui.emoji.choices))
	for _, choice := range ui.emoji.choices {
		codes = append(codes, choice.code)
	}
	return codes
}

func TestEmoji_ClosingColonExpands(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedKeys(ui, "nice :thumbsup: and :+1: done")
	if got := ui.input.Value(); got != "nice 👍 and 👍 done" {
		t.Errorf("got %q", got)
	}
}

func TestEmoji_LeftAsTyped(t *testing.T) {
	cases := map[string]string{
		"unknown code":           ":notacode:",
		"colon after a letter":   "a:tada:",
		"inside a code span":     "run ` :tada:",
		"inside a second span":   "`x` and ` :tada:",
		"clock and URL":          "at 10:30, https://example.com",
		"two colons, empty code": "::",
	}
	for name, text := range cases {
		ui, _, _ := connectedModel(t)
		ui = typedKeys(ui, text)
		if got := ui.input.Value(); got != text {
			t.Errorf("%s: want %q, got %q", name, text, got)
		}
		if len(ui.emoji.choices) != 0 {
			t.Errorf("%s: the chooser should be closed, got %v", name, choiceCodes(ui))
		}
	}
}

func TestEmoji_CodeLeftAsTyped(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedKeys(ui, "`x` :tada:")
	if got := ui.input.Value(); got != "`x` 🎉" {
		t.Errorf("after a closed span: got %q", got)
	}
	ui.input.Reset()
	ui.input.SetValue("```go\n")
	ui = typedKeys(ui, ":tada:")
	if got := ui.input.Value(); got != "```go\n:tada:" {
		t.Errorf("inside a fence: got %q", got)
	}
	ui.input.SetValue("```\ncode\n```\n")
	ui = typedKeys(ui, ":tada:")
	if got := ui.input.Value(); got != "```\ncode\n```\n🎉" {
		t.Errorf("after a closed fence: got %q", got)
	}
}

func TestEmoji_PasteNotExpanded(t *testing.T) {
	ui, _, _ := connectedModel(t)
	next, _ := ui.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(":tada:"), Paste: true})
	if got := next.(model).input.Value(); got != ":tada:" {
		t.Errorf("got %q", got)
	}
}

func TestEmoji_ChooserOpensNarrowsAndPicks(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedKeys(ui, "ok :t")
	if len(ui.emoji.choices) != 0 {
		t.Errorf("one code character shouldn't open the chooser, got %v", choiceCodes(ui))
	}
	ui = typedKeys(ui, "h")
	if len(ui.emoji.choices) != 0 {
		t.Errorf("more matches than the limit should show nothing, got %v", choiceCodes(ui))
	}
	ui = typedKeys(ui, "umbs")
	if got := strings.Join(choiceCodes(ui), " "); got != "thumbsup thumbsdown" {
		t.Fatalf("want thumbsup then thumbsdown, got %q", got)
	}
	ui = pressed(ui, tea.KeyDown)
	ui = pressed(ui, tea.KeyTab)
	if got := ui.input.Value(); got != "ok 👎" {
		t.Errorf("tab should pick the highlighted emoji, got %q", got)
	}
	if len(ui.emoji.choices) != 0 {
		t.Error("picking should close the chooser")
	}
}

func TestEmoji_EnterPicksRatherThanSends(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedKeys(ui, ":thumbs")
	ui = pressed(ui, tea.KeyEnter)
	if got := ui.input.Value(); got != "👍" {
		t.Errorf("enter should pick, got %q", got)
	}
	if ui.thinking || len(ui.entries) != 0 {
		t.Errorf("enter on the chooser must not send, entries=%+v", ui.entries)
	}
}

func TestEmoji_EscClosesThenEnterSends(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedKeys(ui, ":thumbs")
	ui = pressed(ui, tea.KeyEsc)
	if len(ui.emoji.choices) != 0 || ui.input.Value() != ":thumbs" {
		t.Fatalf("esc should close the chooser and keep the text, value=%q", ui.input.Value())
	}
	ui = pressed(ui, tea.KeyEnter)
	if len(ui.entries) != 1 || ui.entries[0].text != ":thumbs" {
		t.Errorf("enter with the chooser closed should send, entries=%+v", ui.entries)
	}
}

func TestEmoji_ArrowsMoveTheChooserNotHistory(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedAndSent(t, ui, "earlier")
	ui = typedKeys(ui, ":thumbs")
	ui = pressed(ui, tea.KeyUp)
	if got := ui.input.Value(); got != ":thumbs" {
		t.Errorf("↑ with the chooser open shouldn't recall history, got %q", got)
	}
	ui = pressed(ui, tea.KeyDown)
	ui = pressed(ui, tea.KeyDown)
	if ui.emoji.selected != 1 {
		t.Errorf("↓ should stop on the last choice, selected=%d", ui.emoji.selected)
	}
}

func TestEmoji_OtherKeysCloseAndBackspaceReopens(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedKeys(ui, ":thumbsx")
	if len(ui.emoji.choices) != 0 {
		t.Fatalf("no match should show nothing, got %v", choiceCodes(ui))
	}
	ui = pressed(ui, tea.KeyBackspace)
	if len(ui.emoji.choices) != 2 {
		t.Fatalf("backspace back to :thumbs should reopen, got %v", choiceCodes(ui))
	}
	ui = typedKeys(ui, " ")
	if len(ui.emoji.choices) != 0 || ui.input.Value() != ":thumbs " {
		t.Errorf("space should close the chooser and type, value=%q", ui.input.Value())
	}
}

func TestEmoji_LimitFromConfig(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui.emojiChoices = 1
	ui = typedKeys(ui, ":thumbs")
	if len(ui.emoji.choices) != 0 {
		t.Errorf("two matches over a limit of 1 should show nothing, got %v", choiceCodes(ui))
	}
	ui.emojiChoices = 0
	ui = typedKeys(ui, "up")
	if len(ui.emoji.choices) != 0 {
		t.Errorf("0 should turn the chooser off, got %v", choiceCodes(ui))
	}
	ui = typedKeys(ui, ":")
	if got := ui.input.Value(); got != "👍" {
		t.Errorf("expansion works with the chooser off, got %q", got)
	}
}

func TestEmoji_ChooserShownAboveTheInput(t *testing.T) {
	ui, _, _ := connectedModel(t)
	before := ui.vp.Height
	ui = typedKeys(ui, ":thumbs")
	view := ui.View()
	if !strings.Contains(view, "👍 :thumbsup:") || !strings.Contains(view, "👎 :thumbsdown:") {
		t.Errorf("the chooser rows should be drawn, view:\n%s", view)
	}
	if !strings.Contains(view, "tab/enter pick") {
		t.Error("the footer should say how to pick")
	}
	if ui.vp.Height != before-2 {
		t.Errorf("the transcript should give up two rows, %d → %d", before, ui.vp.Height)
	}
}

func TestMatchEmoji_OneRowPerEmojiShortestFirst(t *testing.T) {
	saved := emojiTable
	t.Cleanup(func() { emojiTable = saved })
	emojiTable = []emojiCode{{"aa_long", "Y"}, {"aab", "X"}, {"aabb", "X"}, {"ab", "Z"}}
	choices := matchEmoji("aa", 5)
	if len(choices) != 2 || choices[0].code != "aab" || choices[1].code != "aa_long" {
		t.Errorf("want aab then aa_long, got %+v", choices)
	}
	if glyph, found := lookupEmoji("aabb"); !found || glyph != "X" {
		t.Errorf("lookup: got %q %v", glyph, found)
	}
	if _, found := lookupEmoji("aa"); found {
		t.Error("a prefix is not a code")
	}
}
