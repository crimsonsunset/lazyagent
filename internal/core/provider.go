package core

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/illegalstudio/lazyagent/internal/amp"
	"github.com/illegalstudio/lazyagent/internal/claude"
	"github.com/illegalstudio/lazyagent/internal/codex"
	"github.com/illegalstudio/lazyagent/internal/cursor"
	"github.com/illegalstudio/lazyagent/internal/diskcache"
	"github.com/illegalstudio/lazyagent/internal/grok"
	"github.com/illegalstudio/lazyagent/internal/kilo"
	"github.com/illegalstudio/lazyagent/internal/kimi"
	"github.com/illegalstudio/lazyagent/internal/model"
	"github.com/illegalstudio/lazyagent/internal/opencode"
	"github.com/illegalstudio/lazyagent/internal/pi"
)

// LiveProvider discovers real Claude Code sessions from disk.
type LiveProvider struct {
	cache        *model.SessionCache
	desktopCache *claude.DesktopCache
	claudeDirs   []string
	cwdIdx       *claude.CWDIndex
}

// NewLiveProvider creates a LiveProvider with mtime-based caches.
// claudeDirs configures which Claude base directories to scan (e.g. ~/.claude).
// When empty, auto-detection via CLAUDE_CONFIG_DIR / ~/.claude is used.
func NewLiveProvider(claudeDirs []string) *LiveProvider {
	return &LiveProvider{
		cache:        model.NewSessionCache(),
		desktopCache: claude.NewDesktopCache(),
		claudeDirs:   claudeDirs,
		cwdIdx:       claude.NewCWDIndex(),
	}
}

var (
	_ DirScopedProvider = (*LiveProvider)(nil)
	_ CachePersister    = (*LiveProvider)(nil)
)

func (p *LiveProvider) DiscoverSessions() ([]*model.Session, error) {
	return claude.DiscoverSessions(p.cache, p.desktopCache, p.claudeDirs)
}

// DiscoverSessionsMatching implements DirScopedProvider: it scopes discovery
// to files whose cwd matches cwdMatch, skipping full parses of the rest.
func (p *LiveProvider) DiscoverSessionsMatching(cwdMatch func(string) bool) ([]*model.Session, error) {
	return claude.DiscoverSessionsFilteredIndexed(p.cache, p.desktopCache, p.claudeDirs, cwdMatch, p.cwdIdx)
}

// LoadCaches implements CachePersister.
func (p *LiveProvider) LoadCaches(dir string) error {
	err1 := p.cache.LoadFrom(filepath.Join(dir, "discovery-claude.json"))
	err2 := p.cwdIdx.LoadFrom(filepath.Join(dir, "cwdindex-claude.json"))
	return firstErr(err1, err2)
}

// SaveCaches implements CachePersister.
func (p *LiveProvider) SaveCaches(dir string) error {
	if err := diskcache.EnsureDir(dir); err != nil {
		return err
	}
	err1 := p.cache.SaveTo(filepath.Join(dir, "discovery-claude.json"))
	err2 := p.cwdIdx.SaveTo(filepath.Join(dir, "cwdindex-claude.json"))
	return firstErr(err1, err2)
}

func (p *LiveProvider) UseWatcher() bool               { return true }
func (p *LiveProvider) RefreshInterval() time.Duration { return 0 }
func (p *LiveProvider) WatchDirs() []string {
	dirs := claude.ClaudeProjectsDirs(p.claudeDirs)
	if d := claude.DesktopSessionsDir(); d != "" {
		dirs = append(dirs, d)
	}
	return dirs
}

// PiProvider discovers pi coding agent sessions from disk.
type PiProvider struct {
	cache *model.SessionCache
}

// NewPiProvider creates a PiProvider with an mtime-based cache.
func NewPiProvider() *PiProvider {
	return &PiProvider{cache: model.NewSessionCache()}
}

var _ CachePersister = (*PiProvider)(nil)

func (p *PiProvider) DiscoverSessions() ([]*model.Session, error) {
	return pi.DiscoverSessions(p.cache)
}

// LoadCaches implements CachePersister.
func (p *PiProvider) LoadCaches(dir string) error {
	return p.cache.LoadFrom(filepath.Join(dir, "discovery-pi.json"))
}

// SaveCaches implements CachePersister.
func (p *PiProvider) SaveCaches(dir string) error {
	if err := diskcache.EnsureDir(dir); err != nil {
		return err
	}
	return p.cache.SaveTo(filepath.Join(dir, "discovery-pi.json"))
}

