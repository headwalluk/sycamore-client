package main

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/protocol"
)

// connectedModel returns a laid-out model on agent "prod", connected and
// showing conversation C_1, with the pool and link exposed.
func connectedModel(t *testing.T) (model, *fakePool, *fakeConn) {
	t.Helper()
	prodLink := newFakeConn()
	prodLink.sess = core.Session{PrincipalID: "P_1", ConversationID: "C_1", ProtocolVersion: 3}
	pool := newFakePool().withLink("prod", prodLink)
	m := sizedPool(t, pool, twoInstanceCfg())
	m.connected = true
	m.convTitle = "Deploy notes"
	return m, pool, prodLink
}

// stateChange delivers one pool state event to the model.
func stateChange(m model, agent string, state core.AgentState, attempt int, err error) model {
	next, _ := m.Update(agentStateMsg{agent: agent, state: state, attempt: attempt, err: err})
	return next.(model)
}

// lastNote returns the text of the newest transcript note.
func lastNote(t *testing.T, m model) string {
	t.Helper()
	text := ""
	for index := len(m.entries) - 1; index >= 0 && text == ""; index-- {
		if m.entries[index].kind == entryNote {
			text = m.entries[index].text
		}
	}
	return text
}

func TestWaitForEvent_TranslatesPoolEvents(t *testing.T) {
	events := make(chan core.Event, 2)
	events <- core.Event{Agent: "zee", Kind: core.EventMessage, Message: &protocol.Pong{}}
	events <- core.Event{Agent: "zee", Kind: core.EventState, State: core.StateReconnecting, Attempt: 2, Err: errors.New("EOF")}

	if message, isMessage := waitForEvent(events)().(recvMsg); !isMessage || message.agent != "zee" {
		t.Errorf("a message event should become a recvMsg for zee, got %#v", message)
	}
	change, isState := waitForEvent(events)().(agentStateMsg)
	if !isState || change.agent != "zee" || change.state != core.StateReconnecting || change.attempt != 2 || change.err == nil {
		t.Errorf("a state event should become an agentStateMsg, got %#v", change)
	}
	close(events)
	if _, isClosed := waitForEvent(events)().(poolClosedMsg); !isClosed {
		t.Error("a closed pool should become poolClosedMsg")
	}
}

func TestPoolClosed_Quits(t *testing.T) {
	m, _, _ := connectedModel(t)
	next, cmd := m.Update(poolClosedMsg{})
	if !next.(model).quitting {
		t.Error("the pool shutting down should quit")
	}
	if _, isQuit := cmd().(tea.QuitMsg); !isQuit {
		t.Error("expected a Quit Cmd")
	}
}

func TestRecvMsg_FromAnotherAgentIgnored(t *testing.T) {
	m, _, _ := connectedModel(t)
	before := len(m.entries)
	next, cmd := m.Update(recvMsg{agent: "coop", msg: &protocol.Pong{}})
	if got := len(next.(model).entries); got != before {
		t.Errorf("another agent's message must not reach the transcript (%d → %d entries)", before, got)
	}
	if cmd == nil {
		t.Error("the read loop must be re-issued even for an ignored message")
	}
}

func TestDrop_EntersReconnectingAndRemembersConversation(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = stateChange(ui, "prod", core.StateReconnecting, 1, errors.New("EOF"))

	if !ui.reconnecting || ui.connected || ui.reconnectAttempt != 1 {
		t.Errorf("want reconnecting on attempt 1, got reconnecting=%v connected=%v attempt=%d", ui.reconnecting, ui.connected, ui.reconnectAttempt)
	}
	if ui.resumeConvID != "C_1" || ui.resumeConvTitle != "Deploy notes" {
		t.Errorf("should remember the conversation to resume, got %q / %q", ui.resumeConvID, ui.resumeConvTitle)
	}
	if note := lastNote(t, ui); !strings.Contains(note, "connection lost: EOF") {
		t.Errorf("the note should give the reason, got %q", note)
	}
	if footer := ansi.Strip(ui.renderFooter()); !strings.Contains(footer, "reconnecting… (attempt 1)") {
		t.Errorf("footer should show the attempt:\n%s", footer)
	}

	ui = stateChange(ui, "prod", core.StateReconnecting, 2, errors.New("connection refused"))
	if ui.reconnectAttempt != 2 {
		t.Errorf("a later attempt should update the count, got %d", ui.reconnectAttempt)
	}
	if strings.Contains(lastNote(t, ui), "refused") {
		t.Error("later attempts should not add a note each")
	}
}

