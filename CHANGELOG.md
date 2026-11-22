# Changelog

All notable changes to sycamore-client will be recorded here.

The format is loosely based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.1] - 2026-11-22

The TUI's direct connection to an agent on the same host, and an install
runbook. **The direct (no-SSH) socket connection is built and unit-tested, but
not yet validated against a live agent.** Connections over SSH are unchanged.

### Added

- **`sycamore-tui <socket-path>`** is shorthand for `--socket <socket-path>`,
  for an agent on the same host.
- **`docs/install.md`**: a runbook from `git clone` to a connected TUI, over
  SSH or on the agent's host.

### Fixed

- **The TUI no longer ignores stray arguments.** A positional argument used
  to be dropped silently, leaving the TUI on the `default` agent. More than
  one, a path given both ways, or a socket path with `--instance` or `--probe`
  is now an error listing every problem.
- **`--socket` sessions apply the config's `[ui]` settings**
  (`markdown_style`, `mouse_capture`) instead of the defaults.

## [1.0.0] - 2026-11-19

The first stable release, and the first from the public repository: the TUI
and the Linux GUI at parity, with the fixes from a pre-publication security
review. Live-checked in tmux against Zee and Coop: connect, switching agents,
`--probe`, reconnect after a dropped tunnel, and clean tunnel shutdown. The GUI
was run and used, but its new behaviour (blocked remote images, web-only links,
per-agent drafts) is covered by tests rather than a full live check.

### Security

- **The TUI strips terminal control characters from everything the agent
  sends** (replies, titles, names, tool details, errors), so a reply can't
  move the cursor, rewrite the screen, set the clipboard or plant links.
  Newlines and tabs are kept.
- **Tunnel sockets stay private without `$XDG_RUNTIME_DIR`.** They now go in a
  per-user `0700` directory under the temp dir (`sycamore-<uid>`), which is
  refused if it's a symlink, someone else's or open to others. The start-up
  clean-up of stray tunnels looks only there.
- **SSH arguments are hardened.** The destination follows `--`; a host or user
  starting with `-` is a config error; the remote socket path is quoted for
  the far side's shell (paths with spaces now probe correctly); instance names
  are made filename-safe for socket names.
- **GUI avatars:** the digest in a welcome must be a SHA-256 before it names a
  cache file, and a cached avatar is read only if it's a regular file within
  the size limit.
- **The GUI loads no remote content.** A Content-Security-Policy lets images
  come only from the app or `data:` URIs, so a Markdown image in a reply makes
  no network request. Links open in the browser only if they're `http` or
  `https`.
- **Go 1.26.11 is pinned** (`toolchain` in `go.work` and every module),
  clearing the standard-library advisories, and Go 1.26 is now the minimum.
  `golang.org/x/text`, `x/net`, `x/sys`, `x/term` and `x/crypto` are updated,
  and Wails to v2.13.0. `govulncheck` reports nothing for any module.
- The GUI frontend's build tools move to Vite 8, Svelte 5 (components
  unchanged, in legacy mode) and TypeScript 6, clearing every `npm audit`
  advisory. All were dev-only.

### Fixed

- **A GUI chat goes to the conversation it's shown in**, and each agent keeps
  its own unsent draft, so switching agents (including with the unread
  shortcut) can't send a draft to the wrong one.
- **A silent or slow agent can't hang a client.** A connection with no welcome
  within 20s is dropped and redialled, and so is one whose events back up past
  10,000. Each write to the socket times out after 30s.
- `make tui-release` rebuilds every binary from the current source instead of
  keeping one already in `bin/`, and the pure-Go `lipo` is pinned to v0.10.0.
- A fresh clone has the `gui/frontend/dist` placeholder the GUI's embed needs,
  and `.claude/settings.local.json` is ignored by the repository itself.

## [0.15.0] - 2026-10-20

Everyday polish for the TUI on the way to 1.0.0: input history, `/edit`,
emoji shortcodes, and the files an agent attaches to a reply. Both clients now
shut their SSH tunnels down cleanly however they're stopped. Not 1.0.0 yet: the
final live check of both clients comes first.

