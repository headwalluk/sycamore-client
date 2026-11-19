# CLAUDE.md

Working notes for Claude Code in this repository.

## Start-of-session reading

1. [`README.md`](./README.md): what this is, how to build, test and lint.
2. `dev-notes/`, when present. It's private and not in the public repository. [`00-project-tracker.md`](./dev-notes/00-project-tracker.md) is the authority on progress and what's next. The `planning-*.md` doc for the area you're touching records why things are the way they are; code comments point into those docs by section.

The **Sycamore daemon** is a separate repo, checked out beside this one at `../sycamore/`. For the wire protocol, `docs/sycamore-protocol/` there is the spec, and `src/socket/server.ts` is the tiebreaker when the two disagree.

## What this repo is

Clients for the Sycamore agent, which listens on a Unix socket on its own host. The clients reach it over an SSH tunnel (shelling out to OpenSSH), and the daemon identifies the caller with `SO_PEERCRED`, so the SSH login user on the agent's host is the identity. There is no in-app credential.

## Layout

A Go workspace (`go.work`, Go 1.26) of three modules, imported as `github.com/headwalluk/sycamore-client/{core,tui,gui}`:

- `core/`: the shared library. `protocol` (the wire types), `Client` (one connection's session), `tunnel` (the SSH transport), `config` (the TOML file, and the one place the environment is read), `Pool` (one connection per agent: on demand, reconnect, idle retire), plus `FoldProgress` for live progress rows.
- `tui/`: the Bubble Tea client, `sycamore-tui`. Per-agent state is `agentCtx`; the model embeds a pointer to the agent on screen, and `withAgent` applies another agent's events to its own state.
- `gui/`: the Wails client, `sycamore-gui` (Svelte + TypeScript frontend in `gui/frontend/`). Go builds view-model DTOs and emits them as events; Svelte only renders. `App.Schema()` exists so Wails generates TypeScript types for emitted DTOs.

`tui/go.mod` and `gui/go.mod` carry `replace …/core => ../core`, so they also resolve outside the workspace.

## Commands

There's no module at the root, so `./...` doesn't resolve there. Use the Makefile, or name the modules:

```sh
make test    # go test ./core/... ./tui/... ./gui/...
make vet
make lint    # golangci-lint + core/internal/tools/loopreturn
make         # bin/sycamore-tui
make gui     # bin/sycamore-gui-linux-amd64 (runs wails build)
```

`gui` embeds `frontend/dist`, which is empty until the frontend is built and is emptied by every `wails build`. `make test`, `make vet` and `make lint` put a placeholder there first; plain `go` commands on `gui` need a frontend build (`cd gui/frontend && npm run build`) or that placeholder.

After changing a bound `App` method or an emitted DTO, regenerate the TypeScript bindings with `cd gui && wails generate module -tags webkit2_41`.

## Wails

- Build with `-tags webkit2_41`: Debian 13 has only `libwebkit2gtk-4.1-dev`. `wails doctor`'s `libwebkit: Not Found` is a false alarm.
- Don't re-run `wails init`; it would overwrite `app.go`, `main.go` and the frontend.
- Wails can't cross-compile, so only linux/amd64 GUI builds exist. macOS and Windows are parked until after 1.0.0.

## House style

The user's global rules apply (descriptive names, no discarded errors, no `return` inside a loop, the environment read in one place, short comments that say how the code works). `make lint` enforces what it can; keep it at zero findings. Verify the TUI live in tmux and the GUI over `ssh -X`: hand the user a checklist rather than driving either headlessly.

## Manual steps

- `core.Version` (`core/version.go`) is bumped by hand at each release, with a `CHANGELOG.md` entry and the tracker's release line.
