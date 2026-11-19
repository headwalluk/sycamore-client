package core

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// clientPair returns a Client paired with a raw net.Conn (server
// side) for hand-crafted message scripts. The Client takes ownership
// of the client side and runs its read goroutine immediately.
func clientPair(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	srv, cli := net.Pipe()
	t.Cleanup(func() { closeChecked(t, srv) })
	conn := newConn(cli)
	c := NewClient(conn)
	t.Cleanup(func() { closeChecked(t, c) })
	return c, srv
}

// recvMessage reads one message from the Client's channel with a
// short timeout. Test failures here usually mean the run loop got
// stuck or dropped a message.
func recvMessage(t *testing.T, c *Client) protocol.ServerMessage {
	t.Helper()
	select {
	case m, ok := <-c.Messages():
		if !ok {
			t.Fatal("Messages channel closed before message arrived")
		}
		return m
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
		return nil
	}
}

func TestClient_WelcomeUpdatesSession(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","principal":"Coop","principal_id":"P_1","conversation_id":"C_42","conversation_title":"Yesterday"}`+"\n"))
	}()

	msg := recvMessage(t, client)
	w, ok := msg.(*protocol.Welcome)
	if !ok {
		t.Fatalf("expected *Welcome, got %T", msg)
	}
	if w.Principal != "Coop" {
		t.Errorf("Principal: got %q", w.Principal)
	}

	got := client.Session()
	if got.PrincipalID != "P_1" {
		t.Errorf("PrincipalID: want %q, got %q", "P_1", got.PrincipalID)
	}
	if got.ConversationID != "C_42" {
		t.Errorf("ConversationID: want %q, got %q", "C_42", got.ConversationID)
	}
}

func TestClient_WelcomeWithNilConversationClearsSession(t *testing.T) {
	client, srv := clientPair(t)

	// First welcome carries a conversation_id.
	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","principal":"Coop","principal_id":"P_1","conversation_id":"C_42","conversation_title":"Old"}`+"\n"))
	}()
	_ = recvMessage(t, client)
	if client.Session().ConversationID != "C_42" {
		t.Fatalf("setup: ConversationID not populated")
	}

	// Second welcome (re-welcome after reconnect, modeled here as
	// another line) carries nil — the cookbook says adopt wholesale.
	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","principal":"Coop","principal_id":"P_1","conversation_id":null,"conversation_title":null}`+"\n"))
	}()
	_ = recvMessage(t, client)
	if client.Session().ConversationID != "" {
		t.Errorf("re-welcome with nil: ConversationID should be cleared, got %q", client.Session().ConversationID)
	}
}

func TestClient_NewConversationUpdatesSession(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","principal":"Coop","principal_id":"P_1","conversation_id":null,"conversation_title":null}`+"\n"))
		writeTo(t, srv, []byte(`{"type":"new_conversation","conversation_id":"C_NEW"}`+"\n"))
	}()

	_ = recvMessage(t, client)
	_ = recvMessage(t, client)

	if got := client.Session().ConversationID; got != "C_NEW" {
		t.Errorf("ConversationID: want %q, got %q", "C_NEW", got)
	}
}

func TestClient_ResponseUpdatesSession(t *testing.T) {
	client, srv := clientPair(t)

	// Daemon auto-creates a conversation on first chat — the only
	// signal is response.conversation_id.
	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","principal":"Coop","principal_id":"P_1","conversation_id":null,"conversation_title":null}`+"\n"))
		writeTo(t, srv, []byte(`{"type":"response","conversation_id":"C_AUTO","turn_id":"T1","content":[],"stop_reason":null,"model":"m","provider":"p","usage":{"inputTokens":0,"outputTokens":0},"elapsed_ms":1}`+"\n"))
	}()

	_ = recvMessage(t, client)
	_ = recvMessage(t, client)

	if got := client.Session().ConversationID; got != "C_AUTO" {
		t.Errorf("ConversationID: want %q, got %q", "C_AUTO", got)
	}
}

