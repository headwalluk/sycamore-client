package core

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/headwalluk/sycamore-client/core/config"
	"github.com/headwalluk/sycamore-client/core/protocol"
	"github.com/headwalluk/sycamore-client/core/tunnel"
)

// The pool keeps one connection per agent, keyed by name. See
// dev-notes/planning-multi-agent.md §3b for the design.

// Connection is one live agent connection. Both *Client (a local socket)
// and *InstanceClient (over an SSH tunnel) satisfy it.
type Connection interface {
	Messages() <-chan protocol.ServerMessage
	Send(protocol.ClientMessage) error
	Session() Session
	SetConversation(id string)
	Err() error
	Close() error
}

// Dialer opens a fresh connection to one agent. ctx bounds only the setup.
type Dialer func(ctx context.Context) (Connection, error)

// InstanceDialer dials a configured instance over a fresh SSH tunnel.
func InstanceDialer(inst config.Instance) Dialer {
	return func(ctx context.Context) (Connection, error) {
		client, err := DialInstance(ctx, inst)
		if err != nil {
			// A nil *InstanceClient in the interface would not compare nil.
			return nil, err
		}
		return client, nil
	}
}

// SocketDialer dials a local socket path directly.
func SocketDialer(socketPath string) Dialer {
	return func(ctx context.Context) (Connection, error) {
		client, err := Connect(ctx, socketPath)
		if err != nil {
			// A nil *Client in the interface would not compare nil.
			return nil, err
		}
		return client, nil
	}
}

// AgentState is where an agent's connection is in its lifecycle.
type AgentState int

const (
	StateOffline      AgentState = iota // not opened, closed, or retired while idle
	StateConnecting                     // first dial in progress
	StateOnline                         // connected; messages flow
	StateReconnecting                   // a dial failed or the connection dropped; retrying
	StateFailed                         // a dial failed in a way retrying can't fix
)

func (s AgentState) String() string {
	names := map[AgentState]string{
		StateOffline:      "offline",
		StateConnecting:   "connecting",
		StateOnline:       "online",
		StateReconnecting: "reconnecting",
		StateFailed:       "failed",
	}
	name, known := names[s]
	if !known {
		name = fmt.Sprintf("AgentState(%d)", int(s))
	}
	return name
}

// EventKind says which fields of an [Event] are set.
type EventKind int

const (
	EventMessage EventKind = iota // Message is set
	EventState                    // State, Attempt and Err are set
)

// Event is one thing that happened on one agent. An agent's events arrive
// in the order they happened; events from different agents interleave.
type Event struct {
	Agent string
	Kind  EventKind

	Message protocol.ServerMessage

	State   AgentState
	Attempt int   // reconnect attempt number while StateReconnecting, 1-based
	Err     error // why the agent dropped, failed a dial, or failed to close
}

// PoolOptions tune the pool. Zero values take the defaults below.
type PoolOptions struct {
	// DialTimeout bounds each dial attempt.
	DialTimeout time.Duration

	// IdleTimeout retires a connection with no traffic for this long,
	// unless it's pinned or has a chat turn in flight. Zero never retires.
	IdleTimeout time.Duration

	// StableGrace is how long a connection must stay up before a later
	// drop restarts the backoff from the first attempt.
	StableGrace time.Duration

	// Backoff returns the wait before reconnect attempt n (1-based).
	Backoff func(attempt int) time.Duration

	// IsPermanent reports whether a dial error should stop retrying.
	IsPermanent func(error) bool

	// WelcomeTimeout drops a new connection that hasn't sent its welcome
	// in this long.
	WelcomeTimeout time.Duration

	// MaxQueuedEvents drops an agent's connection when this many events
	// are waiting for the consumer, rather than queueing without limit.
	MaxQueuedEvents int
}

// Defaults for PoolOptions.
const (
	DefaultDialTimeout     = 25 * time.Second
	DefaultStableGrace     = 5 * time.Second
	DefaultWelcomeTimeout  = 20 * time.Second
	DefaultMaxQueuedEvents = 10000
)

// idleCheckCeiling caps how often an online agent checks for idleness.
const idleCheckCeiling = time.Minute

// ErrUnknownAgent is returned for a name the pool has no dialer for.
var ErrUnknownAgent = errors.New("pool: unknown agent")