func TestDrop_MidTurnClearsTheTurn(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui.thinking = true
	ui.steps = []protocol.Progress{{TurnID: "T_1", Kind: protocol.ProgressToolStart, ToolName: "shell_exec"}}
	ui = stateChange(ui, "prod", core.StateReconnecting, 1, nil)

	if ui.thinking || len(ui.steps) != 0 {
		t.Errorf("a drop should end the in-flight turn (thinking=%v, steps=%d)", ui.thinking, len(ui.steps))
	}
	found := false
	for _, item := range ui.entries {
		found = found || (item.kind == entryNote && strings.Contains(item.text, "mid-turn"))
	}
	if !found {
		t.Error("expected the mid-turn note")
	}
}

func TestReconnected_ResetsViewAndPendsResume(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui.entries = append(ui.entries, entry{kind: entryNote, text: "old transcript"})
	ui = stateChange(ui, "prod", core.StateReconnecting, 1, nil)
	ui = stateChange(ui, "prod", core.StateOnline, 0, nil)

	if ui.reconnecting || ui.reconnectAttempt != 0 {
		t.Errorf("coming back should end reconnecting, got reconnecting=%v attempt=%d", ui.reconnecting, ui.reconnectAttempt)
	}
	if !ui.resumePending || ui.resumeConvID != "C_1" {
		t.Errorf("the welcome should re-pin C_1: pending=%v id=%q", ui.resumePending, ui.resumeConvID)
	}
	if len(ui.entries) != 1 || lastNote(t, ui) != "— reconnected —" {
		t.Errorf("the view should be reset to just the reconnected note, got %+v", ui.entries)
	}
}

func TestOnline_AtStartupChangesNothing(t *testing.T) {
	m, _, _ := connectedModel(t)
	m.entries = append(m.entries, entry{kind: entryNote, text: "kept"})
	m = stateChange(m, "prod", core.StateOnline, 0, nil)
	if len(m.entries) != 1 || m.resumePending {
		t.Errorf("the adopted start-up connection coming online must not reset the view, got %+v", m.entries)
	}
}

func TestFailed_StopsAndExplains(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = stateChange(ui, "prod", core.StateReconnecting, 1, nil)
	ui = stateChange(ui, "prod", core.StateFailed, 0, errors.New("SSH authentication failed"))

	if ui.reconnecting || ui.connected {
		t.Errorf("a permanent failure should stop reconnecting (reconnecting=%v connected=%v)", ui.reconnecting, ui.connected)
	}
	if note := lastNote(t, ui); !strings.Contains(note, "SSH authentication failed") || !strings.Contains(note, "/connect prod") {
		t.Errorf("the note should say why and how to retry, got %q", note)
	}
}

func TestSubmit_ChatRefusedWhileReconnecting(t *testing.T) {
	m, _, prodLink := connectedModel(t)
	m = stateChange(m, "prod", core.StateReconnecting, 1, nil)
	m.input.SetValue("are you there?")
	next, _ := m.submit()
	if len(prodLink.sent) != 0 {
		t.Errorf("nothing should be sent while down, sent=%+v", prodLink.sent)
	}
	if got := next.(model).input.Value(); got != "are you there?" {
		t.Errorf("the draft should be kept, got %q", got)
	}
}

