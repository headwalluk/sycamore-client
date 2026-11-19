package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/config"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// stripANSI removes ANSI escape sequences so assertions can match the
// plain text content of rendered output. glamour styles text per-word
// (each word in its own escape span), so a raw substring search for
// multi-word text fails on the rendered bytes but succeeds once stripped.
func stripANSI(s string) string { return ansi.Strip(s) }

// makeClient stands up a real Unix socket on a temp path, accepts one
// inbound connection on a goroutine, and returns the client side as a
// *core.Client paired with the raw server-side net.Conn for canned
// scripts. Both sides are closed during t.Cleanup.
func makeClient(t *testing.T) (*core.Client, net.Conn) {
	t.Helper()

	socketPath := t.TempDir() + "/test.sock"
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { closeChecked(t, listener) })

	srvCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			errCh <- err
			return
		}
		srvCh <- c
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := core.Connect(ctx, socketPath)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { closeChecked(t, client) })

	select {
	case srv := <-srvCh:
		t.Cleanup(func() { closeChecked(t, srv) })
		return client, srv
	case err := <-errCh:
		t.Fatalf("accept: %v", err)
	case <-time.After(time.Second):
		t.Fatal("server-side accept timed out")
	}
	return nil, nil
}

// sized returns a model that has received a WindowSizeMsg, so its
// viewport/input are laid out and refresh() does real work.
func sized(t *testing.T, client *core.Client) model {
	t.Helper()
	return sizedConn(t, clientLink{client}, nil)
}

// clientLink adapts a real client to a link for agent "prod".
type clientLink struct{ *core.Client }

func (clientLink) Name() string { return "prod" }

// receive reads the next message from a real client, as the pool would
// deliver it for agent "prod".
func receive(t *testing.T, client *core.Client) recvMsg {
	t.Helper()
	select {
	case msg, open := <-client.Messages():
		if !open {
			t.Fatal("client closed before a message arrived")
		}
		return recvMsg{agent: "prod", msg: msg}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for a message")
		return recvMsg{}
	}
}

// --- pure formatting helpers ---

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1.0k", 1234: "1.2k", 12345: "12.3k"}
	for in, want := range cases {
		if got := formatTokens(in); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[int]string{0: "0ms", 820: "820ms", 1000: "1.0s", 4123: "4.1s"}
	for in, want := range cases {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("no-op truncate = %q", got)
	}
	if got := truncate("hello world", 5); got != "hell…" {
		t.Errorf("truncate = %q, want %q", got, "hell…")
	}
}

// --- transcript rendering ---

func TestRenderServer_Response(t *testing.T) {
	client, _ := makeClient(t)
	m := sized(t, client)
	out := m.renderServer(&protocol.Response{
		Type: protocol.TypeResponse, Provider: "anthropic", Model: "opus",
		ElapsedMS: 4123, Usage: protocol.Usage{InputTokens: 1234, OutputTokens: 800},
		Content: []protocol.ContentBlock{
			{Type: "text", Text: "Here's a plan."},
			{Type: "tool_use", Name: "web_search"},
		},
	})
	// Strip ANSI: the agent text block now renders through glamour, which
	// styles per-word; the surrounding plain labels/stats are unaffected.
	plain := stripANSI(out)
	for _, want := range []string{"Prod", "Here's a plan.", "[tool: web_search]", "1.2k in", "800 out", "4.1s"} {
		if !strings.Contains(plain, want) {
			t.Errorf("response render missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderServer_ResponseListsAttachmentsBeforeStats(t *testing.T) {
	client, _ := makeClient(t)
	m := sized(t, client)
	plain := stripANSI(m.renderServer(&protocol.Response{
		Type: protocol.TypeResponse, ElapsedMS: 4123, Usage: protocol.Usage{InputTokens: 1234, OutputTokens: 800},
		Content: []protocol.ContentBlock{{Type: "text", Text: "Here's your robot."}},
		Attachments: []protocol.Attachment{
			{ID: "att_1", Filename: "robot.png", MIME: "image/png", SizeBytes: 421888},
			{ID: "att_2", Filename: "q3.csv", MIME: "text/csv", SizeBytes: 800},
		},
	}))
	robot := strings.Index(plain, "⎘ robot.png · image/png · 412.0 KB")
	csv := strings.Index(plain, "⎘ q3.csv · text/csv · 800 B")
	stats := strings.Index(plain, "1.2k in")
	if robot < 0 || csv < robot || stats < csv {
		t.Errorf("want both attachments, in order, before the stats line:\n%s", plain)
	}
	if strings.Contains(plain, "expired") {
		t.Errorf("live attachments aren't expired:\n%s", plain)
	}
}

func TestRenderHistory_ExpiredAttachmentIsLabelled(t *testing.T) {
	client, _ := makeClient(t)
	m := sized(t, client)
	plain := stripANSI(m.renderHistory(protocol.HistoryMessage{
		Role:        protocol.RoleAssistant,
		Content:     []protocol.ContentBlock{{Type: "text", Text: "Here."}},
		Attachments: []protocol.Attachment{{ID: "att_1", Filename: "robot.png", MIME: "image/png", SizeBytes: 2 * 1024 * 1024, Expired: true}},
	}))
	if !strings.Contains(plain, "⎘ robot.png · image/png · 2.0 MB · expired") {
		t.Errorf("history render missing the expired attachment:\n%s", plain)
	}
}

func TestRenderAttachments_WrapsUnderTheFilename(t *testing.T) {
	lines := renderAttachments([]protocol.Attachment{
		{Filename: "quarterly-sales-figures-by-region.xlsx", MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", SizeBytes: 5000},
	}, 24)
	if len(lines) < 2 {
		t.Fatalf("want a wrapped attachment, got %q", lines)
	}
	for index, line := range lines {
		plain := stripANSI(line)
		if width := lipgloss.Width(plain); width > 24 {
			t.Errorf("line %d is %d wide, over 24: %q", index, width, plain)
		}
		wantPrefix := "  "
		if index == 0 {
			wantPrefix = "⎘ "
		}
		if !strings.HasPrefix(plain, wantPrefix) {
			t.Errorf("line %d should start %q: %q", index, wantPrefix, plain)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 421888: "412.0 KB", 10 * 1024 * 1024: "10.0 MB"}
	for size, want := range cases {
		if got := formatBytes(size); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", size, got, want)
		}
	}
}

func TestRenderServer_WelcomeAndError(t *testing.T) {
	client, _ := makeClient(t)
	m := sized(t, client)

	if out := m.renderServer(&protocol.Error{Message: "Connection rejected: no contact"}); !strings.Contains(out, "Error:") || !strings.Contains(out, "no contact") {
		t.Errorf("error render = %q", out)
	}
	if out := m.renderServer(&protocol.Welcome{Principal: "Paul"}); !strings.Contains(out, "connected as Paul") {
		t.Errorf("welcome render = %q", out)
	}
}

// --- status bar / model state ---

func TestApplyStatus_WelcomeThenResponse(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)

	title := "Trip planning"
	ui, _ = update(ui, recvMsg{agent: "prod", msg: &protocol.Welcome{Principal: "Paul", ConversationTitle: &title}})
	if !ui.connected || ui.principal != "Paul" || ui.convTitle != "Trip planning" {
		t.Errorf("welcome status not applied: %+v", ui)
	}
	if !strings.Contains(ui.renderHeader(), "Trip planning") {
		t.Errorf("header missing title: %q", ui.renderHeader())
	}

	// A response clears the working flag and sets the model label.
	ui.thinking = true
	ui, _ = update(ui, recvMsg{agent: "prod", msg: &protocol.Response{Model: "claude-opus-4-8", Content: []protocol.ContentBlock{{Type: "text", Text: "ok"}}}})
	if ui.thinking {
		t.Error("thinking should clear on response")
	}
	if ui.modelLabel != "claude-opus-4-8" {
		t.Errorf("modelLabel = %q", ui.modelLabel)
	}
}

func TestEnter_SendsSetsThinkingAndClearsInput(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)

	ui.input.SetValue("hello there")
	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = next.(model)

	if ui.input.Value() != "" {
		t.Errorf("input not cleared: %q", ui.input.Value())
	}
	if !ui.thinking {
		t.Error("thinking should be set after sending")
	}
	if cmd == nil {
		t.Fatal("enter should issue a Cmd (send + spinner tick)")
	}
	// The user's line is now in the transcript.
	if len(ui.entries) != 1 || ui.entries[0].kind != entryUser || ui.entries[0].text != "hello there" {
		t.Errorf("user entry not recorded: %+v", ui.entries)
	}
}

func TestEnter_IgnoredWhileThinkingOrBlank(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)

	if _, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Error("blank enter should be a no-op")
	}

	ui.input.SetValue("queued")
	ui.thinking = true
	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("enter while thinking should be a no-op")
	}
	if next.(model).input.Value() != "queued" {
		t.Error("input should be preserved when send is suppressed")
	}
}

