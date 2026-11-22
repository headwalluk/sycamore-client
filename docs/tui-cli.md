# `sycamore-tui`

```
sycamore-tui [flags]
sycamore-tui [flags] <socket-path>
```

With no flags it opens the `default` agent from the
[config file](./configuration.md). Flags take one dash or two, and go before
a socket path.

On the agent's own host, `sycamore-tui /var/sycamore/my-agent.sock` (the same
as `--socket`) connects straight to the socket as the local user, who needs
access to it (usually the `sycamore` group). The agent shows as `local`.

| Flag | Does |
| --- | --- |
| `--instance <name>` | Open that agent instead of the default. |
| `--config <path>` | Read that config file instead of the default one. |
| `--socket <path>` | Connect straight to a local socket: no SSH, and no config needed (one is still read, if present, so `/connect` and `[ui]` apply). Can't be combined with `--instance` or `--probe`. |
| `--probe` | Test the connection in three stages (SSH tunnel, socket, connect), then exit; non-zero if a stage fails. Configured instances only. |
| `--init` | Write a commented starter config and exit. Won't overwrite one. |
| `--version` | Print the version and exit. |

## Chatting

Enter sends. A line starting with `/` is a command. Scrolling to the top of
the transcript loads older messages. The input's cursor is hidden while the
terminal or tmux pane doesn't have focus (in tmux this needs
`set -g focus-events on`).

While the agent works, its narration and a row per tool call
(`↳ shell_exec {…}` ending `✓` or `✗` with the time) appear under your message,
and the footer names the running tool; they give way to the reply. This needs
an agent advertising `progress` (Sycamore 0.51.0+).

Files the agent attaches to a reply are listed under it, one per line
(`⎘ robot.png · image/png · 412.0 KB`), dimmed and marked `expired` once the
agent has deleted its copy (after 7 days by default). The TUI shows the
details only; fetch the file from sycamore-web. This needs an agent advertising
`attachments` (Sycamore 0.69.0+).

Each agent works on one reply at a time. While it's busy you can switch agent
or conversation and carry on elsewhere; a message to the busy agent waits in
the input.

### Emoji

Type a shortcode such as `:thumbsup:` and it becomes 👍 as you type the
closing colon. The codes are GitHub's (the ones Slack and Discord use too).
After a colon and two letters, a short list of matches opens above the input
when there are 5 or fewer (`[ui] emoji_choices`): Tab or Enter picks, ↑ / ↓
move, Esc closes it, and typing narrows it. The colon has to start the line
or follow a space, so `10:30` and URLs are left alone, as is anything inside
backticks or a fenced code block. Pasted text isn't converted.

## Commands

| Command | Does |
| --- | --- |
| `/model [alias]` | Show the conversation's model, or switch it. |
| `/models` | List the configured model aliases (or press ctrl+y for a picker). |
| `/new` | Start a new conversation. |
| `/info` | Show the conversation's stats. |
| `/rename [title]` | Set the title; blank lets the agent choose one. |
| `/park` | Fold the conversation into the agent's memory and leave it open. Needs an agent advertising `park`. |
| `/dream` | Run the agent's memory consolidation. |
| `/capture <on\|off\|flush\|status>` | Control request capture on the agent. |
| `/ping` | Liveness check. |
| `/list` | List your conversations (also `/conversations`). |
| `/switch [n\|text]` | Switch to a conversation by `/list` number or title text; `agent:text` does it on another agent. Bare, it opens the switcher. |
| `/connect [name]` | Switch to an agent, connecting it if needed; bare, it opens a picker. The agent you leave stays connected. |
| `/close [name]` | Disconnect an agent (bare: the one on screen) and forget its transcript. Not the last one. |
| `/edit [text]` | Write a message in your editor, starting from `text`; what you save comes back into the input to send. |
| `/help` | List the commands and keys. |
| `/quit` | Exit (also `/exit`). |

## The switcher

`/switch` opens your agents above the conversations of the highlighted one,
starting on the agent and conversation on screen.

- **Tab** / **Shift-Tab** move between the two lists; the filter at the top
  filters the one with focus, and each keeps its own filter text.
- Highlighting an agent lists its conversations. One that isn't connected says
  so, and connects only when you press **Enter** on it.
- **Enter** on a conversation opens it; **Esc** closes the switcher.
- The first conversation row, **+ New conversation**, puts the agent on screen
  and starts one there, as `/new` does. It stays whatever you filter by, and
  it's the way into a brand-new agent that has no conversations yet.
- Agent rows: `●` idle, `◐` working (with the tool and round), `◆` a reply
  waiting, `!` a failed turn, `○` disconnected, `…` connecting, `✗` failed to
  connect. Conversation rows mark the current one, `◆` unread and `◐` working.

The footer counts what's waiting off screen (`1 working · 2 unread`).

## Keys

| Key | Does |
| --- | --- |
| Enter | Send. |
| ctrl+j, or end a line with `\` | New line in the input. |
| ↑ / ↓ | Move in the input; from its first line ↑ recalls what you sent to this agent, and from its last line ↓ goes forward, back to your draft. |
| Page Up / Page Down, mouse wheel | Scroll the transcript. |
| ctrl+r | Go to the next reply waiting, on any agent. |
| ctrl+y | Switch model. |
| ctrl+o | Toggle mouse capture (off: select text with the mouse). |
| ctrl+c | Clear the input; on an empty input, quit. |
| ctrl+d | On an empty input, quit; otherwise delete the next character. |
| Esc | Close the switcher, a picker or the emoji list. |
| Tab | Pick from the emoji list. |