func (p *PiProvider) UseWatcher() bool               { return true }
func (p *PiProvider) RefreshInterval() time.Duration { return 0 }
func (p *PiProvider) WatchDirs() []string            { return []string{pi.PiSessionsDir()} }

// OpenCodeProvider discovers OpenCode sessions from SQLite.
type OpenCodeProvider struct {
	cache *opencode.SessionCache
}

var _ DirScopedProvider = (*OpenCodeProvider)(nil)

// NewOpenCodeProvider creates an OpenCodeProvider.
func NewOpenCodeProvider() *OpenCodeProvider {
	return &OpenCodeProvider{cache: opencode.NewSessionCache()}
}

func (p *OpenCodeProvider) DiscoverSessions() ([]*model.Session, error) {
	return opencode.DiscoverSessions(p.cache)
}

// DiscoverSessionsMatching implements DirScopedProvider: it scopes discovery
// to sessions whose directory matches cwdMatch, skipping message loads for
// the rest (see opencodefamily.DiscoverSessionsForFiltered).
func (p *OpenCodeProvider) DiscoverSessionsMatching(cwdMatch func(string) bool) ([]*model.Session, error) {
	return opencode.DiscoverSessionsFiltered(p.cache, cwdMatch)
}

func (p *OpenCodeProvider) UseWatcher() bool               { return false }
func (p *OpenCodeProvider) RefreshInterval() time.Duration { return 3 * time.Second }
func (p *OpenCodeProvider) WatchDirs() []string {
	if d := opencode.OpenCodeDataDir(); d != "" {
		return []string{d}
	}
	return nil
}

// KiloProvider discovers Kilo sessions from SQLite.
type KiloProvider struct {
	cache *kilo.SessionCache
}

var _ DirScopedProvider = (*KiloProvider)(nil)

// NewKiloProvider creates a KiloProvider.
func NewKiloProvider() *KiloProvider {
	return &KiloProvider{cache: kilo.NewSessionCache()}
}

func (p *KiloProvider) DiscoverSessions() ([]*model.Session, error) {
	return kilo.DiscoverSessions(p.cache)
}

// DiscoverSessionsMatching implements DirScopedProvider: it scopes discovery
// to sessions whose directory matches cwdMatch, skipping message loads for
// the rest (see opencodefamily.DiscoverSessionsForFiltered).
func (p *KiloProvider) DiscoverSessionsMatching(cwdMatch func(string) bool) ([]*model.Session, error) {
	return kilo.DiscoverSessionsFiltered(p.cache, cwdMatch)
}

func (p *KiloProvider) UseWatcher() bool               { return false }
func (p *KiloProvider) RefreshInterval() time.Duration { return 3 * time.Second }
func (p *KiloProvider) WatchDirs() []string {
	if d := kilo.KiloDataDir(); d != "" {
		return []string{d}
	}
	return nil
}

// CursorProvider discovers Cursor sessions from store.db files.
type CursorProvider struct {
	cache *cursor.SessionCache
}

// NewCursorProvider creates a CursorProvider.
func NewCursorProvider() *CursorProvider {
	return &CursorProvider{cache: cursor.NewSessionCache()}
}

var _ CachePersister = (*CursorProvider)(nil)

func (p *CursorProvider) DiscoverSessions() ([]*model.Session, error) {
	return cursor.DiscoverSessions(p.cache)
}

// LoadCaches implements CachePersister. Without it every restart re-decodes
// every recent chat's bubbles from state.vscdb (~760 MB, minutes) (#111).
func (p *CursorProvider) LoadCaches(dir string) error {
	return p.cache.LoadFrom(filepath.Join(dir, "discovery-cursor.json"))
}

// SaveCaches implements CachePersister.
func (p *CursorProvider) SaveCaches(dir string) error {
	if err := diskcache.EnsureDir(dir); err != nil {
		return err
	}
	return p.cache.SaveTo(filepath.Join(dir, "discovery-cursor.json"))
}

func (p *CursorProvider) UseWatcher() bool { return false }

// RefreshInterval is 1s so a chat change reaches the API within about a second
// of the database write. A cycle with no change costs a WAL stat (#114).
func (p *CursorProvider) RefreshInterval() time.Duration { return time.Second }
func (p *CursorProvider) WatchDirs() []string {
	if d := cursor.StateDBDir(); d != "" {
		return []string{d}
	}
	return nil
}

