// Package config loads the sycamore-client configuration file: a TOML
// document declaring one or more named Sycamore instances, each with
// the SSH connection details and the remote socket path needed to reach
// it. It is consumed by both UI binaries — the TUI and the GUI — so it
// lives in core alongside the protocol and tunnel packages.
//
// The file lives at $XDG_CONFIG_HOME/sycamore-client/config.toml
// (falling back to ~/.config/sycamore-client/config.toml). Shape:
//
//	default = "prod"
//
//	[instance.prod]
//	host   = "prod-host.example"   # ssh destination — may be a ~/.ssh/config Host alias
//	user   = "sycamore"          # SSH login user (the identity SO_PEERCRED authenticates)
//	# port     = 22              # optional; omit to defer to ssh_config
//	# identity = "~/.ssh/id_ed25519"   # optional; omit to defer to ssh_config/agent
//	socket = "/var/sycamore/prod.sock"  # path to sycamore.sock on the TARGET host
//
//	[instance.dev]
//	host   = "dev-host.example"
//	user   = "you"
//	socket = "/home/you/Projects/sycamore/var/dev.sock"  # dev-mode repo socket
//
//	[ui]
//	markdown_style = "dark"   # glamour theme for agent replies (TUI)
//	mouse_capture  = true     # wheel-scroll on; false = native text selection
//
// The SSH user is the identity the daemon authenticates over the tunnel
// (see dev-notes/planning-ssh-tunnel.md §5), not the local account — so
// `user` is load-bearing, not cosmetic.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultMarkdownStyle is the glamour theme used to render agent Markdown
// when the config doesn't pick one. The set of valid names is glamour's,
// so it's validated UI-side (the TUI) rather than here — core stays free
// of the glamour dependency and its style vocabulary.
const DefaultMarkdownStyle = "dark"

// DefaultMouseCapture is the default for [ui] mouse_capture: mouse reporting
// on, so the transcript scrolls with the wheel. Turning it off restores the
// terminal's native click-drag text selection (at the cost of wheel-scroll).
const DefaultMouseCapture = true

// DefaultConversationCache is the default for [ui] conversation_cache: how
// many recently shown conversations each agent keeps ready to switch back to.
const DefaultConversationCache = 8

// DefaultEmojiChoices is the default for [ui] emoji_choices: the most
// matches the TUI's emoji chooser shows.
const DefaultEmojiChoices = 5

// DefaultIdleTimeout is the default for [connections] idle_timeout: how
// long an agent's connection may sit unused before it's closed.
const DefaultIdleTimeout = 20 * time.Minute

// File is a parsed, validated configuration document.
type File struct {
	// Default names the instance used when the operator doesn't pick
	// one explicitly. Optional; when set it must match an instance.
	Default string `toml:"default"`

	// Instances maps an instance name (the TOML table key under
	// [instance.<name>]) to its connection details.
	Instances map[string]Instance `toml:"instance"`

	// UI holds client-wide presentation settings, shared across both
	// front-ends.
	UI UI `toml:"ui"`

	// Connections holds connection-lifecycle settings.
	Connections Connections `toml:"connections"`
}

// Connections groups connection-lifecycle settings under [connections].
type Connections struct {
	// IdleTimeoutText is idle_timeout as written, a Go duration such as
	// "20m" or "1h30m"; "0" never closes an idle connection. Empty takes
	// DefaultIdleTimeout.
	IdleTimeoutText string `toml:"idle_timeout"`

	// IdleTimeout is IdleTimeoutText parsed by the loader.
	IdleTimeout time.Duration `toml:"-"`

	// OpenOnStart names instances to connect in the background at
	// start-up, besides the one shown. Each must name an instance.
	OpenOnStart []string `toml:"open_on_start"`
}