func TestClient_SendAutoPopulatesConversationID(t *testing.T) {
	client, srv := clientPair(t)

	// Seed session.
	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","principal":"Coop","principal_id":"P_1","conversation_id":"C_X","conversation_title":"x"}`+"\n"))
	}()
	_ = recvMessage(t, client)

	// Read the next outgoing line.
	out := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if sc.Scan() {
			out <- sc.Text()
		} else {
			out <- ""
		}
	}()

	if err := client.Send(protocol.ClientMessage{Type: protocol.TypeChat, Message: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case line := <-out:
		var got protocol.ClientMessage
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.ConversationID != "C_X" {
			t.Errorf("ConversationID: want %q, got %q (wire: %s)", "C_X", got.ConversationID, line)
		}
	case <-time.After(time.Second):
		t.Fatal("server-side read timed out")
	}
}

func TestClient_SendAutoPopulateOnlyOnRelevantTypes(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","principal":"Coop","principal_id":"P_1","conversation_id":"C_X","conversation_title":"x"}`+"\n"))
	}()
	_ = recvMessage(t, client)

	// "new" doesn't take conversation_id — auto-populate must not
	// add it.
	out := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if sc.Scan() {
			out <- sc.Text()
		} else {
			out <- ""
		}
	}()

	if err := client.Send(protocol.ClientMessage{Type: protocol.TypeNew}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case line := <-out:
		if strings.Contains(line, "conversation_id") {
			t.Errorf("'new' should not carry conversation_id, wire: %s", line)
		}
	case <-time.After(time.Second):
		t.Fatal("server-side read timed out")
	}
}

func TestClient_SendDoesNotOverrideExplicitConversationID(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","principal":"Coop","principal_id":"P_1","conversation_id":"C_SESSION","conversation_title":"x"}`+"\n"))
	}()
	_ = recvMessage(t, client)

	out := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if sc.Scan() {
			out <- sc.Text()
		} else {
			out <- ""
		}
	}()

	// Caller passes an explicit ConversationID that disagrees with
	// the session — leave it alone.
	if err := client.Send(protocol.ClientMessage{
		Type:           protocol.TypeChat,
		Message:        "hi",
		ConversationID: "C_EXPLICIT",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case line := <-out:
		var got protocol.ClientMessage
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.ConversationID != "C_EXPLICIT" {
			t.Errorf("ConversationID: want %q (caller's), got %q", "C_EXPLICIT", got.ConversationID)
		}
	case <-time.After(time.Second):
		t.Fatal("server-side read timed out")
	}
}

func TestClient_MessagesChannelClosesOnEOF(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"pong"}`+"\n"))
		closeChecked(t, srv)
	}()

	if msg := recvMessage(t, client); msg == nil {
		t.Fatal("expected pong")
	}

	select {
	case _, ok := <-client.Messages():
		if ok {
			t.Fatal("expected channel close after EOF")
		}
	case <-time.After(time.Second):
		t.Fatal("Messages channel did not close after EOF")
	}

	if err := client.Err(); !errors.Is(err, io.EOF) {
		t.Errorf("Err: want io.EOF, got %v", err)
	}
}

func TestClient_CloseShutsDownReadLoop(t *testing.T) {
	client, srv := clientPair(t)
	_ = srv // not used; the run loop is blocked on Recv

	// Close should unwedge the read loop and drain the channel.
	done := make(chan struct{})
	go func() {
		closeChecked(t, client)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not return within timeout")
	}

	// After Close, Messages channel must be drained-and-closed.
	select {
	case _, ok := <-client.Messages():
		if ok {
			t.Fatal("expected closed Messages channel after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Messages channel not closed after Close")
	}

	if err := client.Err(); err != nil {
		t.Errorf("clean close should leave Err nil, got %v", err)
	}
}

func TestClient_CloseIsIdempotent(t *testing.T) {
	c, _ := clientPair(t)
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close should not error, got %v", err)
	}
}

func TestClient_UnknownTypeIsSkipped(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"future_feature","x":1}`+"\n"))
		writeTo(t, srv, []byte(`{"type":"pong"}`+"\n"))
	}()

	msg := recvMessage(t, client)
	if _, ok := msg.(*protocol.Pong); !ok {
		t.Fatalf("expected *Pong after unknown-type skip, got %T", msg)
	}
}

