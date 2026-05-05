package api

import (
	"compress/gzip"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// cacheControlValue applies to every cacheable response. The index is
// refreshable at runtime, so we stay short and require revalidation — the
// ETag handshake (304) is cheap, and `immutable` would be a lie now that
// the corpus can change.
const cacheControlValue = "public, max-age=60, must-revalidate"

// Cache adds ETag + Cache-Control headers on 200 responses and short-circuits
// matching If-None-Match requests with 304 Not Modified.
//
// etagFn is called per-request so the ETag tracks the current index even
// after a refresh swaps it.
func Cache(etagFn func() string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		etag := etagFn()
		if matches(r.Header.Get("If-None-Match"), etag) {
			h := w.Header()
			h.Set("ETag", etag)
			h.Set("Cache-Control", cacheControlValue)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		next.ServeHTTP(&cacheWriter{ResponseWriter: w, etag: etag}, r)
	})
}

// matches handles the simple cases of If-None-Match: a single value (with or
// without quotes) and the wildcard "*". Multi-value lists fall back to
// substring matching, which is good enough for our single-ETag world.
func matches(header, etag string) bool {
	if header == "" {
		return false
	}
	if strings.TrimSpace(header) == "*" {
		return true
	}
	return strings.Contains(header, etag)
}

type cacheWriter struct {
	http.ResponseWriter
	etag        string
	wroteHeader bool
}

func (cw *cacheWriter) WriteHeader(status int) {
	if cw.wroteHeader {
		return
	}
	if status == http.StatusOK {
		h := cw.Header()
		h.Set("ETag", cw.etag)
		h.Set("Cache-Control", cacheControlValue)
	}
	cw.ResponseWriter.WriteHeader(status)
	cw.wroteHeader = true
}

func (cw *cacheWriter) Write(b []byte) (int, error) {
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	return cw.ResponseWriter.Write(b)
}

// Gzip compresses responses when the client advertises Accept-Encoding: gzip.
// Skipped otherwise, so a curl with no Accept-Encoding still gets readable
// JSON without manual decompression.
func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Encoding", "gzip")
		h.Set("Vary", "Accept-Encoding")
		// Length will be wrong for the compressed body; safest to drop it.
		h.Del("Content-Length")

		gw := gzip.NewWriter(w)
		defer gw.Close()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gw: gw}, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gw *gzip.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) { return g.gw.Write(b) }

// Logger emits one line per request after the inner handler returns,
// recording client IP, method, path, response status, response size, and
// wall-clock duration.
//
// Place this as the outermost middleware so 401s from RequireBearer and
// 304s from Cache are still observed — those layers write status codes
// directly and would be invisible to a logger sitting deeper in the chain.
//
// Response size is measured at this layer, so for gzipped responses it
// reflects the on-the-wire (compressed) byte count, not the original JSON.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(lw, r)
		log.Printf("%s %s %s -> %d %dB (%s)",
			clientIP(r), r.Method, r.URL.Path, lw.status, lw.bytes, time.Since(start))
	})
}

// clientIP returns the request's source address with the port stripped,
// or "unknown" if RemoteAddr is unset (e.g. synthetic requests). Note that
// behind a proxy this is the proxy's address — honoring X-Forwarded-For
// would require trusting the proxy, which we don't configure here.
func clientIP(r *http.Request) string {
	if r.RemoteAddr == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// loggingWriter remembers the status passed to WriteHeader and the byte
// count passed to Write so Logger can report them after the handler chain
// returns. Defaults status to 200 because Go's net/http treats a Write
// without an explicit WriteHeader as an implicit 200.
type loggingWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (lw *loggingWriter) WriteHeader(status int) {
	lw.status = status
	lw.ResponseWriter.WriteHeader(status)
}

func (lw *loggingWriter) Write(b []byte) (int, error) {
	n, err := lw.ResponseWriter.Write(b)
	lw.bytes += n
	return n, err
}
