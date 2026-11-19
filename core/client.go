package core

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/headwalluk/sycamore-client/core/protocol"
)

// Session is the per-connection state tracked from server messages.
// PrincipalID is set from the welcome and doesn't change for the
// lifetime of a connection. ConversationID is the conversation Send
// targets: set from welcome, new_conversation and SetConversation, and
// from a response only while it is still empty. It is empty when there
// is no active conversation.
//
// ProtocolVersion is the daemon's chat-socket protocol version, taken
// from the welcome and normalised so it is always >= 1 (an older daemon
// that omits the field is treated as version 1). Agent is the welcome's
// agent profile, nil before protocol 3. Gate optional features on
// [Session.Has], not on the version.
type Session struct {
	PrincipalID     string
	ConversationID  string
	ProtocolVersion int
	Agent           *protocol.AgentProfile
}

// protocolV2Capabilities are the features a protocol-2 daemon, which
// sends no agent profile, is known to support.
var protocolV2Capabilities = []string{protocol.CapConversations, protocol.CapHistory}

// Has reports whether the daemon supports the named capability: from
// the agent profile when there is one (protocol 3+), else by the
// protocol-2 fallback.
func (s Session) Has(capability string) bool {
	switch {
	case s.Agent != nil:
		return slices.Contains(s.Agent.Capabilities, capability)
	case s.ProtocolVersion >= 2:
		return slices.Contains(protocolV2Capabilities, capability)
	default:
		return false
	}
}

// Client is a session-aware wrapper around a [Conn]. It owns a
// goroutine that reads server messages, runs the session-state
// reducer, and pushes typed messages onto [Client.Messages] for the
// UI to consume.
//
// Client.Send is safe to call from multiple goroutines. Client.Close
// is idempotent. The Messages channel is closed when the read loop
// exits (clean EOF or transport error); inspect [Client.Err] after
// the channel drains to learn why.
type Client struct {
	conn *Conn

	sendMu sync.Mutex

	sessMu  sync.RWMutex
	session Session

	messages  chan protocol.ServerMessage
	closing   chan struct{}
	done      chan struct{}
	closeOnce sync.Once

	errMu sync.Mutex
	err   error
}

// messageBufferSize sizes the Messages channel. The read loop blocks
// on send when the buffer fills; pick a value that absorbs short UI
// stalls without unbounded memory growth. Sixteen messages is enough
// to cover an auto-rename + response + side-channel pong while the
// UI redraws.
const messageBufferSize = 16

// NewClient wraps an existing Conn and starts the read goroutine.
// The Client takes ownership of conn — do not call Send or Recv on
// it directly afterwards.
func NewClient(conn *Conn) *Client {
	client := &Client{
		conn:     conn,
		messages: make(chan protocol.ServerMessage, messageBufferSize),
		closing:  make(chan struct{}),
		done:     make(chan struct{}),
	}
	go client.run()
	return client
}

// Connect dials a socket and wraps the resulting Conn in a Client.
// The common production entry point.
func Connect(ctx context.Context, socketPath string) (*Client, error) {
	conn, err := Dial(ctx, socketPath)
	if err != nil {
		return nil, err
	}
	return NewClient(conn), nil
}

// Messages returns the channel of decoded incoming server messages.
// The channel is closed when the read loop exits — either because
// the peer closed the socket, the transport returned an error, or
// Close was called. Use [Client.Err] after the channel drains to
// distinguish.
func (c *Client) Messages() <-chan protocol.ServerMessage {
	return c.messages
}

// Session returns a snapshot of the current session state. Safe to
// call concurrently with the read loop.
func (c *Client) Session() Session {
	c.sessMu.RLock()
	defer c.sessMu.RUnlock()
	return c.session
}

// SetConversation overrides the active conversation id that Send uses to
// auto-populate conversation-aware messages (chat, info, history, …). A
// client switches conversations by calling this, then re-seeding the
// transcript with that conversation's history; subsequent chats then
// follow the switch. Safe for concurrent use. ProtocolVersion and
// PrincipalID are left untouched.
func (c *Client) SetConversation(id string) {
	c.sessMu.Lock()
	c.session.ConversationID = id
	c.sessMu.Unlock()
}

// typesAcceptingConversationID is the set of client-message types
// where the daemon reads conversation_id. Send auto-populates the
// field for these when the caller leaves it empty; the cookbook
// (§5) names chat, info, set_model, show_model, and rename.
var typesAcceptingConversationID = map[string]bool{
	protocol.TypeChat:      true,
	protocol.TypeInfo:      true,
	protocol.TypeSetModel:  true,
	protocol.TypeShowModel: true,
	protocol.TypeRename:    true,
	protocol.TypeHistory:   true, // v2: omitted id defaults to the session conversation
	// park's fallback is "most recently updated", which is the wrong
	// conversation as soon as the user has switched into an older
	// thread — so the id always goes on the wire, never omitted.
	protocol.TypePark: true,
}