func TestClient_MalformedBodyIsReported(t *testing.T) {
	client, srv := clientPair(t)

	// "response" with a syntactically-valid envelope but a body that
	// can't unmarshal into protocol.Response (content is a string
	// instead of an array). Reported, then the stream carries on.
	go func() {
		writeTo(t, srv, []byte(`{"type":"response","content":"not an array"}`+"\n"))
		writeTo(t, srv, []byte(`{"type":"pong"}`+"\n"))
	}()

	undecodable, isUndecodable := recvMessage(t, client).(*protocol.Undecodable)
	if !isUndecodable || undecodable.Kind != protocol.TypeResponse || undecodable.Err == "" || !undecodable.EndsTurn() {
		t.Fatalf("a malformed response should arrive as Undecodable, got %+v", undecodable)
	}
	if _, ok := recvMessage(t, client).(*protocol.Pong); !ok {
		t.Fatal("the next message should still arrive")
	}
}

func TestClient_ConcurrentSend(t *testing.T) {
	client, srv := clientPair(t)

	const senderCount = 20

	// Server: read n lines into a slice.
	type result struct {
		lines []string
		err   error
	}
	out := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		var lines []string
		scanning := true
		for scanning && len(lines) < senderCount {
			scanning = sc.Scan()
			if scanning {
				lines = append(lines, sc.Text())
			}
		}
		out <- result{lines: lines, err: sc.Err()}
	}()

	var wg sync.WaitGroup
	for index := 0; index < senderCount; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if err := client.Send(protocol.ClientMessage{
				Type:    protocol.TypeChat,
				Message: "concurrent",
			}); err != nil {
				t.Errorf("Send %d: %v", index, err)
			}
		}(index)
	}
	wg.Wait()

	select {
	case got := <-out:
		if got.err != nil {
			t.Fatalf("server read: %v", got.err)
		}
		if len(got.lines) != senderCount {
			t.Fatalf("got %d lines, want %d", len(got.lines), senderCount)
		}
		// Every line must be a valid, complete ClientMessage —
		// interleaved bytes from concurrent writes would produce a
		// parse error here.
		for index, line := range got.lines {
			var cm protocol.ClientMessage
			if err := json.Unmarshal([]byte(line), &cm); err != nil {
				t.Errorf("line %d unparseable (write interleaving?): %v\n  %s", index, err, line)
			}
			if cm.Type != protocol.TypeChat || cm.Message != "concurrent" {
				t.Errorf("line %d corrupted: %+v", index, cm)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server-side read timed out")
	}
}

func TestClient_OrderingPreserved(t *testing.T) {
	client, srv := clientPair(t)

	// Send three pongs back-to-back; consumer must see them in order.
	go func() {
		for i := 0; i < 3; i++ {
			writeTo(t, srv, []byte(`{"type":"pong"}`+"\n"))
		}
	}()

	for i := 0; i < 3; i++ {
		msg := recvMessage(t, client)
		if _, ok := msg.(*protocol.Pong); !ok {
			t.Errorf("message %d: expected *Pong, got %T", i, msg)
		}
	}
}

// ── protocol v2: welcome.protocol_version, history, conversations ──

func TestClient_WelcomeCapturesProtocolVersion(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","protocol_version":2,"principal":"Coop","principal_id":"P_1","conversation_id":"C_42","conversation_title":"x"}`+"\n"))
	}()

	msg := recvMessage(t, client)
	w, ok := msg.(*protocol.Welcome)
	if !ok {
		t.Fatalf("expected *Welcome, got %T", msg)
	}
	if w.ProtocolVersion != 2 {
		t.Errorf("Welcome.ProtocolVersion: want 2, got %d", w.ProtocolVersion)
	}
	if got := client.Session().ProtocolVersion; got != 2 {
		t.Errorf("Session.ProtocolVersion: want 2, got %d", got)
	}
}

func TestClient_WelcomeProtocolVersionAbsentDefaultsToOne(t *testing.T) {
	client, srv := clientPair(t)

	// A pre-v2 daemon omits protocol_version — the session must
	// normalise the absent (zero) value to 1.
	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","principal":"Coop","principal_id":"P_1","conversation_id":null,"conversation_title":null}`+"\n"))
	}()
	_ = recvMessage(t, client)

	if got := client.Session().ProtocolVersion; got != 1 {
		t.Errorf("Session.ProtocolVersion: absent field should default to 1, got %d", got)
	}
}