func TestTyping_AppendsToInput(t *testing.T) {
	client, _ := makeClient(t)
	m := sized(t, client)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	if got := next.(model).input.Value(); got != "hi" {
		t.Errorf("input = %q, want hi", got)
	}
}

// ctrl+d is the shell EOF idiom: it deletes the character ahead of the
// cursor when there's text, and only quits on an empty input line.
func TestCtrlD_DeletesForwardWithText(t *testing.T) {
	client, _ := makeClient(t)
	m := sized(t, client)

	// Type "ab" then move the cursor left so it sits before "b".
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")})
	next, _ = next.(model).Update(tea.KeyMsg{Type: tea.KeyLeft})

	// ctrl+d should delete the char ahead ("b"), not quit.
	next, cmd := next.(model).Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("ctrl+d with text should not quit")
		}
	}
	if got := next.(model).input.Value(); got != "a" {
		t.Errorf("input = %q, want a", got)
	}
}

func TestQuitKeys(t *testing.T) {
	client, _ := makeClient(t)
	for _, kt := range []tea.KeyType{tea.KeyCtrlC, tea.KeyCtrlD} {
		m := sized(t, client)
		_, cmd := m.Update(tea.KeyMsg{Type: kt})
		if cmd == nil {
			t.Fatalf("%v: expected a Cmd", kt)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("%v: expected QuitMsg, got %T", kt, cmd())
		}
	}
}

func TestEsc_DoesNotQuit(t *testing.T) {
	client, _ := makeClient(t)
	m := sized(t, client)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(model).quitting || cmd != nil {
		t.Error("a stray Esc must not quit")
	}
}

func TestCtrlC_ClearsADraftBeforeQuitting(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)
	ui.input.SetValue("half a thought")
	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	ui = next.(model)
	if ui.quitting || cmd != nil || ui.input.Value() != "" {
		t.Errorf("ctrl+c with a draft should clear it, not quit (quitting=%v, input=%q)", ui.quitting, ui.input.Value())
	}
	next, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !next.(model).quitting || cmd == nil {
		t.Error("ctrl+c on an empty input should quit")
	}
}

func TestMouseToggle(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client) // starts with mouseCapture = true (sized passes true)

	if !ui.mouseCapture {
		t.Fatal("expected mouse capture on at start")
	}
	if !strings.Contains(ui.renderFooter(), "mouse:on") {
		t.Errorf("footer should show mouse:on, got %q", ui.renderFooter())
	}

	// ctrl+o turns it off and issues a (disable-mouse) Cmd.
	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	ui = next.(model)
	if ui.mouseCapture {
		t.Error("ctrl+o should turn mouse capture off")
	}
	if cmd == nil {
		t.Fatal("ctrl+o should issue a mouse Cmd")
	}
	if !strings.Contains(ui.renderFooter(), "mouse:off") {
		t.Errorf("footer should show mouse:off, got %q", ui.renderFooter())
	}

	// ctrl+o again turns it back on.
	next, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	ui = next.(model)
	if !ui.mouseCapture {
		t.Error("second ctrl+o should turn mouse capture back on")
	}
	if cmd == nil {
		t.Fatal("second ctrl+o should issue a mouse Cmd")
	}
}

// --- read-loop / send Cmds over a real socket (unchanged behaviour) ---

func TestSendChat_SerializesToWire(t *testing.T) {
	client, srv := makeClient(t)
	lineCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if sc.Scan() {
			lineCh <- sc.Text()
		} else {
			lineCh <- ""
		}
	}()

	if msg := sendChat(clientLink{client}, "hello")(); msg != nil {
		t.Fatalf("sendChat should return nil on success, got %T", msg)
	}

	select {
	case line := <-lineCh:
		var cm protocol.ClientMessage
		if err := json.Unmarshal([]byte(line), &cm); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if cm.Type != protocol.TypeChat || cm.Message != "hello" {
			t.Errorf("bad wire form: %+v", cm)
		}
	case <-time.After(time.Second):
		t.Fatal("server didn't see send")
	}
}

func TestSendChat_ReportsErrorAfterClose(t *testing.T) {
	client, _ := makeClient(t)
	closeChecked(t, client)
	if _, ok := sendChat(clientLink{client}, "hello")().(sendErrMsg); !ok {
		t.Fatal("expected sendErrMsg after close")
	}
}

// TestViewSnapshot renders a scripted session and prints it, so `go test
// -run Snapshot -v` shows the laid-out UI. Also asserts the frame has
// the expected number of rows for its height.
func TestViewSnapshot(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)

	title := "Trip planning"
	ui, _ = update(ui, recvMsg{agent: "prod", msg: &protocol.Welcome{Principal: "Paul", ConversationTitle: &title}})
	ui.modelLabel = "claude-opus-4-8"
	ui.entries = append(ui.entries, entry{kind: entryUser, text: "Plan me a 3-day trip to Lisbon."})
	ui, _ = update(ui, recvMsg{agent: "prod", msg: &protocol.Response{
		Model: "claude-opus-4-8", ElapsedMS: 4123,
		Usage:   protocol.Usage{InputTokens: 1234, OutputTokens: 800},
		Content: []protocol.ContentBlock{{Type: "text", Text: "Here's a plan. Day 1: Alfama, the Castelo, and sunset at Portas do Sol."}, {Type: "tool_use", Name: "web_search"}},
	}})

	view := ui.View()
	if rows := strings.Count(view, "\n") + 1; rows != 18 {
		t.Errorf("view has %d rows, want 18 (height)", rows)
	}
	fmt.Printf("\n%s\n", view)
}