func TestSendErr_ReportsAndEndsTurn(t *testing.T) {
	m, _, _ := connectedModel(t)
	m.thinking = true
	next, _ := m.Update(sendErrMsg{agent: "prod", err: errors.New("broken pipe")})
	got := next.(model)
	if got.quitting {
		t.Error("a failed send must not quit; the pool reconnects")
	}
	if got.thinking {
		t.Error("the unsent turn should not leave the spinner running")
	}
	if note := lastNote(t, got); !strings.Contains(note, "broken pipe") {
		t.Errorf("expected a send-failed note, got %q", note)
	}
}

func TestConnect_FocusesAndKeepsTheOldAgentOpen(t *testing.T) {
	ui, pool, _ := connectedModel(t)
	ui.entries = append(ui.entries, entry{kind: entryNote, text: "prod transcript"})
	next, cmd := ui.focusAgent("coop")
	ui = next.(model)

	if ui.active != "coop" || ui.instance != "coop" {
		t.Errorf("coop should be on screen, active=%q instance=%q", ui.active, ui.instance)
	}
	if !slices.Equal(pool.opened, []string{"coop"}) || len(pool.closed) != 0 {
		t.Errorf("coop should be opened and nothing closed: opened=%v closed=%v", pool.opened, pool.closed)
	}
	if pool.pinned["prod"] || !pool.pinned["coop"] {
		t.Errorf("the pin should follow the screen, pinned=%v", pool.pinned)
	}
	if cmd == nil {
		t.Error("expected the spinner to start")
	}
	if len(ui.agents["prod"].entries) != 1 {
		t.Error("prod's transcript should be kept while it's in the background")
	}

	back, _ := ui.focusAgent("prod")
	ui = back.(model)
	if ui.active != "prod" || len(ui.entries) != 1 {
		t.Errorf("switching back should show prod's transcript again, entries=%+v", ui.entries)
	}
}

func TestConnect_AlreadyOnScreenIsANote(t *testing.T) {
	ui, pool, _ := connectedModel(t)
	next, _ := ui.focusAgent("prod")
	ui = next.(model)
	if len(pool.opened) != 0 {
		t.Errorf("nothing should be opened, opened=%v", pool.opened)
	}
	if !strings.Contains(lastNote(t, ui), "already on prod") {
		t.Errorf("expected a note, got %q", lastNote(t, ui))
	}
}

func TestConnect_UnknownAgentIsANote(t *testing.T) {
	ui, pool, _ := connectedModel(t)
	pool.openErr = core.ErrUnknownAgent
	next, _ := ui.focusAgent("nobody")
	ui = next.(model)
	if ui.active != "prod" {
		t.Error("an unknown agent must not take the screen")
	}
	if _, tracked := ui.agents["nobody"]; tracked {
		t.Error("an unknown agent must not be tracked")
	}
	if !strings.Contains(lastNote(t, ui), "could not connect to \"nobody\"") {
		t.Errorf("expected a note, got %q", lastNote(t, ui))
	}
}

func TestConnect_RetryAfterFailureResumes(t *testing.T) {
	ui, pool, _ := connectedModel(t)
	ui = stateChange(ui, "prod", core.StateReconnecting, 1, nil)
	ui = stateChange(ui, "prod", core.StateFailed, 0, errors.New("SSH authentication failed"))
	next, _ := ui.focusAgent("prod")
	ui = next.(model)
	if !slices.Equal(pool.opened, []string{"prod"}) {
		t.Fatalf("/connect prod after a failure should reopen it, opened=%v", pool.opened)
	}
	ui = stateChange(ui, "prod", core.StateConnecting, 0, nil)
	ui = stateChange(ui, "prod", core.StateOnline, 0, nil)
	if !ui.resumePending || ui.resumeConvID != "C_1" || ui.connecting {
		t.Errorf("reconnecting by hand should resume C_1: pending=%v id=%q connecting=%v", ui.resumePending, ui.resumeConvID, ui.connecting)
	}
}

