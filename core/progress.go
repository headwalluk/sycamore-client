package core

import "github.com/headwalluk/sycamore-client/core/protocol"

// ProgressRow is one display row of a running turn's progress: a line of
// the model's narration, or a tool call whose start and end have been
// merged. Exactly one of Narration or ToolName is set.
type ProgressRow struct {
	Narration string `json:"narration,omitempty"`

	ToolName     string `json:"toolName,omitempty"`
	InputSummary string `json:"inputSummary,omitempty"`
	Finished     bool   `json:"finished,omitempty"`
	IsError      bool   `json:"isError,omitempty"`
	ElapsedMS    int    `json:"elapsedMs,omitempty"`
}

// FoldProgress turns a turn's progress steps into display rows, merging each
// tool_end into the row its tool_start opened (matched by tool_use_id). An
// end with no start still gets a row; unknown kinds are skipped.
func FoldProgress(steps []protocol.Progress) []ProgressRow {
	var rows []ProgressRow
	openTools := map[string]int{}
	for _, step := range steps {
		switch step.Kind {
		case protocol.ProgressAssistantText:
			rows = append(rows, ProgressRow{Narration: step.Text})
		case protocol.ProgressToolStart:
			openTools[step.ToolUseID] = len(rows)
			rows = append(rows, ProgressRow{ToolName: step.ToolName, InputSummary: step.InputSummary})
		case protocol.ProgressToolEnd:
			index, started := openTools[step.ToolUseID]
			if !started {
				index = len(rows)
				rows = append(rows, ProgressRow{ToolName: step.ToolName})
			}
			delete(openTools, step.ToolUseID)
			rows[index].Finished = true
			rows[index].IsError = step.IsError
			rows[index].ElapsedMS = step.ElapsedMS
		default:
			// Unknown kinds are ignored, per the protocol's forward-compatibility rule.
		}
	}
	return rows
}
