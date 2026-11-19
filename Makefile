# Sycamore client build targets.
#
# This is a Go *workspace* (go.work) with no module at the root, so the usual
# `./...` shorthand does not resolve here — every target names the modules
# explicitly (./core/... ./tui/... ./gui/...).
#
# The TUI is pure Go and cross-compiles to every target from this one host.
# The GUI is Wails (CGO + a native webview per OS) and CANNOT be cross-compiled
# — non-Linux GUI builds need a builder running that OS, so only linux/amd64 is
# wired up here for now. See CLAUDE.md "Wails specifics".

BIN     := bin
VERSION := $(shell grep -oP 'Version = "\K[^"]+' core/version.go)

# Pure-Go static binaries for the TUI; no cgo, no host libraries.
TUI_ENV := CGO_ENABLED=0

.DEFAULT_GOAL := tui

# Pure-Go lipo for the macOS universal binary, pinned so a release build
# runs known code.
LIPO := github.com/konoui/lipo@v0.10.0

TUI_RELEASE := $(BIN)/sycamore-tui-linux-amd64 \
               $(BIN)/sycamore-tui-linux-arm64 \
               $(BIN)/sycamore-tui-windows-amd64.exe \
               $(BIN)/sycamore-tui-darwin-universal

# The release binaries are phony too, so every tui-release rebuilds them
# from the current source rather than trusting an existing file.
.PHONY: tui tui-release gui gui-embed test vet lint clean version help $(TUI_RELEASE)

## tui: build the TUI for THIS host into bin/sycamore-tui (dev build)
tui:
	@mkdir -p $(BIN)
	$(TUI_ENV) go build -o $(BIN)/sycamore-tui ./tui
	@echo "built $(BIN)/sycamore-tui ($(VERSION))"

## tui-release: cross-build the TUI for all release targets into bin/
tui-release: $(TUI_RELEASE)
	@echo "TUI $(VERSION) release binaries in $(BIN)/"

$(BIN)/sycamore-tui-linux-amd64:
	@mkdir -p $(BIN)
	$(TUI_ENV) GOOS=linux GOARCH=amd64 go build -o $@ ./tui

$(BIN)/sycamore-tui-linux-arm64:
	@mkdir -p $(BIN)
	$(TUI_ENV) GOOS=linux GOARCH=arm64 go build -o $@ ./tui

$(BIN)/sycamore-tui-windows-amd64.exe:
	@mkdir -p $(BIN)
	$(TUI_ENV) GOOS=windows GOARCH=amd64 go build -o $@ ./tui

# macOS universal ("fat") binary: build both arches, then merge with a pure-Go
# lipo so it works on this Linux host (real lipo is macOS-only). The per-arch
# intermediates are removed, leaving only the universal binary.
$(BIN)/sycamore-tui-darwin-universal:
	@mkdir -p $(BIN)
	$(TUI_ENV) GOOS=darwin GOARCH=amd64 go build -o $(BIN)/.tui-darwin-amd64 ./tui
	$(TUI_ENV) GOOS=darwin GOARCH=arm64 go build -o $(BIN)/.tui-darwin-arm64 ./tui
	go run $(LIPO) -create -output $@ \
		$(BIN)/.tui-darwin-amd64 $(BIN)/.tui-darwin-arm64
	@rm -f $(BIN)/.tui-darwin-amd64 $(BIN)/.tui-darwin-arm64

## gui: build the Wails GUI for linux/amd64 (this host only — see header note)
gui:
	@mkdir -p $(BIN)
	cd gui && wails build -tags webkit2_41 -platform linux/amd64
	cp gui/build/bin/sycamore-gui $(BIN)/sycamore-gui-linux-amd64
	@touch gui/frontend/dist/.gitkeep
	@echo "built $(BIN)/sycamore-gui-linux-amd64 ($(VERSION))"

# gui embeds frontend/dist, which is empty until the frontend is built (and
# emptied by every wails build), so Go tooling needs a placeholder in it.
gui-embed:
	@mkdir -p gui/frontend/dist
	@[ -n "$$(ls -A gui/frontend/dist)" ] || touch gui/frontend/dist/.gitkeep

## test: run the whole workspace test suite
test: gui-embed
	go test ./core/... ./tui/... ./gui/...

## vet: vet the Go modules
vet: gui-embed
	go vet ./core/... ./tui/... ./gui/...

## lint: the house style: golangci-lint (.golangci.yml) plus the no-return-in-a-loop check
lint: gui-embed
	golangci-lint run ./core/... ./tui/... ./gui/...
	go run ./core/internal/tools/loopreturn core tui gui

## version: print the core library version
version:
	@echo $(VERSION)

## clean: remove build artifacts
clean:
	rm -rf $(BIN) gui/build/bin

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'
