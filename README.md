# sycamore-client

Clients for the [Sycamore](https://github.com/headwalluk/sycamore) AI agent. The agent
(the daemon) is a separate repository; these are the programs that talk to it.

- **`sycamore-tui`**, a terminal client, for any OS.
- **`sycamore-gui`**, a desktop client (Wails + Svelte), Linux only for now.

Both connect to agents over SSH: each agent's chat socket is forwarded from its
host, and the agent knows you by the SSH user you log in as. There are no app
passwords. Both hold several agents at once, each connected in the background,
with conversation history, switching, live tool progress and Markdown replies.

## Use

```sh
sycamore-tui --init     # write a starter config to ~/.config/sycamore-client/config.toml
sycamore-tui --probe    # test the connection layer by layer (add --instance <name> for another agent)
sycamore-tui            # connect and chat
```

- [`docs/configuration.md`](./docs/configuration.md): the config file, which
  both clients read.
- [`docs/tui-cli.md`](./docs/tui-cli.md): the TUI's flags, commands and keys.

The GUI has no flags. It reads the same config and opens the default agent (or
the only one, if there's just one).

If `--probe` fails, the failing line says where to look:

| Fails at | Usually means |
| --- | --- |
| `SSH tunnel` | Can't reach or log in to the host: check the host name, the network, and that plain `ssh` works. |
| `Socket` | Logged in, but there's no socket at `socket =`: check the path, and that the agent is running. |
| `Connect` | The socket is there but won't open: the SSH user needs access to it on the agent's host (usually the `sycamore` group). |

## Build

A Go workspace (`go.work`, Go 1.24) of three modules: `core/` (the shared
library), `tui/` and `gui/`. There's no module at the root, so `./...` doesn't
resolve there: name the modules (`./core/... ./tui/... ./gui/...`), or use the
Makefile.

```sh
make              # bin/sycamore-tui for this host
make tui-release  # bin/sycamore-tui-{linux-amd64,linux-arm64,windows-amd64.exe,darwin-universal}
make gui          # bin/sycamore-gui-linux-amd64
make test         # the workspace test suite
make lint         # golangci-lint (.golangci.yml) plus the no-return-in-a-loop check
make help         # every target
```

Tools, beyond Go:

- **GUI:** the Wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`),
  Node and npm, and `libgtk-3-dev` plus `libwebkit2gtk-4.1-dev`. Builds pass
  `-tags webkit2_41` (the Makefile does). `wails doctor` reporting
  `libwebkit: Not Found` is a false alarm: it looks for the 4.0 package.
  `cd gui && wails dev -tags webkit2_41` runs it with hot reload.
- **Lint:** golangci-lint v2
  (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`).

The GUI can't be cross-compiled (Wails links each OS's native webview), so
other platforms need a build on that platform.

## More

- [`CHANGELOG.md`](./CHANGELOG.md): what changed in each release.
- [`CLAUDE.md`](./CLAUDE.md): working notes for AI-assisted development.

## License

[GNU Affero General Public License v3.0](./LICENSE). Copyright (C) 2026 Headwall Networks Ltd, trading as Headwall Hosting.
