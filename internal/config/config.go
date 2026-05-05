// Package config loads the server's TOML configuration file.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration is time.Duration with TOML/text unmarshalling, so config can use
// strings like "30s" or "5m".
type Duration time.Duration

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

// Config is the top-level configuration for the server.
type Config struct {
	Listen   string   `toml:"listen"`
	Secret   string   `toml:"secret"`
	Strict   bool     `toml:"strict"`
	CacheDir string   `toml:"cache_dir"`
	Sources  []Source `toml:"sources"`
}

// Source describes one input proto. Exactly one of URL, Path, Glob must be set.
//
//   - URL  — fetched once and cached under CacheDir.
//   - Path — read directly from disk.
//   - Glob — expanded with filepath.Glob; each match is parsed.
type Source struct {
	URL  string `toml:"url"`
	Path string `toml:"path"`
	Glob string `toml:"glob"`

	// RefreshInterval, if > 0, polls the source on this cadence:
	//   URL  — conditional GET with stored ETag/Last-Modified.
	//   Path — stat for mtime/size change.
	//   Glob — re-expand and stat each match.
	// Unset means manual-only refresh via POST /v1/refresh.
	//
	// Values are Go duration strings: "30s", "5m", "1h", "1h30m", "500ms".
	// Valid units: ns, us, ms, s, m, h. No "d"/"w" — use "24h" / "168h".
	RefreshInterval Duration `toml:"refresh_interval"`
}

// Load reads, defaults, env-overrides, and validates a TOML config file.
func Load(path string) (*Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	c.applyDefaults()
	c.applyEnvOverrides()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.CacheDir == "" {
		c.CacheDir = "./.cache"
	}
}

// Env vars override their config-file equivalents. Useful for keeping secrets
// out of the file while still version-controlling everything else.
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("API_SECRET"); v != "" {
		c.Secret = v
	}
	if v := os.Getenv("LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("CACHE_DIR"); v != "" {
		c.CacheDir = v
	}
}

// Validate enforces the structural invariants. Secret is *not* required —
// the auth middleware decides what to do with an empty secret.
func (c *Config) Validate() error {
	if len(c.Sources) == 0 {
		return errors.New("config: at least one [[sources]] entry is required")
	}
	for i, s := range c.Sources {
		set := 0
		if s.URL != "" {
			set++
		}
		if s.Path != "" {
			set++
		}
		if s.Glob != "" {
			set++
		}
		if set != 1 {
			return fmt.Errorf("config: sources[%d] must set exactly one of url/path/glob", i)
		}
	}
	return nil
}
