package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Input history (↑ / ↓ recall what you've sent to the agent on screen) and
// /edit (compose the draft in an external editor). See
// dev-notes/snag-list.md items 2 and 5.

// historyLimit caps how many sent lines each agent remembers.
const historyLimit = 200

// recordHistory remembers a sent line for ↑, skipping a repeat of the last
// one, and ends any history browsing.
func (m *model) recordHistory(line string) {
	if count := len(m.history); count == 0 || m.history[count-1] != line {
		m.history = append(m.history, line)
	}
	if overflow := len(m.history) - historyLimit; overflow > 0 {
		m.history = m.history[overflow:]
	}
	m.historyIndex = len(m.history)
	m.historyDraft = ""
}

// onFirstRow reports whether the input's cursor is on its first visual row.
func (m model) onFirstRow() bool {
	return m.input.Line() == 0 && m.input.LineInfo().RowOffset == 0
}

// onLastRow reports whether the input's cursor is on its last visual row.
func (m model) onLastRow() bool {
	info := m.input.LineInfo()
	return m.input.Line() == m.input.LineCount()-1 && info.RowOffset == info.Height-1
}

// recallPrevious shows the line sent before the one shown, keeping the
// draft to come back to. It reports false when there's nothing older.
func (m *model) recallPrevious() bool {
	if m.historyIndex <= 0 || len(m.history) == 0 {
		return false
	}
	if m.historyIndex >= len(m.history) {
		m.historyIndex = len(m.history)
		m.historyDraft = m.input.Value()
	}
	m.historyIndex--
	m.input.SetValue(m.history[m.historyIndex])
	m.relayout()
	return true
}

// recallNext shows the line sent after the one shown, or the draft after
// the newest. It reports false when not browsing history.
func (m *model) recallNext() bool {
	if m.historyIndex >= len(m.history) {
		return false
	}
	m.historyIndex++
	if m.historyIndex == len(m.history) {
		m.input.SetValue(m.historyDraft)
		m.historyDraft = ""
	} else {
		m.input.SetValue(m.history[m.historyIndex])
	}
	m.relayout()
	return true
}

// editorDoneMsg reports the external editor exiting.
type editorDoneMsg struct {
	path string
	err  error
}

// openEditor suspends the TUI and opens the configured editor on a temporary
// file holding text; the result comes back as editorDoneMsg.
func (m model) openEditor(text string) (tea.Model, tea.Cmd) {
	file, err := os.CreateTemp("", "sycamore-*.md")
	if err != nil {
		m.note(fmt.Sprintf("/edit: couldn't create a temporary file: %v", err))
		return m, nil
	}
	path := file.Name()
	_, writeErr := file.WriteString(text)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		m.note(fmt.Sprintf("/edit: couldn't write the temporary file: %v", err))
		return m, m.removeTemp(path)
	}
	words := strings.Fields(m.editor)
	if len(words) == 0 {
		m.note("/edit: no editor configured — set [ui] editor, or $VISUAL / $EDITOR")
		return m, m.removeTemp(path)
	}
	command := exec.Command(words[0], append(words[1:], path)...)
	return m, tea.ExecProcess(command, func(err error) tea.Msg { return editorDoneMsg{path: path, err: err} })
}

// finishEditing puts what the editor saved into the input to review and
// send, and removes the temporary file.
func (m model) finishEditing(done editorDoneMsg) (tea.Model, tea.Cmd) {
	if done.err != nil {
		m.note(fmt.Sprintf("/edit: the editor exited with an error, so nothing was loaded: %v", done.err))
		return m, m.removeTemp(done.path)
	}
	edited, err := os.ReadFile(done.path)
	if err != nil {
		m.note(fmt.Sprintf("/edit: couldn't read what you wrote: %v", err))
		return m, m.removeTemp(done.path)
	}
	m.input.SetValue(strings.TrimSuffix(string(edited), "\n"))
	m.relayout()
	return m, m.removeTemp(done.path)
}

// tempRemoveFailedMsg reports a temporary file that couldn't be removed.
type tempRemoveFailedMsg struct {
	path string
	err  error
}

// removeTemp deletes a temporary file off the UI thread, reporting failure.
func (m model) removeTemp(path string) tea.Cmd {
	return func() tea.Msg {
		var failure tea.Msg
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failure = tempRemoveFailedMsg{path: path, err: err}
		}
		return failure
	}
}