// DirScopedProvider is implemented by providers that can discover sessions
// for a single directory more cheaply than a full scan (e.g. by head-reading
// each file's cwd before fully parsing it).
type DirScopedProvider interface {
	DiscoverSessionsMatching(cwdMatch func(string) bool) ([]*model.Session, error)
}

// CodexProvider discovers Codex CLI sessions from JSONL transcripts.
type CodexProvider struct {
	cache  *model.SessionCache
	cwdIdx *codex.CWDIndex
}

var (
	_ DirScopedProvider = (*CodexProvider)(nil)
	_ CachePersister    = (*CodexProvider)(nil)
)

// NewCodexProvider creates a CodexProvider.
func NewCodexProvider() *CodexProvider {
	return &CodexProvider{cache: model.NewSessionCache(), cwdIdx: codex.NewCWDIndex()}
}

func (p *CodexProvider) DiscoverSessions() ([]*model.Session, error) {
	return codex.DiscoverSessions(p.cache)
}

// DiscoverSessionsMatching implements DirScopedProvider: it scopes discovery
// to files whose cwd matches cwdMatch, skipping full parses of the rest.
func (p *CodexProvider) DiscoverSessionsMatching(cwdMatch func(string) bool) ([]*model.Session, error) {
	return codex.DiscoverSessionsFilteredIndexed(p.cache, cwdMatch, p.cwdIdx)
}

// LoadCaches implements CachePersister.
func (p *CodexProvider) LoadCaches(dir string) error {
	err1 := p.cache.LoadFrom(filepath.Join(dir, "discovery-codex.json"))
	err2 := p.cwdIdx.LoadFrom(filepath.Join(dir, "cwdindex-codex.json"))
	return firstErr(err1, err2)
}

// SaveCaches implements CachePersister.
func (p *CodexProvider) SaveCaches(dir string) error {
	if err := diskcache.EnsureDir(dir); err != nil {
		return err
	}
	err1 := p.cache.SaveTo(filepath.Join(dir, "discovery-codex.json"))
	err2 := p.cwdIdx.SaveTo(filepath.Join(dir, "cwdindex-codex.json"))
	return firstErr(err1, err2)
}

func (p *CodexProvider) UseWatcher() bool               { return false }
func (p *CodexProvider) RefreshInterval() time.Duration { return 3 * time.Second }
func (p *CodexProvider) WatchDirs() []string {
	if d := codex.SessionsDir(); d != "" {
		return []string{d}
	}
	return nil
}

// AmpProvider discovers Amp CLI sessions from local thread JSON files.
type AmpProvider struct {
	cache *model.SessionCache
}

// NewAmpProvider creates an AmpProvider.
func NewAmpProvider() *AmpProvider {
	return &AmpProvider{cache: model.NewSessionCache()}
}

var _ CachePersister = (*AmpProvider)(nil)

func (p *AmpProvider) DiscoverSessions() ([]*model.Session, error) {
	return amp.DiscoverSessions(p.cache)
}

// LoadCaches implements CachePersister.
func (p *AmpProvider) LoadCaches(dir string) error {
	return p.cache.LoadFrom(filepath.Join(dir, "discovery-amp.json"))
}

// SaveCaches implements CachePersister.
func (p *AmpProvider) SaveCaches(dir string) error {
	if err := diskcache.EnsureDir(dir); err != nil {
		return err
	}
	return p.cache.SaveTo(filepath.Join(dir, "discovery-amp.json"))
}

func (p *AmpProvider) UseWatcher() bool               { return false }
func (p *AmpProvider) RefreshInterval() time.Duration { return 3 * time.Second }
func (p *AmpProvider) WatchDirs() []string {
	if d := amp.ThreadsDir(); d != "" {
		return []string{d}
	}
	return nil
}

// GrokProvider discovers xAI Grok CLI sessions from disk.
type GrokProvider struct {
	cache *model.SessionCache
}

// NewGrokProvider creates a GrokProvider with an mtime-based cache.
func NewGrokProvider() *GrokProvider {
	return &GrokProvider{cache: model.NewSessionCache()}
}

var _ CachePersister = (*GrokProvider)(nil)

func (p *GrokProvider) DiscoverSessions() ([]*model.Session, error) {
	return grok.DiscoverSessions(p.cache)
}

// LoadCaches implements CachePersister.
func (p *GrokProvider) LoadCaches(dir string) error {
	return p.cache.LoadFrom(filepath.Join(dir, "discovery-grok.json"))
}

// SaveCaches implements CachePersister.
func (p *GrokProvider) SaveCaches(dir string) error {
	if err := diskcache.EnsureDir(dir); err != nil {
		return err
	}
	return p.cache.SaveTo(filepath.Join(dir, "discovery-grok.json"))
}

