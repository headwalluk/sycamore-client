package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/headwalluk/sycamore-client/core/protocol"
	"github.com/headwalluk/sycamore-client/core/tunnel"
)

// fakeConnection is an in-memory Connection. Push server messages with
// deliver; drop simulates the far end going away.
type fakeConnection struct {
	messages chan protocol.ServerMessage

	mu       sync.Mutex
	sent     []protocol.ClientMessage
	sendErr  error
	session  Session
	closed   bool
	closeErr error
	dropErr  error
}

func newFakeConnection() *fakeConnection {
	return &fakeConnection{messages: make(chan protocol.ServerMessage, 64)}
}

func (f *fakeConnection) Messages() <-chan protocol.ServerMessage { return f.messages }

func (f *fakeConnection) Send(msg protocol.ClientMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeConnection) Session() Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session
}

func (f *fakeConnection) SetConversation(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.session.ConversationID = id
}

func (f *fakeConnection) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropErr
}

func (f *fakeConnection) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeChannel()
	return f.closeErr
}

// closeChannel ends the message stream once. Callers hold f.mu.
func (f *fakeConnection) closeChannel() {
	if !f.closed {
		f.closed = true
		close(f.messages)
	}
}

// drop ends the connection from the far side with err.
func (f *fakeConnection) drop(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropErr = err
	f.closeChannel()
}

func (f *fakeConnection) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeConnection) sentMessages() []protocol.ClientMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.ClientMessage(nil), f.sent...)
}

// scriptedDialer returns each outcome in turn, then keeps failing. It
// counts calls.
type scriptedDialer struct {
	mu       sync.Mutex
	outcomes []dialOutcome
	calls    atomic.Int32
}

type dialOutcome struct {
	connection *fakeConnection
	err        error
}

func (d *scriptedDialer) dial(ctx context.Context) (Connection, error) {
	d.calls.Add(1)
	d.mu.Lock()
	defer d.mu.Unlock()
	outcome := dialOutcome{err: errors.New("no more scripted dials")}
	if len(d.outcomes) > 0 {
		outcome = d.outcomes[0]
		d.outcomes = d.outcomes[1:]
	}
	var connection Connection
	if outcome.connection != nil {
		connection = outcome.connection
	}
	return connection, outcome.err
}

func succeed(connection *fakeConnection) dialOutcome { return dialOutcome{connection: connection} }
func fail(err error) dialOutcome                     { return dialOutcome{err: err} }

// testPool returns a pool with millisecond timings, closed at cleanup.
func testPool(t *testing.T, options PoolOptions) *Pool {
	t.Helper()
	if options.Backoff == nil {
		options.Backoff = func(int) time.Duration { return time.Millisecond }
	}
	if options.StableGrace == 0 {
		options.StableGrace = time.Hour // tests opt in to a short grace
	}
	pool := NewPool(options)
	t.Cleanup(pool.CloseAll)
	return pool
}