// Send marshals msg and writes it to the connection. If msg.Type is
// one of the conversation-aware kinds (chat, info, set_model,
// show_model, rename) and msg.ConversationID is empty, Send fills it
// in from the current session state. An explicit non-empty value
// passed in by the caller is never overridden.
//
// Send is safe to call from multiple goroutines.
func (c *Client) Send(msg protocol.ClientMessage) error {
	if msg.ConversationID == "" && typesAcceptingConversationID[msg.Type] {
		c.sessMu.RLock()
		msg.ConversationID = c.session.ConversationID
		c.sessMu.RUnlock()
	}

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.conn.Send(msg)
}

// Close terminates the read loop and closes the underlying Conn.
// Safe to call multiple times. Blocks until the read goroutine has
// exited so callers can be sure no further messages will appear.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closing)
		err = c.conn.Close()
	})
	<-c.done
	return err
}

// Err returns the read-loop's terminal error, if any. Returns nil
// before the read loop exits and after a clean shutdown via Close.
// An [io.EOF] return indicates the peer closed the socket cleanly.
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

func (c *Client) setErr(err error) {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.err == nil {
		c.err = err
	}
}

func (c *Client) run() {
	defer close(c.done)
	defer close(c.messages)

	running := true
	for running {
		kind, raw, err := c.conn.Recv()
		if err != nil {
			// Close-initiated shutdown produces a read error; don't
			// surface it as a transport failure.
			select {
			case <-c.closing:
			default:
				c.setErr(err)
			}
			break
		}

		msg, decodeErr := decodeServerMessage(kind, raw)
		switch {
		case decodeErr != nil:
			// A known type that didn't decode is shown, not dropped.
			msg = &protocol.Undecodable{Kind: kind, Err: decodeErr.Error()}
		case msg == nil:
			// An unknown type: the daemon may add kinds before this client
			// catches up, and a stale client still handles the ones it knows.
			continue
		default:
			c.updateSession(msg)
		}

		select {
		case c.messages <- msg:
		case <-c.closing:
			running = false
		}
	}
}

func (c *Client) updateSession(msg protocol.ServerMessage) {
	switch typed := msg.(type) {
	case *protocol.Welcome:
		c.sessMu.Lock()
		c.session.PrincipalID = typed.PrincipalID
		if typed.ConversationID != nil {
			c.session.ConversationID = *typed.ConversationID
		} else {
			c.session.ConversationID = ""
		}
		// Normalise the protocol version: an older daemon omits the
		// field (decoding to 0); treat that as version 1.
		if typed.ProtocolVersion < 1 {
			c.session.ProtocolVersion = 1
		} else {
			c.session.ProtocolVersion = typed.ProtocolVersion
		}
		c.session.Agent = typed.Agent
		c.sessMu.Unlock()
	case *protocol.NewConversation:
		c.sessMu.Lock()
		c.session.ConversationID = typed.ConversationID
		c.sessMu.Unlock()
	case *protocol.Response:
		// Adopt the id only when a first chat created the conversation
		// implicitly; a late reply for a conversation switched away from
		// must not pull the session back to it.
		c.sessMu.Lock()
		if c.session.ConversationID == "" {
			c.session.ConversationID = typed.ConversationID
		}
		c.sessMu.Unlock()
	}
}

// decodeServerMessage allocates the concrete protocol.* struct
// matching kind and unmarshals raw into it. Returns (nil, nil) for
// kinds this client doesn't know about so the run loop can skip
// them.
func decodeServerMessage(kind string, raw json.RawMessage) (protocol.ServerMessage, error) {
	var msg protocol.ServerMessage
	switch kind {
	case protocol.TypeWelcome:
		msg = &protocol.Welcome{}
	case protocol.TypeResponse:
		msg = &protocol.Response{}
	case protocol.TypeError:
		msg = &protocol.Error{}
	case protocol.TypePong:
		msg = &protocol.Pong{}
	case protocol.TypeNewConversation:
		msg = &protocol.NewConversation{}
	case protocol.TypeFinalizing:
		msg = &protocol.Finalizing{}
	case protocol.TypeCaptureStatus:
		msg = &protocol.CaptureStatus{}
	case protocol.TypeModelsList:
		msg = &protocol.ModelsList{}
	case protocol.TypeModelSet:
		msg = &protocol.ModelSet{}
	case protocol.TypeModelShow:
		msg = &protocol.ModelShow{}
	case protocol.TypeInfoResponse:
		msg = &protocol.InfoResponse{}
	case protocol.TypeDreamResult:
		msg = &protocol.DreamResult{}
	case protocol.TypeConversationRenamed:
		msg = &protocol.ConversationRenamed{}
	case protocol.TypeConversationParked:
		msg = &protocol.ConversationParked{}
	case protocol.TypeHistoryPage:
		msg = &protocol.HistoryPage{}
	case protocol.TypeConversationsList:
		msg = &protocol.ConversationsList{}
	case protocol.TypeProgress:
		msg = &protocol.Progress{}
	case protocol.TypeAvatarImage:
		msg = &protocol.AvatarImage{}
	default:
		return nil, nil
	}
	if err := json.Unmarshal(raw, msg); err != nil {
		return nil, fmt.Errorf("decode %s: %w", kind, err)
	}
	return msg, nil
}