func TestClient_HistoryPageDecodes(t *testing.T) {
	client, srv := clientPair(t)

	// One turn → a user record then the assistant reply, sharing a
	// sequence; the assistant record carries model/provider.
	go func() {
		writeTo(t, srv, []byte(`{"type":"history_page","conversation_id":"C_42","messages":[`+
			`{"role":"user","content":[{"type":"text","text":"hi"}],"sequence":11,"turn_id":"T_1","created_at":"2026-06-21T09:00:00.000Z"},`+
			`{"role":"assistant","content":[{"type":"text","text":"hello"}],"sequence":11,"turn_id":"T_1","created_at":"2026-06-21T09:00:01.000Z","model":"claude-sonnet-4-6","provider":"anthropic"}`+
			`],"has_more":true,"oldest_sequence":11}`+"\n"))
	}()

	msg := recvMessage(t, client)
	hp, ok := msg.(*protocol.HistoryPage)
	if !ok {
		t.Fatalf("expected *HistoryPage, got %T", msg)
	}
	if hp.ConversationID != "C_42" {
		t.Errorf("ConversationID: got %q", hp.ConversationID)
	}
	if len(hp.Messages) != 2 {
		t.Fatalf("Messages: want 2, got %d", len(hp.Messages))
	}
	if hp.Messages[0].Role != protocol.RoleUser || hp.Messages[1].Role != protocol.RoleAssistant {
		t.Errorf("roles: got %q, %q", hp.Messages[0].Role, hp.Messages[1].Role)
	}
	if hp.Messages[0].Sequence != 11 || hp.Messages[1].Sequence != 11 {
		t.Errorf("both records should share sequence 11, got %d, %d", hp.Messages[0].Sequence, hp.Messages[1].Sequence)
	}
	if hp.Messages[1].Model != "claude-sonnet-4-6" || hp.Messages[1].Provider != "anthropic" {
		t.Errorf("assistant model/provider: got %q/%q", hp.Messages[1].Model, hp.Messages[1].Provider)
	}
	if hp.Messages[0].Model != "" {
		t.Errorf("user record should have no model, got %q", hp.Messages[0].Model)
	}
	if blocks := hp.Messages[1].Content; len(blocks) != 1 || blocks[0].Text != "hello" {
		t.Errorf("assistant content not decoded as ContentBlock: %+v", hp.Messages[1].Content)
	}
	if !hp.HasMore {
		t.Error("HasMore: want true")
	}
	if hp.OldestSequence == nil || *hp.OldestSequence != 11 {
		t.Errorf("OldestSequence: want 11, got %v", hp.OldestSequence)
	}
}

