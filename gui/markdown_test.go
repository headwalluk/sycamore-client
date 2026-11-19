package main

import (
	"strings"
	"testing"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

func TestRenderMarkdown_TablesListsAndCode(t *testing.T) {
	rendered := renderMarkdown("| Service | State |\n| --- | --- |\n| nginx | up |\n\n- one\n- two\n\n```\nuptime\n```\n")
	for _, want := range []string{"<table>", "<td>nginx</td>", "<li>one</li>", "<pre><code>uptime"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in:\n%s", want, rendered)
		}
	}
}

func TestRenderMarkdown_IsSafe(t *testing.T) {
	rendered := renderMarkdown("<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n[click](javascript:alert(1))\n")
	for _, forbidden := range []string{"<script", "onerror", "javascript:"} {
		if strings.Contains(strings.ToLower(rendered), forbidden) {
			t.Errorf("%q must not survive rendering:\n%s", forbidden, rendered)
		}
	}
}

func TestRenderMarkdown_LinksSurvive(t *testing.T) {
	rendered := renderMarkdown("See [the docs](https://example.com/docs) or https://example.com/raw")
	if !strings.Contains(rendered, `href="https://example.com/docs"`) || !strings.Contains(rendered, `href="https://example.com/raw"`) {
		t.Errorf("ordinary and bare links should render:\n%s", rendered)
	}
}

func TestBlocksToHTML_EscapesPlaceholders(t *testing.T) {
	rendered := blocksToHTML([]protocol.ContentBlock{
		{Type: "text", Text: "**done**"},
		{Type: "tool_use", Name: "<b>shell</b>"},
		{Type: "tool_result"},
	})
	if !strings.Contains(rendered, "<strong>done</strong>") {
		t.Errorf("text should render as Markdown:\n%s", rendered)
	}
	if strings.Contains(rendered, "<b>shell</b>") || !strings.Contains(rendered, "[tool: &lt;b&gt;shell&lt;/b&gt;]") {
		t.Errorf("a tool name must be escaped:\n%s", rendered)
	}
}

func TestTranslate_ResponseCarriesHTML(t *testing.T) {
	entries := translate(&protocol.Response{Content: []protocol.ContentBlock{{Type: "text", Text: "# Title"}}})
	if len(entries) != 1 || !strings.Contains(entries[0].HTML, "<h1>Title</h1>") || entries[0].Text != "# Title" {
		t.Errorf("a reply should carry both the text and its HTML: %+v", entries)
	}
}