// --- slash commands ---

func TestParseCommand(t *testing.T) {
	t.Run("client-side", func(t *testing.T) {
		if c := parseCommand("/help"); c.wire != nil || c.quit || !strings.Contains(c.note, "/model") {
			t.Errorf("/help = %+v", c)
		}
		if c := parseCommand("/quit"); !c.quit {
			t.Error("/quit should quit")
		}
		if c := parseCommand("/exit"); !c.quit {
			t.Error("/exit should quit")
		}
		if c := parseCommand("/bogus"); c.wire != nil || c.note == "" {
			t.Errorf("/bogus should be a note, got %+v", c)
		}
	})

	t.Run("wire commands", func(t *testing.T) {
		cases := []struct {
			line     string
			wantType string
			thinking bool
			check    func(protocol.ClientMessage) bool
		}{
			{"/new", protocol.TypeNew, true, nil},
			{"/info", protocol.TypeInfo, false, nil},
			{"/models", protocol.TypeModels, false, nil},
			{"/model", protocol.TypeShowModel, false, nil},
			{"/model fast", protocol.TypeSetModel, false, func(m protocol.ClientMessage) bool { return m.Model == "fast" }},
			{"/rename", protocol.TypeRename, true, func(m protocol.ClientMessage) bool { return m.Title == "" }},
			{"/rename My Trip", protocol.TypeRename, false, func(m protocol.ClientMessage) bool { return m.Title == "My Trip" }},
			{"/dream", protocol.TypeDream, true, nil},
			// The id is left empty here — core.Client.Send fills it from
			// the session on the way out.
			{"/park", protocol.TypePark, true, func(m protocol.ClientMessage) bool { return m.ConversationID == "" }},
			{"/capture on", protocol.TypeCapture, false, func(m protocol.ClientMessage) bool { return m.Action == "on" }},
			{"/capture", protocol.TypeCapture, false, func(m protocol.ClientMessage) bool { return m.Action == "status" }},
			{"/ping", protocol.TypePing, false, nil},
		}
		for _, testCase := range cases {
			got := parseCommand(testCase.line)
			if got.wire == nil {
				t.Errorf("%s: expected a wire message, got %+v", testCase.line, got)
				continue
			}
			if got.wire.Type != testCase.wantType {
				t.Errorf("%s: type = %q, want %q", testCase.line, got.wire.Type, testCase.wantType)
			}
			if got.thinking != testCase.thinking {
				t.Errorf("%s: thinking = %v, want %v", testCase.line, got.thinking, testCase.thinking)
			}
			if testCase.check != nil && !testCase.check(*got.wire) {
				t.Errorf("%s: field check failed on %+v", testCase.line, *got.wire)
			}
		}
	})

	t.Run("capture validation", func(t *testing.T) {
		if c := parseCommand("/capture nonsense"); c.wire != nil || c.note == "" {
			t.Errorf("/capture nonsense should be a usage note, got %+v", c)
		}
	})
}

func TestSubmit_HelpIsClientSide(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)
	ui.input.SetValue("/help")
	next, cmd := ui.submit()
	ui = next.(model)

	if cmd != nil {
		t.Error("/help should not issue a wire Cmd")
	}
	if ui.thinking {
		t.Error("/help should not set thinking")
	}
	last := ui.entries[len(ui.entries)-1]
	if last.kind != entryNote || !strings.Contains(last.text, "/model") {
		t.Errorf("expected a help note, got %+v", last)
	}
}

func TestSubmit_QuitCommand(t *testing.T) {
	client, _ := makeClient(t)
	m := sized(t, client)
	m.input.SetValue("/quit")
	next, cmd := m.submit()
	if !next.(model).quitting {
		t.Error("/quit should set quitting")
	}
	if cmd == nil {
		t.Fatal("/quit should issue a Cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected QuitMsg, got %T", cmd())
	}
}

func TestSubmit_DreamSetsThinking(t *testing.T) {
	client, _ := makeClient(t)
	m := sized(t, client)
	m.input.SetValue("/dream")
	next, cmd := m.submit()
	if !next.(model).thinking {
		t.Error("/dream should set thinking (LLM call)")
	}
	if cmd == nil {
		t.Error("/dream should issue a wire Cmd")
	}
}

func TestSubmit_ParkSetsThinking(t *testing.T) {
	fc := newFakeConn()
	fc.sess.ProtocolVersion = 3
	fc.sess.Agent = &protocol.AgentProfile{Name: "zee", Capabilities: []string{protocol.CapPark}}
	m := sizedConn(t, fc, nil)
	m.input.SetValue("/park")
	next, cmd := m.submit()
	if !next.(model).thinking {
		t.Error("/park should set thinking (the drain is an LLM call)")
	}
	if cmd == nil {
		t.Fatal("/park should issue a wire Cmd")
	}
	runCmd(cmd)
	if len(fc.sent) != 1 || fc.sent[0].Type != protocol.TypePark {
		t.Errorf("expected one park on the wire, sent=%+v", fc.sent)
	}
}

func TestSubmit_ParkRefusedWithoutCapability(t *testing.T) {
	fc := newFakeConn()
	fc.sess.ProtocolVersion = 2 // no agent profile: park can't be assumed
	m := sizedConn(t, fc, nil)
	m.input.SetValue("/park")
	next, cmd := m.submit()
	got := next.(model)
	if got.thinking {
		t.Error("a refused /park must not start the working state")
	}
	if cmd != nil {
		runCmd(cmd)
	}
	if len(fc.sent) != 0 {
		t.Errorf("nothing should reach the wire, sent=%+v", fc.sent)
	}
	last := got.entries[len(got.entries)-1]
	if last.kind != entryNote || !strings.Contains(last.text, `"park"`) {
		t.Errorf("expected a note naming the park capability, got %+v", last)
	}
}

func TestRecv_ProgressKeepsTurnRunning(t *testing.T) {
	fc := newFakeConn()
	m := sizedConn(t, fc, nil)
	m.thinking = true
	before := len(m.entries)
	next, _ := m.Update(recvMsg{agent: "prod", msg: &protocol.Progress{
		ConversationID: "C_1", TurnID: "T_1", Round: 1,
		Kind: protocol.ProgressToolStart, ToolName: "shell_exec",
	}})
	got := next.(model)
	if !got.thinking {
		t.Error("a progress step must not end the turn")
	}
	if len(got.entries) != before {
		t.Errorf("progress isn't rendered yet; entries grew from %d to %d", before, len(got.entries))
	}
}