// UI groups client-wide presentation settings under the [ui] table.
type UI struct {
	// MarkdownStyle names the glamour theme used to render agent
	// Markdown replies in the TUI (e.g. "dark", "light", "dracula").
	// Empty defaults to DefaultMarkdownStyle. The value is validated
	// against glamour's known styles by the TUI, not here.
	MarkdownStyle string `toml:"markdown_style"`

	// MouseCapture controls whether the TUI captures the mouse (enabling
	// wheel-scroll) at startup. A pointer so an omitted key is
	// distinguishable from an explicit false; the loader normalises nil
	// to DefaultMouseCapture, so it is always non-nil after Load. The
	// state can still be toggled at runtime (ctrl+o in the TUI).
	MouseCapture *bool `toml:"mouse_capture"`

	// ConversationCache is how many recently shown conversations each agent
	// keeps, besides the one on screen, so switching back is instant and a
	// reply to one you've left lands in it. A pointer so an explicit 0 (keep
	// none) is distinguishable from omitted; the loader normalises nil to
	// DefaultConversationCache.
	ConversationCache *int `toml:"conversation_cache"`

	// Editor is the command /edit runs to compose a message, e.g. "vim" or
	// "emacsclient -t"; the file to edit is added as the last argument.
	// Empty falls back to $VISUAL, then $EDITOR, then vi (see ResolveEditor).
	Editor string `toml:"editor"`

	// EmojiChoices is the most matches for a partly typed :shortcode: the
	// TUI offers to pick from; more shows nothing, 0 turns the chooser off.
	// A pointer so an explicit 0 is distinguishable from omitted; the loader
	// normalises nil to DefaultEmojiChoices.
	EmojiChoices *int `toml:"emoji_choices"`
}

// RuntimeDir returns $XDG_RUNTIME_DIR, where the client puts its tunnel
// sockets, or "" when it isn't set (the tunnel then uses the system
// temporary directory).
func RuntimeDir() string {
	return os.Getenv("XDG_RUNTIME_DIR")
}

// defaultEditor is the editor used when nothing else names one.
const defaultEditor = "vi"

// ResolveEditor returns the editor command for /edit: the configured one,
// else $VISUAL, else $EDITOR, else vi. It's the one place the client reads
// those variables.
func ResolveEditor(configured string) string {
	editor := strings.TrimSpace(configured)
	for _, variable := range []string{"VISUAL", "EDITOR"} {
		if editor == "" {
			editor = strings.TrimSpace(os.Getenv(variable))
		}
	}
	if editor == "" {
		editor = defaultEditor
	}
	return editor
}

// Instance is the connection detail for a single Sycamore agent.
type Instance struct {
	// Name is the table key (e.g. "prod"). Filled in by the loader from
	// the map key; not read from the file body.
	Name string `toml:"-"`

	// Host is the ssh destination. May be a bare hostname or a
	// ~/.ssh/config Host alias — shelling out to ssh means aliases,
	// ProxyJump, etc. resolve for free. Required.
	Host string `toml:"host"`

	// User is the SSH login user. Optional — omit to let ssh_config
	// decide. This is the POSIX identity the daemon authenticates via
	// SO_PEERCRED on the target host.
	User string `toml:"user"`

	// Port is the SSH port. Optional; 0 means defer to ssh_config.
	Port int `toml:"port"`

	// Identity is the path to an SSH private key. Optional — omit to
	// defer to ssh_config / the agent. A leading ~ is expanded against
	// the local home directory at load time.
	Identity string `toml:"identity"`

	// Socket is the absolute path to sycamore.sock on the TARGET host.
	// Not tilde-expanded locally — it is a remote path ssh forwards
	// verbatim. Required.
	Socket string `toml:"socket"`

	// Team optionally groups instances in the client's agent lists. It is
	// a label only; it changes nothing about the connection.
	Team string `toml:"team"`
}

// ErrNoInstances is returned by Resolve when the file declares none.
var ErrNoInstances = errors.New("config: no instances defined")

// DefaultPath returns the conventional config-file location, honouring
// $XDG_CONFIG_HOME and falling back to ~/.config.
func DefaultPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "sycamore-client", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "sycamore-client", "config.toml"), nil
}

// LoadDefault loads the config from DefaultPath. A missing file surfaces
// as an error wrapping os.ErrNotExist, so callers can offer to write an
// example via errors.Is(err, os.ErrNotExist).
func LoadDefault() (*File, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return Load(path)
}

