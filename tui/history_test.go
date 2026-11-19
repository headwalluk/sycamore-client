package main

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/headwalluk/sycamore-client/core/config"
)

// typedAndSent sends each line through the input, as if typed and entered.
func typedAndSent(t *testing.T, ui model, lines ...string) model {
	t.Helper()
	for _, line := range lines {
		ui.input.SetValue(line)
		next, _ := ui.submit()
		ui = next.(model)
		ui.thinking, ui.turnActive = false, false // pretend each reply landed
	}
	return ui
}

func key(keyType tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: keyType} }

func TestHistory_UpAndDownRecallWhatWasSent(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedAndSent(t, ui, "first", "/info", "third")
	ui.input.SetValue("my draft")

	for _, want := range []string{"third", "/info", "first", "first"} {
		next, _ := ui.Update(key(tea.KeyUp))
		ui = next.(model)
		if got := ui.input.Value(); got != want {
			t.Fatalf("↑: want %q, got %q", want, got)
		}
	}
	for _, want := range []string{"/info", "third", "my draft"} {
		next, _ := ui.Update(key(tea.KeyDown))
		ui = next.(model)
		if got := ui.input.Value(); got != want {
			t.Fatalf("↓: want %q, got %q", want, got)
		}
	}
	next, _ := ui.Update(key(tea.KeyDown))
	if got := next.(model).input.Value(); got != "my draft" {
		t.Errorf("↓ past the draft should stay on it, got %q", got)
	}
}

func TestHistory_UpOnALaterLineMovesTheCursor(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedAndSent(t, ui, "earlier")
	ui.input.SetValue("line one\nline two") // cursor ends on the second line
	next, _ := ui.Update(key(tea.KeyUp))
	ui = next.(model)
	if ui.input.Value() != "line one\nline two" || ui.input.Line() != 0 {
		t.Errorf("↑ from the second line should move up within the draft, value=%q line=%d", ui.input.Value(), ui.input.Line())
	}
	next, _ = ui.Update(key(tea.KeyUp))
	if got := next.(model).input.Value(); got != "earlier" {
		t.Errorf("↑ again, from the first line, should recall, got %q", got)
	}
}

func TestHistory_SkipsRepeatsAndIsPerAgent(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = typedAndSent(t, ui, "same", "same")
	if len(ui.history) != 1 {
		t.Errorf("a repeated line should be kept once, got %v", ui.history)
	}
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	next, _ = ui.Update(key(tea.KeyUp))
	if got := next.(model).input.Value(); got != "" {
		t.Errorf("coop has its own (empty) history, got %q", got)
	}
}

func TestHistory_IsCapped(t *testing.T) {
	m, _, _ := connectedModel(t)
	for index := range historyLimit + 5 {
		m.recordHistory(strings.Repeat("x", index+1))
	}
	if len(m.history) != historyLimit || m.history[0] != strings.Repeat("x", 6) {
		t.Errorf("history should keep the newest %d, got %d starting %q", historyLimit, len(m.history), m.history[0])
	}
}

func TestEdit_OpensTheEditorAndLoadsTheResult(t *testing.T) {
	ui, _, prodLink := connectedModel(t)
	ui.editor = "true" // exits at once; the test plays the editor's part
	ui.input.SetValue("/edit a start")
	next, cmd := ui.submit()
	ui = next.(model)
	if cmd == nil {
		t.Fatal("/edit should hand the terminal to the editor")
	}
	if len(prodLink.sent) != 0 || len(ui.history) != 0 {
		t.Error("/edit itself is neither sent nor remembered")
	}

	file, err := os.CreateTemp(t.TempDir(), "edit-*.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("a long message\nover two lines\n"); err != nil {
		t.Fatal(err)
	}
	closeChecked(t, file)
	next, removal := ui.Update(editorDoneMsg{path: file.Name()})
	ui = next.(model)
	if got := ui.input.Value(); got != "a long message\nover two lines" {
		t.Errorf("the edited text should land in the input without its final newline, got %q", got)
	}
	runCmd(removal)
	if _, err := os.Stat(file.Name()); !os.IsNotExist(err) {
		t.Error("the temporary file should be removed")
	}
}

func TestEdit_StartsWithTheGivenText(t *testing.T) {
	m, _, _ := connectedModel(t)
	m.editor = "true"
	_, cmd := m.openEditor("draft text")
	if cmd == nil {
		t.Fatal("expected the editor Cmd")
	}
}

func TestEdit_AnEditorFailureLoadsNothing(t *testing.T) {
	m, _, _ := connectedModel(t)
	m.input.SetValue("kept")
	next, _ := m.Update(editorDoneMsg{path: t.TempDir() + "/missing.md", err: os.ErrPermission})
	m = next.(model)
	if m.input.Value() != "kept" || !strings.Contains(lastNote(t, m), "editor exited with an error") {
		t.Errorf("a failed edit should say so and leave the input, got %q / %q", m.input.Value(), lastNote(t, m))
	}
}

func TestEdit_NoEditorSaysSo(t *testing.T) {
	m, _, _ := connectedModel(t)
	m.editor = "  "
	next, _ := m.openEditor("")
	if !strings.Contains(lastNote(t, next.(model)), "no editor configured") {
		t.Errorf("expected a note, got %q", lastNote(t, next.(model)))
	}
}

func TestResolveEditor_FallsBackThroughTheEnvironment(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nano")
	if got := config.ResolveEditor(""); got != "nano" {
		t.Errorf("want $EDITOR, got %q", got)
	}
	t.Setenv("VISUAL", "emacsclient -t")
	if got := config.ResolveEditor(""); got != "emacsclient -t" {
		t.Errorf("$VISUAL wins over $EDITOR, got %q", got)
	}
	if got := config.ResolveEditor("vim"); got != "vim" {
		t.Errorf("the configured editor wins, got %q", got)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := config.ResolveEditor(""); got != "vi" {
		t.Errorf("with nothing set, vi, got %q", got)
	}
}