// The drained and no-op cases must not read alike: a failed fold arrives
// as an Error, so "nothing to park" has to mean exactly that.
func TestRenderServer_ConversationParked(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)

	title := "WordPress Weekly"
	drained := ui.renderServer(&protocol.ConversationParked{
		ConversationID: "C_X", ConversationTitle: &title, Parked: true, TurnsParked: 7,
	})
	for _, want := range []string{"parked", "WordPress Weekly", "7 turns", "still open"} {
		if !strings.Contains(drained, want) {
			t.Errorf("drained park missing %q:\n%s", want, drained)
		}
	}

	one := ui.renderServer(&protocol.ConversationParked{
		ConversationID: "C_X", ConversationTitle: &title, Parked: true, TurnsParked: 1,
	})
	if !strings.Contains(one, "1 turn ") {
		t.Errorf("single turn should not be pluralised:\n%s", one)
	}

	noop := ui.renderServer(&protocol.ConversationParked{
		ConversationID: "C_X", ConversationTitle: nil, Parked: false, TurnsParked: 0,
	})
	for _, want := range []string{"(untitled)", "already consolidated", "nothing to park"} {
		if !strings.Contains(noop, want) {
			t.Errorf("no-op park missing %q:\n%s", want, noop)
		}
	}
	if strings.Contains(noop, "folded") {
		t.Errorf("no-op park should not claim a fold happened:\n%s", noop)
	}
}

func TestRenderServer_CommandReplies(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)

	cur := "default"
	models := ui.renderServer(&protocol.ModelsList{
		Current: &cur,
		Aliases: []protocol.Alias{{Name: "default", Target: "anthropic/opus"}, {Name: "fast", Target: "anthropic/haiku"}},
	})
	if !strings.Contains(models, "Models") || !strings.Contains(models, "* default") || !strings.Contains(models, "anthropic/haiku") {
		t.Errorf("models render:\n%s", models)
	}

	title := "Trip"
	alias := "default"
	info := ui.renderServer(&protocol.InfoResponse{
		ConversationTitle: &title, ModelAlias: &alias,
		TurnCount: 3, EstimatedTokens: 12345, ConsolidationThreshold: 50000, Locale: "en-GB", LocaleSource: "host",
	})
	for _, want := range []string{"Info", "title:", "Trip", "turns:   3", "12.3k", "en-GB (host)"} {
		if !strings.Contains(info, want) {
			t.Errorf("info render missing %q:\n%s", want, info)
		}
	}
}

func TestViewSnapshotCommands(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)
	ui, _ = update(ui, recvMsg{agent: "prod", msg: &protocol.Welcome{Principal: "Paul"}})
	ui.modelLabel = "claude-opus-4-8"

	// /models
	ui.entries = append(ui.entries, entry{kind: entryUser, text: "/models"})
	cur := "default"
	ui, _ = update(ui, recvMsg{agent: "prod", msg: &protocol.ModelsList{Current: &cur, Aliases: []protocol.Alias{
		{Name: "default", Target: "anthropic/claude-opus-4-8"},
		{Name: "fast", Target: "anthropic/claude-haiku-4-5"},
	}}})

	// /help (client-side note)
	ui.entries = append(ui.entries, entry{kind: entryUser, text: "/help"})
	ui.entries = append(ui.entries, entry{kind: entryNote, text: parseCommand("/help").note})
	ui.refresh()

	fmt.Printf("\n%s\n", ui.View())
}

func update(m model, msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(model), cmd
}

// --- snag items 1 & 3: multi-line input + newline keys ---

func TestInput_CtrlJInsertsNewlineNotSubmit(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)
	ui.input.SetValue("first")

	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	ui = next.(model)

	if cmd != nil {
		t.Error("ctrl+j should not issue a Cmd")
	}
	if ui.input.Value() != "first\n" {
		t.Errorf("ctrl+j should insert a newline, value=%q", ui.input.Value())
	}
	if len(ui.entries) != 0 {
		t.Errorf("ctrl+j must not submit, entries=%d", len(ui.entries))
	}
}

