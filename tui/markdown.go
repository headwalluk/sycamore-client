package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/headwalluk/sycamore-client/core/config"
)

// resolveMarkdownStyle validates a configured glamour style name against
// glamour's own registry (the single source of truth, so we never hardcode
// the list). An empty name falls back to the config default. An unknown
// name is an error listing the valid choices, so a typo in the config is
// reported at startup rather than silently ignored — matching the config
// loader's strict-unknown-key stance.
func resolveMarkdownStyle(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return config.DefaultMarkdownStyle, nil
	}
	if _, ok := styles.DefaultStyles[name]; ok {
		return name, nil
	}
	valid := make([]string, 0, len(styles.DefaultStyles))
	for s := range styles.DefaultStyles {
		valid = append(valid, s)
	}
	sort.Strings(valid)
	return "", fmt.Errorf("unknown markdown_style %q (valid: %s)", name, strings.Join(valid, ", "))
}

// mdRenderer renders the agent's Markdown replies to terminal ANSI via
// glamour, wrapped to the current transcript body width.
//
// glamour fixes its word-wrap width at construction time, so the renderer
// is rebuilt whenever the body width changes (a terminal resize). Within a
// width, rendered output is cached by source string: refresh() re-renders
// the whole transcript on every new message, and re-glamouring unchanged
// entries each time would be O(n²) over a long conversation — the cache
// makes a steady-state append re-render only the new entry. A glamour
// failure (rare, but malformed input is possible) degrades to the plain
// wrap() path so a bad parse never drops the text.
type mdRenderer struct {
	style string // glamour style name (validated against styles.DefaultStyles)
	width int
	tr    *glamour.TermRenderer
	cache map[string][]string
}

// newMDRenderer builds a renderer for the given glamour style name. The
// name is expected to be pre-validated (see resolveMarkdownStyle); an
// unrecognised one degrades to the default style rather than failing, so a
// renderer always renders.
func newMDRenderer(style string) *mdRenderer {
	if _, ok := styles.DefaultStyles[style]; !ok {
		style = config.DefaultMarkdownStyle
	}
	return &mdRenderer{style: style, cache: map[string][]string{}}
}

// render formats md to width w and returns the terminal lines, ready to be
// spliced under a labelBlock indent. On any glamour error (or if the
// renderer failed to build) it falls back to wrap(md, w) so the text still
// shows as plain wrapped lines.
func (r *mdRenderer) render(md string, width int) []string {
	if width < 1 {
		width = 1
	}
	if r.tr == nil || r.width != width {
		r.rebuild(width)
	}
	if r.tr == nil {
		return wrap(md, width) // construction failed — plain fallback
	}
	if cached, ok := r.cache[md]; ok {
		return cached
	}
	out, err := r.tr.Render(md)
	if err != nil {
		return wrap(md, width)
	}
	lines := trimBlankEdges(strings.Split(strings.TrimRight(out, "\n"), "\n"))
	r.cache[md] = lines
	return lines
}

// rebuild constructs a glamour renderer for width w, zeroing glamour's
// default document margin so the rendered block sits flush-left and our
// own labelBlock indent stays the single source of horizontal offset.
// Resetting the cache here is what invalidates stale (wrong-width) output.
func (r *mdRenderer) rebuild(width int) {
	r.width = width
	r.cache = map[string][]string{}

	// Copy the configured style (value copy) and reassign the Document
	// margin to a zero pointer — we mutate our copy's field, never the
	// shared global. The style was validated at construction, but defend
	// against an unknown key by falling back to dark.
	base, ok := styles.DefaultStyles[r.style]
	if !ok {
		base = &styles.DarkStyleConfig
	}
	cfg := *base
	zero := uint(0)
	cfg.Document.Margin = &zero

	tr, err := glamour.NewTermRenderer(
		glamour.WithStyles(cfg),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		r.tr = nil
		return
	}
	r.tr = tr
}

// trimBlankEdges drops leading and trailing visually-blank lines (glamour
// frames blocks with them), so the agent label and the token-stats line
// stay tight against the body. A line is blank when it carries no visible
// glyphs — empty, only ANSI escapes, or (glamour's case) escape-styled
// space padding to the wrap width, which a plain width check would miss.
func trimBlankEdges(lines []string) []string {
	blank := func(s string) bool { return strings.TrimSpace(ansi.Strip(s)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}
	return lines
}