func TestBackgroundAgent_MessagesLandInItsOwnTranscript(t *testing.T) {
	ui, _, _ := connectedModel(t)
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	onScreen := len(ui.entries)

	next, _ = ui.Update(recvMsg{agent: "prod", msg: &protocol.Response{ConversationID: "C_1", TurnID: "T_1",
		Content: []protocol.ContentBlock{{Type: "text", Text: "prod's reply"}}}})
	ui = next.(model)

	if len(ui.entries) != onScreen {
		t.Errorf("coop's transcript should be untouched (%d → %d entries)", onScreen, len(ui.entries))
	}
	if strings.Contains(ansi.Strip(ui.vp.View()), "prod's reply") {
		t.Error("a background reply must not be drawn")
	}
	prodEntries := ui.agents["prod"].entries
	if len(prodEntries) == 0 || prodEntries[len(prodEntries)-1].kind != entryServer {
		t.Fatalf("the reply should be in prod's transcript, got %+v", prodEntries)
	}

	back, _ := ui.focusAgent("prod")
	ui = back.(model)
	if !strings.Contains(ansi.Strip(ui.vp.View()), "prod's reply") {
		t.Error("the reply should show once prod is back on screen")
	}
}

func TestBackgroundAgent_TurnRunsWhileYouChatElsewhere(t *testing.T) {
	ui, _, prodLink := connectedModel(t)
	ui.input.SetValue("long job for prod")
	next, _ := ui.submit()
	ui = next.(model)
	if !ui.thinking {
		t.Fatal("prod should be working")
	}

	next, _ = ui.focusAgent("coop")
	ui = next.(model)
	coopLink := ui.client.(*fakeConn)
	ui.input.SetValue("quick question for coop")
	next, cmd := ui.submit()
	ui = next.(model)
	runCmd(cmd)
	if len(coopLink.sent) != 1 || coopLink.sent[0].Message != "quick question for coop" {
		t.Errorf("coop should take a chat while prod is busy, sent=%+v", coopLink.sent)
	}
	if !ui.thinking || !ui.agents["prod"].thinking {
		t.Error("both agents should now be working")
	}
	if len(prodLink.sent) != 0 {
		t.Errorf("nothing extra should go to prod, sent=%+v", prodLink.sent)
	}

	// prod's reply lands while coop is on screen.
	next, _ = ui.Update(recvMsg{agent: "prod", msg: &protocol.Response{ConversationID: "C_1"}})
	ui = next.(model)
	if ui.agents["prod"].thinking || !ui.thinking {
		t.Errorf("prod's reply should end prod's turn only (prod=%v coop=%v)", ui.agents["prod"].thinking, ui.thinking)
	}
}

func TestBusyAgent_KeepsTheDraftButAllowsSwitching(t *testing.T) {
	ui, _, prodLink := connectedModel(t)
	ui.thinking = true
	ui.input.SetValue("second question")
	next, _ := ui.submit()
	ui = next.(model)
	if ui.input.Value() != "second question" || len(prodLink.sent) != 0 {
		t.Errorf("a busy agent should keep the draft and send nothing, draft=%q sent=%+v", ui.input.Value(), prodLink.sent)
	}
	if !strings.Contains(lastNote(t, ui), "still working") {
		t.Errorf("expected a busy note, got %q", lastNote(t, ui))
	}

	ui.input.SetValue("/park")
	next, _ = ui.submit()
	ui = next.(model)
	if len(prodLink.sent) != 0 || !strings.Contains(lastNote(t, ui), "can wait") {
		t.Errorf("a wire command should wait for the reply, sent=%+v note=%q", prodLink.sent, lastNote(t, ui))
	}

	ui.input.SetValue("/connect coop")
	next, _ = ui.submit()
	if next.(model).active != "coop" {
		t.Error("/connect should still work while the agent is busy")
	}
}