func TestInput_TrailingBackslashSoftNewline(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)
	ui.input.SetValue(`line one\`)

	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = next.(model)

	if cmd != nil {
		t.Error("trailing-backslash enter should not submit")
	}
	if ui.input.Value() != "line one\n" {
		t.Errorf("value = %q, want \"line one\\n\" (backslash stripped, newline added)", ui.input.Value())
	}
	if len(ui.entries) != 0 {
		t.Errorf("backslash continuation must not submit, entries=%d", len(ui.entries))
	}
}

func TestInput_NonTrailingBackslashSubmitsNormally(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)
	ui.input.SetValue(`a\b`)

	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = next.(model)

	if cmd == nil {
		t.Error("a non-trailing backslash should submit normally")
	}
	if len(ui.entries) != 1 || ui.entries[0].text != `a\b` {
		t.Errorf("entry = %+v, want text a\\b", ui.entries)
	}
}

func TestInput_MultiLineSubmit(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)
	ui.input.SetValue("line one\nline two")

	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = next.(model)

	if cmd == nil {
		t.Fatal("multi-line enter should submit (send + spinner)")
	}
	if !ui.thinking {
		t.Error("thinking should be set after sending")
	}
	if ui.input.Value() != "" {
		t.Errorf("input not cleared: %q", ui.input.Value())
	}
	if len(ui.entries) != 1 || ui.entries[0].text != "line one\nline two" {
		t.Errorf("multi-line user entry = %+v", ui.entries)
	}
}

func TestInput_GrowsWithNewlinesShrinksViewport(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client) // 60x18: input 1 row, viewport 12

	inputBefore, vpBefore := ui.input.Height(), ui.vp.Height
	for i := 0; i < 2; i++ {
		next, _ := ui.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
		ui = next.(model)
	}
	if ui.input.Height() <= inputBefore {
		t.Errorf("input should grow with newlines: %d → %d", inputBefore, ui.input.Height())
	}
	if ui.vp.Height >= vpBefore {
		t.Errorf("viewport should shrink as input grows: %d → %d", vpBefore, ui.vp.Height)
	}
	// Total rows still add up to the terminal height.
	if got := fixedChromeRows + ui.input.Height() + ui.vp.Height; got != 18 {
		t.Errorf("layout rows = %d, want 18", got)
	}
}

// --- M17: conversation list & switch ---

// sizedV2 returns a sized model whose client has completed a protocol-v2
// welcome (no resumed conversation, so nothing auto-seeds), plus the
// server side for wire assertions.
func sizedV2(t *testing.T) (model, *core.Client, net.Conn) {
	t.Helper()
	client, srv := makeClient(t)
	m := sized(t, client)
	writeTo(t, srv, []byte(`{"type":"welcome","protocol_version":2,"principal":"P","principal_id":"P1","conversation_id":null,"conversation_title":null}`+"\n"))
	rm := receive(t, client)
	m, _ = update(m, rm)
	return m, client, srv
}

func sampleConvList() *protocol.ConversationsList {
	h := "Holidays"
	return &protocol.ConversationsList{
		Conversations: []protocol.ConversationSummary{
			{ID: "C1", Title: &h, UpdatedAt: "2026-06-21T14:05:02.873Z", ModelAlias: "default", TurnCount: 9},
			{ID: "C2", Title: nil, UpdatedAt: "2026-06-20T18:30:00.000Z", ModelAlias: "sonnet", TurnCount: 0},
		},
		HasMore: false,
	}
}

func submitText(t *testing.T, m model, text string) (model, tea.Cmd) {
	t.Helper()
	m.input.SetValue(text)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(model), cmd
}

func TestParseCommand_ConversationCommands(t *testing.T) {
	if got := parseCommand("/list"); got.convFetch != convActionList {
		t.Errorf("/list convFetch = %v", got.convFetch)
	}
	if got := parseCommand("/switch"); got.convFetch != convActionPicker {
		t.Errorf("/switch (bare) convFetch = %v", got.convFetch)
	}
	got := parseCommand("/switch holiday plans")
	if got.convFetch != convActionSwitchArg || got.switchArg != "holiday plans" {
		t.Errorf("/switch arg: convFetch=%v switchArg=%q", got.convFetch, got.switchArg)
	}
}

func TestList_RendersConversationsWithNullTitle(t *testing.T) {
	ui, _, _ := sizedV2(t)

	ui, _ = submitText(t, ui, "/list")
	if ui.convAction != convActionList {
		t.Fatalf("expected convActionList pending, got %v", ui.convAction)
	}

	ui, _ = update(ui, recvMsg{agent: "prod", msg: sampleConvList()})
	if ui.convAction != convActionNone {
		t.Errorf("convAction should reset after reply, got %v", ui.convAction)
	}

	view := ui.vp.View()
	for _, want := range []string{"Holidays", "(untitled)", "9 turns"} {
		if !strings.Contains(view, want) {
			t.Errorf("list render missing %q in:\n%s", want, view)
		}
	}
}

func TestSwitch_GatedOnV1Daemon(t *testing.T) {
	client, srv := makeClient(t)
	ui := sized(t, client)
	writeTo(t, srv, []byte(`{"type":"welcome","principal":"P","principal_id":"P1","conversation_id":"C0","conversation_title":null}`+"\n"))
	msg := receive(t, client)
	ui, _ = update(ui, msg)

	ui, _ = submitText(t, ui, "/switch")
	if ui.switcher == nil {
		t.Fatal("the switcher should open, so another agent can still be picked")
	}
	if ui.convAction != convActionNone {
		t.Errorf("v1 daemon must not arm a conversations fetch, got %v", ui.convAction)
	}
	if !strings.Contains(ui.switcher.note, "protocol v2") {
		t.Errorf("the conversations box should say it needs protocol v2, got %q", ui.switcher.note)
	}
}

func TestSwitch_PickerOpensThenSelectsAndReseeds(t *testing.T) {
	ui, _, srv := sizedV2(t)

	// /switch with no arg arms the picker; the reply opens it.
	ui, _ = submitText(t, ui, "/switch")
	if ui.convAction != convActionPicker {
		t.Fatalf("expected convActionPicker, got %v", ui.convAction)
	}
	if ui.switcher == nil || ui.switcher.note != "loading…" {
		t.Fatal("the switcher should open at once, loading the list")
	}
	ui, _ = update(ui, recvMsg{agent: "prod", msg: sampleConvList()})
	if len(ui.switcher.conversations.filtered) != 3 { // the new-conversation row, then two
		t.Errorf("switcher should hold 2 conversations, got %d", len(ui.switcher.conversations.filtered))
	}

	// Selecting the first row switches to C1 and triggers a re-seed.
	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = next.(model)
	if ui.switcher != nil {
		t.Error("switcher should close on selection")
	}
	if got := ui.client.Session().ConversationID; got != "C1" {
		t.Errorf("session should switch to C1, got %q", got)
	}
	if ui.convTitle != "Holidays" || !ui.histLoading {
		t.Errorf("switch view state: title=%q histLoading=%v", ui.convTitle, ui.histLoading)
	}
	// The re-seed Cmd requests history for the new conversation.
	if cmd == nil {
		t.Fatal("expected a history re-seed Cmd")
	}
	lineCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if sc.Scan() {
			lineCh <- sc.Text()
		}
	}()
	cmd()
	select {
	case line := <-lineCh:
		var hm protocol.ClientMessage
		if err := json.Unmarshal([]byte(line), &hm); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if hm.Type != protocol.TypeHistory || hm.ConversationID != "C1" {
			t.Errorf("re-seed wire form: %+v", hm)
		}
	case <-time.After(time.Second):
		t.Fatal("no history re-seed seen on the wire")
	}
}

func TestSwitch_ByArgIndexAndTitle(t *testing.T) {
	t.Run("by 1-based index", func(t *testing.T) {
		m, _, _ := sizedV2(t)
		m, _ = submitText(t, m, "/switch 2")
		m, _ = update(m, recvMsg{agent: "prod", msg: sampleConvList()})
		if got := m.client.Session().ConversationID; got != "C2" {
			t.Errorf("index switch: session = %q, want C2", got)
		}
	})

	t.Run("by title substring", func(t *testing.T) {
		m, _, _ := sizedV2(t)
		m, _ = submitText(t, m, "/switch holid")
		m, _ = update(m, recvMsg{agent: "prod", msg: sampleConvList()})
		if got := m.client.Session().ConversationID; got != "C1" {
			t.Errorf("title switch: session = %q, want C1", got)
		}
	})

	t.Run("no match notes and does not switch", func(t *testing.T) {
		ui, _, _ := sizedV2(t)
		ui, _ = submitText(t, ui, "/switch zzz")
		ui, _ = update(ui, recvMsg{agent: "prod", msg: sampleConvList()})
		if got := ui.client.Session().ConversationID; got != "" {
			t.Errorf("no-match should not switch, session = %q", got)
		}
		last := ui.entries[len(ui.entries)-1]
		if last.kind != entryNote || !strings.Contains(last.text, "no conversation matching") {
			t.Errorf("expected a no-match note, got %+v", last)
		}
	})
}

func sampleModelsList() *protocol.ModelsList {
	cur := "default"
	return &protocol.ModelsList{
		Aliases: []protocol.Alias{
			{Name: "default", Target: "anthropic/claude-3-5-sonnet"},
			{Name: "opus", Target: "anthropic/claude-opus-4"},
		},
		Current: &cur,
	}
}

func TestModelPicker_CtrlYOpensThenSelectsSendsSetModel(t *testing.T) {
	ui, client, srv := sizedV2(t)

	// ctrl+y arms the picker and requests the model list…
	next, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	ui = next.(model)
	if !ui.modelPickerPending {
		t.Fatal("ctrl+y should mark the model picker pending")
	}
	if cmd == nil {
		t.Fatal("ctrl+y should issue a models request Cmd")
	}
	// …which goes out as a `models` message on the wire.
	assertWire(t, srv, cmd, func(hm protocol.ClientMessage) {
		if hm.Type != protocol.TypeModels {
			t.Errorf("expected a models request, got %+v", hm)
		}
	})

	// The reply opens the picker rather than dumping into the transcript.
	ui, _ = update(ui, recvMsg{agent: "prod", msg: sampleModelsList()})
	if ui.picker == nil {
		t.Fatal("model picker should be open")
	}
	if ui.modelPickerPending {
		t.Error("pending flag should clear once the picker opens")
	}
	if ui.pickerAction != pickerSetModel {
		t.Errorf("picker action = %v, want pickerSetModel", ui.pickerAction)
	}
	if len(ui.picker.filtered) != 2 {
		t.Fatalf("picker should hold 2 models, got %d", len(ui.picker.filtered))
	}
	if !strings.Contains(ui.picker.filtered[0].title, "current") {
		t.Errorf("the current model should be marked, got %q", ui.picker.filtered[0].title)
	}

	// Move to the second row and select it → set_model with that alias.
	next, _ = ui.Update(tea.KeyMsg{Type: tea.KeyDown})
	ui = next.(model)
	next, cmd = ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = next.(model)
	if ui.picker != nil {
		t.Error("picker should close on selection")
	}
	if cmd == nil {
		t.Fatal("selection should issue a set_model Cmd")
	}
	assertWire(t, srv, cmd, func(hm protocol.ClientMessage) {
		if hm.Type != protocol.TypeSetModel || hm.Model != "opus" {
			t.Errorf("set_model wire form: %+v", hm)
		}
	})
	_ = client
}

func TestModelsList_WithoutPendingRendersIntoTranscript(t *testing.T) {
	ui, _, _ := sizedV2(t)
	before := len(ui.entries)
	ui, _ = update(ui, recvMsg{agent: "prod", msg: sampleModelsList()})
	if ui.picker != nil {
		t.Error("an unsolicited models_list should not open a picker")
	}
	if len(ui.entries) != before+1 {
		t.Errorf("models_list should append one transcript entry, have %d (was %d)", len(ui.entries), before)
	}
}

// assertWire runs a Cmd that writes one client message and checks it.
func assertWire(t *testing.T, srv net.Conn, cmd tea.Cmd, check func(protocol.ClientMessage)) {
	t.Helper()
	lineCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if sc.Scan() {
			lineCh <- sc.Text()
		}
	}()
	cmd()
	select {
	case line := <-lineCh:
		var hm protocol.ClientMessage
		if err := json.Unmarshal([]byte(line), &hm); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		check(hm)
	case <-time.After(time.Second):
		t.Fatal("no message seen on the wire")
	}
}

// --- M15: history backfill ---

func TestFetchHistory_SerializesToWire(t *testing.T) {
	client, srv := makeClient(t)

	lines := make(chan string, 2)
	go func() {
		sc := bufio.NewScanner(srv)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	// Newest page (seed): no before_sequence on the wire.
	fetchHistory(clientLink{client}, "C1", 0, historyPageLimit)()
	// Older page (scroll-back): carries the cursor.
	fetchHistory(clientLink{client}, "C1", 10, historyPageLimit)()

	l1, l2 := waitLine(t, lines), waitLine(t, lines)

	var seed protocol.ClientMessage
	if err := json.Unmarshal([]byte(l1), &seed); err != nil {
		t.Fatalf("unmarshal seed: %v", err)
	}
	if seed.Type != protocol.TypeHistory || seed.ConversationID != "C1" || seed.Limit != historyPageLimit {
		t.Errorf("seed wire form: %+v", seed)
	}
	if strings.Contains(l1, "before_sequence") {
		t.Errorf("seed should omit before_sequence, wire: %s", l1)
	}

	var older protocol.ClientMessage
	if err := json.Unmarshal([]byte(l2), &older); err != nil {
		t.Fatalf("unmarshal older: %v", err)
	}
	if older.BeforeSequence != 10 {
		t.Errorf("older page should carry before_sequence=10, got %+v", older)
	}
}

func waitLine(t *testing.T, lines <-chan string) string {
	t.Helper()
	select {
	case l := <-lines:
		return l
	case <-time.After(time.Second):
		t.Fatal("server didn't see send")
		return ""
	}
}

func TestSeedHistory_GatedOnProtocolVersion(t *testing.T) {
	cases := []struct {
		name     string
		welcome  string
		wantSeed bool
	}{
		{"v2 with conversation fires", `{"type":"welcome","protocol_version":2,"principal":"P","principal_id":"P1","conversation_id":"C1","conversation_title":null}`, true},
		{"v1 daemon does not", `{"type":"welcome","principal":"P","principal_id":"P1","conversation_id":"C1","conversation_title":null}`, false},
		{"v2 without conversation does not", `{"type":"welcome","protocol_version":2,"principal":"P","principal_id":"P1","conversation_id":null,"conversation_title":null}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, srv := makeClient(t)
			ui := sized(t, client)

			// Drive the welcome over the real wire so the client's
			// session (and protocol_version) is populated by the read
			// loop before the model handles it.
			writeTo(t, srv, []byte(tc.welcome+"\n"))
			rm := receive(t, client)
			ui, _ = update(ui, rm)

			if ui.histSeeded != tc.wantSeed || ui.histLoading != tc.wantSeed {
				t.Errorf("seed=%v loading=%v, want both %v", ui.histSeeded, ui.histLoading, tc.wantSeed)
			}
		})
	}
}

