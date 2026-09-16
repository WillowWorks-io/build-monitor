package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = `
token_file: /tmp/token
orgs:
  - gh/acme
`

func TestLoadDefaults(t *testing.T) {
	c, err := Load(write(t, minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8770" {
		t.Errorf("Listen = %q, want the loopback default", c.Listen)
	}
	if c.PollInterval != time.Minute {
		t.Errorf("PollInterval = %v, want 1m", c.PollInterval)
	}
	// Watching only trunk is the classic build-monitor semantic: it
	// answers "is trunk green?" without feature-branch noise.
	if c.BranchFilter != BranchDefault {
		t.Errorf("BranchFilter = %q, want %q", c.BranchFilter, BranchDefault)
	}
}

func TestLoadRejectsBadConfigs(t *testing.T) {
	cases := map[string]string{
		"no orgs":           "token_file: /tmp/token\norgs: []\n",
		"no token_file":     "orgs:\n  - gh/acme\n",
		"poll below floor":  minimal + "poll_interval: 5s\n",
		"bad branch_filter": minimal + "branch_filter: sometimes\n",
		"not yaml":          "{{{",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Error("expected an error, got none")
			}
		})
	}

	if _, err := Load(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("a missing config file should error")
	}
}

func TestPollIntervalFloor(t *testing.T) {
	// Polling harder than the floor earns rate limiting without showing
	// anything a build actually does in the interval.
	if _, err := Load(write(t, minimal+"poll_interval: 15s\n")); err != nil {
		t.Errorf("15s sits exactly on the floor and should load: %v", err)
	}
	if _, err := Load(write(t, minimal+"poll_interval: 14s\n")); err == nil {
		t.Error("14s is below the floor and should be rejected")
	}
}

func TestBranchFilterAllIsAccepted(t *testing.T) {
	c, err := Load(write(t, minimal+`branch_filter: "*"`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.BranchFilter != BranchAll {
		t.Errorf("BranchFilter = %q, want %q", c.BranchFilter, BranchAll)
	}
}

func TestExcluded(t *testing.T) {
	c := &Config{Exclude: []string{"*/archived-*", "acme/legacy-billing", "sandbox/*"}}
	cases := map[string]bool{
		"acme/archived-widgets": true,
		"other/archived-thing":  true,
		"acme/legacy-billing":   true,
		"sandbox/anything":      true,
		"acme/widgets":          false,
		"acme/archive-widgets":  false, // "archive-" is not "archived-"
		"":                      false,
	}
	for in, want := range cases {
		if got := c.Excluded(in); got != want {
			t.Errorf("Excluded(%q) = %v, want %v", in, got, want)
		}
	}

	if (&Config{}).Excluded("acme/widgets") {
		t.Error("no patterns should exclude nothing")
	}
}

func TestTokenReadsAndTrims(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	if err := os.WriteFile(tok, []byte("  secret-value\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Config{TokenFile: tok}
	got, err := c.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got != "secret-value" {
		t.Errorf("Token() = %q, want the value with whitespace trimmed", got)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Config{TokenFile: empty}).Token(); err == nil {
		t.Error("an empty token file should error rather than send a blank header")
	}

	if _, err := (&Config{TokenFile: filepath.Join(dir, "absent")}).Token(); err == nil {
		t.Error("a missing token file should error")
	}
}

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory available")
	}
	got := expand("~/.config/circleci/token")
	if !strings.HasPrefix(got, home) {
		t.Errorf("expand did not resolve ~: %q", got)
	}
	if got := expand("/absolute/path"); got != "/absolute/path" {
		t.Errorf("absolute paths must pass through unchanged, got %q", got)
	}
}
