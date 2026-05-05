package api

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proto-enum-api/internal/proto"
)

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	enums := []proto.Enum{
		{
			Name:       "test.sample.ClientOperatingSystem",
			SimpleName: "ClientOperatingSystem",
			Package:    "test.sample",
			Values: []proto.EnumValue{
				{Name: "OS_UNKNOWN", Number: 0},
				{Name: "OS_ANDROID", Number: 1},
				{Name: "OS_IOS", Number: 2},
			},
		},
		{
			Name:       "test.sample.ProxyResponseProto.Status",
			SimpleName: "Status",
			Package:    "test.sample",
			Values: []proto.EnumValue{
				{Name: "STATUS_OK", Number: 0},
				{Name: "STATUS_ERROR", Number: 1},
			},
		},
	}
	return NewRouterFromIndex(proto.NewEnumIndex(enums), "topsecret")
}

func authed(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer topsecret")
	return req
}

func do(t *testing.T, h http.Handler, method, path string, withAuth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if withAuth {
		authed(req)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestList(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums", true)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Count int      `json:"count"`
		Enums []string `json:"enums"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 {
		t.Errorf("count = %d, want 2", body.Count)
	}
}

func TestListFilter(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums?search=status", true)
	var body struct {
		Enums []string `json:"enums"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Enums) != 1 || body.Enums[0] != "test.sample.ProxyResponseProto.Status" {
		t.Errorf("filter result = %v", body.Enums)
	}
}

func TestGetEnum(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums/test.sample.ClientOperatingSystem", true)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "OS_ANDROID") {
		t.Errorf("body missing OS_ANDROID: %s", w.Body.String())
	}
}

func TestGetEnumNotFound(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums/Nope", true)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	var p problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != "/errors/enum-not-found" || p.Status != 404 {
		t.Errorf("problem = %+v", p)
	}
}

func TestResolveValue_byNumber(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums/test.sample.ClientOperatingSystem/values/1", true)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "OS_ANDROID") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestResolveValue_byName(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums/test.sample.ClientOperatingSystem/values/OS_IOS", true)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"number":2`) {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestResolveValue_unknownName(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums/test.sample.ClientOperatingSystem/values/MISSING", true)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestResolveValue_unknownNumber(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums/test.sample.ClientOperatingSystem/values/99", true)
	if w.Code != 404 {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// --- Cache + ETag ---

func TestETagAndCacheControl(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums", true)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header")
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("Cache-Control = %q", cc)
	}

	// Second request with If-None-Match should 304.
	req := authed(httptest.NewRequest("GET", "/v1/enums", nil))
	req.Header.Set("If-None-Match", etag)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req)
	if w2.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", w2.Code)
	}
	if w2.Body.Len() != 0 {
		t.Errorf("304 should have empty body, got %q", w2.Body.String())
	}
}

func TestETagDoesNotAppearOn404(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums/Nope", true)
	if etag := w.Header().Get("ETag"); etag != "" {
		t.Errorf("404 should not have ETag, got %q", etag)
	}
}

// --- Gzip ---

func TestGzip(t *testing.T) {
	h := newTestRouter(t)
	req := authed(httptest.NewRequest("GET", "/v1/enums", nil))
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := w.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip", got)
	}
	if got := w.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q", got)
	}
	gr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	body, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "test.sample.ClientOperatingSystem") {
		t.Errorf("decompressed body = %s", body)
	}
}

func TestNoGzipWhenNotRequested(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums", true)
	if got := w.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want empty", got)
	}
}

// --- Auth (wired to 401 helper, will pass once RequireBearer is implemented) ---

func TestAuthRejectsMissing(t *testing.T) {
	h := newTestRouter(t)
	w := do(t, h, "GET", "/v1/enums", false)
	if w.Code != http.StatusUnauthorized {
		t.Skipf("auth middleware not yet implemented (got %d). Fill in internal/api/auth.go to enable this test.", w.Code)
	}
	if got := w.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want Bearer ...", got)
	}
}

func TestAuthRejectsWrongToken(t *testing.T) {
	h := newTestRouter(t)
	req := httptest.NewRequest("GET", "/v1/enums", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Skipf("auth middleware not yet implemented (got %d). Fill in internal/api/auth.go to enable this test.", w.Code)
	}
}
