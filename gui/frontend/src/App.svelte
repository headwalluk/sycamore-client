<script lang="ts">
  import { onMount, afterUpdate, tick } from 'svelte'
  import { BrowserOpenURL, EventsOn } from '../wailsjs/runtime/runtime'
  import {
    Agents,
    Close,
    Command,
    Connect,
    Conversations,
    LoadOlder,
    Models,
    NextUnread,
    SendChat,
    SetModel,
    Start,
    SwitchConversation,
  } from '../wailsjs/go/main/App.js'
  import type { core, main } from '../wailsjs/go/models'

  // Multi-agent chat over the Go→JS event bridge. Every agent keeps its own
  // view here, and every view a transcript per conversation; the header
  // chooses the agent and the conversation. Translation lives in Go;
  // payloads arrive ready to render, tagged with agent and conversation.
  // Design: dev-notes/planning-gui-chat.md, dev-notes/planning-multi-agent.md.

  type AgentView = {
    conversation: string // the conversation shown ("" before one exists)
    transcripts: Record<string, main.Entry[]>
    hasMore: boolean
    loadingOlder: boolean
    session: main.SessionInfo
    state: string
    attempt: number
    error: string
    thinking: boolean
    turnConversation: string // where the running turn was sent
    progress: core.ProgressRow[]
    progressStatus: string
    conversations: main.ConversationRow[]
    // input history: what was sent, oldest first; historyIndex is the entry
    // shown while browsing (history.length when not), historyDraft the draft
    // to return to.
    history: string[]
    historyIndex: number
    historyDraft: string
    draft: string // the unsent input, kept while another agent is on screen
  }

  const historyLimit = 200

  let views: Record<string, AgentView> = {}
  let active = ''
  let agents: main.AgentInfo[] = []
  let draft = ''
  let error = ''
  let flash = '' // a brief notice, e.g. nothing unread

  let panelOpen = false
  let panelFilter = ''
  let panelNote = ''

  let modelPanelOpen = false
  let models: main.ModelRow[] = []

  let transcriptEl: HTMLElement
  let inputEl: HTMLTextAreaElement
  let pinned = true // auto-scroll while the reader is at the bottom

  function blankView(agent: string): AgentView {
    return {
      conversation: '',
      transcripts: {},
      hasMore: false,
      loadingOlder: false,
      session: { agent, principal: '', conversationTitle: '', protocolVersion: 0 } as main.SessionInfo,
      state: 'connecting',
      attempt: 0,
      error: '',
      thinking: false,
      turnConversation: '',
      progress: [],
      progressStatus: '',
      conversations: [],
      history: [],
      historyIndex: 0,
      historyDraft: '',
      draft: '',
    }
  }

  // update applies a change to one agent's view, creating it on first use.
  function update(agent: string, change: (view: AgentView) => void) {
    const view = views[agent] ?? blankView(agent)
    change(view)
    views = { ...views, [agent]: view }
  }

  async function refreshAgents() {
    agents = (await Agents()) ?? []
  }

  // keepScroll runs a change that adds rows above the reader, then holds the
  // reader's place.
  async function keepScroll(change: () => void) {
    const before = transcriptEl ? transcriptEl.scrollHeight - transcriptEl.scrollTop : 0
    change()
    await tick()
    if (transcriptEl) transcriptEl.scrollTop = transcriptEl.scrollHeight - before
  }

  onMount(() => {
    EventsOn('chat:entry', (entry: main.Entry) =>
      update(entry.agent, (view) => {
        const current = view.transcripts[entry.conversationId] ?? []
        view.transcripts = { ...view.transcripts, [entry.conversationId]: [...current, entry] }
      }),
    )
    EventsOn('chat:history', (page: main.HistoryMsg) => {
      const apply = () =>
        update(page.agent, (view) => {
          const existing = view.transcripts[page.conversationId] ?? []
          let merged: main.Entry[]
          if (page.older) {
            merged = [...page.entries, ...existing]
          } else if (existing.some((entry) => entry.kind === 'history')) {
            merged = page.entries // a fresh copy of a conversation seen before
          } else {
            merged = [...page.entries, ...existing] // history above the welcome
          }
          view.transcripts = { ...view.transcripts, [page.conversationId]: merged }
          if (page.conversationId === view.conversation) {
            view.hasMore = page.hasMore
            view.loadingOlder = false
          }
        })
      if (page.older && page.agent === active) keepScroll(apply)
      else apply()
    })
    EventsOn('chat:conversation', (message: main.ConversationMsg) =>
      update(message.agent, (view) => {
        if (message.adopt) {
          const { ['']: untitled = [], ...rest } = view.transcripts
          view.transcripts = { ...rest, [message.conversationId]: [...untitled, ...(rest[message.conversationId] ?? [])] }
          if (view.turnConversation === '') view.turnConversation = message.conversationId
        }
        view.conversation = message.conversationId
        view.hasMore = false
        view.loadingOlder = false
        if (message.agent === active) pinned = true
      }),
    )
    EventsOn('chat:conversations', (list: main.ConversationsMsg) =>
      update(list.agent, (view) => (view.conversations = list.conversations ?? [])),
    )
    EventsOn('app:agents', (list: main.AgentInfo[]) => (agents = list ?? []))
    EventsOn('app:panel', (agent: string) => {
      if (agent === active && !panelOpen) togglePanel()
    })
    EventsOn('chat:models', (list: main.ModelsMsg) => {
      if (list.agent === active) models = list.models ?? []
    })
    // The Go side sends the whole status bar each time, so it replaces.
    EventsOn('chat:session', (info: main.SessionInfo) => update(info.agent, (view) => (view.session = info)))
    EventsOn('chat:progress', (message: main.ProgressMsg) =>
      update(message.agent, (view) => {
        view.progress = message.rows ?? []
        view.progressStatus = message.status ?? ''
      }),
    )
    EventsOn('chat:thinking', (message: main.ThinkingMsg) =>
      update(message.agent, (view) => (view.thinking = message.thinking)),
    )
    EventsOn('chat:state', (message: main.StateMsg) =>
      update(message.agent, (view) => {
        view.state = message.state
        view.attempt = message.attempt ?? 0
        view.error = message.error ?? ''
      }),
    )
    EventsOn('app:focus', (agent: string) => focus(agent))

    Start()
      .then((agent) => {
        focus(agent)
        refreshAgents()
      })
      .catch((err) => (error = `${err}`))
  })

  // focus puts an agent on screen, parking the outgoing agent's draft and
  // restoring the incoming one's.
  function focus(agent: string) {
    if (agent !== active) {
      const outgoing = draft
      if (active) update(active, (view) => (view.draft = outgoing))
      draft = views[agent]?.draft ?? ''
    }
    active = agent
    panelOpen = false
    modelPanelOpen = false
    pinned = true
  }

  $: current = views[active] ?? blankView(active)
  $: transcript = current.transcripts[current.conversation] ?? []
  $: turnHere = current.thinking && current.turnConversation === current.conversation
  $: turnElsewhere = current.thinking && !turnHere
  $: working = agents.filter((agent) => agent.working && !agent.active).length
  $: waiting = agents.reduce((count, agent) => count + agent.unread, 0)
  $: openCount = agents.filter((agent) => agent.open).length
  $: teams = [...new Set(agents.map((agent) => agent.team ?? ''))]
  $: ready = current.state === 'connected' && !current.thinking
  // Slash commands go through even while the agent is busy or down: the Go
  // side decides which of them can run.
  $: sendable = draft.trim().startsWith('/') || ready
  $: panelRows = current.conversations.filter((row) =>
    `${row.title} ${row.updatedAt}`.toLowerCase().includes(panelFilter.trim().toLowerCase()),
  )

  afterUpdate(() => {
    if (pinned && transcriptEl) transcriptEl.scrollTop = transcriptEl.scrollHeight
  })

  function onScroll() {
    if (!transcriptEl) return
    const gap = transcriptEl.scrollHeight - transcriptEl.scrollTop - transcriptEl.clientHeight
    pinned = gap < 40
    if (transcriptEl.scrollTop < 40 && current.hasMore && !current.loadingOlder) {
      update(active, (view) => (view.loadingOlder = true))
      LoadOlder(active).catch((err) => (error = `${err}`))
    }
  }

  function choose(agent: string) {
    if (agent === active) return
    Connect(agent).catch((err) => (error = `${err}`))
  }

  function closeAgent(agent: string) {
    Close(agent)
      .then((nowActive) => {
        const { [agent]: _closed, ...rest } = views
        views = rest
        focus(nowActive)
      })
      .catch((err) => (error = `${err}`))
  }

  function showFlash(text: string) {
    flash = text
    setTimeout(() => (flash = ''), 2500)
  }

  // ctrl+r jumps to the next reply waiting, as in the TUI.
  function onWindowKeydown(event: KeyboardEvent) {
    if (event.ctrlKey && event.key.toLowerCase() === 'r') {
      event.preventDefault()
      NextUnread()
        .then((moved) => {
          if (!moved) showFlash('nothing unread')
        })
        .catch((err) => (error = `${err}`))
    }
  }

  function initial(agent: main.AgentInfo): string {
    return (agent.displayName || agent.name).slice(0, 1).toUpperCase()
  }

  function toggleModelPanel() {
    modelPanelOpen = !modelPanelOpen
    panelOpen = false
    models = []
    if (modelPanelOpen) Models(active).catch((err) => (error = `${err}`))
  }

  function chooseModel(row: main.ModelRow) {
    modelPanelOpen = false
    if (!row.current) SetModel(active, row.name).catch((err) => (error = `${err}`))
  }

  function togglePanel() {
    modelPanelOpen = false
    panelOpen = !panelOpen
    panelFilter = ''
    panelNote = ''
    if (panelOpen) Conversations(active).catch((err) => (panelNote = `${err}`))
  }

  function openConversation(row: main.ConversationRow) {
    panelOpen = false
    if (row.id === current.conversation) return
    pinned = true
    SwitchConversation(active, row.id, row.title === '(untitled)' ? '' : row.title).catch((err) => (error = `${err}`))
  }

  function onPanelKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') panelOpen = false
    if (event.key === 'Enter' && panelRows.length > 0) openConversation(panelRows[0])
  }

  // isWebLink accepts only absolute http and https URLs for the system browser.
  function isWebLink(href: string): boolean {
    let web = false
    try {
      const parsed = new URL(href)
      web = parsed.protocol === 'http:' || parsed.protocol === 'https:'
    } catch (err) {
      console.warn(`not opening a malformed link ${JSON.stringify(href)}: ${err}`)
    }
    return web
  }

  // Links in a reply open in the browser; left alone they would navigate the
  // app's own window away from the chat.
  function onTranscriptClick(event: MouseEvent) {
    const link = (event.target as HTMLElement).closest('a')
    if (!link) return
    event.preventDefault()
    const href = link.getAttribute('href')
    if (href && isWebLink(href)) BrowserOpenURL(href)
  }

  function duration(milliseconds: number): string {
    return milliseconds < 1000 ? `${milliseconds}ms` : `${(milliseconds / 1000).toFixed(1)}s`
  }

  function remember(text: string) {
    update(active, (view) => {
      if (view.history[view.history.length - 1] !== text) view.history = [...view.history, text].slice(-historyLimit)
      view.historyIndex = view.history.length
      view.historyDraft = ''
    })
  }

  // ↑ on the first line recalls what was sent before; ↓ on the last line
  // goes forward again, back to the draft.
  function recall(direction: -1 | 1): boolean {
    const caret = inputEl.selectionStart
    const collapsed = caret === inputEl.selectionEnd
    const onFirstLine = !draft.slice(0, caret).includes('\n')
    const onLastLine = !draft.slice(caret).includes('\n')
    const view = current
    let recalled = false
    if (collapsed && direction < 0 && onFirstLine && view.historyIndex > 0) {
      update(active, (changed) => {
        if (changed.historyIndex >= changed.history.length) changed.historyDraft = draft
        changed.historyIndex -= 1
        draft = changed.history[changed.historyIndex]
      })
      recalled = true
    } else if (collapsed && direction > 0 && onLastLine && view.historyIndex < view.history.length) {
      update(active, (changed) => {
        changed.historyIndex += 1
        draft = changed.historyIndex === changed.history.length ? changed.historyDraft : changed.history[changed.historyIndex]
      })
      recalled = true
    }
    return recalled
  }

  function send() {
    const text = draft.trim()
    if (!text || !sendable) return
    remember(text)
    draft = ''
    error = ''
    pinned = true
    if (text.startsWith('/')) {
      if (!current.thinking) update(active, (view) => (view.turnConversation = view.conversation))
      Command(active, text).catch((err) => (error = `${err}`))
      return
    }
    update(active, (view) => (view.turnConversation = view.conversation))
    SendChat(active, text).catch((err) => (error = `${err}`))
  }

  function onKeydown(ev: KeyboardEvent) {
    // Enter sends; Shift+Enter / Ctrl+Enter inserts a newline (browser default).
    if (ev.key === 'Enter' && !ev.shiftKey && !ev.ctrlKey) {
      ev.preventDefault()
      send()
    }
    if ((ev.key === 'ArrowUp' && recall(-1)) || (ev.key === 'ArrowDown' && recall(1))) {
      ev.preventDefault()
    }
  }

  // The agent's name, reactive: a plain function called in the markup isn't
  // re-run when the state it reads changes.
  $: agentTitle =
    current.session.displayName || agents.find((agent) => agent.name === active)?.displayName || active || 'agent'

  function label(role: string, name: string): string {
    switch (role) {
      case 'you':
        return 'you'
      case 'agent':
        return name
      default:
        return ''
    }
  }

  function stateText(view: AgentView): string {
    switch (view.state) {
      case 'connecting':
        return 'connecting…'
      case 'connected':
        return view.thinking ? `◐ ${view.progressStatus || 'thinking…'}` : '● connected'
      case 'reconnecting':
        return `reconnecting… (attempt ${view.attempt})`
      case 'failed':
        return '✗ failed'
      default:
        return '○ disconnected'
    }
  }

  function rowDetail(row: main.ConversationRow): string {
    const marks = [row.working ? 'working' : '', row.unread ? 'unread' : '', row.current ? 'current' : '']
    const when = row.updatedAt.slice(0, 16).replace('T', ' ')
    return [...marks.filter(Boolean), `${row.turnCount} turns`, when].join(' · ')
  }
