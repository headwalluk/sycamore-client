// Package protocol defines the wire-level message types exchanged with
// the Sycamore agent's chat socket.
//
// The wire format is newline-delimited JSON: one JSON object per line in
// each direction. Every message carries a "type" discriminator. Clients
// send a [ClientMessage] populated with the fields the daemon expects
// for that type; servers send one of the typed structs in this file,
// dispatched on the "type" field via the low-level Recv primitive in
// the parent core package.
//
// The authoritative reference for the wire schema lives in the daemon
// repo at docs/sycamore-protocol/. When this file disagrees with the
// daemon's actual behaviour, the daemon wins; cross-reference its
// src/socket/server.ts.
package protocol

import "encoding/json"

// Message type discriminators. Grouped by direction, matching the
// daemon's own naming.
const (
	// Client → server.
	TypeChat          = "chat"
	TypeNew           = "new"
	TypeInfo          = "info"
	TypeDream         = "dream"
	TypeModels        = "models"
	TypeSetModel      = "set_model"
	TypeShowModel     = "show_model"
	TypeRename        = "rename"
	TypeCapture       = "capture"
	TypePark          = "park"
	TypePing          = "ping"
	TypeHistory       = "history"       // protocol v2
	TypeConversations = "conversations" // protocol v2
	TypeAvatar        = "avatar"        // protocol v3

	// Server → client.
	TypeWelcome             = "welcome"
	TypeResponse            = "response"
	TypeError               = "error"
	TypePong                = "pong"
	TypeNewConversation     = "new_conversation"
	TypeFinalizing          = "finalizing"
	TypeCaptureStatus       = "capture_status"
	TypeModelsList          = "models_list"
	TypeModelSet            = "model_set"
	TypeModelShow           = "model_show"
	TypeInfoResponse        = "info_response"
	TypeDreamResult         = "dream_result"
	TypeConversationRenamed = "conversation_renamed"
	TypeConversationParked  = "conversation_parked"
	TypeHistoryPage         = "history_page"       // protocol v2
	TypeConversationsList   = "conversations_list" // protocol v2
	TypeProgress            = "progress"           // capability "progress"
	TypeAvatarImage         = "avatar_image"       // protocol v3
)

// Capability names listed in [AgentProfile.Capabilities] (protocol 3).
// Gate optional features on these via core.Session.Has rather than on
// the protocol version; unknown names are ignored.
const (
	CapConversations = "conversations"
	CapHistory       = "history"
	CapPark          = "park"
	CapProgress      = "progress"
	CapAvatar        = "avatar"
	CapAttachments   = "attachments"
)

// Progress step kinds, the Kind field of a [Progress] message.
const (
	ProgressAssistantText = "assistant_text"
	ProgressToolStart     = "tool_start"
	ProgressToolEnd       = "tool_end"
)

// History record roles. The Role field of a [HistoryMessage] is one of
// these — dispatch on it to map a record onto a user vs. assistant
// transcript entry.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// ContentBlock is one element of a [Response.Content] array. It is a
// flat struct with every possible field tagged omitempty; dispatch on
// Type at the call site. The four valid Type values are "text",
// "tool_use", "tool_result", and "image".
//
// Representation choice (decision #3, 2026-05-22): JSON's "additional
// fields are ignored" semantics make the flat-struct form idiomatic.
// A sealed-interface form was rejected as too much surface area for a
// four-variant union.
type ContentBlock struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	// tool_result. Content is either a JSON string or a JSON array of
	// nested ContentBlock — left as RawMessage so callers can branch.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`

	// image. Data is base64-encoded image bytes.
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

