// Package config loads the radiator's YAML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// BranchFilter decides which pipelines a project's tile reflects.
type BranchFilter string

const (
	// BranchDefault watches only each repo's default branch, so the tile
	// answers "is trunk green?" without feature-branch noise.
	BranchDefault BranchFilter = "default"
	// BranchAll watches every branch, matching CircleCI's "All" tab.
	BranchAll BranchFilter = "*"
)

// Config is the whole of the radiator's configuration.
type Config struct {
	TokenFile    string        `yaml:"token_file"`
	Listen       string        `yaml:"listen"`
	PollInterval time.Duration `yaml:"poll_interval"`
	BranchFilter BranchFilter  `yaml:"branch_filter"`
	Exclude      []string      `yaml:"exclude"`
	Orgs         []string      `yaml:"orgs"`
}

// Load reads and validates the config file at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(expand(path))
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	c := &Config{
		Listen:       "127.0.0.1:8770",
		PollInterval: time.Minute,
		BranchFilter: BranchDefault,
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if len(c.Orgs) == 0 {
		return nil, fmt.Errorf("config lists no orgs")
	}
	if c.TokenFile == "" {
		return nil, fmt.Errorf("config sets no token_file")
	}
	// Polling harder than this earns rate limiting without showing you
	// anything a build actually does in the interval.
	if c.PollInterval < 15*time.Second {
		return nil, fmt.Errorf("poll_interval %s is below the 15s floor", c.PollInterval)
	}
	switch c.BranchFilter {
	case BranchDefault, BranchAll:
	default:
		return nil, fmt.Errorf("branch_filter must be %q or %q, got %q",
			BranchDefault, BranchAll, c.BranchFilter)
	}
	return c, nil
}

// Token reads the CircleCI token off disk.
func (c *Config) Token() (string, error) {
	b, err := os.ReadFile(expand(c.TokenFile))
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", c.TokenFile)
	}
	return tok, nil
}

// Excluded reports whether "org/project" matches an exclude pattern.
func (c *Config) Excluded(orgProject string) bool {
	for _, pat := range c.Exclude {
		if ok, err := filepath.Match(pat, orgProject); err == nil && ok {
			return true
		}
	}
	return false
}

func expand(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