// Load reads, parses, and validates the config file at path. Unknown
// keys are rejected so a typo in a hand-edited file is reported rather
// than silently ignored.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	f, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return f, nil
}

// parse decodes and validates raw TOML. Split out from Load so tests
// can exercise content cases without touching the filesystem.
func parse(data []byte) (*File, error) {
	var file File
	md, err := toml.Decode(string(data), &file)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	// Reject unknown keys — catches typos like `hostt =` or a
	// misplaced `[instances.prod]` (note the plural) that would
	// otherwise be silently dropped.
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("unknown key(s): %s", strings.Join(keys, ", "))
	}

	if err := file.normalizeAndValidate(); err != nil {
		return nil, err
	}
	return &file, nil
}

// normalizeAndValidate fills in each Instance's Name from its map key,
// expands the local identity path, and checks the invariants, reporting
// every problem at once.
func (f *File) normalizeAndValidate() error {
	var problems []error

	// Stable ordering so error messages are deterministic.
	names := make([]string, 0, len(f.Instances))
	for name := range f.Instances {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		inst := f.Instances[name]
		inst.Name = name

		if strings.TrimSpace(inst.Host) == "" {
			problems = append(problems, fmt.Errorf("instance %q: host is required", name))
		}
		if strings.TrimSpace(inst.Socket) == "" {
			problems = append(problems, fmt.Errorf("instance %q: socket is required", name))
		}
		if strings.HasPrefix(inst.Host, "-") || strings.HasPrefix(inst.User, "-") {
			problems = append(problems, fmt.Errorf("instance %q: host and user can't start with '-'", name))
		}
		if inst.Port < 0 || inst.Port > 65535 {
			problems = append(problems, fmt.Errorf("instance %q: port %d out of range (1-65535)", name, inst.Port))
		}
		expanded, err := expandTilde(inst.Identity)
		if err != nil {
			problems = append(problems, fmt.Errorf("instance %q: %w", name, err))
		}
		inst.Identity = expanded

		f.Instances[name] = inst
	}

	if f.Default != "" {
		if _, ok := f.Instances[f.Default]; !ok {
			problems = append(problems, fmt.Errorf("default %q does not match any instance", f.Default))
		}
	}

	// Sensible default so the TUI never has to bury one in code; an
	// unrecognised value is left as-is for the TUI to reject against
	// glamour's actual style set.
	if strings.TrimSpace(f.UI.MarkdownStyle) == "" {
		f.UI.MarkdownStyle = DefaultMarkdownStyle
	}

	// Normalise the optional mouse_capture to a concrete value, so callers
	// can dereference without a nil check.
	if f.UI.MouseCapture == nil {
		capture := DefaultMouseCapture
		f.UI.MouseCapture = &capture
	}

	if f.UI.ConversationCache == nil {
		cacheSize := DefaultConversationCache
		f.UI.ConversationCache = &cacheSize
	}
	if *f.UI.ConversationCache < 0 {
		problems = append(problems, fmt.Errorf("ui: conversation_cache %d must not be negative", *f.UI.ConversationCache))
	}

	if f.UI.EmojiChoices == nil {
		choices := DefaultEmojiChoices
		f.UI.EmojiChoices = &choices
	}
	if *f.UI.EmojiChoices < 0 {
		problems = append(problems, fmt.Errorf("ui: emoji_choices %d must not be negative", *f.UI.EmojiChoices))
	}

	f.Connections.IdleTimeout = DefaultIdleTimeout
	if text := strings.TrimSpace(f.Connections.IdleTimeoutText); text != "" {
		idle, err := time.ParseDuration(text)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("connections: idle_timeout %q is not a duration (e.g. \"20m\", or \"0\" for never)", text))
		case idle < 0:
			problems = append(problems, fmt.Errorf("connections: idle_timeout %q must not be negative", text))
		default:
			f.Connections.IdleTimeout = idle
		}
	}

	for _, name := range f.Connections.OpenOnStart {
		if _, ok := f.Instances[name]; !ok {
			problems = append(problems, fmt.Errorf("connections: open_on_start names %q, which is not an instance (have: %s)", name, f.instanceList()))
		}
	}

	return errors.Join(problems...)
}