// ErrNoWelcome is why a connection whose agent never said hello was dropped.
var ErrNoWelcome = errors.New("pool: no welcome from the agent")

// ErrBacklog is why a connection was dropped when its events backed up.
var ErrBacklog = errors.New("pool: the client isn't keeping up with the agent")

// ErrNotConnected is returned by Send when the agent has no live connection.
var ErrNotConnected = errors.New("pool: agent is not connected")

// BackoffDelay is the default reconnect schedule: 1s, 2s, 4s, 8s, 16s,
// then 30s, each with up to 20% jitter.
func BackoffDelay(attempt int) time.Duration {
	const base = time.Second
	const ceiling = 30 * time.Second
	if attempt < 1 {
		attempt = 1
	}
	delay := ceiling
	// The shift overflows to <= 0 for large attempts; the cap covers that.
	if shifted := base << (attempt - 1); shifted > 0 && shifted < ceiling {
		delay = shifted
	}
	return delay + time.Duration(rand.Int64N(int64(delay)/5+1))
}

// IsPermanentDialError reports whether err is an SSH authentication or
// host-key failure, which retrying won't fix (and could trip fail2ban).
func IsPermanentDialError(err error) bool {
	return errors.Is(err, tunnel.ErrAuthentication) || errors.Is(err, tunnel.ErrHostKey)
}

// Pool holds one connection per agent, opened on demand, reconnected when
// it drops, and retired when idle. Its methods are safe for concurrent use.
type Pool struct {
	options PoolOptions

	mu     sync.Mutex
	agents map[string]*poolAgent
	closed bool

	queue *eventQueue
}

// poolAgent is one agent's registration and lifecycle state. Fields after
// mu are guarded by it.
type poolAgent struct {
	name string
	dial Dialer

	// broken receives a send failure, so the lifecycle goroutine treats
	// the connection as dropped. Buffered; extra failures are discarded.
	broken chan error

	mu           sync.Mutex
	connection   Connection
	lastSession  Session // the dropped connection's session, while down
	state        AgentState
	pinned       bool
	inFlight     bool
	lastActivity time.Time
	running      bool
	cancel       context.CancelFunc
	done         chan struct{}
}

// NewPool returns an empty pool. Register agents with Add.
func NewPool(options PoolOptions) *Pool {
	if options.DialTimeout <= 0 {
		options.DialTimeout = DefaultDialTimeout
	}
	if options.StableGrace <= 0 {
		options.StableGrace = DefaultStableGrace
	}
	if options.Backoff == nil {
		options.Backoff = BackoffDelay
	}
	if options.IsPermanent == nil {
		options.IsPermanent = IsPermanentDialError
	}
	if options.WelcomeTimeout <= 0 {
		options.WelcomeTimeout = DefaultWelcomeTimeout
	}
	if options.MaxQueuedEvents <= 0 {
		options.MaxQueuedEvents = DefaultMaxQueuedEvents
	}
	return &Pool{
		options: options,
		agents:  map[string]*poolAgent{},
		queue:   newEventQueue(),
	}
}

// Add registers an agent and how to dial it. It doesn't connect. Adding a
// name again replaces its dialer for future dials.
func (p *Pool) Add(name string, dial Dialer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, found := p.agents[name]; found {
		existing.dial = dial
		return
	}
	p.agents[name] = &poolAgent{name: name, dial: dial, broken: make(chan error, 1)}
}

// Names returns the registered agent names, sorted.
func (p *Pool) Names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	names := make([]string, 0, len(p.agents))
	for name := range p.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Events returns the channel every agent's events are delivered on. It is
// closed by CloseAll once the pool has shut down.
func (p *Pool) Events() <-chan Event {
	return p.queue.out
}

// Open starts connecting an agent in the background. It does nothing if
// the agent is already open, connecting or reconnecting.
func (p *Pool) Open(name string) error {
	return p.start(name, nil)
}

// Adopt hands the pool a connection that was dialled outside it, such as a
// start-up connection whose failure should be reported before any UI
// appears. The agent must be registered, since reconnects use its dialer.
func (p *Pool) Adopt(name string, connection Connection) error {
	return p.start(name, connection)
}