func (p *GrokProvider) UseWatcher() bool               { return true }
func (p *GrokProvider) RefreshInterval() time.Duration { return 0 }
func (p *GrokProvider) WatchDirs() []string            { return []string{grok.GrokSessionsDir()} }

// KimiProvider discovers Kimi Code CLI sessions from disk.
type KimiProvider struct {
	cache *model.SessionCache
}

// NewKimiProvider creates a KimiProvider with an mtime-based cache.
func NewKimiProvider() *KimiProvider {
	return &KimiProvider{cache: model.NewSessionCache()}
}

var _ CachePersister = (*KimiProvider)(nil)

func (p *KimiProvider) DiscoverSessions() ([]*model.Session, error) {
	return kimi.DiscoverSessions(p.cache)
}

// LoadCaches implements CachePersister.
func (p *KimiProvider) LoadCaches(dir string) error {
	return p.cache.LoadFrom(filepath.Join(dir, "discovery-kimi.json"))
}

// SaveCaches implements CachePersister.
func (p *KimiProvider) SaveCaches(dir string) error {
	if err := diskcache.EnsureDir(dir); err != nil {
		return err
	}
	return p.cache.SaveTo(filepath.Join(dir, "discovery-kimi.json"))
}

func (p *KimiProvider) UseWatcher() bool               { return true }
func (p *KimiProvider) RefreshInterval() time.Duration { return 0 }
func (p *KimiProvider) WatchDirs() []string            { return []string{kimi.SessionsDir()} }

// BuildProvider creates a SessionProvider based on agent mode and config.
// When agentMode is "all", it reads the agents config to decide which providers
// to include. A specific agentMode (e.g. "claude") overrides the config.
func BuildProvider(agentMode string, cfg Config) SessionProvider {
	switch agentMode {
	case "claude":
		return NewLiveProvider(cfg.ClaudeDirs)
	case "pi":
		return NewPiProvider()
	case "opencode":
		return NewOpenCodeProvider()
	case "kilo":
		return NewKiloProvider()
	case "cursor":
		return NewCursorProvider()
	case "codex":
		return NewCodexProvider()
	case "amp":
		return NewAmpProvider()
	case "grok":
		return NewGrokProvider()
	case "kimi":
		return NewKimiProvider()
	default: // "all"
		var providers []SessionProvider
		if cfg.AgentEnabled("claude") {
			providers = append(providers, NewLiveProvider(cfg.ClaudeDirs))
		}
		if cfg.AgentEnabled("pi") {
			providers = append(providers, NewPiProvider())
		}
		if cfg.AgentEnabled("opencode") {
			providers = append(providers, NewOpenCodeProvider())
		}
		if cfg.AgentEnabled("kilo") {
			providers = append(providers, NewKiloProvider())
		}
		if cfg.AgentEnabled("cursor") {
			providers = append(providers, NewCursorProvider())
		}
		if cfg.AgentEnabled("codex") {
			providers = append(providers, NewCodexProvider())
		}
		if cfg.AgentEnabled("amp") {
			providers = append(providers, NewAmpProvider())
		}
		if cfg.AgentEnabled("grok") {
			providers = append(providers, NewGrokProvider())
		}
		if cfg.AgentEnabled("kimi") {
			providers = append(providers, NewKimiProvider())
		}
		if len(providers) == 0 {
			// All disabled — return a no-op provider.
			return MultiProvider{}
		}
		if len(providers) == 1 {
			return providers[0]
		}
		return MultiProvider{Providers: providers}
	}
}

// MultiProvider merges sessions from multiple providers.
type MultiProvider struct {
	Providers []SessionProvider
}

func (m MultiProvider) DiscoverSessions() ([]*model.Session, error) {
	if len(m.Providers) <= 1 {
		// Fast path: no need for goroutines.
		for _, p := range m.Providers {
			return p.DiscoverSessions()
		}
		return nil, nil
	}

	type result struct {
		sessions []*model.Session
	}
	results := make([]result, len(m.Providers))
	var wg sync.WaitGroup
	for i, p := range m.Providers {
		wg.Add(1)
		go func(idx int, prov SessionProvider) {
			defer wg.Done()
			sessions, err := prov.DiscoverSessions()
			if err == nil {
				results[idx] = result{sessions: sessions}
			}
		}(i, p)
	}
	wg.Wait()

	var all []*model.Session
	for _, r := range results {
		all = append(all, r.sessions...)
	}
	return all, nil
}

