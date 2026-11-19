package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// renderedWidth returns the maximum display width across rendered lines.
func renderedWidth(lines []string) int {
	max := 0
	for _, ln := range lines {
		if w := lipgloss.Width(ln); w > max {
			max = w
		}
	}
	return max
}

func TestMarkdown_TableRendersBorders(t *testing.T) {
	r := newMDRenderer("dark")
	md := "| Svc | Port |\n|-----|------|\n| web | 443 |\n| db  | 5432 |"
	lines := r.render(md, 50)
	joined := stripANSI(strings.Join(lines, "\n"))

	// glamour draws the table with box-drawing column separators and a
	// horizontal rule under the header.
	for _, rune_ := range []string{"│", "─"} {
		if !strings.Contains(joined, rune_) {
			t.Errorf("table render missing box-drawing %q in:\n%s", rune_, joined)
		}
	}
	// Cell contents survive.
	for _, want := range []string{"Svc", "Port", "web", "443", "5432"} {
		if !strings.Contains(joined, want) {
			t.Errorf("table render missing cell %q", want)
		}
	}
}

func TestMarkdown_ListRendersBullets(t *testing.T) {
	r := newMDRenderer("dark")
	lines := r.render("- alpha\n- beta", 50)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "•") {
		t.Errorf("list render missing bullet in:\n%s", joined)
	}
	if !strings.Contains(joined, "alpha") || !strings.Contains(joined, "beta") {
		t.Errorf("list render missing items in:\n%s", joined)
	}
}

func TestMarkdown_NoBlankEdges(t *testing.T) {
	r := newMDRenderer("dark")
	lines := r.render("- alpha\n- beta", 50)
	if len(lines) == 0 {
		t.Fatal("no lines rendered")
	}
	// glamour frames blocks with padded blank lines; trimBlankEdges must
	// strip them so the label/stats stay tight against the body.
	if strings.TrimSpace(stripANSI(lines[0])) == "" {
		t.Errorf("leading blank line not trimmed: %q", lines[0])
	}
	if last := lines[len(lines)-1]; strings.TrimSpace(stripANSI(last)) == "" {
		t.Errorf("trailing blank line not trimmed: %q", last)
	}
}

func TestMarkdown_StaysWithinWidth(t *testing.T) {
	r := newMDRenderer("dark")
	md := "Here is a longer paragraph that will certainly need to wrap across several lines at a narrow width to verify the wrap budget holds."
	w := 40
	lines := r.render(md, w)
	if got := renderedWidth(lines); got > w {
		t.Errorf("rendered width %d exceeds budget %d", got, w)
	}
}

func TestMarkdown_CacheHitAndWidthInvalidation(t *testing.T) {
	renderer := newMDRenderer("dark")
	md := "some **bold** text"

	renderer.render(md, 50)
	if len(renderer.cache) != 1 {
		t.Fatalf("after first render want 1 cache entry, got %d", len(renderer.cache))
	}
	tr := renderer.tr // same-width re-render must not rebuild the renderer
	renderer.render(md, 50)
	if len(renderer.cache) != 1 {
		t.Errorf("same-width re-render should hit cache, entries=%d", len(renderer.cache))
	}
	if renderer.tr != tr {
		t.Errorf("same-width re-render rebuilt the renderer")
	}

	// A width change invalidates: cache reset, renderer rebuilt.
	renderer.render(md, 30)
	if renderer.width != 30 {
		t.Errorf("width not updated, got %d", renderer.width)
	}
	if renderer.tr == tr {
		t.Errorf("width change did not rebuild renderer")
	}
}

func TestResolveMarkdownStyle(t *testing.T) {
	// Empty → the config default.
	if got, err := resolveMarkdownStyle(""); err != nil || got != "dark" {
		t.Errorf("empty style = (%q, %v), want (dark, nil)", got, err)
	}
	// A known glamour style passes through.
	if got, err := resolveMarkdownStyle("dracula"); err != nil || got != "dracula" {
		t.Errorf("dracula = (%q, %v), want (dracula, nil)", got, err)
	}
	// An unknown one errors, and the message lists valid choices.
	got, err := resolveMarkdownStyle("neon")
	if err == nil {
		t.Fatalf("unknown style should error, got %q", got)
	}
	if !strings.Contains(err.Error(), "dark") {
		t.Errorf("error should list valid styles, got: %v", err)
	}
}

func TestNewMDRenderer_UnknownStyleFallsBack(t *testing.T) {
	// A renderer must always render; an unrecognised style degrades to the
	// default rather than producing a nil renderer.
	r := newMDRenderer("bogus")
	if r.style != "dark" {
		t.Errorf("style = %q, want dark fallback", r.style)
	}
	if lines := r.render("- a", 40); len(lines) == 0 {
		t.Error("fallback renderer produced no output")
	}
}

func TestMarkdown_NilRendererFallsBackToWrap(t *testing.T) {
	// A model without an md renderer (e.g. a zero-value struct) must still
	// render text via the plain wrap() path rather than panicking.
	var m model
	got := m.renderText("plain text here", 40)
	want := wrap("plain text here", 40)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("nil-renderer fallback = %q, want plain wrap %q", got, want)
	}
}
