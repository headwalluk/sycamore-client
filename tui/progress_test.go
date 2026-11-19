package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// progressModel returns a laid-out model on conversation C_1 with a chat
// turn in flight, against an agent advertising progress.
func progressModel(t *testing.T) (model, *fakeConn) {
	t.Helper()
	fc := newFakeConn()
	fc.sess.ProtocolVersion = 3
	fc.sess.ConversationID = "C_1"
	fc.sess.Agent = &protocol.AgentProfile{Name: "zee", Capabilities: []string{protocol.CapProgress}}
	m := sizedConn(t, fc, nil)
	m.convID = "C_1"
	m.thinking = true
	return m, fc
}

// feed delivers server messages to the model in order, as the read loop would.
func feed(m model, messages ...protocol.ServerMessage) model {
	for _, message := range messages {
		next, _ := m.Update(recvMsg{agent: m.instance, msg: message})
		m = next.(model)
	}
	return m
}

func toolStart(turnID, toolUseID, name, summary string, round int) *protocol.Progress {
	return &protocol.Progress{ConversationID: "C_1", TurnID: turnID, Round: round,
		Kind: protocol.ProgressToolStart, ToolUseID: toolUseID, ToolName: name, InputSummary: summary}
}

func toolEnd(turnID, toolUseID, name string, isError bool, elapsedMS, round int) *protocol.Progress {
	return &protocol.Progress{ConversationID: "C_1", TurnID: turnID, Round: round,
		Kind: protocol.ProgressToolEnd, ToolUseID: toolUseID, ToolName: name, IsError: isError, ElapsedMS: elapsedMS}
}

func narration(turnID, text string, round int) *protocol.Progress {
	return &protocol.Progress{ConversationID: "C_1", TurnID: turnID, Round: round,
		Kind: protocol.ProgressAssistantText, Text: text}
}

func TestSendChat_RequestsProgressWhenAdvertised(t *testing.T) {
	withCapability := newFakeConn()
	withCapability.sess.Agent = &protocol.AgentProfile{Name: "zee", Capabilities: []string{protocol.CapProgress}}
	runCmd(sendChat(withCapability, "hi"))
	if len(withCapability.sent) != 1 || !withCapability.sent[0].Progress {
		t.Errorf("chat should ask for progress, sent=%+v", withCapability.sent)
	}

	without := newFakeConn()
	without.sess.ProtocolVersion = 2
	runCmd(sendChat(without, "hi"))
	if len(without.sent) != 1 || without.sent[0].Progress {
		t.Errorf("chat must not ask an agent without the capability, sent=%+v", without.sent)
	}
}

func TestApplyProgress_RendersLiveBlock(t *testing.T) {
	ui, _ := progressModel(t)
	ui = feed(ui,
		narration("T_1", "Checking the logs.", 1),
		toolStart("T_1", "tu_1", "shell_exec", `{"command":"uptime"}`, 1),
		toolEnd("T_1", "tu_1", "shell_exec", false, 412, 1),
		toolStart("T_1", "tu_2", "read_file", `{"path":"/etc/hosts"}`, 2),
	)
	block := ansi.Strip(ui.renderProgress())
	for _, want := range []string{"Checking the logs.", "↳ shell_exec", `{"command":"uptime"}`, "✓ 412ms", "↳ read_file", "…"} {
		if !strings.Contains(block, want) {
			t.Errorf("live block missing %q:\n%s", want, block)
		}
	}
	if !ui.thinking {
		t.Error("progress must not end the turn")
	}
	if !strings.Contains(ansi.Strip(ui.vp.View()), "read_file") {
		t.Error("the live block should be in the viewport")
	}
}

func TestApplyProgress_FailedToolShowsCross(t *testing.T) {
	m, _ := progressModel(t)
	m = feed(m,
		toolStart("T_1", "tu_1", "shell_exec", "{}", 1),
		toolEnd("T_1", "tu_1", "shell_exec", true, 1500, 1),
	)
	if block := ansi.Strip(m.renderProgress()); !strings.Contains(block, "✗ 1.5s") {
		t.Errorf("a failed tool should show ✗ and its time:\n%s", block)
	}
}

func TestApplyProgress_IgnoredWhenNoTurnRunning(t *testing.T) {
	m, _ := progressModel(t)
	m.thinking = false
	m = feed(m, toolStart("T_1", "tu_1", "shell_exec", "{}", 1))
	if len(m.steps) != 0 {
		t.Errorf("a step outside a turn should be ignored, got %+v", m.steps)
	}
}

