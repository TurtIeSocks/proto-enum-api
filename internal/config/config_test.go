package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_minimal(t *testing.T) {
	p := writeTemp(t, `
secret = "abc"
[[sources]]
path = "x.proto"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" {
		t.Errorf("default Listen = %q", c.Listen)
	}
	if c.CacheDir != "./.cache" {
		t.Errorf("default CacheDir = %q", c.CacheDir)
	}
	if c.Secret != "abc" {
		t.Errorf("Secret = %q", c.Secret)
	}
}

func TestLoad_envOverrides(t *testing.T) {
	t.Setenv("API_SECRET", "fromenv")
	t.Setenv("LISTEN", ":9999")
	p := writeTemp(t, `
secret = "fromfile"
listen = ":1111"
[[sources]]
path = "x.proto"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Secret != "fromenv" {
		t.Errorf("Secret = %q, want fromenv", c.Secret)
	}
	if c.Listen != ":9999" {
		t.Errorf("Listen = %q, want :9999", c.Listen)
	}
}

func TestValidate_noSources(t *testing.T) {
	p := writeTemp(t, `secret = "abc"`)
	if _, err := Load(p); err == nil {
		t.Error("expected error for missing sources")
	}
}

func TestValidate_multipleFieldsPerSource(t *testing.T) {
	p := writeTemp(t, `
secret = "abc"
[[sources]]
url = "https://example.com/x.proto"
path = "x.proto"
`)
	if _, err := Load(p); err == nil {
		t.Error("expected error for source with both url and path")
	}
}

func TestValidate_emptySource(t *testing.T) {
	p := writeTemp(t, `
secret = "abc"
[[sources]]
`)
	if _, err := Load(p); err == nil {
		t.Error("expected error for source with no fields set")
	}
}
