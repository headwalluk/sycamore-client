# Configuration

Both clients read one TOML file: `~/.config/sycamore-client/config.toml`
(`$XDG_CONFIG_HOME/sycamore-client/config.toml` if that's set). The TUI can read
another with `--config <path>`; the GUI always reads the default path.
`sycamore-tui --init` writes a commented starter file.

The file is checked when it loads: an unknown key (a typo such as `hostt`, or
`[instances.x]` for `[instance.x]`) is an error, and every problem is reported
at once.

## Agents

Each agent is an `[instance.<name>]` table. The name is yours to choose; it's
what `--instance`, `/connect` and `/switch agent:text` take.

```toml
default = "zee"

[instance.zee]
host   = "zee-host"               # ssh destination
user   = "zee"                    # the SSH login user, which is who the agent sees
socket = "/var/sycamore/zee.sock" # the agent's socket on that host

[instance.coop]
host   = "coop-host"
socket = "/var/sycamore/coop.sock"
team   = "dev"
```

| Field | Required | Meaning |
| --- | --- | --- |
| `host` | yes | The SSH destination: a host name, an address, or a `Host` alias from `~/.ssh/config` (which can then supply the user, port, key and `ProxyJump`). |
| `socket` | yes | The absolute path of the agent's socket **on its host**. Sent to the remote side as is; a `~` isn't expanded. |
| `user` | no | The SSH login user. Omitted, `~/.ssh/config` decides. |
| `port` | no | The SSH port. Omitted, `~/.ssh/config` decides (usually 22). |
| `identity` | no | A private key file on this machine. A leading `~/` is expanded. Omitted, the SSH agent or `~/.ssh/config` decides. |
| `team` | no | A label that groups agents in the TUI's `/switch` list and the GUI's sidebar. Nothing else. |

`default` (top level, optional) names the agent to open when none is named.
With only one agent, that one is the default.

**Identity.** The client shells out to your `ssh` and forwards the agent's
socket, and the agent identifies you by the SSH login user on its host, not
your local account. That user needs to be in the `sycamore` group there, and
to be a gateway `posix_login` on one of the agent's contacts. If not,
`sycamore-tui --probe` shows `Connect … FAIL`.

`ssh` runs in batch mode, so it never prompts: log in with a key (one with a
passphrase loaded into `ssh-agent`), and accept a new host key with plain
`ssh` first.

## `[ui]`

```toml
[ui]
markdown_style     = "dark"
mouse_capture      = true
conversation_cache = 8
editor             = "emacsclient -t"
emoji_choices      = 5
```

| Field | Default | Meaning |
| --- | --- | --- |
| `markdown_style` | `dark` | The TUI's Markdown theme: `dark`, `light`, `dracula`, `tokyo-night`, `pink`, `ascii` or `notty`. Anything else is an error at start-up. |
| `mouse_capture` | `true` | Whether the TUI captures the mouse, so the wheel scrolls the transcript. `false` starts with the terminal's own click-drag selection. `ctrl+o` toggles it while running, and a Shift-drag selects text in most terminals even while captured. |
| `conversation_cache` | `8` | How many recently viewed conversations the TUI keeps per agent, besides the one on screen: switching back to one is instant, and a reply to one you've left lands in it. `0` keeps none (replies are still marked unread). |
| `editor` | `$VISUAL`, then `$EDITOR`, then `vi` | The command the TUI's `/edit` runs; the file is added as its last argument. |
| `emoji_choices` | `5` | The most matches for a partly typed `:shortcode:` the TUI lists to pick from; with more, nothing shows until you narrow it. `0` turns the list off (complete codes still convert). |

## `[connections]`

```toml
[connections]
idle_timeout  = "20m"
open_on_start = ["coop"]
```

| Field | Default | Meaning |
| --- | --- | --- |
| `idle_timeout` | `"20m"` | How long an agent's connection (and its SSH tunnel) may sit unused before it's closed. A Go duration, e.g. `"45s"` or `"1h30m"`; `"0"` never closes. The agent on screen, and one with a reply on its way, are never closed. A closed agent reconnects, to the conversation it was showing, when you next use it. |
| `open_on_start` | none | Agents to connect in the background at start-up, besides the one shown. Each must name an `[instance.*]`. |

A dropped connection reconnects on its own, waiting longer between attempts
(1 second, doubling to 30), and returns to the conversation it was showing. It
stops retrying if SSH rejects the key or the host key doesn't match; fix that,
then `/connect` again (or pick the agent again in the GUI).

## Files outside the config

- Tunnel sockets: `$XDG_RUNTIME_DIR`, else a private `sycamore-<uid>`
  directory (mode `0700`) in the system temporary directory, one pair per
  connection, removed when it closes. Each name carries the client's
  process id; at start-up, a client stops any tunnel whose client has gone.
- Agent avatars (GUI): `~/.cache/sycamore-client/avatars/`, one file per image,
  named by its SHA-256.