func TestApplyProgress_IgnoresOtherConversation(t *testing.T) {
	m, _ := progressModel(t)
	other := toolStart("T_9", "tu_1", "shell_exec", "{}", 1)
	other.ConversationID = "C_OTHER"
	m = feed(m, other)
	if len(m.steps) != 0 {
		t.Errorf("a step for another conversation should be ignored, got %+v", m.steps)
	}
}

func TestApplyProgress_AcceptedWhileFirstChatCreatesConversation(t *testing.T) {
	m, _ := progressModel(t)
	m.convID = ""
	m = feed(m, toolStart("T_1", "tu_1", "shell_exec", "{}", 1))
	if len(m.steps) != 1 {
		t.Errorf("with no conversation yet, the first chat's steps should be kept, got %+v", m.steps)
	}
}

func TestApplyProgress_NewTurnStartsAfresh(t *testing.T) {
	ui, _ := progressModel(t)
	ui = feed(ui,
		toolStart("T_1", "tu_1", "shell_exec", "{}", 1),
		toolStart("T_2", "tu_2", "read_file", "{}", 1),
	)
	if len(ui.steps) != 1 || ui.steps[0].TurnID != "T_2" {
		t.Errorf("a new turn id should replace the old steps, got %+v", ui.steps)
	}
}

func TestResponse_DropsLiveBlock(t *testing.T) {
	ui, _ := progressModel(t)
	ui = feed(ui, toolStart("T_1", "tu_1", "shell_exec", "{}", 1))
	ui = feed(ui, &protocol.Response{ConversationID: "C_1", TurnID: "T_1",
		Content: []protocol.ContentBlock{{Type: "text", Text: "done"}}})
	if ui.thinking || len(ui.steps) != 0 {
		t.Errorf("the response should end the turn and drop its steps (thinking=%v, steps=%d)", ui.thinking, len(ui.steps))
	}
	if ui.renderProgress() != "" {
		t.Error("no live block once the response has landed")
	}
}

func TestSubmit_ChatClearsPreviousSteps(t *testing.T) {
	m, _ := progressModel(t)
	m.thinking = false
	m.steps = []protocol.Progress{*toolStart("T_OLD", "tu_1", "shell_exec", "{}", 1)}
	m.input.SetValue("next question")
	next, _ := m.submit()
	if got := next.(model).steps; len(got) != 0 {
		t.Errorf("a new chat turn should start with no steps, got %+v", got)
	}
}

func TestFooter_ShowsRunningTool(t *testing.T) {
	ui, _ := progressModel(t)
	if got := ui.progressStatus(); got != "thinking…" {
		t.Errorf("before any step: %q", got)
	}
	ui = feed(ui, toolStart("T_1", "tu_1", "shell_exec", "{}", 3))
	if footer := ansi.Strip(ui.renderFooter()); !strings.Contains(footer, "shell_exec · round 3") {
		t.Errorf("footer should name the running tool and round:\n%s", footer)
	}
	ui = feed(ui, toolEnd("T_1", "tu_1", "shell_exec", false, 10, 3))
	if got := ui.progressStatus(); got != "thinking · round 3" {
		t.Errorf("after the tool ends: %q", got)
	}
}

func TestRenderToolLine_FitsWidth(t *testing.T) {
	long := core.ProgressRow{ToolName: "shell_exec", InputSummary: strings.Repeat("x", 200), Finished: true, ElapsedMS: 412}
	const width = 40
	if got := ansi.StringWidth(renderToolLine(long, width)); got > width {
		t.Errorf("tool row is %d wide, want at most %d", got, width)
	}
}

func TestFooter_DropsHintsWhenStatusDoesNotFit(t *testing.T) {
	m, _ := progressModel(t)
	m = feed(m, toolStart("T_1", "tu_1", "read_file", "{}", 3))
	footer := ansi.Strip(m.renderFooter())
	if got := ansi.StringWidth(footer); got > m.width {
		t.Errorf("footer is %d wide in a %d-column pane:\n%s", got, m.width, footer)
	}
	if !strings.Contains(footer, "read_file · round 3") || strings.Contains(footer, "ctrl+y") {
		t.Errorf("the live status should survive and the hints go:\n%s", footer)
	}
}
