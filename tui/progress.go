package main

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// maxFooterToolName caps the tool name shown in the footer so a long name
// can't push the status off a narrow pane.
const maxFooterToolName = 24

// applyProgress records one progress step for the in-flight chat turn and
// redraws. Steps are ignored when no turn is running or when they belong to
// a conversation other than the one on screen; a new turn id starts afresh.
func (m *model) applyProgress(step *protocol.Progress) {
	if !m.thinking {
		return
	}
	// An empty conversation means a first chat is creating one, so its
	// steps are accepted whatever their id.
	if m.convID != "" && step.ConversationID != m.convID {
		return
	}
	if len(m.steps) > 0 && m.steps[0].TurnID != step.TurnID {
		m.steps = nil
	}
	m.steps = append(m.steps, *step)
	m.refresh()
}

// renderProgress draws the in-flight turn's steps under the agent label,
// or returns "" when there is nothing to show.
func (m model) renderProgress() string {
	if !m.thinking || len(m.steps) == 0 {
		return ""
	}
	label, plainLabel := m.agentLabel()
	width := m.bodyWidth(plainLabel)
	var rows []string
	for _, line := range core.FoldProgress(m.steps) {
		if line.ToolName == "" {
			for _, wrapped := range wrap(line.Narration, width) {
				rows = append(rows, systemStyle.Render(wrapped))
			}
			continue
		}
		rows = append(rows, renderToolLine(line, width))
	}
	return labelBlock(label, plainLabel, rows)
}

// renderToolLine renders one tool row, clipping the input summary so the
// row fits in width.
func renderToolLine(line core.ProgressRow, width int) string {
	prefix := "↳ " + line.ToolName
	status := "…"
	statusStyle := dimStyle
	if line.Finished {
		status = "✓ " + formatDuration(line.ElapsedMS)
		statusStyle = connOK
		if line.IsError {
			status = "✗ " + formatDuration(line.ElapsedMS)
			statusStyle = errorStyle
		}
	}
	summary := ""
	if room := width - lipgloss.Width(prefix) - lipgloss.Width(status) - 2; room > 0 && line.InputSummary != "" {
		summary = " " + truncate(line.InputSummary, room)
	}
	return dimStyle.Render(prefix+summary) + " " + statusStyle.Render(status)
}

// progressStatus is the footer's working text: the running tool, or the
// round the model is on, falling back to "thinking…" before any step.
func (m model) progressStatus() string {
	status := "thinking…"
	if len(m.steps) > 0 {
		latest := m.steps[len(m.steps)-1]
		status = fmt.Sprintf("thinking · round %d", latest.Round)
		if latest.Kind == protocol.ProgressToolStart {
			status = fmt.Sprintf("%s · round %d", truncate(latest.ToolName, maxFooterToolName), latest.Round)
		}
	}
	return status
}
