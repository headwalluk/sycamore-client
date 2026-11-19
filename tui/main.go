package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/headwalluk/sycamore-client/core"
	"github.com/headwalluk/sycamore-client/core/config"
)

// Setup budgets. A direct local socket is instant; an SSH tunnel has to
// authenticate and forward, so it gets a longer leash.
const (
	directConnectTimeout = 5 * time.Second
	dialInstanceTimeout  = 25 * time.Second
	probeTimeout         = 45 * time.Second
)

func main() {
	var (
		instance    = flag.String("instance", "", "named instance from the config file (defaults to the config's `default`)")
		socket      = flag.String("socket", "", "connect directly to a local socket path, bypassing SSH and the config file")
		cfgPath     = flag.String("config", "", "config file path (default: $XDG_CONFIG_HOME/sycamore-client/config.toml)")
		probe       = flag.Bool("probe", false, "run the staged connection test against the instance and exit")
		doInit      = flag.Bool("init", false, "write an example config file and exit")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	// Go's flag package accepts -flag and --flag interchangeably; we
	// present the GNU-style --flag form to match the daemon's `sycamore`
	// / `sycamore-daemon` CLIs.
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Printf("sycamore-tui %s\n", core.Version)
		return
	}

	if *doInit {
		runInit(*cfgPath)
		return
	}

	sweepStaleTunnels()

	// A direct local socket bypasses both the config file and the
	// tunnel — handy for a daemon sharing this host.
	if *socket != "" {
		ctx, cancel := context.WithTimeout(context.Background(), directConnectTimeout)
		defer cancel()
		client, err := core.Connect(ctx, *socket)
		if err != nil {
			fatalf("%v", err)
		}
		// No config file is required for a direct socket, but load one so
		// /connect <name> still works after this start. A missing one just
		// leaves /connect unavailable; a broken one says so first.
		cfg, cfgErr := loadConfig(*cfgPath)
		if cfgErr != nil && !errors.Is(cfgErr, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "sycamore-tui: ignoring the config file (/connect won't be available): %v\n", cfgErr)
		}
		pool := newPool(cfg)
		pool.Add(socketAgentName, core.SocketDialer(*socket))
		runProgram(pool, socketAgentName, client, resolveStyle(config.DefaultMarkdownStyle), config.DefaultMouseCapture, cfg)
		return
	}

	// Config-driven: resolve which instance, then either probe or dial.
	inst, mdStyle, mouseCapture, cfg := resolveInstance(*cfgPath, *instance)

	if *probe {
		runProbe(inst)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), dialInstanceTimeout)
	defer cancel()
	ic, err := core.DialInstance(ctx, inst)
	if err != nil {
		fatalf("connecting to %q: %v", inst.Name, err)
	}
	runProgram(newPool(cfg), inst.Name, ic, mdStyle, mouseCapture, cfg)
}

// socketAgentName is the agent name for a --socket connection.
const socketAgentName = "local"

// newPool builds the connection pool with every configured instance
// registered, using the config's idle timeout (the default without one).
func newPool(cfg *config.File) *core.Pool {
	idleTimeout := config.DefaultIdleTimeout
	if cfg != nil {
		idleTimeout = cfg.Connections.IdleTimeout
	}
	pool := core.NewPool(core.PoolOptions{DialTimeout: dialInstanceTimeout, IdleTimeout: idleTimeout})
	if cfg != nil {
		for name, inst := range cfg.Instances {
			pool.Add(name, core.InstanceDialer(inst))
		}
	}
	return pool
}

// runProgram drives the Bubble Tea chat UI. The start-up connection is
// dialled before the UI appears, so a failure reads as a plain error; the
// pool then owns it, along with every later dial. label is the agent to
// show; mdStyle is the validated glamour theme; mouseCapture sets the
// initial mouse-reporting state (toggleable at runtime with ctrl+o).
func runProgram(pool *core.Pool, label string, startup core.Connection, mdStyle string, mouseCapture bool, cfg *config.File) {
	defer pool.CloseAll()
	if err := pool.Adopt(label, startup); err != nil {
		if closeErr := startup.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		fatalf("%v", err)
	}
	pool.Pin(label, true)
	if cfg != nil {
		for _, name := range cfg.Connections.OpenOnStart {
			if name == label {
				continue
			}
			if err := pool.Open(name); err != nil {
				pool.CloseAll()
				fatalf("open_on_start: %v", err)
			}
		}
	}

	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithReportFocus()}
	if mouseCapture {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	program := tea.NewProgram(newModel(poolAdapter{pool}, label, mdStyle, mouseCapture, cfg), opts...)

	// Bubble Tea handles SIGINT and SIGTERM; a hang-up (the terminal or tmux
	// pane closing) must quit cleanly too, or the tunnels outlive the client.
	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)
	defer signal.Stop(hangups)
	go func() {
		<-hangups
		program.Quit()
	}()

	if _, err := program.Run(); err != nil {
		pool.CloseAll()
		fatalf("%v", err)
	}
}

