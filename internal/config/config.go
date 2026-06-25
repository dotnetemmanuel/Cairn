// Package config loads Cairn's user configuration from
// ~/.config/cairn/config.yml, applying Event Horizon defaults for anything the
// user omits (or if the file is absent entirely).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dotnetemmanuel/cairn/internal/theme"
	"gopkg.in/yaml.v3"
)

// SectionType selects how a section is loaded.
const (
	SectionSearch        = "search"        // default: a GitHub search filter
	SectionNotifications = "notifications" // the notifications REST feed
)

// Section is a board section. A search section is backed by a GitHub search
// filter; a notifications section pulls the REST notifications feed and ignores
// Filter.
type Section struct {
	Title  string `yaml:"title"`
	Type   string `yaml:"type"` // "" defaults to search
	Filter string `yaml:"filter"`
	// ShowClosed overrides the global ShowClosed for this section (nil = inherit).
	ShowClosed *bool `yaml:"showClosed,omitempty"`
	// ClosedLimit overrides the global cap for this section (0 = inherit).
	ClosedLimit int `yaml:"closedLimit,omitempty"`
}

// Config is the full user configuration. Fields beyond Phase 0 (sections,
// repoPaths, keybindings) are declared now so the schema is stable, but only
// Theme and DefaultTrunk are exercised in Phase 0.
type Config struct {
	Theme        theme.Palette     `yaml:"theme"`
	DefaultTrunk string            `yaml:"defaultTrunk"`
	RepoPaths    map[string]string `yaml:"repoPaths"`
	Sections     []Section         `yaml:"sections"`
	// ShowClosed toggles the recently-closed/merged tail shown under each PR
	// section's open list. Per-section ShowClosed overrides this.
	ShowClosed bool `yaml:"showClosed"`
	// ClosedLimit caps that tail (per-section ClosedLimit overrides it).
	ClosedLimit int `yaml:"closedLimit"`
}

// Default returns the built-in configuration used when no file exists.
func Default() Config {
	return Config{
		Theme:        theme.DefaultPalette(),
		DefaultTrunk: "main",
		RepoPaths:    map[string]string{},
		ShowClosed:   true, // recently-closed tail on by default; configurable
		ClosedLimit:  15,
		Sections: []Section{
			{Title: "My PRs", Filter: "is:open is:pr author:@me"},
			{Title: "Needs my review", Filter: "is:open is:pr review-requested:@me"},
			{Title: "Involved", Filter: "is:open is:pr involves:@me"},
			{Title: "Notifications", Type: SectionNotifications},
		},
	}
}

// Path returns the resolved config file path, honoring XDG_CONFIG_HOME.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cairn", "config.yml"), nil
}

// Load reads the config file, layering it over the defaults so that omitted
// keys keep their default values. A missing file is not an error — defaults are
// returned. A present-but-malformed file is an error.
func Load() (Config, error) {
	cfg := Default()

	path, err := Path()
	if err != nil {
		return cfg, err
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("reading %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}