// Resolve returns the instance to connect to. An explicit name wins; an
// empty name falls back to the file's `default`, and failing that to the
// sole instance when exactly one is defined. Any other empty-name case
// is ambiguous and returns an error naming the choices.
func (f *File) Resolve(name string) (Instance, error) {
	if len(f.Instances) == 0 {
		return Instance{}, ErrNoInstances
	}

	if name != "" {
		inst, ok := f.Instances[name]
		if !ok {
			return Instance{}, fmt.Errorf("config: no instance named %q (have: %s)", name, f.instanceList())
		}
		return inst, nil
	}

	if f.Default != "" {
		// Presence already checked in validation.
		return f.Instances[f.Default], nil
	}

	if len(f.Instances) == 1 {
		var sole Instance
		for _, inst := range f.Instances {
			sole = inst
		}
		return sole, nil
	}

	return Instance{}, fmt.Errorf("config: no instance specified and no default set (have: %s)", f.instanceList())
}

// instanceList returns the instance names sorted, for error messages.
func (f *File) instanceList() string {
	names := make([]string, 0, len(f.Instances))
	for name := range f.Instances {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// expandTilde rewrites a leading "~/" to the local home directory. A
// bare "~" is treated the same. Paths without a leading tilde — and the
// empty string — are returned unchanged.
func expandTilde(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		// e.g. "~user/..." — we don't resolve other users' homes.
		return "", fmt.Errorf("cannot expand %q (only ~/ is supported)", path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand %q: %w", path, err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// ExampleTOML is a commented starter config a UI can write on first run
// (e.g. behind a `--init` flag) so the operator has a template to edit.
const ExampleTOML = `# sycamore-client configuration.
#
# Declare one [instance.<name>] table per Sycamore agent you connect to.
# 'default' picks the instance used when you don't name one explicitly.

default = "prod"

[instance.prod]
host   = "prod-host.example"            # ssh destination (may be a ~/.ssh/config Host alias)
user   = "sycamore"                   # SSH login user — the identity the daemon authenticates
# port     = 22                       # optional; omit to defer to ~/.ssh/config
# identity = "~/.ssh/id_ed25519"      # optional; omit to defer to ssh-agent / ~/.ssh/config
socket = "/var/sycamore/prod.sock"     # path to sycamore.sock on the TARGET host
# team   = "ops"                        # optional; groups agents in the /switch list

# A development instance running from source (npm run dev). Its socket
# lives in the daemon repo's var/ directory, not /var/sycamore/.
[instance.dev]
host   = "dev-host.example"
user   = "you"
socket = "/home/you/Projects/sycamore/var/dev.sock"

# Client-wide presentation settings.
[ui]
# Glamour theme for rendering the agent's Markdown replies in the TUI.
# One of: dark, light, dracula, tokyo-night, pink, ascii, notty.
markdown_style = "dark"

# Capture the mouse for wheel-scrolling the transcript. Set false to start
# with the terminal's native click-drag text selection instead (you can
# also toggle this at runtime with ctrl+o). Keyboard scroll (PgUp/PgDn and
# the arrows) works regardless.
mouse_capture = true

# How many recently viewed conversations each agent keeps ready, besides the
# one on screen: switching back to one is instant, and a reply to one you've
# left lands in it. 0 keeps none.
conversation_cache = 8

# The editor /edit opens to compose a long message (the file is added as the
# last argument). Omit to use $VISUAL, then $EDITOR, then vi.
# editor = "emacsclient -t"

# Typing :thumbsup: in the TUI gives 👍. A partly typed code (two or more
# letters) with at most this many matches opens a chooser; 0 turns it off.
emoji_choices = 5

# Connection lifecycle.
[connections]
# Close an agent's connection (and its SSH tunnel) after this long unused.
# It reopens when you next use the agent. The agent on screen, and one
# with a reply still coming, are never closed. "0" keeps them open.
idle_timeout = "20m"

# Agents to connect in the background at start-up, besides the one shown,
# so switching to them (/connect <name>) is instant. Empty by default.
# open_on_start = ["dev"]
`