### Added

- **"+ New conversation" in the TUI switcher.** The first row of every agent's
  conversations starts a new one there (as `/new` does), so a brand-new agent,
  with nothing to switch to yet, can be opened from the switcher.
- **Input history (TUI and GUI).** ↑ from the first line of the input recalls
  what you sent to the agent on screen; ↓ from the last line goes forward
  again, back to your draft. Each agent keeps its own.
- **`/edit [text]` (TUI).** Compose a message in your editor; what you save
  lands in the input to review and send. The editor is the new `[ui] editor`,
  else `$VISUAL`, else `$EDITOR`, else `vi`.
- **Emoji shortcodes (TUI).** `:thumbsup:` becomes 👍 as you type the closing
  colon (GitHub's codes). A partly typed code with few matches opens a list
  above the input to pick from (Tab or Enter); the most it lists is the new
  `[ui] emoji_choices` (default 5, `0` turns it off). Code spans, fenced
  blocks and pasted text are left alone.
- **Attachments the agent sends (TUI).** A reply lists each file the agent
  attached (name, type and size), dimmed and marked "expired" once the agent
  has deleted it. Live replies and history both show them. Details only; no
  download yet. Needs Sycamore 0.69.0+ with attachments turned on.

### Changed

- The TUI hides its input cursor while its terminal or tmux pane is
  unfocused, so only the focused pane shows one (tmux needs
  `focus-events on`).
- A message from the agent that the client can't read is now shown ("couldn't
  read a response message from the agent: …") rather than silently dropped,
  and ends the turn if it was a reply.
- A config file with several mistakes reports all of them at once.
- The GUI returns to the conversation it was showing when an agent closed for
  being idle reconnects, as the TUI does.

### Fixed

- **Tunnels left running after a client exits.** A client stopped by a signal
  (ctrl+c where the GUI was started, a closed terminal, tmux pane or SSH
  session) exited without closing its tunnels, so their background `ssh`
  masters lived on. Both clients now quit cleanly on SIGINT, SIGTERM and
  SIGHUP, and at start-up both stop any `sycamore-*` tunnel whose client
  process no longer exists (catching crashes too).
- The GUI header showed "agent" instead of the agent's name.
- Docs cut to the README, `docs/configuration.md` and `docs/tui-cli.md`, all
  checked against the code; the quick start is folded into the README.
- **Esc no longer quits the TUI** (it still closes the switcher and pickers),
  and **ctrl+c clears a draft first**, quitting only on an empty input.

## [0.14.0] - 2026-09-27

The desktop GUI catches up with the TUI. Until now it could hold one
conversation with one agent; it now does everything 0.13.0's TUI does, in a
window: many agents at once, each in the background while you work with
another, Markdown replies, live tool progress, history and conversation
switching, and the TUI's slash commands. Linux only for now (see the 1.0.0
plan in `dev-notes/00-project-tracker.md`).

### Added

- **Several agents at once (GUI).** A sidebar lists every configured agent,
  grouped by team, with its avatar (or initial on its accent colour), name and
  state: idle, working (with the tool it's running), a reply or an error
  waiting, disconnected, connecting or failed, plus an unread count. Click to
  switch; the agent you leave stays connected and keeps working. × disconnects
  one. **ctrl+r** jumps to the next reply waiting.
- **Markdown replies (GUI):** tables, lists, code, quotes and links, rendered
  safely (raw HTML and `javascript:` links are dropped). Links open in your
  browser.
- **Live progress (GUI):** the agent's narration and a row per tool (`✓` / `✗`
  with its time) while a reply is worked on.
- **History and conversations (GUI):** the current conversation opens with its
  recent history, and scrolling up loads older messages. The conversation title
  in the header opens a list of your conversations to switch between. Switching
  works while a reply is still coming: it lands in its own conversation, marked
  unread.
- **Slash commands (GUI):** `/model`, `/models`, `/new`, `/info`, `/rename`,
  `/park`, `/dream`, `/capture`, `/ping`, `/list`, `/switch`, `/connect`,
  `/close` and `/help`, with replies worded as in the TUI. The model in the
  header opens a model picker.