// Usage is the token-accounting block attached to every [Response].
type Usage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`
}

// ClientMessage is the single outgoing-message struct. Populate Type
// with one of the TypeChat / TypeNew / ... constants and set only the
// fields that apply to that type. Unused fields are omitted from the
// JSON.
type ClientMessage struct {
	Type           string `json:"type"`
	Message        string `json:"message,omitempty"`         // chat
	ConversationID string `json:"conversation_id,omitempty"` // chat, info, set_model, show_model, rename, history, park
	Action         string `json:"action,omitempty"`          // capture: "on" | "off" | "flush" | "status"
	Model          string `json:"model,omitempty"`           // set_model
	Title          string `json:"title,omitempty"`           // rename

	// History pagination (protocol v2). BeforeSequence is the
	// backward-pagination cursor: omit (zero) for the newest page,
	// else pass a prior page's OldestSequence to fetch older turns.
	BeforeSequence int `json:"before_sequence,omitempty"` // history

	// Limit is the page size — turns for history, conversations for
	// conversations. Omit (zero) to take the server default (50);
	// the server clamps to its max (200).
	Limit int `json:"limit,omitempty"` // history, conversations

	// BeforeUpdatedAt is the conversations backward-pagination cursor:
	// an ISO-8601 timestamp; pass a prior page's last updated_at to
	// fetch older conversations. (protocol v2)
	BeforeUpdatedAt string `json:"before_updated_at,omitempty"` // conversations

	// Progress asks the daemon to send [Progress] steps while a chat
	// turn runs. A daemon without the "progress" capability ignores it.
	Progress bool `json:"progress,omitempty"` // chat
}

// Welcome is the first message the server sends on every new
// connection. ConversationID and ConversationTitle are nullable: nil
// means the principal has no active conversation and the next chat
// will start one.
type Welcome struct {
	Type string `json:"type"`

	// ProtocolVersion is the daemon's chat-socket protocol version
	// (protocol v2 added it). 1 was the original; 2 added history /
	// conversations. The field is additive: an older daemon omits it,
	// decoding here to 0 — callers must treat 0 (absent) as 1. The
	// session reducer normalises this into [Session.ProtocolVersion],
	// which is always >= 1; gate v2 features on that.
	ProtocolVersion int `json:"protocol_version"`

	Principal         string  `json:"principal"`
	PrincipalID       string  `json:"principal_id"`
	ConversationID    *string `json:"conversation_id"`
	ConversationTitle *string `json:"conversation_title"`

	// Agent is how the agent presents itself (protocol 3); nil from an
	// older daemon.
	Agent *AgentProfile `json:"agent,omitempty"`
}

// AgentProfile is the welcome's agent object (protocol 3). Name (the
// agent's POSIX user) and Capabilities are always present; the other
// fields are omitted by the daemon when the operator hasn't set them.
// AccentColor is always "#RRGGBB" when set.
type AgentProfile struct {
	Name            string      `json:"name"`
	DisplayName     string      `json:"display_name,omitempty"`
	Tagline         string      `json:"tagline,omitempty"`
	AccentColor     string      `json:"accent_color,omitempty"`
	Avatar          *AvatarInfo `json:"avatar,omitempty"`
	SycamoreVersion string      `json:"sycamore_version,omitempty"`
	Capabilities    []string    `json:"capabilities"`

	// Attachments is present exactly when Capabilities lists "attachments".
	Attachments *AttachmentLimits `json:"attachments,omitempty"`
}

// AttachmentLimits is the agent profile's attachments object. MaxBytes
// is the largest file the agent can attach.
type AttachmentLimits struct {
	MaxBytes int `json:"max_bytes"`
}

// Attachment describes a file the agent attached to a turn with its
// chat_attach tool. The bytes are fetched separately by ID. MIME comes
// from the daemon's table by extension: use it as given, never sniff.
type Attachment struct {
	ID        string `json:"id"`
	Filename  string `json:"filename"`
	MIME      string `json:"mime"`
	SizeBytes int    `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	ExpiresAt string `json:"expires_at"`
	Expired   bool   `json:"expired"`
}

// AvatarInfo describes the agent's avatar without carrying it. SHA256
// is the lowercase hex digest of the image bytes; compare it with a
// cached copy before sending an "avatar" request.
type AvatarInfo struct {
	SHA256 string `json:"sha256"`
	MIME   string `json:"mime"` // image/png | image/jpeg | image/webp
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
}