func TestClient_HistoryPageEmptyOldestSequenceNull(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"history_page","conversation_id":"C_42","messages":[],"has_more":false,"oldest_sequence":null}`+"\n"))
	}()

	msg := recvMessage(t, client)
	hp, ok := msg.(*protocol.HistoryPage)
	if !ok {
		t.Fatalf("expected *HistoryPage, got %T", msg)
	}
	if len(hp.Messages) != 0 {
		t.Errorf("Messages: want empty, got %d", len(hp.Messages))
	}
	if hp.OldestSequence != nil {
		t.Errorf("OldestSequence: want nil on empty page, got %d", *hp.OldestSequence)
	}
}

func TestClient_ConversationsListDecodes(t *testing.T) {
	client, srv := clientPair(t)

	// Second row has a null title (not-yet-titled).
	go func() {
		writeTo(t, srv, []byte(`{"type":"conversations_list","conversations":[`+
			`{"id":"C_1","title":"Planning my holidays","updated_at":"2026-06-21T14:05:02.873Z","model_alias":"default","turn_count":9},`+
			`{"id":"C_2","title":null,"updated_at":"2026-06-20T18:30:00.000Z","model_alias":"sonnet","turn_count":0}`+
			`],"has_more":false}`+"\n"))
	}()

	msg := recvMessage(t, client)
	cl, ok := msg.(*protocol.ConversationsList)
	if !ok {
		t.Fatalf("expected *ConversationsList, got %T", msg)
	}
	if len(cl.Conversations) != 2 {
		t.Fatalf("Conversations: want 2, got %d", len(cl.Conversations))
	}
	if cl.Conversations[0].Title == nil || *cl.Conversations[0].Title != "Planning my holidays" {
		t.Errorf("row 0 title: got %v", cl.Conversations[0].Title)
	}
	if cl.Conversations[1].Title != nil {
		t.Errorf("row 1 title should be nil (not-yet-titled), got %q", *cl.Conversations[1].Title)
	}
	if cl.Conversations[0].TurnCount != 9 || cl.Conversations[0].ModelAlias != "default" {
		t.Errorf("row 0 fields: turn_count=%d model_alias=%q", cl.Conversations[0].TurnCount, cl.Conversations[0].ModelAlias)
	}
	if cl.HasMore {
		t.Error("HasMore: want false")
	}
}

func TestClient_SetConversationOverridesSessionAndSend(t *testing.T) {
	client, srv := clientPair(t)

	// Seed the session with one conversation.
	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","protocol_version":2,"principal":"Coop","principal_id":"P_1","conversation_id":"C_OLD","conversation_title":"x"}`+"\n"))
	}()
	_ = recvMessage(t, client)

	// Switch the active conversation.
	client.SetConversation("C_NEW")
	if got := client.Session().ConversationID; got != "C_NEW" {
		t.Fatalf("Session.ConversationID after switch: want C_NEW, got %q", got)
	}
	// PrincipalID survives the switch.
	if got := client.Session().PrincipalID; got != "P_1" {
		t.Errorf("PrincipalID should survive switch, got %q", got)
	}

	// A subsequent chat auto-populates the switched conversation.
	out := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if sc.Scan() {
			out <- sc.Text()
		} else {
			out <- ""
		}
	}()
	if err := client.Send(protocol.ClientMessage{Type: protocol.TypeChat, Message: "hi"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case line := <-out:
		var got protocol.ClientMessage
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.ConversationID != "C_NEW" {
			t.Errorf("chat after switch should target C_NEW, got %q (wire: %s)", got.ConversationID, line)
		}
	case <-time.After(time.Second):
		t.Fatal("server-side read timed out")
	}
}

func TestClient_SendAutoPopulatesHistoryConversationID(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","protocol_version":2,"principal":"Coop","principal_id":"P_1","conversation_id":"C_X","conversation_title":"x"}`+"\n"))
	}()
	_ = recvMessage(t, client)

	out := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if sc.Scan() {
			out <- sc.Text()
		} else {
			out <- ""
		}
	}()

	// A history request with no explicit conversation_id should inherit
	// the session's, so seed/scrollback target the active conversation.
	if err := client.Send(protocol.ClientMessage{Type: protocol.TypeHistory, Limit: 50}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case line := <-out:
		var got protocol.ClientMessage
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.ConversationID != "C_X" {
			t.Errorf("ConversationID: want %q, got %q (wire: %s)", "C_X", got.ConversationID, line)
		}
		if got.Limit != 50 {
			t.Errorf("Limit: want 50, got %d", got.Limit)
		}
	case <-time.After(time.Second):
		t.Fatal("server-side read timed out")
	}
}

// A park sent after a /switch must name the switched-to conversation.
// The daemon's fallback for an omitted id is "most recently updated",
// which would park the wrong thread — so the id has to be on the wire.
func TestClient_SendAutoPopulatesParkConversationID(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","protocol_version":2,"principal":"Coop","principal_id":"P_1","conversation_id":"C_RECENT","conversation_title":"x"}`+"\n"))
	}()
	_ = recvMessage(t, client)

	client.SetConversation("C_OLDER")

	out := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(srv)
		if sc.Scan() {
			out <- sc.Text()
		} else {
			out <- ""
		}
	}()

	if err := client.Send(protocol.ClientMessage{Type: protocol.TypePark}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case line := <-out:
		var got protocol.ClientMessage
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.ConversationID != "C_OLDER" {
			t.Errorf("park after switch should target C_OLDER, got %q (wire: %s)", got.ConversationID, line)
		}
	case <-time.After(time.Second):
		t.Fatal("server-side read timed out")
	}
}