// sweepStaleTunnels stops tunnels left behind by clients that exited without
// closing them, warning (before the UI starts) if any can't be stopped.
func sweepStaleTunnels() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := core.SweepStaleTunnels(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "sycamore-tui: cleaning up tunnels left by an earlier client: %v\n", err)
	}
}

// resolveStyle validates a configured glamour style name, exiting with a
// helpful message (listing the valid choices) on an unknown value.
func resolveStyle(name string) string {
	style, err := resolveMarkdownStyle(name)
	if err != nil {
		fatalf("%v", err)
	}
	return style
}

// resolveInstance loads the config file and resolves the named instance
// (or the default), returning it alongside the validated Markdown style and
// the initial mouse-capture state. Exits with a helpful message on any
// failure.
func resolveInstance(cfgPath, name string) (config.Instance, string, bool, *config.File) {
	file, err := loadConfig(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fatalf("no config file found.\n  Run `sycamore-tui --init` to create one, or pass --socket <path> for a local connection.")
		}
		fatalf("%v", err)
	}
	inst, err := file.Resolve(name)
	if err != nil {
		fatalf("%v", err)
	}
	// MouseCapture is normalised to non-nil by the loader. f is returned so
	// the model can resolve other instances at runtime for /connect.
	return inst, resolveStyle(file.UI.MarkdownStyle), *file.UI.MouseCapture, file
}

func loadConfig(cfgPath string) (*config.File, error) {
	if cfgPath != "" {
		return config.Load(cfgPath)
	}
	return config.LoadDefault()
}

// runProbe runs the staged test-connection and prints each stage,
// exiting non-zero if any stage failed.
func runProbe(inst config.Instance) {
	fmt.Printf("Testing connection to %q (%s)…\n\n", inst.Name, inst.Host)

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	res := core.ProbeInstance(ctx, inst)

	printStage("SSH tunnel", res.SSH)
	printStage("Socket", res.Socket)
	printStage("Connect", res.Connect)
	if res.Cleanup != nil {
		fmt.Printf("\n  (closing the test tunnel failed: %v)\n", res.Cleanup)
	}

	if res.OK() {
		fmt.Println("\nAll good — ready to connect.")
		return
	}
	os.Exit(1)
}

func printStage(label string, stage core.Stage) {
	switch {
	case !stage.Attempted:
		fmt.Printf("  %-11s …  skipped\n", label)
	case stage.OK:
		fmt.Printf("  %-11s …  OK    %s\n", label, stage.Detail)
	default:
		fmt.Printf("  %-11s …  FAIL  %s\n", label, stage.Detail)
	}
}

// runInit writes the example config to the target path, refusing to
// clobber an existing file.
func runInit(cfgPath string) {
	path := cfgPath
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			fatalf("%v", err)
		}
		path = p
	}

	if _, err := os.Stat(path); err == nil {
		fatalf("%s already exists — refusing to overwrite it.", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		fatalf("%v", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(config.ExampleTOML), 0o600); err != nil {
		fatalf("write config: %v", err)
	}
	fmt.Printf("Wrote an example config to %s\nEdit it to match your Sycamore instances, then run `sycamore-tui`.\n", path)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sycamore-tui: "+format+"\n", args...)
	os.Exit(1)
}

// usage prints help with GNU-style --flag names (the stdlib default
// renders a single dash). Flags are still accepted with either one or
// two dashes; this only controls presentation.
func usage() {
	var help strings.Builder
	help.WriteString("sycamore-tui — terminal client for the Sycamore agent.\n\n")
	help.WriteString("Usage:\n  sycamore-tui [flags]\n\nFlags:\n")
	flag.VisitAll(func(option *flag.Flag) {
		fmt.Fprintf(&help, "  --%-10s %s\n", option.Name, option.Usage)
	})
	if _, err := io.WriteString(flag.CommandLine.Output(), help.String()); err != nil {
		os.Exit(2) // nowhere left to report it; the exit status says so
	}
}