func TestBackgroundAgent_StateChangesStayWithIt(t *testing.T) {
	ui, _, _ := connectedModel(t)
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	ui = stateChange(ui, "coop", core.StateOnline, 0, nil)
	ui.connected = true

	ui = stateChange(ui, "prod", core.StateReconnecting, 1, errors.New("EOF"))
	if ui.reconnecting || !ui.connected {
		t.Error("prod dropping must not affect coop on screen")
	}
	prod := ui.agents["prod"]
	if !prod.reconnecting || prod.connected {
		t.Error("prod should be reconnecting in the background")
	}
	if last := prod.entries[len(prod.entries)-1]; !strings.Contains(last.text, "connection lost: EOF") {
		t.Errorf("the drop note belongs in prod's transcript, got %+v", last)
	}
}

func TestBackgroundAgent_PickerRepliesDontOpenOverlays(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui.agents["prod"].convAction = convActionPicker
	ui.agents["prod"].modelPickerPending = true
	next, _ := ui.focusAgent("coop")
	ui = next.(model)

	next, _ = ui.Update(recvMsg{agent: "prod", msg: &protocol.ConversationsList{Conversations: []protocol.ConversationSummary{}}})
	ui = next.(model)
	next, _ = ui.Update(recvMsg{agent: "prod", msg: &protocol.ModelsList{}})
	ui = next.(model)
	if ui.picker != nil || ui.switcher != nil {
		t.Error("a background agent's reply must not open an overlay over the agent on screen")
	}
	prod := ui.agents["prod"]
	if prod.conversations == nil {
		t.Error("the conversation list should be kept for the switcher")
	}
	if len(prod.entries) != 1 || prod.modelPickerPending {
		t.Errorf("the model list should land in prod's transcript instead, entries=%d", len(prod.entries))
	}
}

func TestSendErr_GoesToTheAgentThatSent(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui.thinking = true
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	next, _ = ui.Update(sendErrMsg{agent: "prod", err: errors.New("broken pipe")})
	ui = next.(model)
	prod := ui.agents["prod"]
	if prod.thinking || !strings.Contains(prod.entries[len(prod.entries)-1].text, "broken pipe") {
		t.Errorf("prod's turn should end with the note in its transcript, thinking=%v", prod.thinking)
	}
}

func TestClose_MovesToAnotherAgent(t *testing.T) {
	ui, pool, _ := connectedModel(t)
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	next, _ = ui.closeAgent("")
	ui = next.(model)

	if ui.active != "prod" {
		t.Errorf("closing the agent on screen should move to prod, active=%q", ui.active)
	}
	if _, tracked := ui.agents["coop"]; tracked || !slices.Equal(pool.closed, []string{"coop"}) {
		t.Errorf("coop should be closed and forgotten, closed=%v", pool.closed)
	}
	if !pool.pinned["prod"] || pool.pinned["coop"] {
		t.Errorf("the pin should move to prod, pinned=%v", pool.pinned)
	}

	// coop's trailing events are dropped.
	ui = stateChange(ui, "coop", core.StateOffline, 0, nil)
	next, _ = ui.Update(recvMsg{agent: "coop", msg: &protocol.Pong{}})
	ui = next.(model)
	if _, tracked := ui.agents["coop"]; tracked {
		t.Error("a closed agent's late events must not bring it back")
	}
}

func TestClose_TheOnlyAgentIsRefused(t *testing.T) {
	ui, pool, _ := connectedModel(t)
	next, _ := ui.closeAgent("")
	ui = next.(model)
	if len(pool.closed) != 0 || !strings.Contains(lastNote(t, ui), "only open agent") {
		t.Errorf("closing the last agent should be refused, closed=%v note=%q", pool.closed, lastNote(t, ui))
	}
	next, _ = ui.closeAgent("coop")
	if !strings.Contains(lastNote(t, next.(model)), "coop isn't open") {
		t.Error("closing an agent that isn't open should say so")
	}
}

