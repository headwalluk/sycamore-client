package main

import (
	"bytes"
	"fmt"
	"html"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// markdown renders agent replies to HTML. goldmark's default renderer is the
// safe one: raw HTML in the source is omitted and dangerous link schemes
// (javascript:, vbscript:, data: other than images) are dropped, so the
// output can go straight into the page. GFM adds tables, strikethrough,
// task lists and bare-URL links.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// renderMarkdown converts one Markdown text to HTML. If rendering fails the
// text is shown escaped, with a note saying why, so nothing is lost silently.
func renderMarkdown(source string) string {
	var out bytes.Buffer
	rendered := ""
	if err := markdown.Convert([]byte(source), &out); err != nil {
		rendered = fmt.Sprintf(`<p class="render-error">(couldn't render this reply as Markdown: %s)</p><p>%s</p>`,
			html.EscapeString(err.Error()), html.EscapeString(source))
	} else {
		rendered = out.String()
	}
	return rendered
}

// blocksToHTML renders response content blocks: text as Markdown, tool and
// image blocks as short placeholders.
func blocksToHTML(blocks []protocol.ContentBlock) string {
	var out bytes.Buffer
	for _, block := range blocks {
		switch block.Type {
		case "text":
			out.WriteString(renderMarkdown(block.Text))
		case "tool_use":
			fmt.Fprintf(&out, `<p class="tool">[tool: %s]</p>`, html.EscapeString(block.Name))
		case "tool_result":
			out.WriteString(`<p class="tool">[tool result]</p>`)
		case "image":
			fmt.Fprintf(&out, `<p class="tool">[image: %s]</p>`, html.EscapeString(block.MediaType))
		default:
			// Unknown block types are skipped.
		}
	}
	return out.String()
}
