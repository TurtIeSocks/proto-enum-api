package proto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	pp "github.com/yoheimuta/go-protoparser/v4"
	"github.com/yoheimuta/go-protoparser/v4/parser"
)

// Source describes a single proto input. Mirrors config.Source — kept as a
// separate type here to keep proto a leaf package with no config dep.
type Source struct {
	URL             string
	Path            string
	Glob            string
	RefreshInterval time.Duration
}

// Kind classifies a source for logging and dispatch.
func (s Source) Kind() string {
	switch {
	case s.URL != "":
		return "url"
	case s.Path != "":
		return "path"
	case s.Glob != "":
		return "glob"
	default:
		return "unknown"
	}
}

// Locator returns the user-facing identifier of the source (URL/path/glob).
func (s Source) Locator() string {
	switch {
	case s.URL != "":
		return s.URL
	case s.Path != "":
		return s.Path
	case s.Glob != "":
		return s.Glob
	default:
		return ""
	}
}

// Loader fetches and parses one or more .proto files into an EnumIndex.
type Loader struct {
	Sources  []Source
	Strict   bool
	CacheDir string
	Client   *http.Client
}

// LoadIndex resolves every Source, parses each resulting file, and merges
// all enums into a single index keyed by fully-qualified name.
func (l *Loader) LoadIndex(ctx context.Context) (*EnumIndex, error) {
	if err := l.ensureCacheDir(); err != nil {
		return nil, err
	}
	var all []Enum
	for i, src := range l.Sources {
		enums, _, err := l.LoadSource(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("source[%d]: %w", i, err)
		}
		all = append(all, enums...)
	}
	return NewEnumIndex(all), nil
}

// SourceFingerprint is the per-source state used to detect changes between
// refreshes. Equal fingerprints mean "nothing changed; skip parsing".
type SourceFingerprint struct {
	// Files is the sorted list of "<path>:<mtimeUnixNano>:<size>" entries
	// for every concrete file that backed the source on the last load.
	// For URL sources this is the single cached file; for Path/Glob it's
	// the resolved on-disk file(s).
	Files []string
}

// Equal reports whether two fingerprints describe the same on-disk state.
func (a SourceFingerprint) Equal(b SourceFingerprint) bool {
	if len(a.Files) != len(b.Files) {
		return false
	}
	for i := range a.Files {
		if a.Files[i] != b.Files[i] {
			return false
		}
	}
	return true
}

// LoadSource resolves a single source to local files, parses them, and
// returns the resulting enums plus a fingerprint suitable for change
// detection. URL sources use a conditional GET so an unchanged remote
// produces no body transfer.
func (l *Loader) LoadSource(ctx context.Context, src Source) ([]Enum, SourceFingerprint, error) {
	if err := l.ensureCacheDir(); err != nil {
		return nil, SourceFingerprint{}, err
	}
	paths, err := l.resolve(ctx, src)
	if err != nil {
		return nil, SourceFingerprint{}, err
	}
	var all []Enum
	for _, p := range paths {
		enums, err := l.parseFile(p)
		if err != nil {
			return nil, SourceFingerprint{}, fmt.Errorf("parse %s: %w", p, err)
		}
		all = append(all, enums...)
	}
	fp, err := fingerprint(paths)
	if err != nil {
		return nil, SourceFingerprint{}, err
	}
	return all, fp, nil
}

func (l *Loader) ensureCacheDir() error {
	if l.CacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(l.CacheDir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	return nil
}

// resolve returns the list of local file paths a Source expands to.
func (l *Loader) resolve(ctx context.Context, src Source) ([]string, error) {
	switch {
	case src.URL != "":
		p, _, err := l.fetchToCache(ctx, src.URL)
		if err != nil {
			return nil, err
		}
		return []string{p}, nil
	case src.Path != "":
		return []string{src.Path}, nil
	case src.Glob != "":
		matches, err := filepath.Glob(src.Glob)
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", src.Glob, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("glob %q matched no files", src.Glob)
		}
		return matches, nil
	default:
		return nil, errors.New("source has no url/path/glob")
	}
}

// fingerprint stats every path and returns a stable per-source fingerprint.
// Sorting by path keeps glob results deterministic regardless of FS order.
func fingerprint(paths []string) (SourceFingerprint, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return SourceFingerprint{}, fmt.Errorf("stat %s: %w", p, err)
		}
		out = append(out, fmt.Sprintf("%s:%d:%d", p, st.ModTime().UnixNano(), st.Size()))
	}
	// Stable order so map iteration / FS order doesn't cause spurious diffs.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return SourceFingerprint{Files: out}, nil
}