// Response is the agent's reply to a chat turn. Content is the full
// turn (all tool round-trips inclusive) — there is no streaming. A
// nil StopReason means the turn ended cleanly without an explicit
// stop signal.
type Response struct {
	Type           string         `json:"type"`
	ConversationID string         `json:"conversation_id"`
	TurnID         string         `json:"turn_id"`
	Content        []ContentBlock `json:"content"`
	StopReason     *string        `json:"stop_reason"`
	Model          string         `json:"model"`
	Provider       string         `json:"provider"`
	Usage          Usage          `json:"usage"`
	ElapsedMS      int            `json:"elapsed_ms"`
	Attachments    []Attachment   `json:"attachments,omitempty"` // capability "attachments"
}

// Error is a daemon-side failure. There are no error codes — match on
// Message substrings if you need to branch (see daemon cookbook §8).
type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// Pong is the reply to a client ping. Useful as a liveness probe
// during long-running chat turns.
type Pong struct {
	Type string `json:"type"`
}

// NewConversation acknowledges a "new" message with the fresh
// conversation's id. A conversation created implicitly by a first chat
// is not announced this way; its id arrives only on the [Response].
type NewConversation struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
}

// Finalizing precedes a drain of a conversation's unconsolidated tail
// into memory — after a "new" (closing the prior conversation out) or a
// "park" (leaving it open). It arrives only when there is something to
// drain, and the drain itself is a multi-second LLM call, so it exists
// to let a client say what the pause is for rather than look hung.
type Finalizing struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
}

// CaptureStatus is the reply to every capture action. Directory is an
// absolute path on the daemon host, not the client host.
type CaptureStatus struct {
	Type      string `json:"type"`
	Enabled   bool   `json:"enabled"`
	FileCount int    `json:"file_count"`
	Directory string `json:"directory"`
}

// ModelsList is the reply to a "models" message. Current is nil when
// no model has been pinned to the active conversation.
type ModelsList struct {
	Type    string  `json:"type"`
	Aliases []Alias `json:"aliases"`
	Current *string `json:"current"`
}

// Alias is one entry in [ModelsList.Aliases].
type Alias struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

// ModelSet is the reply to a successful "set_model".
type ModelSet struct {
	Type     string `json:"type"`
	Model    string `json:"model"`
	Resolved string `json:"resolved"`
}

// ModelShow is the reply to "show_model". All three optional fields
// are nil when there is no active conversation.
type ModelShow struct {
	Type           string  `json:"type"`
	ConversationID *string `json:"conversation_id"`
	Alias          *string `json:"alias"`
	Resolved       *string `json:"resolved"`
}

// InfoResponse is the reply to an "info" message — a snapshot of the
// active conversation's bookkeeping state.
type InfoResponse struct {
	Type                   string  `json:"type"`
	ConversationID         *string `json:"conversation_id"`
	ConversationTitle      *string `json:"conversation_title"`
	ModelAlias             *string `json:"model_alias"`
	ModelResolved          *string `json:"model_resolved"`
	TurnCount              int     `json:"turn_count"`
	EstimatedTokens        int     `json:"estimated_tokens"`
	ConsolidationWatermark int     `json:"consolidation_watermark"`
	ConsolidationThreshold int     `json:"consolidation_threshold"`
	Locale                 string  `json:"locale"`
	LocaleSource           string  `json:"locale_source"` // "conversation" | "global" | "host" | "fallback"
}

// DreamResult is the reply to a "dream" message.
type DreamResult struct {
	Type                 string   `json:"type"`
	Dreamed              bool     `json:"dreamed"`
	FilesProcessed       int      `json:"files_processed"`
	LongTermUpdated      bool     `json:"long_term_updated"`
	CoreUpdated          bool     `json:"core_updated"`
	BioUpdates           []string `json:"bio_updates"`
	HeadlinesRegenerated []string `json:"headlines_regenerated"`
	DurationMS           int      `json:"duration_ms"`
}