- **Avatars:** fetched only when an agent's avatar has changed, checked (digest,
  format, size) and cached under `~/.cache/sycamore-client/avatars/`.
- The GUI reconnects on its own after a drop, using the same `core.Pool` as
  the TUI, and returns to the conversation you were in.
- `make gui` builds `bin/sycamore-gui-linux-amd64`.
- `core`: `core.FoldProgress` and `protocol.ConversationOf`, shared by both
  clients.

### Changed

- The TUI switcher calls an agent the client isn't connected to
  "disconnected" rather than "offline", since the agent itself is usually up.
- goldmark (the Markdown library) is now a direct GUI dependency at v1.8.6;
  the TUI's indirect copy, via glamour, moves up with it.

## [0.13.0] - 2026-09-27

Many agents in one TUI. Until now the client talked to one agent at a time,
and `/connect` replaced it. Now every agent you open stays connected, each over
its own SSH tunnel, and keeps working while you're elsewhere: start a long job
on one, switch to another and chat there, and the first one's reply is waiting
when you come back. The plan behind it is in
`dev-notes/planning-multi-agent.md`.

### Added

- **Several agents at once (TUI).** `/connect <name>` switches to an agent,
  connecting it if needed; the agent you leave stays connected in the
  background. New `/close [name]` disconnects one. Each agent works on one
  reply at a time; a message to a busy agent stays in the input box.
- **The switcher.** `/switch` with no argument opens your agents above the
  highlighted agent's conversations. Tab moves between them, the filter applies
  to whichever has focus, and Enter opens a conversation on any agent.
  Highlighting an agent that isn't connected never connects it; Enter does.
