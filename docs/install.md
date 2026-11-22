# Install the TUI

From a fresh clone to chatting with an agent: build `sycamore-tui`, install it
to `/usr/local/bin/`, and connect, either over SSH or on the agent's own host.

## What you need

On the machine you'll run the TUI on:

- **git**, **make** and **Go 1.26** or newer. An older Go (1.21 or later)
  fetches the 1.26 toolchain itself on the first build, unless
  `GOTOOLCHAIN=local` is set. `go version` to check.
- **The OpenSSH client** (`ssh`), for agents on other hosts. The TUI runs your
  `ssh`, so `~/.ssh/config`, your keys, `ssh-agent` and `known_hosts` all apply.

On the agent's host, the user you'll connect as needs two things. Sycamore's
[installation guide](https://github.com/headwalluk/sycamore/blob/main/docs/installation.md)
covers both:

- **Membership of the `sycamore` group** (step 2), so it can open the agent's
  socket in `/var/sycamore/`. A new membership takes effect at the user's
  next login.
- **A contact with that user as a gateway `posix_login`** (step 7), so the
  agent knows who it is.

## 1. Clone

```sh
git clone https://github.com/headwalluk/sycamore-client.git
cd sycamore-client
git checkout v1.0.0    # optional: a release tag rather than the tip of main
```

## 2. Build

```sh
make
./bin/sycamore-tui --version
```

`make` builds `bin/sycamore-tui` for this machine. It's a static binary with
no runtime dependencies. The first build downloads the Go modules.

## 3. Install

```sh
sudo install -m 0755 bin/sycamore-tui /usr/local/bin/sycamore-tui
sycamore-tui --version
```

To update later, pull (or check out the new tag), then repeat steps 2 and 3.
To remove it, delete `/usr/local/bin/sycamore-tui`. Your config is in
`~/.config/sycamore-client/`.

## 4. Connect over SSH

First check that plain `ssh` reaches the agent's host as the right user,
without asking for anything:

```sh
ssh coop@coop-host true
```

The TUI never prompts. It runs `ssh` in batch mode, so a password login won't
work, and a key with a passphrase has to be loaded into `ssh-agent` first.
The first connection to a new host has to be accepted into `known_hosts` by
plain `ssh` too.

Write a starter config, and edit it to describe your agent:

```sh
sycamore-tui --init
```

```toml
default = "coop"

[instance.coop]
host   = "coop-host"               # or a Host alias from ~/.ssh/config
user   = "coop"                    # the SSH login user: who the agent sees
socket = "/var/sycamore/coop.sock" # the agent's socket on its host
```

Delete the example `[instance.dev]` table unless you have a second agent to
put there. [Configuration](./configuration.md) has every field.

Test the connection one stage at a time, then connect:

```sh
sycamore-tui --probe
sycamore-tui
```

| `--probe` fails at | Usually means |
| --- | --- |
| `SSH tunnel` | Can't reach or log in to the host: check the host name, the network, and that the plain `ssh` above works. |
| `Socket` | Logged in, but there's no socket at `socket =`: check the path, and that the agent is running. |
| `Connect` | The socket is there but the agent won't talk. "Closed with no reply" means the SSH user can't open the socket: it isn't in the `sycamore` group, or hasn't logged in again since being added. "Agent rejected the connection" gives the agent's reason, usually that the user has no gateway `posix_login` contact. |

## 5. Connect on the agent's host

On the agent's own host there's no SSH and no config needed. Point the TUI at
the socket:

```sh
sycamore-tui /var/sycamore/coop.sock
```

The agent knows you as your local user, which needs the same `sycamore` group
membership and contact as an SSH user. The agent shows as `local`. If a config
file exists, its `[ui]` settings apply and `/connect` reaches its agents.
