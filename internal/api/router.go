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
// Middleware order (outermost first):
//
//	RequireBearer  — auth gate; rejects with 401 before any work happens
//	Cache          — ETag + Cache-Control + If-None-Match handling
//	Gzip           — opportunistic compression for clients that opt in
//	mux            — actual routing
func NewRouter(idx *proto.EnumIndex, secret string) http.Handler {
	h := &Handlers{Index: idx}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/enums", h.listEnums)
	mux.HandleFunc("GET /v1/enums/{name}", h.getEnum)
	mux.HandleFunc("GET /v1/enums/{name}/values/{key}", h.resolveValue)

	etag := computeETag(idx)
	return RequireBearer(secret, Cache(etag, Gzip(mux)))
}

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
