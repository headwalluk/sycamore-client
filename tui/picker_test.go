package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func samplePicker(n int) picker {
	items := make([]pickerItem, n)
	for i := range items {
		items[i] = pickerItem{id: fmt.Sprintf("id%d", i), title: fmt.Sprintf("item %d", i)}
	}
	p := newPicker("Pick one", items)
	p.setSize(40, 6) // listHeight = 3
	return p
}

func keyType(p picker, t tea.KeyType) (picker, pickerOutcome) {
	return p.Update(tea.KeyMsg{Type: t})
}

func typeRunes(p picker, s string) picker {
	np, _ := p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return np
}

func TestFilterItems(t *testing.T) {
	items := []pickerItem{
		{title: "Planning my holidays", desc: "9 turns"},
		{title: "Q2 planning notes", desc: "sonnet"},
		{title: "Grocery list", desc: "default"},
	}

	if got := filterItems(items, ""); len(got) != 3 {
		t.Errorf("empty query should return all, got %d", len(got))
	}
	// Case-insensitive title substring.
	if got := filterItems(items, "PLANNING"); len(got) != 2 {
		t.Errorf("'PLANNING' should match 2, got %d", len(got))
	}
	// Matches against desc too.
	if got := filterItems(items, "sonnet"); len(got) != 1 || got[0].title != "Q2 planning notes" {
		t.Errorf("desc match failed: %+v", got)
	}
	if got := filterItems(items, "zzz"); len(got) != 0 {
		t.Errorf("no-match query should be empty, got %d", len(got))
	}
}

func TestPicker_CursorMovementAndWindow(t *testing.T) {
	widget := samplePicker(10) // listHeight 3

	// Down five times → cursor 5; window scrolls to keep it visible.
	for i := 0; i < 5; i++ {
		widget, _ = keyType(widget, tea.KeyDown)
	}
	if widget.cursor != 5 {
		t.Fatalf("cursor = %d, want 5", widget.cursor)
	}
	if widget.offset != 3 { // cursor - listHeight + 1 = 5 - 3 + 1
		t.Errorf("offset = %d, want 3 (cursor should stay visible)", widget.offset)
	}

	// Down can't pass the end.
	for i := 0; i < 20; i++ {
		widget, _ = keyType(widget, tea.KeyDown)
	}
	if widget.cursor != 9 {
		t.Errorf("cursor should clamp to last index 9, got %d", widget.cursor)
	}

	// Up can't pass the top.
	for i := 0; i < 50; i++ {
		widget, _ = keyType(widget, tea.KeyUp)
	}
	if widget.cursor != 0 || widget.offset != 0 {
		t.Errorf("cursor/offset should clamp to top, got %d/%d", widget.cursor, widget.offset)
	}
}

func TestPicker_SelectReturnsHighlighted(t *testing.T) {
	widget := samplePicker(5)
	widget, _ = keyType(widget, tea.KeyDown)
	widget, _ = keyType(widget, tea.KeyDown) // cursor 2

	widget, outcome := keyType(widget, tea.KeyEnter)
	if outcome != pickerSelected {
		t.Fatalf("enter outcome = %v, want pickerSelected", outcome)
	}
	it, ok := widget.Selected()
	if !ok || it.id != "id2" {
		t.Errorf("selected = %+v ok=%v, want id2", it, ok)
	}
}

func TestPicker_EscCancels(t *testing.T) {
	p := samplePicker(5)
	if _, outcome := keyType(p, tea.KeyEsc); outcome != pickerCancelled {
		t.Errorf("esc outcome = %v, want pickerCancelled", outcome)
	}
}

func TestPicker_FilterResetsCursorAndSelects(t *testing.T) {
	widget := samplePicker(10)
	widget, _ = keyType(widget, tea.KeyDown)
	widget, _ = keyType(widget, tea.KeyDown) // cursor 2

	// Typing narrows to a single item and resets the cursor.
	widget = typeRunes(widget, "item 7")
	if len(widget.filtered) != 1 || widget.cursor != 0 {
		t.Fatalf("filter state: filtered=%d cursor=%d", len(widget.filtered), widget.cursor)
	}
	widget, outcome := keyType(widget, tea.KeyEnter)
	if outcome != pickerSelected {
		t.Fatalf("enter outcome = %v", outcome)
	}
	if it, _ := widget.Selected(); it.id != "id7" {
		t.Errorf("selected = %q, want id7", it.id)
	}
}

func TestPicker_EnterOnEmptyIsNoop(t *testing.T) {
	p := samplePicker(5)
	p = typeRunes(p, "nomatch")
	if len(p.filtered) != 0 {
		t.Fatalf("expected no matches, got %d", len(p.filtered))
	}
	if _, outcome := keyType(p, tea.KeyEnter); outcome != pickerNone {
		t.Errorf("enter on empty list should be a no-op, got %v", outcome)
	}
}

func TestPicker_ViewShowsTitleAndWindow(t *testing.T) {
	p := samplePicker(10)
	view := p.View()
	if !strings.Contains(view, "Pick one") {
		t.Error("view missing title")
	}
	// Only listHeight (3) rows are rendered, not all 10.
	if strings.Contains(view, "item 9") {
		t.Error("view should window the list, not render item 9 at the top")
	}
}