// httpMeta is the cache-validator state persisted next to a downloaded file
// as <hash>.meta.json. Either field may be empty if the server didn't send it.
type httpMeta struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// fetchToCache downloads url into CacheDir on first access; on subsequent
// calls it issues a conditional GET (If-None-Match / If-Modified-Since) so a
// 304 avoids re-downloading the body. Returns the cached path and whether
// the body was rewritten on this call.
func (l *Loader) fetchToCache(ctx context.Context, url string) (path string, changed bool, err error) {
	cachePath := filepath.Join(l.CacheDir, cacheKey(url))
	metaPath := cachePath + ".meta.json"

	prev, _ := readMeta(metaPath) // best-effort: missing/invalid sidecar -> no validators
	cacheExists := false
	if _, statErr := os.Stat(cachePath); statErr == nil {
		cacheExists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", false, fmt.Errorf("stat cache: %w", statErr)
	}

	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false, err
	}
	if cacheExists && prev.ETag != "" {
		req.Header.Set("If-None-Match", prev.ETag)
	}
	if cacheExists && prev.LastModified != "" {
		req.Header.Set("If-Modified-Since", prev.LastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		if !cacheExists {
			// Server claims unchanged but we have no body — treat as error.
			return "", false, fmt.Errorf("download %s: 304 without cached body", url)
		}
		return cachePath, false, nil
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", false, fmt.Errorf("read body: %w", err)
		}
		if err := os.WriteFile(cachePath, body, 0o644); err != nil {
			return "", false, fmt.Errorf("write cache: %w", err)
		}
		next := httpMeta{
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
		}
		if err := writeMeta(metaPath, next); err != nil {
			// Body is already on disk; surface the meta error as a warning via
			// the returned error so the caller can decide.
			return cachePath, true, fmt.Errorf("write meta: %w", err)
		}
		return cachePath, true, nil
	default:
		return "", false, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
}

// cacheKey hashes a URL to a short, filesystem-safe filename ending in .proto.
// 12 hex chars (48 bits) is plenty to avoid collisions across a config's
// handful of source URLs while keeping filenames readable.
func cacheKey(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:6]) + ".proto"
}

func readMeta(path string) (httpMeta, error) {
	var m httpMeta
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return httpMeta{}, err
	}
	return m, nil
}

func writeMeta(path string, m httpMeta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// parseFile reads a .proto from disk and returns its enums (FQNs included).
func (l *Loader) parseFile(path string) ([]Enum, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	got, err := pp.Parse(f, pp.WithDebug(false), pp.WithPermissive(!l.Strict))
	if err != nil {
		return nil, err
	}
	return extract(got), nil
}

// extract walks a parsed Proto and returns all of its enums (top-level + nested).
func extract(p *parser.Proto) []Enum {
	var pkg string
	for _, v := range p.ProtoBody {
		if pk, ok := v.(*parser.Package); ok {
			pkg = pk.Name
			break
		}
	}

	var out []Enum
	for _, v := range p.ProtoBody {
		switch n := v.(type) {
		case *parser.Enum:
			out = append(out, toEnum(n, pkg, pkg))
		case *parser.Message:
			out = append(out, walkMessage(n, pkg, pkg)...)
		}
	}
	return out
}

// walkMessage recursively descends a message, collecting nested enums.
// prefix is the dot-joined chain of <package>.<enclosing-messages>; it grows
// as we descend.
func walkMessage(m *parser.Message, prefix, pkg string) []Enum {
	here := joinDot(prefix, m.MessageName)
	var out []Enum
	for _, v := range m.MessageBody {
		switch n := v.(type) {
		case *parser.Enum:
			out = append(out, toEnum(n, here, pkg))
		case *parser.Message:
			out = append(out, walkMessage(n, here, pkg)...)
		}
	}
	return out
}

func toEnum(e *parser.Enum, prefix, pkg string) Enum {
	values := make([]EnumValue, 0, len(e.EnumBody))
	for _, v := range e.EnumBody {
		ef, ok := v.(*parser.EnumField)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(ef.Number, 0, 32)
		if err != nil {
			continue
		}
		values = append(values, EnumValue{Name: ef.Ident, Number: int32(n)})
	}
	return Enum{
		Name:       joinDot(prefix, e.EnumName),
		SimpleName: e.EnumName,
		Package:    pkg,
		Values:     values,
	}
}

func joinDot(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}