func TestOpenOnStart_AgentIsAdoptedFromItsFirstEvent(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui = stateChange(ui, "coop", core.StateConnecting, 0, nil)
	coop, tracked := ui.agents["coop"]
	if !tracked || !coop.connecting {
		t.Fatal("a pool-opened agent should be tracked from its Connecting event")
	}
	if ui.active != "prod" {
		t.Error("a background open must not take the screen")
	}
	next, _ := ui.Update(recvMsg{agent: "coop", msg: &protocol.Welcome{Principal: "Paul"}})
	ui = next.(model)
	if ui.agents["coop"].principal != "Paul" {
		t.Error("its welcome should apply in the background")
	}
}

func TestParseCommand_Close(t *testing.T) {
	if c := parseCommand("/close"); c.connect != connectActionClose || c.connectArg != "" {
		t.Errorf("bare /close: %+v", c)
	}
	if c := parseCommand("/close coop"); c.connect != connectActionClose || c.connectArg != "coop" {
		t.Errorf("/close coop: %+v", c)
	}
}

func TestHeader_ShowsDisplayNameOnAccent(t *testing.T) {
	ui, _, prodLink := connectedModel(t)
	prodLink.sess.Agent = &protocol.AgentProfile{Name: "zee", DisplayName: "Zee", AccentColor: "#2E7D32"}
	header := ui.renderHeader()
	if plain := ansi.Strip(header); !strings.HasPrefix(plain, " Zee ") {
		t.Errorf("the header should lead with the display name:\n%s", plain)
	}
	if got := ansi.StringWidth(header); got != ui.width {
		t.Errorf("header is %d wide, want %d", got, ui.width)
	}
	if label, plain := ui.agentLabel(); plain != "Zee" || ansi.Strip(label) != "Zee" {
		t.Errorf("the transcript label should be the display name, got %q / %q", label, plain)
	}

	prodLink.sess.Agent = nil
	if plain := ansi.Strip(ui.renderHeader()); !strings.HasPrefix(plain, " Prod ") {
		t.Errorf("without a profile the config name is used:\n%s", plain)
	}
}

func TestSpinner_StopsWhenIdle(t *testing.T) {
	ui, _, _ := connectedModel(t)
	_, cmd := ui.Update(ui.spin.Tick())
	if cmd != nil {
		t.Error("the spinner should stop ticking with nothing in progress")
	}
	ui = stateChange(ui, "prod", core.StateReconnecting, 1, nil)
	_, cmd = ui.Update(ui.spin.Tick())
	if cmd == nil {
		t.Error("the spinner should keep ticking while reconnecting")
	}
}

// pumpUntil feeds the real pool's events to the model until done holds,
// failing after two seconds. Cmds the model returns are not run; the test
// reads the pool itself.
func pumpUntil(t *testing.T, ui model, pool *core.Pool, done func(model) bool) model {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for !done(ui) {
		delivered := make(chan tea.Msg, 1)
		go func() { delivered <- waitForEvent(pool.Events())() }()
		select {
		case msg := <-delivered:
			next, _ := ui.Update(msg)
			ui = next.(model)
		case <-deadline:
			t.Fatalf("timed out; reconnecting=%v connected=%v instance=%q", ui.reconnecting, ui.connected, ui.instance)
		}
	}
	return ui
}

