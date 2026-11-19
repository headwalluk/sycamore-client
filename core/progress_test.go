package core

import (
	"testing"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

func TestFoldProgress_MergesStartAndEnd(t *testing.T) {
	rows := FoldProgress([]protocol.Progress{
		{Kind: protocol.ProgressAssistantText, Text: "Checking the logs."},
		{Kind: protocol.ProgressToolStart, ToolUseID: "tu_1", ToolName: "shell_exec", InputSummary: `{"command":"uptime"}`},
		{Kind: protocol.ProgressToolEnd, ToolUseID: "tu_1", ToolName: "shell_exec", ElapsedMS: 412},
		{Kind: protocol.ProgressToolStart, ToolUseID: "tu_2", ToolName: "read_file", InputSummary: `{"path":"/etc/hosts"}`},
		{Kind: "future_kind"},
	})
	if len(rows) != 3 {
		t.Fatalf("want narration + two tool rows, got %d: %+v", len(rows), rows)
	}
	if rows[0].Narration != "Checking the logs." {
		t.Errorf("narration row: %+v", rows[0])
	}
	if !rows[1].Finished || rows[1].IsError || rows[1].ElapsedMS != 412 || rows[1].InputSummary != `{"command":"uptime"}` {
		t.Errorf("finished tool row should keep its summary and take the end's result: %+v", rows[1])
	}
	if rows[2].Finished {
		t.Errorf("a tool with no end yet is still running: %+v", rows[2])
	}
}

func TestFoldProgress_EndWithoutStartStillShows(t *testing.T) {
	rows := FoldProgress([]protocol.Progress{{Kind: protocol.ProgressToolEnd, ToolUseID: "tu_9", ToolName: "web_fetch", IsError: true, ElapsedMS: 30}})
	if len(rows) != 1 || rows[0].ToolName != "web_fetch" || !rows[0].IsError || !rows[0].Finished {
		t.Errorf("an orphan end should get its own row: %+v", rows)
	}
}