func TestApplyHistoryPage_SeedPrependsAndRenders(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)

	// A welcome banner is the only entry before the seed lands.
	title := "Resumed thread"
	ui, _ = update(ui, recvMsg{agent: "prod", msg: &protocol.Welcome{Principal: "P", ConversationTitle: &title}})

	oldest := 11
	hp := &protocol.HistoryPage{
		ConversationID: "C1",
		Messages: []protocol.HistoryMessage{
			{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: "text", Text: "hello from the past"}}, Sequence: 11},
			{Role: protocol.RoleAssistant, Content: []protocol.ContentBlock{{Type: "text", Text: "a reply from before"}}, Sequence: 11, Model: "m", Provider: "p"},
		},
		HasMore:        true,
		OldestSequence: &oldest,
	}
	ui, _ = update(ui, recvMsg{agent: "prod", msg: hp})

	// History is prepended (oldest-first) ahead of the welcome banner.
	if len(ui.entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(ui.entries))
	}
	if ui.entries[0].kind != entryHistory || ui.entries[1].kind != entryHistory || ui.entries[2].kind != entryServer {
		t.Errorf("entry order wrong: %v %v %v", ui.entries[0].kind, ui.entries[1].kind, ui.entries[2].kind)
	}

	// Pagination cursor captured; not loading anymore.
	if !ui.histHasMore || ui.histOldest == nil || *ui.histOldest != 11 || ui.histConvID != "C1" || ui.histLoading {
		t.Errorf("cursor state: hasMore=%v oldest=%v conv=%q loading=%v", ui.histHasMore, ui.histOldest, ui.histConvID, ui.histLoading)
	}

	// Rendered transcript shows user → assistant → banner, in order.
	// Strip ANSI first: glamour styles the body text per-word, so the
	// plain phrases only appear contiguously once escapes are removed.
	view := stripANSI(ui.vp.View())
	iUser := strings.Index(view, "hello from the past")
	iReply := strings.Index(view, "a reply from before")
	iBanner := strings.Index(view, "connected as")
	if iUser < 0 || iReply < 0 || iBanner < 0 {
		t.Fatalf("missing rendered content: user=%d reply=%d banner=%d\n%s", iUser, iReply, iBanner, view)
	}
	if !(iUser < iReply && iReply < iBanner) {
		t.Errorf("render order wrong: user=%d reply=%d banner=%d", iUser, iReply, iBanner)
	}
}