func TestEndToEnd_DropReconnectsAndResumesTheConversation(t *testing.T) {
	socketPath := t.TempDir() + "/agent.sock"
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeChecked(t, listener) })
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			server, acceptErr := listener.Accept()
			if acceptErr != nil {
				close(accepted)
				break
			}
			accepted <- server
		}
	}()
	nextServer := func() net.Conn {
		t.Helper()
		select {
		case server := <-accepted:
			t.Cleanup(func() { closeChecked(t, server) })
			return server
		case <-time.After(2 * time.Second):
			t.Fatal("no connection accepted")
			return nil
		}
	}

	pool := core.NewPool(core.PoolOptions{Backoff: func(int) time.Duration { return 10 * time.Millisecond }})
	t.Cleanup(pool.CloseAll)
	pool.Add("prod", core.SocketDialer(socketPath))
	startup, err := core.Connect(t.Context(), socketPath)
	if err != nil {
		t.Fatal(err)
	}
	firstServer := nextServer()
	if err := pool.Adopt("prod", startup); err != nil {
		t.Fatal(err)
	}
	pool.Pin("prod", true)
	ui := sizedPool(t, poolAdapter{pool}, nil)

	welcome := `{"type":"welcome","protocol_version":2,"principal":"Paul","principal_id":"P_1","conversation_id":"%s","conversation_title":"%s"}` + "\n"
	writeTo(t, firstServer, fmt.Appendf(nil, welcome, "C_OLD", "Deploy notes"))
	ui = pumpUntil(t, ui, pool, func(m model) bool { return m.connected })

	closeChecked(t, firstServer)
	ui = pumpUntil(t, ui, pool, func(m model) bool { return m.reconnecting })
	if ui.resumeConvID != "C_OLD" {
		t.Fatalf("the drop should remember C_OLD, got %q", ui.resumeConvID)
	}

	// The daemon's welcome on the new connection resumes a different thread.
	secondServer := nextServer()
	writeTo(t, secondServer, fmt.Appendf(nil, welcome, "C_NEWER", "Something else"))
	ui = pumpUntil(t, ui, pool, func(m model) bool { return m.connected && !m.reconnecting })

	if session, _ := pool.Session("prod"); session.ConversationID != "C_OLD" {
		t.Errorf("after reconnecting, the session should be back on C_OLD, got %q", session.ConversationID)
	}
	if ui.convTitle != "Deploy notes" {
		t.Errorf("the header should show the resumed conversation, got %q", ui.convTitle)
	}
}

// fakeDaemon listens on a Unix socket and hands over each connection.
func fakeDaemon(t *testing.T, name string) (string, func() net.Conn) {
	t.Helper()
	socketPath := t.TempDir() + "/" + name + ".sock"
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeChecked(t, listener) })
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			server, acceptErr := listener.Accept()
			if acceptErr != nil {
				close(accepted)
				break
			}
			accepted <- server
		}
	}()
	return socketPath, func() net.Conn {
		t.Helper()
		select {
		case server := <-accepted:
			t.Cleanup(func() { closeChecked(t, server) })
			return server
		case <-time.After(2 * time.Second):
			t.Fatalf("%s accepted no connection", name)
			return nil
		}
	}
}

