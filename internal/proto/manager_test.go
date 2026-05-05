package proto

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const sampleProto = `syntax = "proto3";
package smoke;
enum Color { COLOR_RED = 0; COLOR_BLUE = 1; }
`

const sampleProtoV2 = `syntax = "proto3";
package smoke;
enum Color { COLOR_RED = 0; COLOR_BLUE = 1; COLOR_GREEN = 2; }
`

// TestManager_URL_ConditionalGet verifies that on the second fetch the loader
// sends If-None-Match and the server's 304 leaves the cached body alone.
func TestManager_URL_ConditionalGet(t *testing.T) {
	const etag = `"v1"`
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(sampleProto))
	}))
	defer srv.Close()

	dir := t.TempDir()
	loader := &Loader{
		Sources:  []Source{{URL: srv.URL + "/schema.proto"}},
		CacheDir: dir,
	}
	mgr, err := NewManager(loader)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Stop()
	if got := mgr.Index().Len(); got != 1 {
		t.Fatalf("initial enums = %d, want 1", got)
	}
	if h := atomic.LoadInt32(&hits); h != 1 {
		t.Errorf("hits after initial load = %d, want 1", h)
	}

	// Manual refresh: server returns 304, no rebuild.
	changed, errs := mgr.RefreshAll(context.Background())
	if len(errs) > 0 {
		t.Fatalf("RefreshAll errs: %v", errs)
	}
	if changed != 0 {
		t.Errorf("changed = %d, want 0 (304 path)", changed)
	}
	if h := atomic.LoadInt32(&hits); h != 2 {
		t.Errorf("hits after refresh = %d, want 2", h)
	}

	// Sidecar should exist with the etag we sent.
	matches, _ := filepath.Glob(filepath.Join(dir, "*.meta.json"))
	if len(matches) != 1 {
		t.Fatalf("meta sidecars = %v, want exactly one", matches)
	}
}

// TestManager_URL_BodyChange verifies a 200 response with a new body causes
// a rebuild and the index reflects the new enum values.
func TestManager_URL_BodyChange(t *testing.T) {
	body := sampleProto
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No ETag → loader can't conditionally short-circuit; server always sends body.
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	loader := &Loader{
		Sources:  []Source{{URL: srv.URL + "/schema.proto"}},
		CacheDir: t.TempDir(),
	}
	mgr, err := NewManager(loader)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Stop()

	e, _ := mgr.Index().Get("smoke.Color")
	if len(e.Values) != 2 {
		t.Fatalf("initial values = %d, want 2", len(e.Values))
	}

	body = sampleProtoV2
	changed, errs := mgr.RefreshAll(context.Background())
	if len(errs) > 0 {
		t.Fatalf("RefreshAll errs: %v", errs)
	}
	if changed != 1 {
		t.Errorf("changed = %d, want 1", changed)
	}
	e, _ = mgr.Index().Get("smoke.Color")
	if len(e.Values) != 3 {
		t.Errorf("post-refresh values = %d, want 3", len(e.Values))
	}
}

// TestManager_Path_MTimeChange verifies file sources detect on-disk changes.
func TestManager_Path_MTimeChange(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.proto")
	if err := os.WriteFile(p, []byte(sampleProto), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := &Loader{Sources: []Source{{Path: p}}, CacheDir: t.TempDir()}
	mgr, err := NewManager(loader)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Stop()

	// Bump mtime forward so the fingerprint actually differs (filesystem
	// timestamp resolution can otherwise round to the same value).
	if err := os.WriteFile(p, []byte(sampleProtoV2), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}

	changed, errs := mgr.RefreshAll(context.Background())
	if len(errs) > 0 {
		t.Fatalf("RefreshAll errs: %v", errs)
	}
	if changed != 1 {
		t.Errorf("changed = %d, want 1", changed)
	}
	e, _ := mgr.Index().Get("smoke.Color")
	if len(e.Values) != 3 {
		t.Errorf("post-refresh values = %d, want 3", len(e.Values))
	}
}

// TestManager_FailureKeepsServingStale: a refresh failure should not blow
// away the in-memory index — Stale() flips true instead.
func TestManager_FailureKeepsServingStale(t *testing.T) {
	healthy := int32(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&healthy) == 0 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(sampleProto))
	}))
	defer srv.Close()

	loader := &Loader{
		Sources:  []Source{{URL: srv.URL + "/schema.proto"}},
		CacheDir: t.TempDir(),
	}
	mgr, err := NewManager(loader)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Stop()
	preLen := mgr.Index().Len()

	atomic.StoreInt32(&healthy, 0)
	_, errs := mgr.RefreshAll(context.Background())
	if len(errs) == 0 {
		t.Fatal("expected refresh error")
	}
	if !mgr.Stale() {
		t.Error("Stale() should be true after a failed refresh")
	}
	if mgr.Index().Len() != preLen {
		t.Error("index length changed despite failed refresh")
	}
}
