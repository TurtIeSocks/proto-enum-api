package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"proto-enum-api/internal/proto"
)

// NewRouter returns the wired-up HTTP handler.
//
// /healthz is intentionally unauthed (liveness probes shouldn't need a token)
// and skips the Cache layer (its body always reflects current state).
//
// Middleware order on the authed paths (outermost first):
//
//	Logger         — records method/path/status/duration for every request
//	RequireBearer  — auth gate; rejects with 401 before any work happens
//	Cache          — ETag + Cache-Control + If-None-Match handling
//	Gzip           — opportunistic compression for clients that opt in
//	mux            — actual routing
func NewRouter(mgr *proto.Manager, secret string) http.Handler {
	provider := managerProvider{mgr}
	h := &Handlers{Provider: provider, Manager: mgr}
	return newRouterWith(h, provider.Index, secret)
}

// NewRouterFromIndex builds a router around a fixed index, no manager. Used
// by tests that don't need refresh behavior.
func NewRouterFromIndex(idx *proto.EnumIndex, secret string) http.Handler {
	provider := staticProvider{idx}
	h := &Handlers{Provider: provider}
	return newRouterWith(h, provider.Index, secret)
}

func newRouterWith(h *Handlers, indexFn func() *proto.EnumIndex, secret string) http.Handler {
	authed := http.NewServeMux()
	authed.HandleFunc("GET /v1/enums", h.listEnums)
	authed.HandleFunc("GET /v1/enums/{name}", h.getEnum)
	authed.HandleFunc("GET /v1/enums/{name}/values/{key}", h.resolveValue)
	authed.HandleFunc("POST /v1/refresh", h.refresh)

	authedChain := RequireBearer(secret, Cache(func() string { return computeETag(indexFn()) }, Gzip(authed)))

	root := http.NewServeMux()
	root.Handle("GET /healthz", http.HandlerFunc(h.healthz))
	root.Handle("/", authedChain)

	return Logger(root)
}

// managerProvider adapts *proto.Manager to IndexProvider.
type managerProvider struct{ m *proto.Manager }

func (p managerProvider) Index() *proto.EnumIndex { return p.m.Index() }
func (p managerProvider) Stale() bool              { return p.m.Stale() }

// staticProvider serves a fixed index — for tests and refresh-less mode.
type staticProvider struct{ idx *proto.EnumIndex }

func (p staticProvider) Index() *proto.EnumIndex { return p.idx }
func (p staticProvider) Stale() bool              { return false }

// computeETag derives a deterministic ETag from the index's enum FQNs and
// value counts. Two servers loading the same proto sources produce the same
// ETag, so a no-op restart doesn't invalidate cached client responses.
func computeETag(idx *proto.EnumIndex) string {
	h := sha256.New()
	for _, name := range idx.List("") {
		e, _ := idx.Get(name)
		fmt.Fprintf(h, "%s|%d\n", name, len(e.Values))
	}
	return `"` + hex.EncodeToString(h.Sum(nil)[:8]) + `"`
}