// ConversationParked is the reply to a "park" message: the
// conversation's unconsolidated tail has been folded into short-term
// memory and the conversation left open (still active, still listed).
//
// Parked distinguishes a drain from a no-op — false means there was
// nothing above the consolidation watermark, which is a success, not a
// failure. A drain that was attempted and failed arrives as an Error
// instead, never as Parked: false; callers must keep the two apart,
// because "nothing to do" must not stand in for "the fold failed".
type ConversationParked struct {
	Type              string  `json:"type"`
	ConversationID    string  `json:"conversation_id"`
	ConversationTitle *string `json:"conversation_title"`
	Parked            bool    `json:"parked"`
	TurnsParked       int     `json:"turns_parked"`
}

// ConversationRenamed is emitted by the daemon's auto-titler, or in
// response to a manual "rename". Source distinguishes the two
// ("auto" | "manual"). It can arrive interleaved with a Response —
// readers should keep reading after it, not return.
type ConversationRenamed struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	Source         string `json:"source"`
}

// HistoryMessage is one record in a [HistoryPage] — a display
// projection of one side of a turn. Role is [RoleUser] or
// [RoleAssistant]. Content is the same flat ContentBlock shape as
// [Response.Content] (raw recorded content, not the daemon's compressed
// summaries), so a renderer handles live and historic turns identically.
//
// One turn expands to up to two records — a user message then the
// assistant reply — that share a Sequence. A non-human-triggered turn
// (cron / event) contributes only an assistant record. Model and
// Provider are present on assistant records only.
type HistoryMessage struct {
	Role      string         `json:"role"`
	Content   []ContentBlock `json:"content"`
	Sequence  int            `json:"sequence"`
	TurnID    string         `json:"turn_id"`
	CreatedAt string         `json:"created_at"`
	Model     string         `json:"model,omitempty"`
	Provider  string         `json:"provider,omitempty"`

	// Attachments is set on assistant records whose turn attached files,
	// with Expired brought up to date (capability "attachments").
	Attachments []Attachment `json:"attachments,omitempty"`
}

// HistoryPage is the reply to a "history" message (protocol v2): one
// page of a conversation's messages, oldest-first within the page, for
// seeding a transcript and scrolling back.
//
// Paginate backwards by sending OldestSequence as the next request's
// BeforeSequence; HasMore reports whether older turns exist before this
// page. OldestSequence is nil only for an empty page.
type HistoryPage struct {
	Type           string           `json:"type"`
	ConversationID string           `json:"conversation_id"`
	Messages       []HistoryMessage `json:"messages"`
	HasMore        bool             `json:"has_more"`
	OldestSequence *int             `json:"oldest_sequence"`
}

// ConversationSummary is one row in a [ConversationsList]. Title is nil
// for a not-yet-titled conversation. TurnCount counts completed turns
// only. UpdatedAt (ISO-8601) is the last-activity time and the sort key.
type ConversationSummary struct {
	ID         string  `json:"id"`
	Title      *string `json:"title"`
	UpdatedAt  string  `json:"updated_at"`
	ModelAlias string  `json:"model_alias"`
	TurnCount  int     `json:"turn_count"`
}

// ConversationsList is the reply to a "conversations" message (protocol
// v2): the caller's conversations, newest-first by UpdatedAt, scoped to
// the caller's contact (and spanning every surface that created one —
// socket, WhatsApp, …). HasMore reports whether older conversations
// exist before this page; page on via [ClientMessage.BeforeUpdatedAt].
type ConversationsList struct {
	Type          string                `json:"type"`
	Conversations []ConversationSummary `json:"conversations"`
	HasMore       bool                  `json:"has_more"`
}

// Progress is one step of a running chat turn, sent only when the chat
// set Progress, and always before that turn's [Response]. Kind is one of
// the Progress* constants and selects which fields are set: Text for
// assistant_text; ToolUseID, ToolName and InputSummary for tool_start;
// ToolUseID, ToolName, IsError and ElapsedMS for tool_end. Round is the
// 1-based model call within the turn.
type Progress struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	TurnID         string `json:"turn_id"`
	Round          int    `json:"round"`
	Kind           string `json:"kind"`

	Text string `json:"text,omitempty"` // assistant_text, clipped to 2000 chars

	ToolUseID    string `json:"tool_use_id,omitempty"`   // tool_start, tool_end
	ToolName     string `json:"tool_name,omitempty"`     // tool_start, tool_end
	InputSummary string `json:"input_summary,omitempty"` // tool_start, compact JSON clipped to 200 chars
	IsError      bool   `json:"is_error,omitempty"`      // tool_end
	ElapsedMS    int    `json:"elapsed_ms,omitempty"`    // tool_end
}

