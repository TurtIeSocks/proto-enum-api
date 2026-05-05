// Package api wires the HTTP layer for the enum service.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"proto-enum-api/internal/proto"
)

// Handlers bundles the proto index and serves HTTP requests against it.
type Handlers struct {
	Index *proto.EnumIndex
}

// listEnums responds to GET /v1/enums?search=...
func (h *Handlers) listEnums(w http.ResponseWriter, r *http.Request) {
	names := h.Index.List(r.URL.Query().Get("search"))
	writeJSON(w, http.StatusOK, map[string]any{
		"count": len(names),
		"enums": names,
	})
}

// getEnum responds to GET /v1/enums/{name}
func (h *Handlers) getEnum(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	e, ok := h.Index.Get(name)
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

	if _, ok := h.Index.Get(name); !ok {
		writeProblem(w, r, http.StatusNotFound, "enum-not-found", "Enum not found",
			fmt.Sprintf("No enum named %q is indexed.", name))
		return
	}

	if n, err := strconv.ParseInt(key, 10, 32); err == nil {
		valueName, ok := h.Index.ResolveNumber(name, int32(n))
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

	number, ok := h.Index.ResolveName(name, key)
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
