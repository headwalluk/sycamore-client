package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validTOML = `
default = "prod"

[instance.prod]
host = "prod-host.example"
user = "sycamore"
socket = "/var/sycamore/prod.sock"

[instance.dev]
host = "dev-host.example"
user = "you"
port = 2222
socket = "/home/you/Projects/sycamore/var/dev.sock"
`

func TestParseValid(t *testing.T) {
	file, err := parse([]byte(validTOML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if file.Default != "prod" {
		t.Errorf("Default = %q, want prod", file.Default)
	}
	if len(file.Instances) != 2 {
		t.Fatalf("got %d instances, want 2", len(file.Instances))
	}

	prod := file.Instances["prod"]
	if prod.Name != "prod" {
		t.Errorf("prod.Name = %q, want prod (filled from map key)", prod.Name)
	}
	if prod.Host != "prod-host.example" || prod.User != "sycamore" {
		t.Errorf("prod host/user wrong: %+v", prod)
	}
	if prod.Socket != "/var/sycamore/prod.sock" {
		t.Errorf("prod.Socket = %q", prod.Socket)
	}

	if got := file.Instances["dev"].Port; got != 2222 {
		t.Errorf("dev.Port = %d, want 2222", got)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"missing host": `
[instance.a]
socket = "/x.sock"
`,
		"missing socket": `
[instance.a]
host = "h"
`,
		"port too high": `
[instance.a]
host = "h"
socket = "/x.sock"
port = 70000
`,
		"default unknown": `
default = "nope"
[instance.a]
host = "h"
socket = "/x.sock"
`,
		"unknown key": `
[instance.a]
host = "h"
socket = "/x.sock"
hostt = "typo"
`,
		"option-shaped host": `
[instance.a]
host = "-oProxyCommand=x"
socket = "/x.sock"
`,
		"option-shaped user": `
[instance.a]
host = "h"
user = "-oX"
socket = "/x.sock"
`,
		"plural table typo": `
[instances.a]
host = "h"
socket = "/x.sock"
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parse([]byte(body)); err == nil {
				t.Fatalf("expected error for %q, got nil", name)
			}
		})
	}
}

func TestParseExpandsIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	file, err := parse([]byte(`
[instance.a]
host = "h"
socket = "/x.sock"
identity = "~/.ssh/id_ed25519"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := filepath.Join(home, ".ssh/id_ed25519")
	if got := file.Instances["a"].Identity; got != want {
		t.Errorf("Identity = %q, want %q", got, want)
	}
}

func TestSocketNotTildeExpanded(t *testing.T) {
	// The socket is a remote path; a literal ~ must survive untouched.
	file, err := parse([]byte(`
[instance.a]
host = "h"
socket = "~/sock/agent.sock"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := file.Instances["a"].Socket; got != "~/sock/agent.sock" {
		t.Errorf("Socket = %q, want the literal remote path", got)
	}
}

func TestResolve(t *testing.T) {
	full, err := parse([]byte(validTOML))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("explicit name", func(t *testing.T) {
		inst, err := full.Resolve("dev")
		if err != nil || inst.Name != "dev" {
			t.Fatalf("Resolve(dev) = %+v, %v", inst, err)
		}
	})

	t.Run("falls back to default", func(t *testing.T) {
		inst, err := full.Resolve("")
		if err != nil || inst.Name != "prod" {
			t.Fatalf("Resolve(\"\") = %+v, %v; want prod", inst, err)
		}
	})

	t.Run("unknown name errors", func(t *testing.T) {
		if _, err := full.Resolve("ghost"); err == nil {
			t.Fatal("expected error for unknown instance")
		}
	})

	t.Run("single instance, no default", func(t *testing.T) {
		file, err := parse([]byte(`
[instance.only]
host = "h"
socket = "/x.sock"
`))
		if err != nil {
			t.Fatal(err)
		}
		inst, err := file.Resolve("")
		if err != nil || inst.Name != "only" {
			t.Fatalf("Resolve(\"\") = %+v, %v; want only", inst, err)
		}
	})

	t.Run("ambiguous, no default", func(t *testing.T) {
		file, err := parse([]byte(`
[instance.a]
host = "h"
socket = "/a.sock"
[instance.b]
host = "h"
socket = "/b.sock"
`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Resolve(""); err == nil {
			t.Fatal("expected ambiguity error with two instances and no default")
		}
	})

	t.Run("no instances", func(t *testing.T) {
		f := &File{}
		if _, err := f.Resolve(""); !errors.Is(err, ErrNoInstances) {
			t.Fatalf("err = %v, want ErrNoInstances", err)
		}
	})
}

func TestDefaultPath(t *testing.T) {
	t.Run("honours XDG_CONFIG_HOME", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
		got, err := DefaultPath()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join("/custom/xdg", "sycamore-client", "config.toml")
		if got != want {
			t.Errorf("DefaultPath = %q, want %q", got, want)
		}
	})

	t.Run("falls back to ~/.config", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		got, err := DefaultPath()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, ".config", "sycamore-client", "config.toml")
		if got != want {
			t.Errorf("DefaultPath = %q, want %q", got, want)
		}
	})
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(validTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Instances) != 2 {
		t.Errorf("got %d instances, want 2", len(f.Instances))
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want wrapping os.ErrNotExist", err)
	}
}

func TestExampleTOMLParses(t *testing.T) {
	// The shipped example must itself be valid, or `--init` hands the
	// operator a config that won't load.
	file, err := parse([]byte(ExampleTOML))
	if err != nil {
		t.Fatalf("ExampleTOML does not parse: %v", err)
	}
	if _, err := file.Resolve(""); err != nil {
		t.Fatalf("ExampleTOML does not resolve a default: %v", err)
	}
	if file.UI.MarkdownStyle != "dark" {
		t.Errorf("ExampleTOML UI.MarkdownStyle = %q, want dark", file.UI.MarkdownStyle)
	}
}

func TestMarkdownStyleDefaulted(t *testing.T) {
	// Omitting [ui] entirely yields the sensible default, not an empty
	// string — the default lives in config, not buried in the UI code.
	f, err := parse([]byte(validTOML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.UI.MarkdownStyle != DefaultMarkdownStyle {
		t.Errorf("MarkdownStyle = %q, want default %q", f.UI.MarkdownStyle, DefaultMarkdownStyle)
	}
}

func TestMarkdownStyleExplicit(t *testing.T) {
	src := validTOML + "\n[ui]\nmarkdown_style = \"dracula\"\n"
	f, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.UI.MarkdownStyle != "dracula" {
		t.Errorf("MarkdownStyle = %q, want dracula", f.UI.MarkdownStyle)
	}
}

func TestMouseCaptureDefaulted(t *testing.T) {
	// Omitting mouse_capture yields the default (true), normalised to a
	// non-nil pointer so callers can dereference safely.
	file, err := parse([]byte(validTOML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if file.UI.MouseCapture == nil {
		t.Fatal("MouseCapture should be normalised to non-nil")
	}
	if *file.UI.MouseCapture != DefaultMouseCapture {
		t.Errorf("MouseCapture = %v, want default %v", *file.UI.MouseCapture, DefaultMouseCapture)
	}
}

func TestMouseCaptureExplicitFalse(t *testing.T) {
	// An explicit false must survive — distinguishable from "omitted".
	src := validTOML + "\n[ui]\nmouse_capture = false\n"
	f, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.UI.MouseCapture == nil || *f.UI.MouseCapture != false {
		t.Errorf("MouseCapture = %v, want explicit false", f.UI.MouseCapture)
	}
}

func TestMarkdownStyleUnknownUIKeyRejected(t *testing.T) {
	// The strict-unknown-key check covers the [ui] table too, so a typo
	// like `markdown_styel` is reported rather than silently dropped.
	src := validTOML + "\n[ui]\nmarkdown_styel = \"dark\"\n"
	if _, err := parse([]byte(src)); err == nil {
		t.Fatal("expected an unknown-key error for a misspelled [ui] key")
	}
}

func TestIdleTimeoutDefaulted(t *testing.T) {
	f, err := parse([]byte(validTOML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.Connections.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("IdleTimeout = %s, want default %s", f.Connections.IdleTimeout, DefaultIdleTimeout)
	}
}

func TestIdleTimeoutParsed(t *testing.T) {
	cases := map[string]time.Duration{`"5m"`: 5 * time.Minute, `"1h30m"`: 90 * time.Minute, `"0"`: 0}
	for value, want := range cases {
		f, err := parse([]byte(validTOML + "\n[connections]\nidle_timeout = " + value + "\n"))
		if err != nil {
			t.Fatalf("idle_timeout = %s: %v", value, err)
		}
		if f.Connections.IdleTimeout != want {
			t.Errorf("idle_timeout = %s: got %s, want %s", value, f.Connections.IdleTimeout, want)
		}
	}
}

func TestIdleTimeoutInvalidRejected(t *testing.T) {
	for _, value := range []string{`"soon"`, `"-5m"`, `"20"`} {
		if _, err := parse([]byte(validTOML + "\n[connections]\nidle_timeout = " + value + "\n")); err == nil {
			t.Errorf("idle_timeout = %s should be rejected", value)
		}
	}
}

func TestOpenOnStartParsed(t *testing.T) {
	f, err := parse([]byte(validTOML + "\n[connections]\nopen_on_start = [\"prod\"]\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(f.Connections.OpenOnStart) != 1 || f.Connections.OpenOnStart[0] != "prod" {
		t.Errorf("OpenOnStart = %v", f.Connections.OpenOnStart)
	}
}

func TestOpenOnStartUnknownInstanceRejected(t *testing.T) {
	_, err := parse([]byte(validTOML + "\n[connections]\nopen_on_start = [\"nosuch\"]\n"))
	if err == nil || !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("an unknown open_on_start name should be rejected, got %v", err)
	}
}

func TestConversationCacheDefaultedAndParsed(t *testing.T) {
	file, err := parse([]byte(validTOML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if file.UI.ConversationCache == nil || *file.UI.ConversationCache != DefaultConversationCache {
		t.Errorf("ConversationCache should default to %d, got %v", DefaultConversationCache, file.UI.ConversationCache)
	}
	file, err = parse([]byte(validTOML + "\n[ui]\nconversation_cache = 0\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *file.UI.ConversationCache != 0 {
		t.Errorf("an explicit 0 should survive, got %d", *file.UI.ConversationCache)
	}
	if _, err := parse([]byte(validTOML + "\n[ui]\nconversation_cache = -1\n")); err == nil {
		t.Error("a negative conversation_cache should be rejected")
	}
}

func TestTeamParsed(t *testing.T) {
	file, err := parse([]byte(`
[instance.zee]
host   = "h"
socket = "/s.sock"
team   = "ops"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := file.Instances["zee"].Team; got != "ops" {
		t.Errorf("Team = %q, want ops", got)
	}
}

func TestParseReportsEveryProblemAtOnce(t *testing.T) {
	_, err := parse([]byte(`
default = "nosuch"

[instance.first]
socket = "/s.sock"

[instance.second]
host = "h"

[connections]
idle_timeout = "soon"
`))
	if err == nil {
		t.Fatal("a config with several problems should be rejected")
	}
	for _, want := range []string{`"first": host is required`, `"second": socket is required`, `default "nosuch"`, `idle_timeout "soon"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should include %q, got:\n%v", want, err)
		}
	}
}

func TestEmojiChoicesDefaultedAndParsed(t *testing.T) {
	file, err := parse([]byte(validTOML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if file.UI.EmojiChoices == nil || *file.UI.EmojiChoices != DefaultEmojiChoices {
		t.Errorf("EmojiChoices should default to %d, got %v", DefaultEmojiChoices, file.UI.EmojiChoices)
	}
	file, err = parse([]byte(validTOML + "\n[ui]\nemoji_choices = 0\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if *file.UI.EmojiChoices != 0 {
		t.Errorf("an explicit 0 should survive, got %d", *file.UI.EmojiChoices)
	}
	if _, err := parse([]byte(validTOML + "\n[ui]\nemoji_choices = -1\n")); err == nil {
		t.Error("a negative emoji_choices should be rejected")
	}
}
