package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// pickerItem is one selectable row. id is the value handed back on
// selection (e.g. a conversation id or an instance name); title is the
// primary text; desc is optional dim secondary text (e.g.
// "9 turns · 2026-06-21"). The filter matches against title and desc.
type pickerItem struct {
	id    string
	title string
	desc  string

	// Used by the switcher only: a status marker before the title, and the
	// title's colour.
	mark   string
	accent lipgloss.Color
}

// picker is a reusable modal list: a type-to-filter box over a scrolling
// (windowed) list of items, with one highlighted row selected on enter.
// It is deliberately generic — conversation switching (M17), instance
// selection (M14, bare /connect), and later model selection all drive the
// same widget. The owning model holds one while the overlay is open and
// routes key messages to Update; an enter resolves to the same action as
// the equivalent typed command (one action path, two front doors).
type picker struct {
	title    string
	items    []pickerItem
	filtered []pickerItem
	input    textinput.Model
	cursor   int // index into filtered (the highlighted row)
	offset   int // first visible filtered row (scroll window top)
	width    int
	height   int
}

// pickerOutcome is what one key press resolved to.
type pickerOutcome int

const (
	pickerNone      pickerOutcome = iota // still open
	pickerSelected                       // enter on a row — read via Selected()
	pickerCancelled                      // esc
)

var (
	pickerTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("63")).Bold(true)
	pickerSelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("63"))
)

func newPicker(title string, items []pickerItem) picker {
	ti := textinput.New()
	ti.Prompt = "filter› "
	ti.Focus()
	return picker{title: title, items: items, filtered: items, input: ti}
}

func (p *picker) setSize(w, h int) {
	p.width, p.height = w, h
	p.input.Width = w - lipgloss.Width(p.input.Prompt) - 1
	p.clampWindow()
}

// listHeight is the number of item rows the list area can show: the modal
// height minus the title, filter, and footer rows.
func (p picker) listHeight() int {
	if h := p.height - 3; h > 0 {
		return h
	}
	return 1
}

// Update folds one key press into the picker, reporting whether it
// resolved to a selection or a cancel (else the overlay stays open).
func (p picker) Update(msg tea.KeyMsg) (picker, pickerOutcome) {
	switch msg.Type {
	case tea.KeyEsc:
		return p, pickerCancelled
	case tea.KeyEnter:
		if len(p.filtered) == 0 {
			return p, pickerNone
		}
		return p, pickerSelected
	case tea.KeyUp, tea.KeyCtrlP:
		if p.cursor > 0 {
			p.cursor--
		}
		p.clampWindow()
		return p, pickerNone
	case tea.KeyDown, tea.KeyCtrlN:
		if p.cursor < len(p.filtered)-1 {
			p.cursor++
		}
		p.clampWindow()
		return p, pickerNone
	default:
		// Any other key edits the filter. (The textinput blink Cmd is
		// dropped — a modal cursor that doesn't blink is fine.)
		p.input, _ = p.input.Update(msg)
		p.applyFilter()
		return p, pickerNone
	}
}

// Selected returns the highlighted item; ok is false when the list is
// empty. Call after Update reports pickerSelected.
func (p picker) Selected() (pickerItem, bool) {
	if p.cursor < 0 || p.cursor >= len(p.filtered) {
		return pickerItem{}, false
	}
	return p.filtered[p.cursor], true
}

func (p *picker) applyFilter() {
	p.filtered = filterItems(p.items, p.input.Value())
	p.cursor = 0
	p.offset = 0
}

// filterItems returns the items whose title or desc contains query
// (case-insensitive substring). An empty query returns all items.
func filterItems(items []pickerItem, query string) []pickerItem {
	normalised := strings.ToLower(strings.TrimSpace(query))
	if normalised == "" {
		return items
	}
	out := make([]pickerItem, 0, len(items))
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.title), normalised) || strings.Contains(strings.ToLower(it.desc), normalised) {
			out = append(out, it)
		}
	}
	return out
}

// clampWindow keeps the cursor in range and the scroll window positioned
// so the cursor stays visible.
func (p *picker) clampWindow() {
	if p.cursor > len(p.filtered)-1 {
		p.cursor = len(p.filtered) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	lh := p.listHeight()
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+lh {
		p.offset = p.cursor - lh + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

func (p picker) View() string {
	var builder strings.Builder
	builder.WriteString(pickerTitleStyle.Width(p.width).Render(" " + p.title))
	builder.WriteByte('\n')
	builder.WriteString(p.input.View())
	builder.WriteByte('\n')

	if len(p.filtered) == 0 {
		builder.WriteString(dimStyle.Render("  (no matches)"))
	} else {
		lh := p.listHeight()
		end := min(p.offset+lh, len(p.filtered))
		for i := p.offset; i < end; i++ {
			builder.WriteString(p.renderRow(p.filtered[i], i == p.cursor))
			if i < end-1 {
				builder.WriteByte('\n')
			}
		}
	}

	pos := 0
	if len(p.filtered) > 0 {
		pos = p.cursor + 1
	}
	builder.WriteByte('\n')
	builder.WriteString(dimStyle.Render(fmt.Sprintf("  ↑/↓ move · enter select · esc cancel · %d/%d", pos, len(p.filtered))))
	return builder.String()
}

// renderRow formats one list row, truncated to the modal width. The
// selected row gets a left bar and a highlight background; others show a
// dim desc beside the title.
func (p picker) renderRow(it pickerItem, selected bool) string {
	if selected {
		body := it.title
		if it.desc != "" {
			body += "  " + it.desc
		}
		return pickerSelStyle.Width(p.width).Render(truncate("▌ "+body, p.width))
	}

	avail := p.width - 2
	title := truncate(it.title, avail)
	row := "  " + title
	if it.desc != "" {
		if rem := avail - lipgloss.Width(title) - 2; rem > 0 {
			row += "  " + dimStyle.Render(truncate(it.desc, rem))
		}
	}
	return row
}