func (m MultiProvider) UseWatcher() bool {
	for _, p := range m.Providers {
		if p.UseWatcher() {
			return true
		}
	}
	return false
}

func (m MultiProvider) RefreshInterval() time.Duration {
	var min time.Duration
	for _, p := range m.Providers {
		d := p.RefreshInterval()
		if d > 0 && (min == 0 || d < min) {
			min = d
		}
	}
	return min
}

func (m MultiProvider) WatchDirs() []string {
	var dirs []string
	for _, p := range m.Providers {
		dirs = append(dirs, p.WatchDirs()...)
	}
	return dirs
}

// DiscoverMatching discovers sessions from p, using the dir-scoped fast path
// (DirScopedProvider) where p (or, for a MultiProvider, a member) implements
// it. If p is a MultiProvider, its members are queried concurrently, with
// the same best-effort error handling as MultiProvider.DiscoverSessions: a
// failing member contributes nothing.
//
// Note: results from a member that does NOT implement DirScopedProvider come
// from a plain DiscoverSessions call and are NOT pre-filtered by cwdMatch —
// filtering those remains the caller's responsibility (e.g. via a downstream
// FilterByDir pass).
func DiscoverMatching(p SessionProvider, cwdMatch func(string) bool) ([]*model.Session, error) {
	if mp, ok := p.(MultiProvider); ok {
		return discoverMatchingMulti(mp, cwdMatch)
	}
	return discoverMatchingOne(p, cwdMatch)
}

func discoverMatchingOne(p SessionProvider, cwdMatch func(string) bool) ([]*model.Session, error) {
	if dsp, ok := p.(DirScopedProvider); ok {
		return dsp.DiscoverSessionsMatching(cwdMatch)
	}
	return p.DiscoverSessions()
}

// DiscoverMatchingStream discovers sessions like DiscoverMatching but
// delivers each provider member's results as they complete via emit
// (called from the discovery goroutines; the caller must make emit safe for
// concurrent use — see sync.Mutex or an equivalent). done is closed once
// every member has finished (after its goroutine's emit call, if any, has
// already returned). Best-effort like MultiProvider: a failing member emits
// nothing, but still counts toward done closing.
//
// Granularity matches DiscoverMatching's fan-out (see streamMembers): a
// MultiProvider's Providers are its members (zero members, and an
// immediately-closed done, for an empty MultiProvider); any other provider
// is a single member. Each member's fast-path-vs-plain decision reuses
// discoverMatchingOne, the same function DiscoverMatching itself uses, so
// there is one implementation of that decision.
func DiscoverMatchingStream(p SessionProvider, cwdMatch func(string) bool, emit func([]*model.Session)) (done <-chan struct{}) {
	doneCh := make(chan struct{})
	members := streamMembers(p)
	if len(members) == 0 {
		close(doneCh)
		return doneCh
	}
	var wg sync.WaitGroup
	for _, member := range members {
		wg.Add(1)
		go func(prov SessionProvider) {
			defer wg.Done()
			sessions, err := discoverMatchingOne(prov, cwdMatch)
			if err == nil {
				emit(sessions)
			}
		}(member)
	}
	go func() {
		wg.Wait()
		close(doneCh)
	}()
	return doneCh
}

// streamMembers returns p's independent discovery members for
// DiscoverMatchingStream: a MultiProvider's Providers slice (possibly
// empty), or a single-element slice containing p itself for any other
// provider.
func streamMembers(p SessionProvider) []SessionProvider {
	if mp, ok := p.(MultiProvider); ok {
		return mp.Providers
	}
	return []SessionProvider{p}
}

func discoverMatchingMulti(mp MultiProvider, cwdMatch func(string) bool) ([]*model.Session, error) {
	if len(mp.Providers) <= 1 {
		// Fast path: no need for goroutines.
		for _, p := range mp.Providers {
			return discoverMatchingOne(p, cwdMatch)
		}
		return nil, nil
	}

	type result struct {
		sessions []*model.Session
	}
	results := make([]result, len(mp.Providers))
	var wg sync.WaitGroup
	for i, p := range mp.Providers {
		wg.Add(1)
		go func(idx int, prov SessionProvider) {
			defer wg.Done()
			sessions, err := discoverMatchingOne(prov, cwdMatch)
			if err == nil {
				results[idx] = result{sessions: sessions}
			}
		}(i, p)
	}
	wg.Wait()

	var all []*model.Session
	for _, r := range results {
		all = append(all, r.sessions...)
	}
	return all, nil
}