func TestClient_ConversationParkedDecodes(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"conversation_parked","conversation_id":"C_X","conversation_title":"WordPress Weekly","parked":true,"turns_parked":7}`+"\n"))
	}()

	msg := recvMessage(t, client)
	parked, ok := msg.(*protocol.ConversationParked)
	if !ok {
		t.Fatalf("want *ConversationParked, got %T", msg)
	}
	if !parked.Parked || parked.TurnsParked != 7 || parked.ConversationID != "C_X" {
		t.Errorf("decoded = %+v", parked)
	}
	if parked.ConversationTitle == nil || *parked.ConversationTitle != "WordPress Weekly" {
		t.Errorf("ConversationTitle = %v", parked.ConversationTitle)
	}
}

// An untitled conversation reports a null title; it must decode to a nil
// pointer, not fail the whole message.
func TestClient_ConversationParkedNullTitle(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"conversation_parked","conversation_id":"C_X","conversation_title":null,"parked":false,"turns_parked":0}`+"\n"))
	}()

	msg := recvMessage(t, client)
	parked, ok := msg.(*protocol.ConversationParked)
	if !ok {
		t.Fatalf("want *ConversationParked, got %T", msg)
	}
	if parked.ConversationTitle != nil {
		t.Errorf("ConversationTitle: want nil, got %q", *parked.ConversationTitle)
	}
	if parked.Parked || parked.TurnsParked != 0 {
		t.Errorf("no-op park decoded as %+v", parked)
	}
}

// ── protocol v3: welcome.agent, capabilities, progress, avatar ──

func TestClient_WelcomeAgentProfileDecodes(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","protocol_version":3,"principal":"Paul","principal_id":"P_1","conversation_id":"C_1","conversation_title":"x","agent":{"name":"zee","display_name":"Zee","tagline":"Operations","accent_color":"#2E7D32","avatar":{"sha256":"9f2c","mime":"image/png","width":256,"height":256,"bytes":18234},"sycamore_version":"0.51.1","capabilities":["conversations","history","park","progress","avatar","future_thing"]}}`+"\n"))
	}()

	w, ok := recvMessage(t, client).(*protocol.Welcome)
	if !ok {
		t.Fatal("expected *Welcome")
	}
	agent := w.Agent
	if agent == nil {
		t.Fatal("Welcome.Agent should decode")
	}
	if agent.Name != "zee" || agent.DisplayName != "Zee" || agent.Tagline != "Operations" ||
		agent.AccentColor != "#2E7D32" || agent.SycamoreVersion != "0.51.1" {
		t.Errorf("profile fields: %+v", agent)
	}
	if agent.Avatar == nil || agent.Avatar.SHA256 != "9f2c" || agent.Avatar.MIME != "image/png" ||
		agent.Avatar.Width != 256 || agent.Avatar.Height != 256 || agent.Avatar.Bytes != 18234 {
		t.Errorf("avatar descriptor: %+v", agent.Avatar)
	}

	session := client.Session()
	if session.Agent == nil || session.Agent.Name != "zee" {
		t.Fatalf("Session.Agent should carry the profile, got %+v", session.Agent)
	}
	for _, capability := range []string{protocol.CapPark, protocol.CapProgress, protocol.CapAvatar} {
		if !session.Has(capability) {
			t.Errorf("Has(%q) should be true", capability)
		}
	}
}

func TestClient_WelcomeWithoutAgentLeavesProfileNil(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","protocol_version":2,"principal":"Paul","principal_id":"P_1","conversation_id":null,"conversation_title":null}`+"\n"))
	}()
	_ = recvMessage(t, client)

	if got := client.Session().Agent; got != nil {
		t.Errorf("a protocol-2 welcome has no agent, got %+v", got)
	}
}