// AvatarImage is the reply to an "avatar" message (protocol 3). Avatar
// is nil when no avatar is configured, which is not an error.
type AvatarImage struct {
	Type   string      `json:"type"`
	Avatar *AvatarData `json:"avatar"`
}

// AvatarData carries the avatar image. Data is base64 with no data:
// prefix; SHA256 is the digest of the decoded bytes and matches
// [AvatarInfo.SHA256] from the same daemon run.
type AvatarData struct {
	SHA256 string `json:"sha256"`
	MIME   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Data   string `json:"data"`
}

// Undecodable is not a wire message: the client delivers it in place of a
// message of a known type it couldn't decode, so the failure is shown rather
// than dropped. Kind is the message's type; Err says what went wrong.
// (Unknown types are skipped silently, per the protocol's
// forward-compatibility rule.)
type Undecodable struct {
	Kind string
	Err  string
}

// EndsTurn reports whether the message that failed to decode would have
// ended a chat turn, so a UI doesn't wait on it for ever.
func (u *Undecodable) EndsTurn() bool {
	return u.Kind == TypeResponse || u.Kind == TypeError
}

// ServerMessage is the sealed interface implemented by every typed
// server message. Dispatch via a type switch on the concrete pointer
// type:
//
//	switch v := msg.(type) {
//	case *protocol.Welcome:
//	    // ...
//	case *protocol.Response:
//	    // ...
//	}
//
// The marker method is unexported, so the set of implementations is
// closed to this package — no external type can be smuggled onto a
// channel of ServerMessage.
//
// Representation choice (deferred sub-question from decision #3,
// resolved 2026-05-22): sealed interface beats a flat tagged-union
// struct here because the 15 server-message shapes diverge sharply.
// The custom-(un)marshal cost that argued against sealed interfaces
// for ContentBlock doesn't apply: the type discriminator is already
// extracted by the low-level Recv, so dispatch happens in Go code,
// not in JSON unmarshalling.
type ServerMessage interface {
	isServerMessage()
}

func (*Welcome) isServerMessage()             {}
func (*Response) isServerMessage()            {}
func (*Error) isServerMessage()               {}
func (*Pong) isServerMessage()                {}
func (*NewConversation) isServerMessage()     {}
func (*Finalizing) isServerMessage()          {}
func (*CaptureStatus) isServerMessage()       {}
func (*ModelsList) isServerMessage()          {}
func (*ModelSet) isServerMessage()            {}
func (*ModelShow) isServerMessage()           {}
func (*InfoResponse) isServerMessage()        {}
func (*DreamResult) isServerMessage()         {}
func (*ConversationRenamed) isServerMessage() {}
func (*ConversationParked) isServerMessage()  {}
func (*HistoryPage) isServerMessage()         {}
func (*ConversationsList) isServerMessage()   {}
func (*Progress) isServerMessage()            {}
func (*AvatarImage) isServerMessage()         {}
func (*Undecodable) isServerMessage()         {}

// ConversationOf returns the conversation a server message belongs to, for
// the kinds that name one (response, progress, history_page,
// conversation_renamed, conversation_parked, finalizing).
func ConversationOf(message ServerMessage) (string, bool) {
	id, carries := "", true
	switch typed := message.(type) {
	case *Response:
		id = typed.ConversationID
	case *Progress:
		id = typed.ConversationID
	case *HistoryPage:
		id = typed.ConversationID
	case *ConversationRenamed:
		id = typed.ConversationID
	case *ConversationParked:
		id = typed.ConversationID
	case *Finalizing:
		id = typed.ConversationID
	default:
		carries = false
	}
	return id, carries && id != ""
}
