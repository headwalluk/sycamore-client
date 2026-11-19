package main

import (
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Emoji shortcodes in the input: a complete :code: becomes its emoji as the
// closing colon is typed, and a partial one with few enough matches opens a
// chooser above the input. See dev-notes/00-project-tracker.md, M30.

//go:generate sh -c "go run ./internal/tools/emojigen \"$(go list -m -f '{{.Dir}}' github.com/yuin/goldmark-emoji)/_tools/github.json\" emoji_table.go"

// emojiCode is one shortcode (without colons) and its emoji.
type emojiCode struct {
	code  string
	glyph string
}

// emojiMinPrefix is how many code characters open the chooser.
const emojiMinPrefix = 2

// emojiChooser is the open chooser; no choices means it is closed.
type emojiChooser struct {
	choices  []emojiCode
	selected int
	typed    int // runes a pick replaces: the colon and the code typed so far
}

// lookupEmoji returns the emoji for a complete shortcode.
func lookupEmoji(code string) (string, bool) {
	index := sort.Search(len(emojiTable), func(at int) bool { return emojiTable[at].code >= code })
	found := index < len(emojiTable) && emojiTable[index].code == code
	glyph := ""
	if found {
		glyph = emojiTable[index].glyph
	}
	return glyph, found
}

// matchEmoji returns the emoji whose codes start with prefix, one row per
// emoji, shortest code first; nil when there are none or more than limit.
func matchEmoji(prefix string, limit int) []emojiCode {
	start := sort.Search(len(emojiTable), func(at int) bool { return emojiTable[at].code >= prefix })
	var matches []emojiCode
	for _, entry := range emojiTable[start:] {
		if !strings.HasPrefix(entry.code, prefix) {
			break
		}
		matches = append(matches, entry)
	}
	sort.SliceStable(matches, func(left, right int) bool {
		return len(matches[left].code) < len(matches[right].code)
	})

	seen := map[string]bool{}
	var choices []emojiCode
	for _, match := range matches {
		if seen[match.glyph] {
			continue
		}
		seen[match.glyph] = true
		choices = append(choices, match)
	}
	if len(choices) > limit {
		choices = nil
	}
	return choices
}

// isCodeRune reports whether r can appear in a shortcode.
func isCodeRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '+' || r == '-'
}

// shortcodeBefore finds a code being typed at the end of before: ":code"
// or, when closed, ":code:". The opening colon must start the line or
// follow whitespace, so "10:30" and "https://" don't count.
func shortcodeBefore(before []rune, closed bool) (string, bool) {
	end := len(before)
	if closed {
		if end == 0 || before[end-1] != ':' {
			return "", false
		}
		end--
	}
	start := end
	for start > 0 && isCodeRune(before[start-1]) {
		start--
	}
	colon := start - 1
	opens := colon >= 0 && before[colon] == ':' && (colon == 0 || unicode.IsSpace(before[colon-1]))
	return string(before[start:end]), opens && start < end
}

// textBeforeCursor returns the cursor's line up to the cursor, and whether
// the cursor is inside a code span or fenced block.
func (m model) textBeforeCursor() ([]rune, bool) {
	lines := strings.Split(m.input.Value(), "\n")
	row := min(m.input.Line(), len(lines)-1)
	info := m.input.LineInfo()
	line := []rune(lines[row])
	before := line[:min(info.StartColumn+info.ColumnOffset, len(line))]

	fences := 0
	for _, earlier := range lines[:row+1] {
		if strings.HasPrefix(strings.TrimSpace(earlier), "```") {
			fences++
		}
	}
	inFence := fences%2 == 1 || strings.HasPrefix(strings.TrimSpace(lines[row]), "```")
	inSpan := strings.Count(string(before), "`")%2 == 1
	return before, inFence || inSpan
}

// afterTyping expands a just-closed shortcode, or opens the chooser for a
// partial one, after a key typed into the input.
func (m *model) afterTyping(msg tea.KeyMsg) {
	m.emoji = emojiChooser{}
	before, inCode := m.textBeforeCursor()
	if inCode {
		return
	}
	expanded := false
	if msg.Type == tea.KeyRunes && string(msg.Runes) == ":" {
		if code, ok := shortcodeBefore(before, true); ok {
			if glyph, found := lookupEmoji(code); found {
				m.replaceBeforeCursor(len([]rune(code))+2, glyph)
				expanded = true
			}
		}
	}
	if code, ok := shortcodeBefore(before, false); ok && !expanded && len([]rune(code)) >= emojiMinPrefix {
		m.emoji = emojiChooser{choices: matchEmoji(code, m.emojiChoices), typed: len([]rune(code)) + 1}
	}
}

// replaceBeforeCursor deletes count runes before the cursor and inserts text.
func (m *model) replaceBeforeCursor(count int, text string) {
	backspace := tea.KeyMsg{Type: tea.KeyBackspace}
	for range count {
		m.input, _ = m.input.Update(backspace)
	}
	m.input.InsertString(text)
}

// chooseEmoji handles a key while the chooser is open, reporting whether it
// used it; any other key closes the chooser and goes on to the input.
func (m *model) chooseEmoji(msg tea.KeyMsg) bool {
	handled := true
	switch msg.Type {
	case tea.KeyTab, tea.KeyEnter:
		m.replaceBeforeCursor(m.emoji.typed, m.emoji.choices[m.emoji.selected].glyph)
		m.emoji = emojiChooser{}
	case tea.KeyUp:
		m.emoji.selected = max(m.emoji.selected-1, 0)
	case tea.KeyDown:
		m.emoji.selected = min(m.emoji.selected+1, len(m.emoji.choices)-1)
	case tea.KeyEsc:
		m.emoji = emojiChooser{}
	default:
		m.emoji = emojiChooser{}
		handled = false
	}
	if handled {
		m.relayout()
	}
	return handled
}

// renderEmojiChooser draws the chooser's rows, or "" when it is closed.
func (m model) renderEmojiChooser() string {
	rows := make([]string, 0, len(m.emoji.choices))
	for index, choice := range m.emoji.choices {
		body := choice.glyph + " :" + choice.code + ":"
		if index == m.emoji.selected {
			rows = append(rows, pickerSelStyle.Width(m.width).Render(ansi.Truncate("▌ "+body, m.width, "…")))
		} else {
			rows = append(rows, ansi.Truncate("  "+body, m.width, "…"))
		}
	}
	return strings.Join(rows, "\n")
}