func TestApplyHistoryPage_EmptyPageClearsLoading(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)
	ui, _ = update(ui, recvMsg{agent: "prod", msg: &protocol.Welcome{Principal: "P"}})
	ui.histLoading = true

	hp := &protocol.HistoryPage{ConversationID: "C1", Messages: nil, HasMore: false, OldestSequence: nil}
	ui, _ = update(ui, recvMsg{agent: "prod", msg: hp})

	if len(ui.entries) != 1 {
		t.Errorf("empty page should add no entries, got %d", len(ui.entries))
	}
	if ui.histLoading || ui.histHasMore || ui.histOldest != nil {
		t.Errorf("empty page state: loading=%v hasMore=%v oldest=%v", ui.histLoading, ui.histHasMore, ui.histOldest)
	}
}

func TestMaybeLoadOlder_TriggerConditions(t *testing.T) {
	oldest := 5

	// Fires at the top when older turns remain.
	t.Run("fires at top with more", func(t *testing.T) {
		client, _ := makeClient(t)
		ui := sized(t, client)
		ui.histHasMore, ui.histOldest, ui.histConvID = true, &oldest, "C1"
		ui.vp.GotoTop()
		if cmd := ui.maybeLoadOlder(); cmd == nil {
			t.Fatal("expected a load-older cmd")
		}
		if !ui.histLoading || !ui.histScrollbackPending {
			t.Errorf("flags not set: loading=%v scrollback=%v", ui.histLoading, ui.histScrollbackPending)
		}
	})

	t.Run("silent while a request is in flight", func(t *testing.T) {
		client, _ := makeClient(t)
		m := sized(t, client)
		m.histHasMore, m.histOldest, m.histLoading = true, &oldest, true
		if cmd := m.maybeLoadOlder(); cmd != nil {
			t.Error("should not fire while loading")
		}
	})

	t.Run("silent when nothing older remains", func(t *testing.T) {
		client, _ := makeClient(t)
		m := sized(t, client)
		m.histHasMore, m.histOldest = false, &oldest
		if cmd := m.maybeLoadOlder(); cmd != nil {
			t.Error("should not fire when no more")
		}
	})
}

// --- M13: context-reset primitive ---

func TestResetView_ClearsConversationKeepsConnection(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)

	// Populate both scopes: conversation-view state and connection state.
	oldest := 9
	ui.entries = []entry{{kind: entryUser, text: "hi"}, {kind: entryNote, text: "note"}}
	ui.convTitle, ui.modelLabel, ui.thinking = "Trip", "opus", true
	ui.histConvID, ui.histOldest, ui.histHasMore = "C1", &oldest, true
	ui.histLoading, ui.histScrollbackPending, ui.histSeeded = true, true, true
	ui.principal, ui.connected = "Paul", true

	ui.resetView()

	// Conversation-scoped state is wiped.
	if len(ui.entries) != 0 || ui.convTitle != "" || ui.modelLabel != "" || ui.thinking {
		t.Errorf("view not cleared: entries=%d title=%q model=%q thinking=%v", len(ui.entries), ui.convTitle, ui.modelLabel, ui.thinking)
	}
	if ui.histConvID != "" || ui.histOldest != nil || ui.histHasMore || ui.histLoading || ui.histScrollbackPending || ui.histSeeded {
		t.Errorf("history cursor not cleared: %+v", ui.convView)
	}

	// Connection-scoped state survives.
	if ui.principal != "Paul" || !ui.connected || ui.instance != "prod" || ui.client == nil {
		t.Errorf("connection state should survive resetView: principal=%q connected=%v instance=%q clientNil=%v", ui.principal, ui.connected, ui.instance, ui.client == nil)
	}
}

func TestNewConversation_ResetsHistoryCursor(t *testing.T) {
	client, _ := makeClient(t)
	ui := sized(t, client)
	oldest := 7
	ui.histHasMore, ui.histOldest = true, &oldest

	ui, _ = update(ui, recvMsg{agent: "prod", msg: &protocol.NewConversation{ConversationID: "C2"}})

	if ui.histHasMore || ui.histOldest != nil {
		t.Errorf("a new conversation must drop the scroll-back cursor: hasMore=%v oldest=%v", ui.histHasMore, ui.histOldest)
	}
}

// ── M14: user-initiated reconnect ───────────────────────────────────

// fakeConn is an in-memory conn implementation for reconnect tests — no
// socket or SSH needed. It records sent messages and whether it's closed.
type fakeConn struct {
	name   string
	sess   core.Session
	convID string
	sent   []protocol.ClientMessage
}

func newFakeConn() *fakeConn { return &fakeConn{name: "prod"} }

func (f *fakeConn) Name() string                        { return f.name }
func (f *fakeConn) Send(m protocol.ClientMessage) error { f.sent = append(f.sent, m); return nil }
func (f *fakeConn) Session() core.Session               { return f.sess }
func (f *fakeConn) SetConversation(id string)           { f.convID = id; f.sess.ConversationID = id }

// fakePool is an in-memory agentPool. Each agent's link is set up front
// (or created on first use); calls are recorded for assertions.
type fakePool struct {
	links   map[string]link
	events  chan core.Event
	openErr error
	opened  []string
	closed  []string
	pinned  map[string]bool
}

func newFakePool() *fakePool {
	// Closed, so a read-loop Cmd run by runCmd returns at once rather than
	// blocking; tests deliver events by calling Update directly.
	events := make(chan core.Event)
	close(events)
	return &fakePool{links: map[string]link{}, events: events, pinned: map[string]bool{}}
}

// withLink registers the link an agent's handle returns.
func (p *fakePool) withLink(name string, agentLink link) *fakePool {
	p.links[name] = agentLink
	return p
}

func (p *fakePool) Events() <-chan core.Event { return p.events }
func (p *fakePool) Open(name string) error {
	p.opened = append(p.opened, name)
	return p.openErr
}
func (p *fakePool) Close(name string)            { p.closed = append(p.closed, name) }
func (p *fakePool) Pin(name string, pinned bool) { p.pinned[name] = pinned }
func (p *fakePool) Link(name string) link {
	if _, found := p.links[name]; !found {
		p.links[name] = &fakeConn{name: name}
	}
	return p.links[name]
}

// runCmd executes cmd for its side effects, expanding batches, and
// discards the resulting messages.
func runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, inner := range batch {
			runCmd(inner)
		}
	}
}

// sizedConn builds a laid-out model on agent "prod" around an arbitrary
// link + config, so tests don't need a real socket.
func sizedConn(t *testing.T, c link, cfg *config.File) model {
	t.Helper()
	return sizedPool(t, newFakePool().withLink("prod", c), cfg)
}

// sizedPool builds a laid-out model on agent "prod" over pool.
func sizedPool(t *testing.T, pool agentPool, cfg *config.File) model {
	t.Helper()
	return sizedOn(t, pool, "prod", cfg)
}

// sizedOn builds a laid-out model over pool with agent on screen.
func sizedOn(t *testing.T, pool agentPool, agent string, cfg *config.File) model {
	t.Helper()
	m := newModel(pool, agent, "dark", true, cfg)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	return next.(model)
}