- **Attention.** Each agent's row shows whether it's idle, working (with the
  tool it's running), has a reply or an error waiting, or is offline,
  connecting or failed. The footer counts what's waiting elsewhere (`1 working
  · 2 unread`), and **ctrl+r** jumps to the next reply.
- **Replies to conversations you've left** are kept in that conversation and
  marked unread, so `/switch` works while a reply is still coming. Each agent
  keeps its recently viewed conversations ready (`[ui] conversation_cache`,
  default 8), so switching back is instant.
- `/switch <agent>:<text>` switches another agent's conversation.
- The header and the agent's transcript label use the agent's own display
  name, in its accent colour.
- Config: `[connections] idle_timeout` (default `"20m"`) closes an unused
  background agent's connection; `[connections] open_on_start` connects agents
  in the background at start-up; `team` on an `[instance.*]` groups agents in
  the switcher. See `docs/configuration.md`.
- `core.Pool`: one connection per agent, opened on demand or adopted,
  reconnected with backoff, retired when idle, with every agent's messages and
  state changes on one ordered channel. The GUI will move onto it next.

### Changed

- Reconnecting moved from the TUI into `core.Pool`, with the same policy as
  before (backoff from 1s to 30s, reset after 5s of a stable connection). It no
  longer retries when SSH rejects your key or the host key doesn't match; it
  stops and says so, and `/connect` tries again.
- `/switch` opens the switcher at once rather than waiting for the list.

## [0.12.0] - 2026-09-26

Live progress in the TUI, and chat-socket protocol 3 in `core`. A multi-tool
turn used to show nothing but `thinking…` until the whole reply landed. Now you
can watch the agent work: what it says it's about to do, each tool it runs, and
whether each one succeeded. This is also the first step of the multi-agent work
(`dev-notes/planning-multi-agent.md`).

### Added

- **Live progress (TUI).** While a reply is being worked on, the transcript
  shows the agent's narration and a line per tool call (`↳ shell_exec {…}`),
  which ends in `✓` or `✗` with the tool's run time once it finishes. The footer
  names the running tool and round. When the reply arrives the live lines give
  way to it, since the reply carries the same content. Needs an agent that
  advertises `progress` (Sycamore v0.51.0 or later); older agents still show
  `thinking…`.
- `core/protocol`: protocol 3. The agent profile in `welcome` (`AgentProfile`,
  with name, display name, tagline, accent colour, avatar description, Sycamore
  version and capabilities), the `progress` flag on `chat` and the `progress`
  server message, the `avatar` request and its `avatar_image` reply, and
  constants for the capability names.
- `core.Session` carries the agent profile, and `Session.Has(capability)`
  reports whether the agent supports a feature. A protocol-2 daemon, which sends
  no profile, counts as supporting `conversations` and `history` only.

### Changed

- `/park` needs the agent to advertise the `park` capability, and says so
  instead of sending otherwise. A Sycamore v0.49 or v0.50 daemon had `/park`
  but no capability list, so it's now refused, as the protocol spec asks.
- The TUI's history and conversation-list features are gated on capabilities
  rather than on the protocol version. No change in behaviour against any
  released daemon.
- The footer drops its key hints when the status doesn't fit, rather than
  running past the edge of a narrow pane.

### Fixed

- `core.Client` no longer lets a late reply move the session back to its
  conversation. A reply now sets the session's conversation only when there
  isn't one yet (the first chat creating it). This couldn't be triggered in the
  TUI yet, but switching away while a turn runs is part of the multi-agent work.

## [0.11.0] - 2026-07-26

`/park` in the TUI. Sycamore grew a command for setting a conversation aside
without closing it: it folds the thread's unconsolidated tail into the agent's
memory and leaves the conversation open, ready to pick up later. It's the thing
to reach for at the end of a working session on a thread you're not finished
with — `/new` is for moving on, `/park` is for coming back.

### Added

- **`/park` (TUI)** — folds the current conversation into the agent's memory and
  leaves it open; the thread stays active and listed, and a later message
  resumes it normally. The trade is the live tail: parked turns reach the agent
  through memory rather than through conversation history. Parking is
  idempotent, so a second `/park` is a no-op.

  The TUI keeps the three outcomes distinct, which the protocol is emphatic
  about: a fold that happened ("parked X: N turns folded into memory · still
  open"), a successful no-op with nothing above the consolidation watermark ("X
  was already consolidated — nothing to park"), and a fold that *failed*, which
  arrives as an error and never as a no-op. Telling you "nothing to do" when the
  fold actually failed is the one misreport worth designing against — the reason
  to park is to be told the content is safe.

- `core/protocol`: `park` client message and the `conversation_parked` reply.
  `core.Client.Send` populates `conversation_id` on a park from the session, so a
  park after `/switch` targets the conversation you're looking at — the daemon's
  fallback is "most recently updated", which is the wrong thread once a client
  offers a switcher.

### Changed

- The `finalizing` notice now reads *"folding conversation into memory…"* rather
  than *"wrapping up the previous conversation…"*. It precedes both `/new` and
  `/park`, and only the former has a "previous" conversation.

## [0.10.0] - 2026-06-29

A model picker for the TUI. Switching models no longer means remembering alias
names — ctrl+y opens the same modal picker used for conversations and instances,
with the current model marked.

### Added

- **Model picker (ctrl+y, TUI)** — requests the model list and opens a filterable
  picker overlay; selecting an alias sends `set_model`. Each row shows the alias
  with its resolved target, and the active model is marked `(current)`. The typed
  `/models` path still dumps the list into the transcript — one models request,
  two front doors, dispatched on which one asked.

## [0.9.0] - 2026-06-27

The desktop GUI can hold a conversation. The Wails client crosses from a
bootstrap splash to a working chat: it connects to your default instance on
launch, streams the transcript, and sends messages — the same round-trip the
TUI got in its early days, over the same shared `core` library. It's the first
real chat surface of the GUI build-out toward the both-clients 1.0.0.

### Added

- **GUI chat round-trip (build-out slice 1)** — the `sycamore-gui` desktop
  client now connects on launch, shows a status bar (instance · principal ·
  conversation · connection state), streams agent replies into a scrolling
  transcript, and sends a chat from the input box. Built on a Go→JS event
  bridge: the Go side runs the `core` read loop and pushes view-model messages
  to the Svelte frontend via Wails runtime events; the frontend is a thin
  renderer with no protocol knowledge. No changes to `core` — the GUI reuses the
  exact connection/session API the TUI does.

  Still to come in later slices: Markdown rendering (replies are plain text for
  now), conversation history and switching, instance switch, and auto-reconnect.

## [0.8.0] - 2026-06-27

Auto-reconnect. The TUI now rides out a dropped connection instead of exiting:
if the agent restarts or the SSH tunnel blips, it reconnects on its own, backing
off between attempts, and drops you straight back into the conversation you were
in. This completes the TUI's connection handling — it's now feature-complete.

### Added

- **Auto-reconnect (M9, TUI)** — when the connection drops unexpectedly the TUI
  reconnects automatically with a capped exponential backoff (1s → 2s → 4s → … →
  30s, with jitter), retrying until it's back or you quit. The footer shows
  `reconnecting… (attempt N)`. On success it **resumes the same conversation** you
  were in — re-pinning it even if the agent's welcome would have picked a
  different one — and reseeds its history. An in-flight turn interrupted by the
  drop is flagged ("may not have completed"); a failed send recovers the same way
  rather than ending the session. While reconnecting, chat drafts are kept (never
  lost) and `ctrl+c` / `/connect` / `/help` still work; `/connect <name>` cancels
  the loop and redirects to another instance.

### Changed

- **Removed the dormant `shift+enter` newline binding** — it never fired on the
  current bubbletea release. `ctrl+j` and a trailing `\` before Enter remain the
  ways to insert a soft newline.

### Fixed

- **Tunnel socket-path collision on reconnect** — the SSH tunnel named its local
  and control sockets after the instance alone, so reconnecting to the *same*
  instance reused the dropped tunnel's paths and tore down the new connection in
  a tight loop. Each tunnel now gets a unique path. (Found in the first live
  test; the symptom was a rapid reconnect storm against a just-restarted agent.)
- **Reconnect backoff no longer resets on a flapping connection** — a connection
  that reconnects then immediately drops again keeps backing off (up to the 30s
  cap) instead of hammering at the 1-second floor; the backoff only resets once a
  connection has stayed up for a few seconds.

## [0.7.0] - 2026-06-23

Desktop GUI groundwork. The second client — `sycamore-gui` — is now a real Wails
application instead of a stub, sharing the same `core` library as the TUI. It's a
scaffold, not yet a usable chat client: the window comes up and the Go↔frontend
binding works, which is the foundation the GUI's chat surfaces will be built on.

### Added

- **Desktop GUI scaffold (M7)** — bootstrapped the Wails desktop client with a
  **Svelte + TypeScript** frontend, sharing the `core` library with the TUI. The
  window builds and renders a splash that reads the core version over the Go↔JS
  binding, proving the chain end-to-end. This is groundwork only — the chat
  surfaces grow on it next. Build with `wails build -tags webkit2_41`.

## [0.6.0] - 2026-06-23

Reconnect without restarting. You can now switch which Sycamore instance the
running TUI is talking to with a single command — handy when you keep more than
one agent (e.g. a personal one and a work one) and want to hop between them in
the same session. Verified live against two instances.

### Added

- **User-initiated reconnect (TUI)** — `/connect <name>` connects to another
  configured instance in place, tearing down the previous SSH tunnel and seeding
  the new conversation's history. Bare `/connect` opens a type-to-filter instance
  picker. The current connection stays live until the new one is established, so a
  failed dial (bad name, unreachable host) leaves you exactly where you were with
  an explanatory note rather than stranding you disconnected. A "connecting…"
  indicator shows in the footer while the tunnel comes up.

### Changed

- The TUI now drives its connection through an interface, so a direct local
  socket (`--socket`) and an SSH-tunnelled instance (`--instance`) reconnect
  through the same path. Reads are tagged by connection generation, so a
  superseded connection closing during a reconnect no longer ends the session.

## [0.5.0] - 2026-06-22

Markdown rendering in the terminal. Agent replies that come back as Markdown —
tables, lists, code blocks, inline emphasis — now render as formatted terminal
text instead of raw source, with a configurable theme. Plus mouse-capture
control so you can select and copy text from the transcript.

### Added

- **Markdown rendering (TUI)** — agent reply text now renders through
  [`glamour`](https://github.com/charmbracelet/glamour): tables get aligned
  borders, lists get bullets, code blocks get syntax tinting. The change lands in
  the shared content-block renderer, so both live replies and backfilled history
  are formatted. Rendering re-wraps on resize and is cached per width (so a new
  message doesn't re-render the whole transcript); a render error falls back to
  the previous plain-wrap path so text is never lost.
- **Configurable Markdown theme** — `[ui] markdown_style` selects the glamour
  theme (`dark` default; also `light`, `dracula`, `tokyo-night`, `pink`, `ascii`,
  `notty`). Validated against glamour's own style set, so a typo is reported at
  startup with the valid choices.
- **Mouse-capture control** — the TUI captures the mouse for wheel-scroll, which
  normally blocks the terminal's text selection. You can now toggle capture at
  runtime with **`ctrl+o`** (the footer shows `mouse:on` / `mouse:off`), set the
  startup state with **`[ui] mouse_capture`** (default `true`), or just hold
  **Shift while dragging** to select without changing anything. Keyboard
  scrolling (PgUp/PgDn, arrows) is unaffected.

## [0.4.0] - 2026-06-21

Conversation history and switching, built on the daemon's new **chat-socket
protocol v2** (daemon v0.42.0): the TUI now opens onto a conversation's recent
history instead of an empty pane, lists and switches between conversations
(including ones started on other surfaces, e.g. WhatsApp), and accepts multi-line
input. Validated end-to-end against a live v0.42.0 instance.

### Added

- **Protocol v2 bindings (`core/protocol`)** — `history` / `conversations` client
  messages and their `history_page` / `conversations_list` replies (with nested
  record types; history content reuses the existing `ContentBlock` shape).
  `protocol_version` added to `welcome`; `core.Client` exposes it via
  `Session.ProtocolVersion` (an older daemon that omits it is treated as v1), so
  v2 features are gated and degrade gracefully against a pre-v2 daemon.
- **History backfill (TUI)** — on connect, a resumed conversation is seeded with
  its recent turns (request/reply pagination, 50 turns/page); scrolling to the top
  pages older turns in, preserving the read position. Live and historic turns share
  one renderer.
- **Conversation list & switch (TUI)** — `/list` (alias `/conversations`) prints
  your conversations; `/switch` opens a type-to-filter picker overlay, and
  `/switch <n|text>` switches directly by list index or title substring. Switching
  re-points the session (`core.Client.SetConversation`), resets the view, and
  re-seeds history — same socket, no reconnect.
- **Reusable picker widget** — a generic, windowed, type-to-filter modal list
  (`tui/picker.go`) backing the conversation switcher; ready to also serve instance
  and model selection later.
- **Multi-line input** — the prompt is now a `bubbles/textarea` that grows with its
  content (and brings readline-style editing). Insert a soft newline with **Ctrl+J**
  or by ending a line with a trailing **`\`**; Enter still sends. (Shift+Enter is
  wired but inert on the current Bubble Tea — it doesn't decode a distinct code; it
  will activate on a future upgrade.)

### Changed

- The transcript input grew from a single-line field to a variable-height textarea;
  the layout recomputes the viewport height as the input grows. `↑`/`↓` now move the
  input cursor, while `PgUp`/`PgDn` and the mouse wheel scroll the transcript.

## [0.3.0] - 2026-06-20

Sprint 2 — the client now connects to a real agent over SSH and is usable
end-to-end. Validated against a live production instance: config-driven
connect over the tunnel, chat, and slash commands all working.

### Added

- **SSH tunnel (`core/tunnel`)** — connects by shelling out to the host's OpenSSH (decision #5), so `~/.ssh/config`, keys, agent, `known_hosts`, and `Host` aliases all apply. A ControlMaster underpins the tunnel: `Open` builds `ssh -M -S <ctl> -fNT -o ExitOnForwardFailure=yes … -L <local>:<remote> <dest>` and relies on `-f`+`ExitOnForwardFailure` for readiness (no polling); `Alive` (`-O check`), `RemoteSocketExists` (`test -S` over the master), and idempotent `Close` (`-O exit` + cleanup). Local sockets live under `$XDG_RUNTIME_DIR` with a `sun_path` length cap; ssh stderr is mapped to operator-facing errors (auth / DNS / refused / timeout / host-key / bind).
- **Config file (`core/config`)** — TOML at `$XDG_CONFIG_HOME/sycamore-client/config.toml` (via `github.com/BurntSushi/toml`). `[instance.<name>]` tables (host / user / port / identity / socket) plus a top-level `default`. `Load` / `LoadDefault` / `DefaultPath` / `Resolve`; unknown keys are rejected to catch typos; local `identity` paths are tilde-expanded; an `ExampleTOML` for `--init`.
- **Connection orchestration (`core`)** — `DialInstance` opens the tunnel and connects a `Client` over it (tearing the tunnel down on any setup failure), and the staged `ProbeInstance` runs SSH → Socket → Connect, classifying the Connect outcome as a clean welcome, an EACCES-style close, or a daemon principal-rejection.
- **TUI rebuilt as a full-screen app** — `lipgloss`-styled status header (instance · principal · model · title), a scrollable `bubbles/viewport` transcript, a `bubbles/textinput` prompt, and a footer with key hints and a `bubbles/spinner` "thinking…" indicator. Config-driven connect via `core.DialInstance`.
- **Slash commands** — `/model [alias]`, `/models`, `/new`, `/info`, `/rename [title]`, `/dream`, `/capture <on|off|flush|status>`, `/ping`, plus client-side `/help` and `/quit` (alias `/exit`), with styled rendering of each reply kind.
- **CLI flags** — `--instance`, `--config`, `--socket` (direct local bypass), `--probe` (staged connection test), `--init` (scaffold a config), `--version`, `--help`. GNU-style `--` presentation to match the daemon's CLIs (one or two dashes both accepted).
- **Documentation** — a `docs/` directory: [quick start](./docs/quick-start.md), [configuration](./docs/configuration.md), and the [command-line reference](./docs/tui-cli.md). README slimmed to a summary plus links.
- **Planning docs** — `dev-notes/planning-ssh-tunnel.md` (the SSH design) and decisions #5 (SSH transport) and #7 (capture paths shown verbatim) settled in `planning-core-design-decisions.md`.

### Changed

- The terminal binary is now **`sycamore-tui`** (was `sycamore`), to avoid colliding with the daemon's own `sycamore` CLI; complements `sycamore-gui`.

## [0.2.0] - 2026-05-22

First functional release — Sprint 1 complete. The client can now connect to a daemon, send chat messages, and render every server-message kind defined in today's protocol.

### Added

- **Wire protocol bindings (`core/protocol`)** — all 23 message types translated from the daemon's Go cookbook: flat `ContentBlock` + `Usage`, flat `ClientMessage`, and the 13 server messages (`Welcome`, `Response`, `Error`, `Pong`, `NewConversation`, `Finalizing`, `CaptureStatus`, `ModelsList`+`Alias`, `ModelSet`, `ModelShow`, `InfoResponse`, `DreamResult`, `ConversationRenamed`). Type-discriminator constants (`TypeChat`, `TypeWelcome`, …) for both directions. `ServerMessage` sealed interface (`isServerMessage()` marker, the 13 concrete pointer types implement it) for type-switch dispatch.
- **`core.Conn`** — low-level wire primitive (renamed from the 0.1.0 placeholder `core.Client`). `Send(protocol.ClientMessage) error`, `Recv() (string, json.RawMessage, error)`, `bufio.Scanner` buffer at 8 MB to fit image-bearing responses, blank-line skipping, clean `io.EOF` on peer close. `core.Dial(ctx, path)` returns `*Conn`.
- **`core.Client`** — session-aware wrapper over `core.Conn`. Owns the read goroutine, decodes each line into the appropriate `protocol.*` concrete pointer, runs the session reducer (`welcome` / `new_conversation` / `response` → `Session{PrincipalID, ConversationID}`), and delivers via `Messages() <-chan protocol.ServerMessage`. Mutex-serialised `Send` is goroutine-safe; auto-populates `ConversationID` on `chat` / `info` / `set_model` / `show_model` / `rename` only when the caller leaves it empty (explicit values are never overridden). Idempotent `Close`, terminal-error inspection via `Err()`, `Connect(ctx, path)` convenience for dial+wrap.
- **TUI chat round-trip** — `tui/main.go` parses a `-socket` flag (default `/var/sycamore/$USER.sock`), dials with a 5 s deadline, and runs a Bubble Tea program. `tui/chat.go` provides a sticky single-line input prompt; server messages stream above it via `tea.Println`. Renderer covers `Welcome` (principal banner + resume state), `Response` (text / tool_use / tool_result / image content blocks + provider/model/usage/elapsed stats line), `Error`, `NewConversation`, `ConversationRenamed`, `Pong`, plus a typed-fallback for unknown kinds. Quit on ctrl+c / esc / ctrl+d; enter sends, backspace/runes/space edit the buffer.
- **Test coverage** — 8 `net.Pipe`-driven tests for `core.Conn` (send, recv, blank-line skip, malformed JSON, EOF, round-trip, >256 KB line), 12 tests for `core.Client` over `net.Pipe` (session reducer for every updating kind, re-welcome clears, auto-populate on relevant types only, explicit override, EOF channel close + `io.EOF` in `Err()`, idempotent `Close`, unknown/malformed skip, ordering, race-clean concurrent `Send`), and 16 TUI tests over a real Unix socket (renderer purity + Cmd-level integration). All green under `-race`.
- **Design decisions #1–#3 pinned** in `dev-notes/planning-core-design-decisions.md`: layered `core` (session-aware Client owns the goroutine, low-level Send/Recv stay exported underneath); single-channel delivery with type discriminator (one `Messages()` channel, type switch at the call site); flat `ContentBlock` per the cookbook. Deferred sub-question (Client/Server message representation) also resolved during M2: flat for outgoing, sealed interface for incoming — asymmetry justified by construct-vs-dispatch.
- **`dev-notes/planning-core-design-decisions.md`** — seven open shape-of-`core` decisions surfaced after reviewing the daemon's chat-socket protocol docs. Three settled (above); #4–#7 still open and gate the next sprint.

### Changed

- `core.Client` (the 0.1.0 connection-wrapper type) renamed to `core.Conn`; the `Client` name now belongs to the session-aware higher-level type.
- `core/protocol/protocol.go` placeholder `Message{Type, Payload}` envelope replaced with the full 23-message wire schema.

## [0.1.0] - 2026-05-14

Initial project scaffold. Workspace and module skeletons are in place; no functional client yet. Roadmap planning happens next, in `dev-notes/00-project-tracker.md`.

### Added

- Go workspace (`go.work`) tying three modules together: `core/` (library), `tui/` (Bubble Tea binary), `gui/` (Wails binary, scaffold pending).
- `core.Dial(ctx, socketPath)` — Unix-domain socket connector for the Sycamore agent's chat socket.
- `core.Version` constant so each binary can report which build is running.
- `core/protocol` placeholder envelope (`{Type, Payload}`) — real wire format TBD, owned by the daemon.
- `core/tunnel.Open` stub returning `"not implemented"` — SSH forwarding still to be wired up.
- TUI stub: minimal Bubble Tea program that prints version and quits on `q` / `ctrl+c` / `esc`.
- GUI stub: placeholder `main` pending `wails init` (frontend flavour deliberately deferred — see `gui/README.md`).
- Project documentation: `README.md`, `CHANGELOG.md`, `LICENSE` (AGPL-3.0, matching the daemon), `CLAUDE.md` (house style + start-of-session reading), `dev-notes/00-project-tracker.md` (template ready for roadmap planning).
