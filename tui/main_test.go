package main

import (
	"strings"
	"testing"

	"github.com/headwalluk/sycamore-client/core/config"
)

func TestDirectSocket(t *testing.T) {
	cases := []struct {
		name         string
		socketFlag   string
		instanceFlag string
		probe        bool
		args         []string
		wantPath     string
		wantErrs     []string
	}{
		{name: "nothing: config-driven"},
		{name: "instance only", instanceFlag: "prod"},
		{name: "socket flag", socketFlag: "/var/sycamore/a.sock", wantPath: "/var/sycamore/a.sock"},
		{name: "positional path", args: []string{"/var/sycamore/a.sock"}, wantPath: "/var/sycamore/a.sock"},
		{
			name:     "two positionals",
			args:     []string{"/var/sycamore/a.sock", "/var/sycamore/b.sock"},
			wantErrs: []string{"at most one socket path"},
		},
		{
			name:     "flag after the path",
			args:     []string{"/var/sycamore/a.sock", "--config", "x.toml"},
			wantErrs: []string{"flags go before the path"},
		},
		{
			name:       "flag and positional",
			socketFlag: "/var/sycamore/a.sock",
			args:       []string{"/var/sycamore/b.sock"},
			wantPath:   "/var/sycamore/a.sock",
			wantErrs:   []string{"give the socket path once"},
		},
		{
			name:         "socket with instance and probe",
			instanceFlag: "prod",
			probe:        true,
			args:         []string{"/var/sycamore/a.sock"},
			wantPath:     "/var/sycamore/a.sock",
			wantErrs:     []string{"--instance can't", "--probe tests"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path, err := directSocket(testCase.socketFlag, testCase.instanceFlag, testCase.probe, testCase.args)
			if path != testCase.wantPath {
				t.Errorf("path = %q, want %q", path, testCase.wantPath)
			}
			if len(testCase.wantErrs) == 0 {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want errors %q, got none", testCase.wantErrs)
			}
			for _, wantErr := range testCase.wantErrs {
				if !strings.Contains(err.Error(), wantErr) {
					t.Errorf("error %q doesn't mention %q", err, wantErr)
				}
			}
		})
	}
}

func TestUISettings(t *testing.T) {
	style, capture := uiSettings(nil)
	if style != config.DefaultMarkdownStyle || capture != config.DefaultMouseCapture {
		t.Errorf("nil config = (%q, %v), want the defaults (%q, %v)",
			style, capture, config.DefaultMarkdownStyle, config.DefaultMouseCapture)
	}

	noCapture := false
	cfg := &config.File{UI: config.UI{MarkdownStyle: "light", MouseCapture: &noCapture}}
	style, capture = uiSettings(cfg)
	if style != "light" || capture {
		t.Errorf("configured = (%q, %v), want (\"light\", false)", style, capture)
	}
}