</script>

<svelte:window on:keydown={onWindowKeydown} />

<main>
  <nav class="sidebar">
    {#each teams as team}
      {#if team}<div class="team">{team}</div>{/if}
      {#each agents.filter((agent) => (agent.team ?? '') === team) as agent (agent.name)}
        <div class="agent-row" class:active={agent.active} class:closed={!agent.open}>
          <button class="agent-button" title={agent.tagline || agent.displayName} on:click={() => choose(agent.name)}>
            {#if agent.avatar}
              <img class="avatar" src={agent.avatar} alt="" />
            {:else}
              <span class="avatar initial" style:background-color={agent.accentColor || null}>{initial(agent)}</span>
            {/if}
            <span class="agent-text">
              <span class="agent-name" style:color={agent.accentColor || null}>{agent.displayName}</span>
              <span class="agent-label">{agent.mark} {agent.label}</span>
            </span>
            {#if agent.unread}<span class="badge">{agent.unread}</span>{/if}
          </button>
          {#if agent.open && openCount > 1}
            <button class="agent-close" title="Disconnect {agent.displayName}" on:click={() => closeAgent(agent.name)}>×</button>
          {/if}
        </div>
      {/each}
    {/each}
    <div class="sidebar-hint">ctrl+r: next unread</div>
  </nav>

  <div class="chat">
  <header>
    <span class="agent-title" style:color={current.session.accentColor || null}>{agentTitle}</span>
    {#if current.session.principal}<span class="sep">·</span><span>{current.session.principal}</span>{/if}
    <span class="sep">·</span>
    <button class="model" title="Switch model" on:click={toggleModelPanel} disabled={current.state !== 'connected'}>
      {current.session.model || 'model'} ▾
    </button>
    <span class="sep">·</span>
    <button class="conversation" title="Switch conversation" on:click={togglePanel} disabled={current.state !== 'connected'}>
      {current.session.conversationTitle ? `“${current.session.conversationTitle}”` : '(no conversation)'} ▾
    </button>
    <span class="spacer"></span>
    {#if flash}<span class="flash">{flash}</span><span class="sep">·</span>{/if}
    {#if working || waiting}
      <span class="others">
        {[working ? `${working} working` : '', waiting ? `${waiting} unread` : ''].filter(Boolean).join(' · ')}
      </span>
      <span class="sep">·</span>
    {/if}
    <span class="state state-{current.state}">{stateText(current)}</span>
  </header>

  {#if panelOpen}
    <div class="panel">
      <!-- svelte-ignore a11y_autofocus -->
      <input
        class="filter"
        placeholder="filter conversations…"
        bind:value={panelFilter}
        on:keydown={onPanelKeydown}
        autofocus
      />
      {#if panelNote}<p class="panel-note">{panelNote}</p>{/if}
      <ul>
        {#each panelRows as row}
          <li>
            <button class="row" class:current={row.current} on:click={() => openConversation(row)}>
              <span class="mark">{row.working ? '◐' : row.unread ? '◆' : ''}</span>
              <span class="row-title">{row.title}</span>
              <span class="row-detail">{rowDetail(row)}</span>
            </button>
          </li>
        {:else}
          {#if !panelNote}<li class="panel-note">{current.conversations.length ? 'no matches' : 'loading…'}</li>{/if}
        {/each}
      </ul>
    </div>
  {/if}

  {#if modelPanelOpen}
    <div class="panel model-panel">
      <ul>
        {#each models as row}
          <li>
            <button class="row" class:current={row.current} on:click={() => chooseModel(row)}>
              <span class="mark">{row.current ? '●' : ''}</span>
              <span class="row-title">{row.name}</span>
              <span class="row-detail">{row.target}</span>
            </button>
          </li>
        {:else}
          <li class="panel-note">loading…</li>
        {/each}
      </ul>
    </div>
  {/if}

  <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
  <section class="transcript" bind:this={transcriptEl} on:scroll={onScroll} on:click={onTranscriptClick}>
    {#if error}<p class="err">{error}</p>{/if}
    {#if current.loadingOlder}<p class="older">loading older messages…</p>{/if}
    {#each transcript as e}
      <div class="entry role-{e.role} kind-{e.kind}">
        {#if label(e.role, agentTitle)}<span class="who" style:color={e.role === 'agent' ? current.session.accentColor || null : null}>{label(e.role, agentTitle)}</span>{/if}
        {#if e.html}
          <div class="body markdown">{@html e.html}</div>
        {:else if e.kind === 'info'}
          <div class="body info"><div class="info-title">{e.title}</div><pre>{e.text}</pre></div>
        {:else}
          <span class="body">{e.text}</span>
        {/if}
        {#if e.tokens}
          <span class="meta">{e.model} · {e.tokens.in}/{e.tokens.out} tok · {e.elapsedMs}ms</span>
        {/if}
      </div>
    {/each}
    {#if turnHere}
      <div class="entry progress">
        <span class="who" style:color={current.session.accentColor || null}>{agentTitle}</span>
        {#if current.progress.length === 0}
          <span class="thinking">thinking…</span>
        {:else}
          {#each current.progress as row}
            {#if row.narration}
              <div class="narration">{row.narration}</div>
            {:else}
              <div class="tool-row">
                ↳ {row.toolName} <span class="summary">{row.inputSummary ?? ''}</span>
                {#if !row.finished}<span class="running">…</span>
                {:else if row.isError}<span class="failed">✗ {duration(row.elapsedMs ?? 0)}</span>
                {:else}<span class="succeeded">✓ {duration(row.elapsedMs ?? 0)}</span>{/if}
              </div>
            {/if}
          {/each}
        {/if}
      </div>
    {/if}
  </section>

  <footer>
    <textarea
      bind:this={inputEl}
      bind:value={draft}
      on:keydown={onKeydown}
      placeholder={turnElsewhere
        ? `${agentTitle} is working on a reply in another conversation…`
        : current.thinking
          ? `${agentTitle} is still working on a reply…`
          : current.state === 'connected'
            ? 'Message… (Enter to send, Shift+Enter for newline, /help for commands)'
            : `${stateText(current)} — /connect or pick an agent`}
      rows="1"
    ></textarea>
    <button on:click={send} disabled={!sendable || !draft.trim()}>Send</button>
  </footer>
  </div>
</main>

<style>
  main {
    height: 100vh;
    display: flex;
    font-family: Nunito, system-ui, sans-serif;
  }
  .chat {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    position: relative;
  }

  /* The agent sidebar. */
  .sidebar {
    width: 15rem;
    flex-shrink: 0;
    overflow-y: auto;
    padding: 0.5rem 0;
    background: rgba(0, 0, 0, 0.25);
    border-right: 1px solid rgba(255, 255, 255, 0.08);
    text-align: left;
    display: flex;
    flex-direction: column;
  }
  .sidebar .team {
    padding: 0.6rem 0.8rem 0.2rem;
    font-size: 0.7rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    opacity: 0.5;
  }
  .agent-row {
    display: flex;
    align-items: center;
    padding-right: 0.4rem;
  }
  .agent-row.active {
    background: rgba(99, 102, 241, 0.3);
  }
  .agent-row.closed {
    opacity: 0.6;
  }
  .agent-button {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 0.55rem;
    padding: 0.4rem 0.8rem;
    background: transparent;
    border: none;
    border-radius: 0;
    color: inherit;
    font: inherit;
    font-weight: 400;
    text-align: left;
    cursor: pointer;
  }
  .avatar {
    width: 2rem;
    height: 2rem;
    border-radius: 50%;
    flex-shrink: 0;
    object-fit: cover;
  }
  .avatar.initial {
    display: flex;
    align-items: center;
    justify-content: center;
    background: #475569;
    color: white;
    font-weight: 700;
  }
  .agent-text {
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .agent-name {
    font-weight: 700;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .agent-label {
    font-size: 0.72rem;
    opacity: 0.65;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .badge {
    margin-left: auto;
    min-width: 1.2rem;
    padding: 0 0.35rem;
    border-radius: 0.6rem;
    background: #f59e0b;
    color: #1b2636;
    font-size: 0.72rem;
    font-weight: 700;
    text-align: center;
  }
  .agent-close {
    padding: 0 0.35rem;
    background: transparent;
    border: none;
    color: inherit;
    opacity: 0.4;
    font-weight: 400;
    cursor: pointer;
  }
  .agent-close:hover {
    opacity: 1;
  }
  .sidebar-hint {
    margin-top: auto;
    padding: 0.6rem 0.8rem 0.2rem;
    font-size: 0.7rem;
    opacity: 0.4;
  }
  header .agent-title {
    font-weight: 700;
  }
  header .flash {
    opacity: 0.8;
  }

  header {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.5rem 0.9rem;
    background: rgba(255, 255, 255, 0.06);
    font-size: 0.85rem;
  }
  header .model {
    padding: 0.1rem 0.4rem;
    background: transparent;
    border: 1px solid rgba(255, 255, 255, 0.15);
    border-radius: 4px;
    color: inherit;
    opacity: 0.75;
    font-family: ui-monospace, monospace;
    font-size: 0.78rem;
    font-weight: 400;
  }
  .model-panel {
    width: min(28rem, calc(100% - 1.8rem));
  }
  .info .info-title {
    font-weight: 700;
    opacity: 0.85;
  }
  .info pre {
    margin: 0.2rem 0 0;
    font-family: ui-monospace, monospace;
    font-size: 0.8rem;
    opacity: 0.85;
    white-space: pre-wrap;
  }
  header .conversation {
    padding: 0.1rem 0.4rem;
    background: transparent;
    border: 1px solid rgba(255, 255, 255, 0.15);
    border-radius: 4px;
    color: inherit;
    font: inherit;
    font-weight: 400;
  }
  header .conversation {
    opacity: 0.9;
    max-width: 40ch;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  header .sep {
    opacity: 0.4;
  }
  header .spacer {
    flex: 1;
  }
  header .others {
    opacity: 0.7;
  }
  .state-connected {
    color: #4ade80;
  }
  .state-connecting,
  .state-reconnecting {
    color: #fbbf24;
  }
  .state-disconnected,
  .state-failed {
    color: #f87171;
  }

  /* The conversations panel. */
  .panel {
    position: absolute;
    top: 2.6rem;
    left: 0.9rem;
    width: min(36rem, calc(100% - 1.8rem));
    max-height: 60vh;
    display: flex;
    flex-direction: column;
    background: #1f2b3d;
    border: 1px solid rgba(255, 255, 255, 0.15);
    border-radius: 6px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
    z-index: 10;
    text-align: left;
  }
  .panel .filter {
    margin: 0.5rem;
    padding: 0.35rem 0.5rem;
    background: rgba(255, 255, 255, 0.08);
    color: inherit;
    border: 1px solid rgba(255, 255, 255, 0.12);
    border-radius: 4px;
    font: inherit;
  }
  .panel ul {
    list-style: none;
    margin: 0;
    padding: 0 0 0.4rem;
    overflow-y: auto;
  }
  .panel .row {
    display: flex;
    gap: 0.5rem;
    align-items: baseline;
    width: 100%;
    padding: 0.35rem 0.7rem;
    background: transparent;
    color: inherit;
    border: none;
    border-radius: 0;
    font: inherit;
    font-weight: 400;
    text-align: left;
    cursor: pointer;
  }
  .panel .row:hover {
    background: rgba(99, 102, 241, 0.35);
  }
  .panel .row.current {
    font-weight: 700;
  }
  .panel .mark {
    width: 1ch;
    color: #fbbf24;
  }
  .panel .row-title {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .panel .row-detail {
    font-size: 0.75rem;
    opacity: 0.55;
    white-space: nowrap;
  }
  .panel-note {
    padding: 0.3rem 0.7rem;
    opacity: 0.6;
    font-size: 0.85rem;
  }

  .transcript {
    flex: 1;
    overflow-y: auto;
    padding: 0.75rem 0.9rem;
    text-align: left;
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }
  .older {
    opacity: 0.5;
    font-size: 0.8rem;
    text-align: center;
  }

  .entry {
    white-space: pre-wrap;
    word-break: break-word;
    line-height: 1.45;
  }
  .entry .who {
    font-weight: 700;
    margin-right: 0.5rem;
  }
  .role-you .who {
    color: #22d3ee;
  }
  .role-agent .who {
    color: #f0abfc;
  }
  .role-system,
  .kind-note {
    opacity: 0.7;
    font-style: italic;
  }
  .kind-error {
    color: #f87171;
    font-style: normal;
  }
  .entry .meta {
    display: block;
    margin-top: 0.15rem;
    font-size: 0.72rem;
    opacity: 0.5;
    font-family: ui-monospace, monospace;
  }
  .thinking {
    opacity: 0.6;
    font-style: italic;
  }
  .err {
    color: #f87171;
  }

  /* Live progress while a turn runs. */
  .progress .narration {
    opacity: 0.7;
    font-style: italic;
    margin: 0.2rem 0;
  }
  .progress .tool-row {
    font-family: ui-monospace, monospace;
    font-size: 0.8rem;
    opacity: 0.8;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .progress .summary {
    opacity: 0.6;
  }
  .progress .succeeded {
    color: #4ade80;
  }
  .progress .failed {
    color: #f87171;
  }

  /* Agent replies rendered from Markdown. */
  .markdown {
    white-space: normal;
  }
  .markdown :global(p) {
    margin: 0.3rem 0;
  }
  .markdown :global(pre) {
    background: rgba(0, 0, 0, 0.35);
    padding: 0.5rem 0.7rem;
    border-radius: 6px;
    overflow-x: auto;
  }
  .markdown :global(code) {
    font-family: ui-monospace, monospace;
    font-size: 0.85em;
  }
  .markdown :global(:not(pre) > code) {
    background: rgba(255, 255, 255, 0.1);
    padding: 0.05rem 0.3rem;
    border-radius: 4px;
  }
  .markdown :global(table) {
    border-collapse: collapse;
    margin: 0.4rem 0;
  }
  .markdown :global(th),
  .markdown :global(td) {
    border: 1px solid rgba(255, 255, 255, 0.2);
    padding: 0.2rem 0.6rem;
    text-align: left;
  }
  .markdown :global(th) {
    background: rgba(255, 255, 255, 0.06);
  }
  .markdown :global(blockquote) {
    border-left: 3px solid rgba(255, 255, 255, 0.25);
    margin: 0.3rem 0;
    padding-left: 0.7rem;
    opacity: 0.85;
  }
  .markdown :global(a) {
    color: #93c5fd;
  }
  .markdown :global(.tool),
  .markdown :global(.render-error) {
    font-family: ui-monospace, monospace;
    font-size: 0.8rem;
    opacity: 0.6;
  }

  footer {
    display: flex;
    gap: 0.5rem;
    padding: 0.6rem 0.9rem;
    border-top: 1px solid rgba(255, 255, 255, 0.08);
  }
  textarea {
    flex: 1;
    resize: none;
    background: rgba(255, 255, 255, 0.08);
    color: inherit;
    border: 1px solid rgba(255, 255, 255, 0.12);
    border-radius: 6px;
    padding: 0.5rem 0.6rem;
    font: inherit;
  }
  textarea:disabled {
    opacity: 0.5;
  }
  footer button {
    padding: 0 1rem;
    border: none;
    border-radius: 6px;
    background: #6366f1;
    color: white;
    font-weight: 600;
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.4;
    cursor: default;
  }
</style>