func TestClient_WelcomeAttachmentLimitsDecode(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","protocol_version":3,"principal":"Paul","principal_id":"P_1","conversation_id":null,"conversation_title":null,"agent":{"name":"dexter","capabilities":["history","attachments"],"attachments":{"max_bytes":10485760}}}`+"\n"))
	}()
	_ = recvMessage(t, client)

	session := client.Session()
	if !session.Has(protocol.CapAttachments) {
		t.Error("Has(attachments) should be true")
	}
	if limits := session.Agent.Attachments; limits == nil || limits.MaxBytes != 10485760 {
		t.Errorf("Attachments limits: %+v", limits)
	}
}

func TestClient_ResponseAttachmentsDecode(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"response","conversation_id":"C_1","turn_id":"T1","content":[{"type":"text","text":"Here."}],"stop_reason":"end_turn","model":"m","provider":"p","usage":{"inputTokens":1,"outputTokens":1},"elapsed_ms":1,`+
			`"attachments":[{"id":"att_1","filename":"robot.png","mime":"image/png","size_bytes":421888,"sha256":"9f86","expires_at":"2026-10-27T10:00:00.000Z","expired":false}]}`+"\n"))
	}()

	response, ok := recvMessage(t, client).(*protocol.Response)
	if !ok {
		t.Fatal("expected *Response")
	}
	want := protocol.Attachment{ID: "att_1", Filename: "robot.png", MIME: "image/png", SizeBytes: 421888,
		SHA256: "9f86", ExpiresAt: "2026-10-27T10:00:00.000Z"}
	if len(response.Attachments) != 1 || response.Attachments[0] != want {
		t.Errorf("Attachments: got %+v", response.Attachments)
	}
}

func TestClient_HistoryPageAttachmentsDecode(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"history_page","conversation_id":"C_1","messages":[`+
			`{"role":"user","content":[{"type":"text","text":"draw a robot"}],"sequence":3,"turn_id":"T_1","created_at":"2026-10-20T09:00:00.000Z"},`+
			`{"role":"assistant","content":[{"type":"text","text":"Here."}],"sequence":3,"turn_id":"T_1","created_at":"2026-10-20T09:00:09.000Z",`+
			`"attachments":[{"id":"att_1","filename":"robot.png","mime":"image/png","size_bytes":421888,"sha256":"9f86","expires_at":"2026-10-13T10:00:00.000Z","expired":true}]}`+
			`],"has_more":false,"oldest_sequence":3}`+"\n"))
	}()

	page, ok := recvMessage(t, client).(*protocol.HistoryPage)
	if !ok {
		t.Fatal("expected *HistoryPage")
	}
	if got := page.Messages[0].Attachments; got != nil {
		t.Errorf("user record should have no attachments, got %+v", got)
	}
	attachments := page.Messages[1].Attachments
	if len(attachments) != 1 || attachments[0].Filename != "robot.png" || !attachments[0].Expired {
		t.Errorf("assistant attachments: got %+v", attachments)
	}
}

func TestSession_Has(t *testing.T) {
	profile := &protocol.AgentProfile{Name: "zee", Capabilities: []string{protocol.CapHistory, protocol.CapPark}}
	cases := []struct {
		name       string
		session    Session
		capability string
		want       bool
	}{
		{"profile lists it", Session{ProtocolVersion: 3, Agent: profile}, protocol.CapPark, true},
		{"profile omits it", Session{ProtocolVersion: 3, Agent: profile}, protocol.CapProgress, false},
		{"profile wins over the v2 fallback", Session{ProtocolVersion: 3, Agent: profile}, protocol.CapConversations, false},
		{"v2 fallback: conversations", Session{ProtocolVersion: 2}, protocol.CapConversations, true},
		{"v2 fallback: history", Session{ProtocolVersion: 2}, protocol.CapHistory, true},
		{"v2 fallback: nothing else", Session{ProtocolVersion: 2}, protocol.CapPark, false},
		{"v1: nothing", Session{ProtocolVersion: 1}, protocol.CapHistory, false},
		{"no welcome yet", Session{}, protocol.CapHistory, false},
	}
	for _, testCase := range cases {
		if got := testCase.session.Has(testCase.capability); got != testCase.want {
			t.Errorf("%s: Has(%q) = %v, want %v", testCase.name, testCase.capability, got, testCase.want)
		}
	}
}