// nextEvent reads one event, failing the test after a second.
func nextEvent(t *testing.T, pool *Pool) Event {
	t.Helper()
	select {
	case event, open := <-pool.Events():
		if !open {
			t.Fatal("Events closed before the expected event")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for an event")
		return Event{}
	}
}

// expectState reads the next event and checks it is the given state.
func expectState(t *testing.T, pool *Pool, agent string, state AgentState) Event {
	t.Helper()
	event := nextEvent(t, pool)
	if event.Kind != EventState || event.Agent != agent || event.State != state {
		t.Fatalf("want %s state %s, got %+v", agent, state, event)
	}
	return event
}

// expectMessage reads the next event and checks it is a message.
func expectMessage(t *testing.T, pool *Pool, agent string) protocol.ServerMessage {
	t.Helper()
	event := nextEvent(t, pool)
	if event.Kind != EventMessage || event.Agent != agent {
		t.Fatalf("want a message from %s, got %+v", agent, event)
	}
	return event.Message
}

func TestPool_AdoptForwardsTaggedMessages(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	dialer := &scriptedDialer{}
	pool.Add("zee", dialer.dial)
	connection := newFakeConnection()
	if err := pool.Adopt("zee", connection); err != nil {
		t.Fatal(err)
	}
	expectState(t, pool, "zee", StateOnline)

	connection.messages <- &protocol.Pong{Type: protocol.TypePong}
	if _, isPong := expectMessage(t, pool, "zee").(*protocol.Pong); !isPong {
		t.Error("the adopted connection's message should be forwarded")
	}
	if dialer.calls.Load() != 0 {
		t.Error("adopting must not dial")
	}
}

func TestPool_OpenRetriesTransientFailures(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	connection := newFakeConnection()
	dialer := &scriptedDialer{outcomes: []dialOutcome{fail(errors.New("connection refused")), succeed(connection)}}
	pool.Add("zee", dialer.dial)
	if err := pool.Open("zee"); err != nil {
		t.Fatal(err)
	}

	expectState(t, pool, "zee", StateConnecting)
	retrying := expectState(t, pool, "zee", StateReconnecting)
	if retrying.Attempt != 1 || retrying.Err == nil || !strings.Contains(retrying.Err.Error(), "refused") {
		t.Errorf("first failure should be attempt 1 with its error, got %+v", retrying)
	}
	expectState(t, pool, "zee", StateOnline)
	if pool.State("zee") != StateOnline {
		t.Errorf("State should report online, got %s", pool.State("zee"))
	}
}

func TestPool_PermanentFailureStopsRetrying(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	dialer := &scriptedDialer{outcomes: []dialOutcome{fail(fmt.Errorf("tunnel: %w", tunnel.ErrAuthentication))}}
	pool.Add("zee", dialer.dial)
	must(t, pool.Open("zee"))

	expectState(t, pool, "zee", StateConnecting)
	failed := expectState(t, pool, "zee", StateFailed)
	if !errors.Is(failed.Err, tunnel.ErrAuthentication) {
		t.Errorf("Failed should carry the auth error, got %v", failed.Err)
	}
	time.Sleep(20 * time.Millisecond)
	if calls := dialer.calls.Load(); calls != 1 {
		t.Errorf("an auth failure must not be retried, dialled %d times", calls)
	}
}

func TestPool_OpenAfterFailureTriesAgain(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	dialer := &scriptedDialer{outcomes: []dialOutcome{fail(tunnel.ErrHostKey), succeed(newFakeConnection())}}
	pool.Add("zee", dialer.dial)
	must(t, pool.Open("zee"))
	expectState(t, pool, "zee", StateConnecting)
	expectState(t, pool, "zee", StateFailed)

	must(t, pool.Open("zee"))
	expectState(t, pool, "zee", StateConnecting)
	expectState(t, pool, "zee", StateOnline)
}

func TestPool_DropReconnects(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	first, second := newFakeConnection(), newFakeConnection()
	dialer := &scriptedDialer{outcomes: []dialOutcome{succeed(second)}}
	pool.Add("zee", dialer.dial)
	must(t, pool.Adopt("zee", first))
	expectState(t, pool, "zee", StateOnline)

	first.drop(errors.New("EOF"))
	dropped := expectState(t, pool, "zee", StateReconnecting)
	if dropped.Attempt != 1 || dropped.Err == nil || !strings.Contains(dropped.Err.Error(), "EOF") {
		t.Errorf("a drop should report attempt 1 and the drop error, got %+v", dropped)
	}
	expectState(t, pool, "zee", StateOnline)

	second.messages <- &protocol.Pong{Type: protocol.TypePong}
	expectMessage(t, pool, "zee")
}

func TestPool_QuickRedropKeepsBackingOff(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	first, second := newFakeConnection(), newFakeConnection()
	dialer := &scriptedDialer{outcomes: []dialOutcome{succeed(second), succeed(newFakeConnection())}}
	pool.Add("zee", dialer.dial)
	must(t, pool.Adopt("zee", first))
	expectState(t, pool, "zee", StateOnline)

	first.drop(nil)
	expectState(t, pool, "zee", StateReconnecting)
	expectState(t, pool, "zee", StateOnline)
	second.drop(nil) // well inside the stability grace
	if again := expectState(t, pool, "zee", StateReconnecting); again.Attempt != 2 {
		t.Errorf("a drop before the grace should keep climbing to attempt 2, got %d", again.Attempt)
	}
}

func TestPool_StableConnectionResetsBackoff(t *testing.T) {
	pool := testPool(t, PoolOptions{StableGrace: 10 * time.Millisecond})
	first, second := newFakeConnection(), newFakeConnection()
	dialer := &scriptedDialer{outcomes: []dialOutcome{succeed(second), succeed(newFakeConnection())}}
	pool.Add("zee", dialer.dial)
	must(t, pool.Adopt("zee", first))
	expectState(t, pool, "zee", StateOnline)

	first.drop(nil)
	expectState(t, pool, "zee", StateReconnecting)
	expectState(t, pool, "zee", StateOnline)
	time.Sleep(30 * time.Millisecond) // past the grace
	second.drop(nil)
	if again := expectState(t, pool, "zee", StateReconnecting); again.Attempt != 1 {
		t.Errorf("a drop after the grace should start again at attempt 1, got %d", again.Attempt)
	}
}

func TestPool_SendFailureDropsAndReconnects(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	first := newFakeConnection()
	first.sendErr = errors.New("broken pipe")
	dialer := &scriptedDialer{outcomes: []dialOutcome{succeed(newFakeConnection())}}
	pool.Add("zee", dialer.dial)
	must(t, pool.Adopt("zee", first))
	expectState(t, pool, "zee", StateOnline)

	if err := pool.Send("zee", protocol.ClientMessage{Type: protocol.TypePing}); err == nil {
		t.Fatal("Send should report the failure")
	}
	dropped := expectState(t, pool, "zee", StateReconnecting)
	if dropped.Err == nil || !strings.Contains(dropped.Err.Error(), "send failed") {
		t.Errorf("the drop should name the send failure, got %v", dropped.Err)
	}
	if !first.isClosed() {
		t.Error("the broken connection should be closed")
	}
	expectState(t, pool, "zee", StateOnline)
}

func TestPool_SendErrors(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	pool.Add("zee", (&scriptedDialer{}).dial)
	if err := pool.Send("zee", protocol.ClientMessage{Type: protocol.TypePing}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("sending to an offline agent: want ErrNotConnected, got %v", err)
	}
	if err := pool.Send("nobody", protocol.ClientMessage{Type: protocol.TypePing}); !errors.Is(err, ErrUnknownAgent) {
		t.Errorf("sending to an unregistered agent: want ErrUnknownAgent, got %v", err)
	}
	if err := pool.Open("nobody"); !errors.Is(err, ErrUnknownAgent) {
		t.Errorf("opening an unregistered agent: want ErrUnknownAgent, got %v", err)
	}
}

func TestPool_LinkDelegatesToTheLiveConnection(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	pool.Add("zee", (&scriptedDialer{}).dial)
	link := pool.Link("zee")
	if got := link.Session(); got != (Session{}) {
		t.Errorf("a never-opened agent's session should be zero, got %+v", got)
	}

	connection := newFakeConnection()
	connection.session = Session{PrincipalID: "P_1", ConversationID: "C_1", ProtocolVersion: 3}
	must(t, pool.Adopt("zee", connection))
	expectState(t, pool, "zee", StateOnline)

	link.SetConversation("C_2")
	if got := link.Session(); got.PrincipalID != "P_1" || got.ConversationID != "C_2" {
		t.Errorf("Session and SetConversation should reach the connection, got %+v", got)
	}
	if err := link.Send(protocol.ClientMessage{Type: protocol.TypeChat, Message: "hi"}); err != nil {
		t.Fatal(err)
	}
	if sent := connection.sentMessages(); len(sent) != 1 || sent[0].Message != "hi" {
		t.Errorf("Send should reach the connection, sent=%+v", sent)
	}
}

func TestPool_CloseGoesOffline(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	pool.Add("zee", (&scriptedDialer{}).dial)
	connection := newFakeConnection()
	connection.closeErr = errors.New("ssh -O exit failed")
	must(t, pool.Adopt("zee", connection))
	expectState(t, pool, "zee", StateOnline)

	pool.Close("zee")
	offline := expectState(t, pool, "zee", StateOffline)
	if offline.Err == nil || !strings.Contains(offline.Err.Error(), "ssh -O exit") {
		t.Errorf("a failed close should be reported on the Offline event, got %v", offline.Err)
	}
	if !connection.isClosed() {
		t.Error("Close should close the connection")
	}
}

func TestPool_IdleConnectionIsRetired(t *testing.T) {
	pool := testPool(t, PoolOptions{IdleTimeout: 20 * time.Millisecond})
	pool.Add("zee", (&scriptedDialer{}).dial)
	connection := newFakeConnection()
	must(t, pool.Adopt("zee", connection))
	expectState(t, pool, "zee", StateOnline)

	expectState(t, pool, "zee", StateOffline)
	if !connection.isClosed() {
		t.Error("retiring should close the connection")
	}
}

func TestPool_PinnedConnectionIsNotRetired(t *testing.T) {
	pool := testPool(t, PoolOptions{IdleTimeout: 10 * time.Millisecond})
	pool.Add("zee", (&scriptedDialer{}).dial)
	pool.Pin("zee", true)
	must(t, pool.Adopt("zee", newFakeConnection()))
	expectState(t, pool, "zee", StateOnline)

	time.Sleep(60 * time.Millisecond)
	if state := pool.State("zee"); state != StateOnline {
		t.Errorf("a pinned agent must stay online, got %s", state)
	}
}

func TestPool_TurnInFlightBlocksRetirement(t *testing.T) {
	pool := testPool(t, PoolOptions{IdleTimeout: 10 * time.Millisecond})
	pool.Add("zee", (&scriptedDialer{}).dial)
	connection := newFakeConnection()
	must(t, pool.Adopt("zee", connection))
	expectState(t, pool, "zee", StateOnline)

	if err := pool.Send("zee", protocol.ClientMessage{Type: protocol.TypeChat, Message: "long job"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if state := pool.State("zee"); state != StateOnline {
		t.Fatalf("an agent with a turn in flight must stay online, got %s", state)
	}

	connection.messages <- &protocol.Response{Type: protocol.TypeResponse, ConversationID: "C_1"}
	expectMessage(t, pool, "zee")
	expectState(t, pool, "zee", StateOffline) // idle again once the reply lands
}

func TestPool_EventsKeepOrderAndNeverBlockReaders(t *testing.T) {
	pool := testPool(t, PoolOptions{})
	pool.Add("zee", (&scriptedDialer{}).dial)
	pool.Add("coop", (&scriptedDialer{}).dial)
	zee, coop := newFakeConnection(), newFakeConnection()
	must(t, pool.Adopt("zee", zee))
	must(t, pool.Adopt("coop", coop))

	// Far more than any channel buffer, delivered while nobody reads.
	const count = 500
	go func() {
		for index := range count {
			zee.messages <- &protocol.Error{Type: protocol.TypeError, Message: fmt.Sprint(index)}
		}
	}()
	coop.messages <- &protocol.Pong{Type: protocol.TypePong}
	time.Sleep(20 * time.Millisecond)

	nextZee := 0
	coopSeen := false
	for nextZee < count || !coopSeen {
		event := nextEvent(t, pool)
		if event.Kind != EventMessage {
			continue
		}
		switch message := event.Message.(type) {
		case *protocol.Error:
			if message.Message != fmt.Sprint(nextZee) {
				t.Fatalf("zee's messages out of order: want %d, got %s", nextZee, message.Message)
			}
			nextZee++
		case *protocol.Pong:
			coopSeen = event.Agent == "coop"
		default:
			t.Fatalf("unexpected message %T", message)
		}
	}
}

func TestPool_CloseAllClosesEverything(t *testing.T) {
	pool := NewPool(PoolOptions{Backoff: func(int) time.Duration { return time.Millisecond }})
	pool.Add("zee", (&scriptedDialer{}).dial)
	connection := newFakeConnection()
	must(t, pool.Adopt("zee", connection))

	pool.CloseAll()
	if !connection.isClosed() {
		t.Error("CloseAll should close every connection")
	}
	drained := false
	deadline := time.After(time.Second)
	for !drained {
		select {
		case _, open := <-pool.Events():
			drained = !open
		case <-deadline:
			t.Fatal("Events should be closed after CloseAll")
		}
	}
	pool.CloseAll() // idempotent
}

func TestBackoffDelay_Schedule(t *testing.T) {
	cases := map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 5: 16 * time.Second, 6: 30 * time.Second, 64: 30 * time.Second}
	for attempt, base := range cases {
		got := BackoffDelay(attempt)
		if got < base || got > base+base/5 {
			t.Errorf("BackoffDelay(%d) = %s, want %s plus up to 20%%", attempt, got, base)
		}
	}
}

func TestAgentState_String(t *testing.T) {
	if StateReconnecting.String() != "reconnecting" || AgentState(99).String() != "AgentState(99)" {
		t.Errorf("unexpected names: %s, %s", StateReconnecting, AgentState(99))
	}
}

func TestSocketDialer_FailureReturnsUntypedNil(t *testing.T) {
	connection, err := SocketDialer(t.TempDir() + "/missing.sock")(context.Background())
	if err == nil {
		t.Fatal("dialling a missing socket should fail")
	}
	if connection != nil {
		t.Errorf("a failed dial must return a nil interface, got %#v", connection)
	}
}

func TestPool_SessionSurvivesADrop(t *testing.T) {
	pool := testPool(t, PoolOptions{Backoff: func(int) time.Duration { return time.Hour }})
	pool.Add("zee", (&scriptedDialer{}).dial)
	connection := newFakeConnection()
	connection.session = Session{PrincipalID: "P_1", ConversationID: "C_7", ProtocolVersion: 3}
	must(t, pool.Adopt("zee", connection))
	expectState(t, pool, "zee", StateOnline)

	connection.drop(nil)
	expectState(t, pool, "zee", StateReconnecting)
	session, connected := pool.Session("zee")
	if connected {
		t.Error("a dropped agent should report not connected")
	}
	if session.ConversationID != "C_7" {
		t.Errorf("the last session should survive the drop, got %+v", session)
	}
}

func TestPool_DropsAConnectionWithNoWelcome(t *testing.T) {
	pool := testPool(t, PoolOptions{WelcomeTimeout: 20 * time.Millisecond})
	silent, replacement := newFakeConnection(), newFakeConnection()
	dialer := &scriptedDialer{outcomes: []dialOutcome{succeed(replacement)}}
	pool.Add("zee", dialer.dial)
	must(t, pool.Adopt("zee", silent))
	expectState(t, pool, "zee", StateOnline)

	dropped := expectState(t, pool, "zee", StateReconnecting)
	if !errors.Is(dropped.Err, ErrNoWelcome) || !silent.isClosed() {
		t.Errorf("a silent connection should be closed for want of a welcome, got %+v", dropped)
	}
	expectState(t, pool, "zee", StateOnline)
	replacement.messages <- &protocol.Welcome{Type: protocol.TypeWelcome}
	expectMessage(t, pool, "zee")
	select {
	case event := <-pool.Events():
		t.Errorf("a welcomed connection should stay up, got %+v", event)
	case <-time.After(60 * time.Millisecond):
	}
}

func TestPool_DropsAConnectionWhoseEventsBackUp(t *testing.T) {
	pool := testPool(t, PoolOptions{MaxQueuedEvents: 3})
	connection := newFakeConnection()
	pool.Add("zee", (&scriptedDialer{}).dial)
	must(t, pool.Adopt("zee", connection))
	// Nothing reads Events yet, so the online state and the messages queue up.
	for range 3 {
		connection.messages <- &protocol.Pong{Type: protocol.TypePong}
	}
	deadline := time.Now().Add(time.Second)
	for !connection.isClosed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !connection.isClosed() {
		t.Fatal("a connection whose events back up should be closed")
	}
	var dropErr error
	for dropErr == nil {
		if event := nextEvent(t, pool); event.Kind == EventState && event.State == StateReconnecting {
			dropErr = event.Err
		}
	}
	if !errors.Is(dropErr, ErrBacklog) {
		t.Errorf("the drop should say the client fell behind, got %v", dropErr)
	}
}