func twoInstanceCfg() *config.File {
	return &config.File{Default: "zee", Instances: map[string]config.Instance{
		"zee":  {Name: "zee", Host: "zee.example", Socket: "/run/s.sock"},
		"coop": {Name: "coop", Host: "coop.example", Socket: "/run/s.sock"},
	}}
}

func TestParseCommand_Connect(t *testing.T) {
	if c := parseCommand("/connect zee"); c.connect != connectActionName || c.connectArg != "zee" {
		t.Errorf("/connect zee = %+v", c)
	}
	if c := parseCommand("/connect"); c.connect != connectActionPicker {
		t.Errorf("bare /connect = %+v", c)
	}
}

func TestStartConnect_NoConfigIsNoted(t *testing.T) {
	ui := sizedConn(t, newFakeConn(), nil)
	next, cmd := ui.startConnect(connectActionName, "zee")
	ui = next.(model)
	if ui.connecting {
		t.Error("must not begin connecting without config")
	}
	if cmd != nil {
		t.Error("no dial Cmd expected without config")
	}
	if len(ui.entries) == 0 || !strings.Contains(ui.entries[len(ui.entries)-1].text, "config") {
		t.Errorf("expected a no-config note, got %+v", ui.entries)
	}
}

func TestInstancesToItems_SortedWithDefaultMarker(t *testing.T) {
	items := instancesToItems(twoInstanceCfg())
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].id != "coop" || items[1].id != "zee" {
		t.Errorf("items not sorted by name: %q, %q", items[0].id, items[1].id)
	}
	if !strings.Contains(items[1].desc, "default") {
		t.Errorf("the default instance should be marked: %q", items[1].desc)
	}
	if strings.Contains(items[0].desc, "default") {
		t.Errorf("a non-default instance should not be marked: %q", items[0].desc)
	}
}

func TestConnect_PickerOpensAndSelectionDials(t *testing.T) {
	ui := sizedConn(t, newFakeConn(), twoInstanceCfg())

	next, _ := ui.startConnect(connectActionPicker, "")
	ui = next.(model)
	if ui.picker == nil {
		t.Fatal("bare /connect should open the instance picker")
	}
	if ui.pickerAction != pickerConnect {
		t.Errorf("picker action = %v, want pickerConnect", ui.pickerAction)
	}

	// The highlighted row (coop, sorted first) is dialed on enter — the same
	// path as /connect coop, not a conversation switch.
	next2, cmd := ui.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ui = next2.(model)
	if ui.picker != nil {
		t.Error("picker should close on selection")
	}
	if ui.active != "coop" {
		t.Errorf("selection should put coop on screen, active=%q", ui.active)
	}
	if cmd == nil {
		t.Fatal("expected the spinner to start after selection")
	}
}

// ── M9: auto-reconnect ──────────────────────────────────────────────

func TestWelcomeHistoryCmd_ResumesPriorConversation(t *testing.T) {
	fc := newFakeConn()
	fc.sess.ProtocolVersion = 2
	ui := sizedConn(t, fc, twoInstanceCfg())
	ui.resumePending = true
	ui.resumeConvID = "conv-old"
	ui.resumeConvTitle = "Old chat"

	newID := "conv-new" // the daemon's welcome resumed a different conversation
	cmd := ui.welcomeHistoryCmd(&protocol.Welcome{ConversationID: &newID})

	if fc.convID != "conv-old" {
		t.Errorf("should re-pin the connection to the prior conversation, got %q", fc.convID)
	}
	if ui.resumePending {
		t.Error("resumePending should be consumed")
	}
	if cmd == nil {
		t.Fatal("expected a history-fetch Cmd for the resumed conversation")
	}
	cmd() // executing the fetch sends a history request
	if len(fc.sent) == 0 || fc.sent[len(fc.sent)-1].ConversationID != "conv-old" {
		t.Errorf("history should be fetched for conv-old, sent=%+v", fc.sent)
	}
}

func TestWelcomeHistoryCmd_NoResumeWhenWelcomeAlreadyMatches(t *testing.T) {
	fc := newFakeConn()
	fc.sess.ProtocolVersion = 2
	ui := sizedConn(t, fc, nil)
	ui.resumePending = true
	ui.resumeConvID = "conv-x"

	same := "conv-x" // welcome already resumed the conversation we were in
	cmd := ui.welcomeHistoryCmd(&protocol.Welcome{ConversationID: &same})

	if fc.convID != "" {
		t.Error("a plain seed (not switchTo) should run when the welcome already matches — no SetConversation")
	}
	if ui.resumePending {
		t.Error("resumePending should be consumed")
	}
	if cmd == nil {
		t.Fatal("expected a seed history Cmd")
	}
}

func TestSubmit_BlockedWhileReconnecting(t *testing.T) {
	ui := sizedConn(t, newFakeConn(), twoInstanceCfg())
	ui.reconnecting = true

	// A plain chat is kept (not lost) and not sent.
	ui.input.SetValue("hello there")
	next, cmd := ui.submit()
	ui = next.(model)
	if ui.input.Value() != "hello there" {
		t.Errorf("draft should be preserved while reconnecting, got %q", ui.input.Value())
	}
	if cmd != nil {
		t.Error("no chat send while reconnecting")
	}
	if ui.thinking {
		t.Error("should not enter the thinking state while reconnecting")
	}

	// A wire-bound slash command is refused.
	ui.input.SetValue("/info")
	next, cmd = ui.submit()
	ui = next.(model)
	if cmd != nil {
		t.Error("a wire command should not be sent while reconnecting")
	}
	if ui.thinking {
		t.Error("a blocked wire command should not set thinking")
	}
}

func TestFooter_ShowsReconnectingAttempt(t *testing.T) {
	m := sizedConn(t, newFakeConn(), nil)
	m.reconnecting = true
	m.reconnectAttempt = 3
	footer := stripANSI(m.renderFooter())
	if !strings.Contains(footer, "reconnecting") || !strings.Contains(footer, "attempt 3") {
		t.Errorf("footer should show the reconnecting attempt, got %q", footer)
	}
}

func TestFocus_BlurHidesTheCursorAndFocusRestoresIt(t *testing.T) {
	ui, _, _ := connectedModel(t)
	// The cursor's Blink is its hidden phase; tests have no colour profile,
	// so the reverse-video cursor can't be seen in View.
	next, _ := ui.Update(tea.BlurMsg{})
	ui = next.(model)
	if ui.input.Focused() || !ui.input.Cursor.Blink {
		t.Fatal("losing focus should blur the input and hide its cursor")
	}
	next, _ = ui.Update(tea.FocusMsg{})
	ui = next.(model)
	if !ui.input.Focused() || ui.input.Cursor.Blink {
		t.Error("regaining focus should show the cursor again")
	}
}

func TestFocus_TypingWhileBlurredStillTypes(t *testing.T) {
	ui, _, _ := connectedModel(t)
	next, _ := ui.Update(tea.BlurMsg{})
	ui = typedKeys(next.(model), "hi")
	if !ui.input.Focused() || ui.input.Value() != "hi" {
		t.Errorf("a key should refocus and type, focused=%v value=%q", ui.input.Focused(), ui.input.Value())
	}
}
