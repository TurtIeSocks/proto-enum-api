// Package api wires the HTTP layer for the enum service.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"proto-enum-api/internal/proto"
)

// IndexProvider is the slice of *proto.Manager the API depends on. Pulling
// it out as an interface keeps tests free of the manager's goroutines.
type IndexProvider interface {
	Index() *proto.EnumIndex
	Stale() bool
}

// Handlers bundles the proto manager and serves HTTP requests against it.
type Handlers struct {
	Provider IndexProvider
	Manager  *proto.Manager // nil-safe: refresh/healthz return 503 when nil
}

type ListResp struct {
	Count uint     `json:"count"`
	Enums []string `json:"enums"`
	Stale bool     `json:"stale,omitempty"`
}

// listEnums responds to GET /v1/enums?search=...
func (h *Handlers) listEnums(w http.ResponseWriter, r *http.Request) {
	idx := h.Provider.Index()
	names := idx.List(r.URL.Query().Get("search"))
	writeJSON(w, http.StatusOK, ListResp{
		Count: uint(len(names)),
		Enums: names,
		Stale: h.Provider.Stale(),
	})
}

// getEnum responds to GET /v1/enums/{name}
func (h *Handlers) getEnum(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	e, ok := h.Provider.Index().Get(name)
	if !ok {
		writeProblem(w, r, http.StatusNotFound, "enum-not-found", "Enum not found",
			fmt.Sprintf("No enum named %q is indexed.", name))
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// resolveValue responds to GET /v1/enums/{name}/values/{key}
//
// key auto-detects: an all-digits key (optionally signed) is looked up by
// number; otherwise it's looked up by value name. This is unambiguous because
// proto enum value names are identifiers — they cannot start with a digit or
// minus sign.
func (h *Handlers) resolveValue(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	key := r.PathValue("key")
	idx := h.Provider.Index()

	if _, ok := idx.Get(name); !ok {
		writeProblem(w, r, http.StatusNotFound, "enum-not-found", "Enum not found",
			fmt.Sprintf("No enum named %q is indexed.", name))
		return
	}

	if n, err := strconv.ParseInt(key, 10, 32); err == nil {
		valueName, ok := idx.ResolveNumber(name, int32(n))
		if !ok {
			writeProblem(w, r, http.StatusNotFound, "value-not-found", "Value not found",
				fmt.Sprintf("Enum %q has no value with number %d.", name, n))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enum":   name,
			"name":   valueName,
			"number": int32(n),
		})
		return
	}

	number, ok := idx.ResolveName(name, key)
	if !ok {
		writeProblem(w, r, http.StatusNotFound, "value-not-found", "Value not found",
			fmt.Sprintf("Enum %q has no value named %q.", name, key))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enum":   name,
		"name":   key,
		"number": number,
	})
}

// healthz responds to GET /healthz with per-source refresh status. Unauthed
// by convention so liveness probes don't need a token.
func (h *Handlers) healthz(w http.ResponseWriter, r *http.Request) {
	if h.Manager == nil {
		writeJSON(w, http.StatusOK, map[string]any{"stale": false, "enums": h.Provider.Index().Len()})
		return
	}
	writeJSON(w, http.StatusOK, h.Manager.Health())
}

// refresh responds to POST /v1/refresh by triggering a synchronous refresh of
// every source. Returns 200 with a summary, or 502 if any source failed.
func (h *Handlers) refresh(w http.ResponseWriter, r *http.Request) {
	if h.Manager == nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "no-manager",
			"Refresh unavailable", "Server was started without a refresh manager.")
		return
	}
	changed, errs := h.Manager.RefreshAll(r.Context())
	body := map[string]any{
		"changed": changed,
		"sources": len(h.Manager.Health().Sources),
	}
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		body["errors"] = msgs
		writeJSON(w, http.StatusBadGateway, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// problem is the RFC 7807 application/problem+json response shape.
type problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, slug, title, detail string) {
	p := problem{
		Type:     "/errors/" + slug,
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}