func TestEndToEnd_TwoAgentsReplyIndependently(t *testing.T) {
	zeeSocket, nextZee := fakeDaemon(t, "zee")
	coopSocket, nextCoop := fakeDaemon(t, "coop")
	pool := core.NewPool(core.PoolOptions{})
	t.Cleanup(pool.CloseAll)
	pool.Add("zee", core.SocketDialer(zeeSocket))
	pool.Add("coop", core.SocketDialer(coopSocket))

	startup, err := core.Connect(t.Context(), zeeSocket)
	if err != nil {
		t.Fatal(err)
	}
	zeeServer := nextZee()
	if err := pool.Adopt("zee", startup); err != nil {
		t.Fatal(err)
	}
	pool.Pin("zee", true)
	ui := sizedOn(t, poolAdapter{pool}, "zee", twoInstanceCfg())

	welcome := `{"type":"welcome","protocol_version":3,"principal":"Paul","principal_id":"P_1","conversation_id":"%s","conversation_title":"%s","agent":{"name":"%s","display_name":"%s","capabilities":["conversations","history"]}}` + "\n"
	writeTo(t, zeeServer, fmt.Appendf(nil, welcome, "C_Z", "Zee thread", "zee", "Zee"))
	ui = pumpUntil(t, ui, pool, func(m model) bool { return m.connected })

	// Ask Zee something, then switch to Coop while Zee works.
	ui.input.SetValue("zee, check the logs")
	next, cmd := ui.submit()
	ui = next.(model)
	runCmd(cmd)
	next, _ = ui.focusAgent("coop")
	ui = next.(model)
	coopServer := nextCoop()
	writeTo(t, coopServer, fmt.Appendf(nil, welcome, "C_C", "Coop thread", "coop", "Coop"))
	ui = pumpUntil(t, ui, pool, func(m model) bool { return m.active == "coop" && m.connected })
	if ui.agentName() != "Coop" {
		t.Errorf("the header should name Coop, got %q", ui.agentName())
	}

	// Zee's reply arrives while Coop is on screen.
	response := `{"type":"response","conversation_id":"C_Z","turn_id":"T_1","content":[{"type":"text","text":"zee's findings"}],"stop_reason":"end_turn","model":"m","provider":"p","usage":{"inputTokens":1,"outputTokens":1},"elapsed_ms":5}` + "\n"
	writeTo(t, zeeServer, []byte(response))
	ui = pumpUntil(t, ui, pool, func(m model) bool { return !m.agents["zee"].thinking })

	if strings.Contains(ansi.Strip(ui.vp.View()), "zee's findings") {
		t.Error("Zee's reply must not be drawn over Coop")
	}
	next, _ = ui.focusAgent("zee")
	ui = next.(model)
	if !strings.Contains(ansi.Strip(ui.vp.View()), "zee's findings") {
		t.Errorf("Zee's reply should be in Zee's transcript:\n%s", ansi.Strip(ui.vp.View()))
	}
	if session, _ := pool.Session("coop"); session.ConversationID != "C_C" {
		t.Errorf("Coop's session should be untouched, got %q", session.ConversationID)
	}
}

func TestBackgroundAgent_ReplyLeavesTheScrollPositionAlone(t *testing.T) {
	ui, _, _ := connectedModel(t)
	next, _ := ui.focusAgent("coop")
	ui = next.(model)
	for index := range 40 {
		ui.entries = append(ui.entries, entry{kind: entryNote, text: fmt.Sprintf("coop line %d", index)})
	}
	ui.refresh()
	ui.vp.SetYOffset(0) // reading from the top
	if ui.vp.AtBottom() {
		t.Fatal("setup: the transcript should be taller than the pane")
	}

	next, _ = ui.Update(recvMsg{agent: "prod", msg: &protocol.Response{ConversationID: "C_1"}})
	ui = next.(model)
	next, _ = ui.Update(agentStateMsg{agent: "prod", state: core.StateReconnecting, attempt: 1})
	ui = next.(model)
	if ui.vp.YOffset != 0 {
		t.Errorf("a background agent's events must not scroll the screen, offset=%d", ui.vp.YOffset)
	}
}

func TestUndecodable_IsShownAndEndsATurn(t *testing.T) {
	ui, _, _ := connectedModel(t)
	ui.thinking, ui.turnActive = true, true
	next, _ := ui.Update(recvMsg{agent: "prod", msg: &protocol.Undecodable{Kind: protocol.TypeResponse, Err: "bad content"}})
	ui = next.(model)
	if !strings.Contains(lastNote(t, ui), "couldn't read a response message from the agent: bad content") {
		t.Errorf("the failure should be shown, got %q", lastNote(t, ui))
	}
	if ui.thinking || ui.turnActive {
		t.Error("an unreadable reply must not leave the turn running")
	}

	ui.thinking = true
	next, _ = ui.Update(recvMsg{agent: "prod", msg: &protocol.Undecodable{Kind: protocol.TypeModelsList, Err: "x"}})
	if !next.(model).thinking {
		t.Error("an unreadable models list isn't the end of a turn")
	}
}