func TestClient_LateResponseDoesNotRepointSession(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"welcome","protocol_version":3,"principal":"Paul","principal_id":"P_1","conversation_id":"C_A","conversation_title":"a"}`+"\n"))
	}()
	_ = recvMessage(t, client)

	// Switch away while a turn on C_A is still running, then let its
	// reply land.
	client.SetConversation("C_B")
	go func() {
		writeTo(t, srv, []byte(`{"type":"response","conversation_id":"C_A","turn_id":"T1","content":[],"stop_reason":null,"model":"m","provider":"p","usage":{"inputTokens":0,"outputTokens":0},"elapsed_ms":1}`+"\n"))
	}()
	_ = recvMessage(t, client)

	if got := client.Session().ConversationID; got != "C_B" {
		t.Errorf("a late reply for C_A must not pull the session back: want C_B, got %q", got)
	}
}

func TestClient_ProgressDecodes(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"progress","conversation_id":"C_1","turn_id":"T_1","round":1,"kind":"assistant_text","text":"I'll check the logs first."}`+"\n"))
		writeTo(t, srv, []byte(`{"type":"progress","conversation_id":"C_1","turn_id":"T_1","round":1,"kind":"tool_start","tool_use_id":"toolu_1","tool_name":"shell_exec","input_summary":"{\"command\":\"uptime\"}"}`+"\n"))
		writeTo(t, srv, []byte(`{"type":"progress","conversation_id":"C_1","turn_id":"T_1","round":2,"kind":"tool_end","tool_use_id":"toolu_1","tool_name":"shell_exec","is_error":true,"elapsed_ms":412}`+"\n"))
	}()

	narration, ok := recvMessage(t, client).(*protocol.Progress)
	if !ok || narration.Kind != protocol.ProgressAssistantText || narration.Text != "I'll check the logs first." ||
		narration.ConversationID != "C_1" || narration.TurnID != "T_1" || narration.Round != 1 {
		t.Errorf("assistant_text: %+v", narration)
	}
	start, ok := recvMessage(t, client).(*protocol.Progress)
	if !ok || start.Kind != protocol.ProgressToolStart || start.ToolUseID != "toolu_1" ||
		start.ToolName != "shell_exec" || start.InputSummary != `{"command":"uptime"}` {
		t.Errorf("tool_start: %+v", start)
	}
	end, ok := recvMessage(t, client).(*protocol.Progress)
	if !ok || end.Kind != protocol.ProgressToolEnd || !end.IsError || end.ElapsedMS != 412 || end.Round != 2 {
		t.Errorf("tool_end: %+v", end)
	}
}

func TestClient_AvatarImageDecodes(t *testing.T) {
	client, srv := clientPair(t)

	go func() {
		writeTo(t, srv, []byte(`{"type":"avatar_image","avatar":{"sha256":"9f2c","mime":"image/webp","width":128,"height":128,"data":"UklGRg=="}}`+"\n"))
		writeTo(t, srv, []byte(`{"type":"avatar_image","avatar":null}`+"\n"))
	}()

	image, ok := recvMessage(t, client).(*protocol.AvatarImage)
	if !ok || image.Avatar == nil {
		t.Fatalf("expected an avatar, got %+v", image)
	}
	if image.Avatar.SHA256 != "9f2c" || image.Avatar.MIME != "image/webp" ||
		image.Avatar.Width != 128 || image.Avatar.Height != 128 || image.Avatar.Data != "UklGRg==" {
		t.Errorf("avatar fields: %+v", image.Avatar)
	}
	empty, ok := recvMessage(t, client).(*protocol.AvatarImage)
	if !ok || empty.Avatar != nil {
		t.Errorf("avatar: null should decode to a nil Avatar, got %+v", empty)
	}
}

func TestClientMessage_ChatProgressFlag(t *testing.T) {
	withProgress, err := json.Marshal(protocol.ClientMessage{Type: protocol.TypeChat, Message: "hi", Progress: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(withProgress), `"progress":true`) {
		t.Errorf("progress should be on the wire: %s", withProgress)
	}
	without, err := json.Marshal(protocol.ClientMessage{Type: protocol.TypeChat, Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without), "progress") {
		t.Errorf("progress should be omitted when false: %s", without)
	}
}
