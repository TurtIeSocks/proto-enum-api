package proto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	URL  string
	Path string
	Glob string
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
	if l.CacheDir != "" {
		if err := os.MkdirAll(l.CacheDir, 0o755); err != nil {
			return nil, fmt.Errorf("create cache dir: %w", err)
		}
	}

	var all []Enum
	for i, src := range l.Sources {
		paths, err := l.resolve(ctx, src)
		if err != nil {
			return nil, fmt.Errorf("source[%d]: %w", i, err)
		}
		for _, p := range paths {
			enums, err := l.parseFile(p)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", p, err)
			}
			all = append(all, enums...)
		}
	}
	return NewEnumIndex(all), nil
}

// resolve returns the list of local file paths a Source expands to.
func (l *Loader) resolve(ctx context.Context, src Source) ([]string, error) {
	switch {
	case src.URL != "":
		p, err := l.fetchToCache(ctx, src.URL)
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

// fetchToCache downloads url into CacheDir using a deterministic filename
// derived from the URL hash, and returns the cached path. If the cache
// already contains the file, it's returned without a re-download.
func (l *Loader) fetchToCache(ctx context.Context, url string) (string, error) {
	cachePath := filepath.Join(l.CacheDir, cacheKey(url))
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat cache: %w", err)
	}

	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if err := os.WriteFile(cachePath, body, 0o644); err != nil {
		return "", fmt.Errorf("write cache: %w", err)
	}
	return cachePath, nil
}

// cacheKey hashes a URL to a short, filesystem-safe filename ending in .proto.
// 12 hex chars (48 bits) is plenty to avoid collisions across a config's
// handful of source URLs while keeping filenames readable.
func cacheKey(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:6]) + ".proto"
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
