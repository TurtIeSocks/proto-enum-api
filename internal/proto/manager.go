package proto

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Manager owns the in-memory EnumIndex served by the API. It refreshes each
// source on its own cadence, swaps a freshly-built index in atomically, and
// exposes per-source health for /healthz.
//
// Lifecycle:
//
//	m, err := NewManager(loader)
//	if err != nil { ... }                 // initial load failed → fatal
//	defer m.Stop()
//	m.Start(ctx)
//
// The index returned by Index() is never mutated in place; readers can hold
// it for as long as they like without locking.
type Manager struct {
	loader *Loader
	idx    atomic.Pointer[EnumIndex]

	mu       sync.Mutex
	sources  []*sourceState
	stopOnce sync.Once
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// sourceState is everything the manager remembers about one source between
// refreshes. Mutated only under Manager.mu.
type sourceState struct {
	src         Source
	enums       []Enum            // last successful parse
	fingerprint SourceFingerprint // last on-disk fingerprint
	lastOK      time.Time         // last successful refresh
	lastTry     time.Time         // last attempt (success or failure)
	lastErr     error             // nil if last attempt succeeded
}

// NewManager performs the initial synchronous load. If any source fails, the
// caller decides what to do (typically: log.Fatal). On success, the index is
// already populated and Index() will return it before Start is called.
func NewManager(loader *Loader) (*Manager, error) {
	m := &Manager{loader: loader}
	if err := m.loader.ensureCacheDir(); err != nil {
		return nil, err
	}

	now := time.Now()
	m.sources = make([]*sourceState, len(loader.Sources))
	var all []Enum
	for i, src := range loader.Sources {
		enums, fp, err := loader.LoadSource(context.Background(), src)
		if err != nil {
			return nil, fmt.Errorf("source[%d] %s: %w", i, src.Locator(), err)
		}
		m.sources[i] = &sourceState{
			src:         src,
			enums:       enums,
			fingerprint: fp,
			lastOK:      now,
			lastTry:     now,
		}
		all = append(all, enums...)
	}
	m.idx.Store(NewEnumIndex(all))
	return m, nil
}

// Index returns the current immutable index. Safe for concurrent reads.
func (m *Manager) Index() *EnumIndex { return m.idx.Load() }

// Start launches one goroutine per source that has a positive RefreshInterval.
// Sources with no interval refresh only via RefreshAll.
func (m *Manager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)
	for i, s := range m.sources {
		if s.src.RefreshInterval <= 0 {
			continue
		}
		m.wg.Add(1)
		go m.runSource(ctx, i)
	}
}

// Stop cancels all refresh goroutines and waits for them to exit.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		m.wg.Wait()
	})
}

func (m *Manager) runSource(ctx context.Context, i int) {
	defer m.wg.Done()
	t := time.NewTicker(m.sources[i].src.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if changed, err := m.refreshOne(ctx, i); err != nil {
				log.Printf("refresh source[%d] %s: %v", i, m.sources[i].src.Locator(), err)
			} else if changed {
				log.Printf("refresh source[%d] %s: index updated", i, m.sources[i].src.Locator())
			}
		}
	}
}

// RefreshAll triggers a refresh of every source on demand. Returns the number
// of sources whose contents changed and any errors encountered (one per
// failing source). The index is rebuilt at most once even if multiple sources
// changed.
func (m *Manager) RefreshAll(ctx context.Context) (changed int, errs []error) {
	for i := range m.sources {
		ch, err := m.refreshOne(ctx, i)
		if err != nil {
			errs = append(errs, fmt.Errorf("source[%d] %s: %w", i, m.sources[i].src.Locator(), err))
			continue
		}
		if ch {
			changed++
		}
	}
	return changed, errs
}

// refreshOne reloads source i. On success, if its fingerprint changed, the
// global index is rebuilt atomically from all sources' last-known enums.
func (m *Manager) refreshOne(ctx context.Context, i int) (bool, error) {
	enums, fp, err := m.loader.LoadSource(ctx, m.sources[i].src)
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sources[i]
	s.lastTry = now
	if err != nil {
		s.lastErr = err
		return false, err
	}
	s.lastErr = nil
	s.lastOK = now
	if s.fingerprint.Equal(fp) {
		return false, nil
	}
	s.enums = enums
	s.fingerprint = fp
	m.rebuildLocked()
	return true, nil
}

// rebuildLocked merges every source's last-good enums into a fresh index and
// publishes it. Caller holds m.mu.
func (m *Manager) rebuildLocked() {
	var all []Enum
	for _, s := range m.sources {
		all = append(all, s.enums...)
	}
	m.idx.Store(NewEnumIndex(all))
}

// SourceHealth is the per-source slice of /healthz output.
type SourceHealth struct {
	Index           int       `json:"index"`
	Kind            string    `json:"kind"`
	Locator         string    `json:"locator"`
	RefreshInterval string    `json:"refreshInterval,omitempty"`
	LastOK          time.Time `json:"lastOK,omitempty"`
	LastTry         time.Time `json:"lastTry,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
	Stale           bool      `json:"stale"`
	Enums           int       `json:"enums"`
}

// Health is the snapshot returned by /healthz.
type Health struct {
	Stale   bool           `json:"stale"`
	Enums   int            `json:"enums"`
	Sources []SourceHealth `json:"sources"`
}

// Health returns a point-in-time snapshot of every source's refresh status.
// "stale" means the most recent attempt failed — the served data is from a
// previous successful load.
func (m *Manager) Health() Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := Health{Sources: make([]SourceHealth, len(m.sources))}
	anyStale := false
	for i, s := range m.sources {
		sh := SourceHealth{
			Index:   i,
			Kind:    s.src.Kind(),
			Locator: s.src.Locator(),
			LastOK:  s.lastOK,
			LastTry: s.lastTry,
			Enums:   len(s.enums),
		}
		if s.src.RefreshInterval > 0 {
			sh.RefreshInterval = s.src.RefreshInterval.String()
		}
		if s.lastErr != nil {
			sh.LastError = s.lastErr.Error()
			sh.Stale = true
			anyStale = true
		}
		h.Sources[i] = sh
	}
	h.Stale = anyStale
	if idx := m.idx.Load(); idx != nil {
		h.Enums = idx.Len()
	}
	return h
}

// Stale reports whether any source's last refresh attempt failed.
func (m *Manager) Stale() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sources {
		if s.lastErr != nil {
			return true
		}
	}
	return false
}