// start runs an agent's lifecycle goroutine, beginning from connection when
// one is supplied, or from a fresh dial.
func (p *Pool) start(name string, connection Connection) error {
	agent, err := p.lookup(name)
	if err != nil {
		return err
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.running {
		if connection != nil {
			return fmt.Errorf("pool: agent %q is already open", name)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	agent.running = true
	agent.cancel = cancel
	done := make(chan struct{})
	agent.done = done
	agent.lastActivity = time.Now()
	go p.run(ctx, agent, connection, done)
	return nil
}

// Close disconnects an agent in the background; it ends up StateOffline.
// Closing an agent that isn't open does nothing.
func (p *Pool) Close(name string) {
	agent, err := p.lookup(name)
	if err != nil {
		return
	}
	agent.mu.Lock()
	cancel := agent.cancel
	agent.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// CloseAll disconnects every agent, waits for them to finish, and closes
// the Events channel.
func (p *Pool) CloseAll() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	agents := make([]*poolAgent, 0, len(p.agents))
	for _, agent := range p.agents {
		agents = append(agents, agent)
	}
	p.mu.Unlock()

	var pending []chan struct{}
	for _, agent := range agents {
		agent.mu.Lock()
		if agent.running {
			agent.cancel()
			pending = append(pending, agent.done)
		}
		agent.mu.Unlock()
	}
	for _, done := range pending {
		<-done
	}
	p.queue.close()
}

// Send writes msg on the agent's live connection. A failed send drops the
// connection, which then reconnects as it would after any other drop.
func (p *Pool) Send(name string, msg protocol.ClientMessage) error {
	agent, err := p.lookup(name)
	if err != nil {
		return err
	}
	agent.mu.Lock()
	connection := agent.connection
	agent.mu.Unlock()
	if connection == nil {
		return ErrNotConnected
	}

	sendErr := connection.Send(msg)
	if sendErr != nil {
		select {
		case agent.broken <- sendErr:
		default:
			// A failure is already pending; one is enough to drop it.
		}
	} else {
		agent.mu.Lock()
		agent.lastActivity = time.Now()
		if msg.Type == protocol.TypeChat {
			agent.inFlight = true
		}
		agent.mu.Unlock()
	}
	return sendErr
}

// Session returns the agent's live session and true, or, while it has no
// live connection, the last session it had and false. A UI uses the last
// session to return to the same conversation after a reconnect.
func (p *Pool) Session(name string) (Session, bool) {
	var session Session
	connected := false
	agent, err := p.lookup(name)
	if err == nil {
		agent.mu.Lock()
		session = agent.lastSession
		if agent.connection != nil {
			session = agent.connection.Session()
			connected = true
		}
		agent.mu.Unlock()
	}
	return session, connected
}

// SetConversation points the agent's session at conversation id. It does
// nothing when the agent has no live connection.
func (p *Pool) SetConversation(name, id string) {
	agent, err := p.lookup(name)
	if err != nil {
		return
	}
	agent.mu.Lock()
	connection := agent.connection
	agent.mu.Unlock()
	if connection != nil {
		connection.SetConversation(id)
	}
}

// State returns the agent's lifecycle state.
func (p *Pool) State(name string) AgentState {
	state := StateOffline
	agent, err := p.lookup(name)
	if err == nil {
		agent.mu.Lock()
		state = agent.state
		agent.mu.Unlock()
	}
	return state
}

// Pin marks an agent as in use (for example, on screen), which exempts it
// from idle retirement.
func (p *Pool) Pin(name string, pinned bool) {
	agent, err := p.lookup(name)
	if err != nil {
		return
	}
	agent.mu.Lock()
	agent.pinned = pinned
	agent.mu.Unlock()
}

// Link returns a handle that sends to, and reads the session of, one agent.
func (p *Pool) Link(name string) *AgentLink {
	return &AgentLink{pool: p, name: name}
}

// AgentLink addresses one agent in a pool.
type AgentLink struct {
	pool *Pool
	name string
}

// Name is the agent this link addresses.
func (l *AgentLink) Name() string { return l.name }

// Send writes msg to the agent. See [Pool.Send].
func (l *AgentLink) Send(msg protocol.ClientMessage) error { return l.pool.Send(l.name, msg) }

// Session returns the agent's session: the live one, or the last one while
// the agent is down. See [Pool.Session].
func (l *AgentLink) Session() Session {
	session, _ := l.pool.Session(l.name)
	return session
}

// SetConversation points the agent's session at conversation id.
func (l *AgentLink) SetConversation(id string) { l.pool.SetConversation(l.name, id) }

// lookup returns a registered agent.
func (p *Pool) lookup(name string) (*poolAgent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	agent, found := p.agents[name]
	if !found {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAgent, name)
	}
	return agent, nil
}

// serveOutcome is why serve stopped reading a connection.
type serveOutcome int

const (
	outcomeDropped serveOutcome = iota // the connection ended or a send failed
	outcomeClosed                      // Close or CloseAll was called
	outcomeRetired                     // idle past the timeout
)

// run is an agent's lifecycle goroutine: dial (or start from an adopted
// connection), serve it, and on a drop back off and redial, until the
// agent is closed, retired, or fails permanently. It closes done, its own
// run's channel, on exit.
func (p *Pool) run(ctx context.Context, agent *poolAgent, connection Connection, done chan struct{}) {
	finalState := StateOffline
	var finalErr error
	attempt := 0

	for ctx.Err() == nil {
		if connection == nil {
			if attempt == 0 {
				p.setState(agent, StateConnecting, 0, nil)
			}
			dialed, dialErr := p.dialOnce(ctx, agent)
			if ctx.Err() != nil {
				connection = dialed // closed on the way out
				break
			}
			if dialErr != nil {
				if p.options.IsPermanent(dialErr) {
					finalState, finalErr = StateFailed, dialErr
					break
				}
				attempt++
				p.setState(agent, StateReconnecting, attempt, dialErr)
				if !wait(ctx, p.options.Backoff(attempt)) {
					break
				}
				continue
			}
			connection = dialed
		}

		p.setOnline(agent, connection)
		connectedAt := time.Now()
		outcome, dropErr := p.serve(ctx, agent, connection)
		p.clearConnection(agent)
		closeErr := connection.Close()
		connection = nil

		if outcome != outcomeDropped {
			finalErr = closeErr
			break
		}
		if time.Since(connectedAt) >= p.options.StableGrace {
			attempt = 0
		}
		attempt++
		p.setState(agent, StateReconnecting, attempt, errors.Join(dropErr, closeErr))
		if !wait(ctx, p.options.Backoff(attempt)) {
			break
		}
	}

	// Closed before it was served: an adoption or a dial cut short.
	if connection != nil {
		finalErr = errors.Join(finalErr, connection.Close())
	}

	// One locked step, so a new Open can't slip in between the final event
	// and the agent being marked stopped.
	agent.mu.Lock()
	agent.state = finalState
	agent.running = false
	agent.cancel = nil
	p.queue.push(Event{Agent: agent.name, Kind: EventState, State: finalState, Err: finalErr})
	agent.mu.Unlock()
	close(done)
}

// dialOnce runs the agent's dialer under the dial timeout.
func (p *Pool) dialOnce(ctx context.Context, agent *poolAgent) (Connection, error) {
	dialCtx, cancel := context.WithTimeout(ctx, p.options.DialTimeout)
	defer cancel()
	p.mu.Lock()
	dial := agent.dial
	p.mu.Unlock()
	return dial(dialCtx)
}

// serve forwards the connection's messages as events until it drops, the
// agent is closed, or it has been idle past the timeout. It also drops a
// connection with no welcome in time, or whose events back up.
func (p *Pool) serve(ctx context.Context, agent *poolAgent, connection Connection) (serveOutcome, error) {
	var idleCheck <-chan time.Time
	if p.options.IdleTimeout > 0 {
		ticker := time.NewTicker(min(p.options.IdleTimeout/4+time.Millisecond, idleCheckCeiling))
		defer ticker.Stop()
		idleCheck = ticker.C
	}
	welcomeTimer := time.NewTimer(p.options.WelcomeTimeout)
	defer welcomeTimer.Stop()
	welcomeDue := welcomeTimer.C

	outcome := outcomeDropped
	var dropErr error
serving:
	for {
		select {
		case <-ctx.Done():
			outcome = outcomeClosed
			break serving
		case sendErr := <-agent.broken:
			dropErr = fmt.Errorf("send failed: %w", sendErr)
			break serving
		case msg, open := <-connection.Messages():
			if !open {
				dropErr = connection.Err()
				break serving
			}
			if _, welcomed := msg.(*protocol.Welcome); welcomed {
				welcomeDue = nil // a nil channel never fires
			}
			p.noteReceived(agent, msg)
			if queued := p.queue.push(Event{Agent: agent.name, Kind: EventMessage, Message: msg}); queued >= p.options.MaxQueuedEvents {
				dropErr = fmt.Errorf("%w: %d events are waiting", ErrBacklog, queued)
				break serving
			}
		case <-welcomeDue:
			dropErr = fmt.Errorf("%w within %s", ErrNoWelcome, p.options.WelcomeTimeout)
			break serving
		case <-idleCheck:
			if p.isIdle(agent) {
				outcome = outcomeRetired
				break serving
			}
		}
	}
	return outcome, dropErr
}

// noteReceived records traffic, and ends the in-flight turn on its reply.
func (p *Pool) noteReceived(agent *poolAgent, msg protocol.ServerMessage) {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	agent.lastActivity = time.Now()
	switch msg.(type) {
	case *protocol.Response, *protocol.Error:
		// No request ids: any error ends the turn, as sycamore-web does.
		agent.inFlight = false
	default:
		// Other messages don't end a chat turn.
	}
}

// isIdle reports whether an agent may be retired now.
func (p *Pool) isIdle(agent *poolAgent) bool {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return !agent.pinned && !agent.inFlight && time.Since(agent.lastActivity) >= p.options.IdleTimeout
}

// setOnline records the live connection and announces it.
func (p *Pool) setOnline(agent *poolAgent, connection Connection) {
	// A send failure from the previous connection must not drop this one.
	select {
	case <-agent.broken:
	default:
	}
	agent.mu.Lock()
	agent.connection = connection
	agent.lastActivity = time.Now()
	agent.inFlight = false
	agent.mu.Unlock()
	p.setState(agent, StateOnline, 0, nil)
}

// clearConnection forgets the live connection, so Send reports
// ErrNotConnected while the agent is down, and keeps its last session.
func (p *Pool) clearConnection(agent *poolAgent) {
	agent.mu.Lock()
	agent.lastSession = agent.connection.Session()
	agent.connection = nil
	agent.inFlight = false
	agent.mu.Unlock()
}

// setState records and announces an agent's state.
func (p *Pool) setState(agent *poolAgent, state AgentState, attempt int, err error) {
	agent.mu.Lock()
	agent.state = state
	agent.mu.Unlock()
	p.queue.push(Event{Agent: agent.name, Kind: EventState, State: state, Attempt: attempt, Err: err})
}

// wait sleeps for delay, returning false if ctx ends first.
func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	completed := false
	select {
	case <-ctx.Done():
	case <-timer.C:
		completed = true
	}
	return completed
}

// eventQueue is an unbounded FIFO in front of the Events channel, so an
// agent's reader never blocks on a slow consumer.
type eventQueue struct {
	mu      sync.Mutex
	items   []Event
	closing bool
	signal  chan struct{}
	out     chan Event
	stopped chan struct{}
}

func newEventQueue() *eventQueue {
	queue := &eventQueue{
		signal:  make(chan struct{}, 1),
		out:     make(chan Event),
		stopped: make(chan struct{}),
	}
	go queue.deliver()
	return queue
}

// push appends an event without blocking, and returns how many are queued.
// Events pushed after close are discarded.
func (q *eventQueue) push(event Event) int {
	q.mu.Lock()
	if !q.closing {
		q.items = append(q.items, event)
	}
	queued := len(q.items)
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
		// A wake-up is already pending.
	}
	return queued
}

// close stops delivery and closes the out channel. Undelivered events are
// dropped, since nothing reads them once the pool has shut down.
func (q *eventQueue) close() {
	q.mu.Lock()
	q.closing = true
	q.mu.Unlock()
	close(q.stopped)
}

// deliver moves queued events to out, in order, until close.
func (q *eventQueue) deliver() {
	defer close(q.out)
	running := true
	for running {
		select {
		case <-q.stopped:
			running = false
		case <-q.signal:
			running = q.drain()
		}
	}
}

// drain delivers everything queued, returning false if close interrupts.
func (q *eventQueue) drain() bool {
	running := true
	for running {
		q.mu.Lock()
		if len(q.items) == 0 {
			q.mu.Unlock()
			break
		}
		next := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()
		select {
		case q.out <- next:
		case <-q.stopped:
			running = false
		}
	}
	return running
}
