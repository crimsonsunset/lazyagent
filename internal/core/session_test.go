package core

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/illegalstudio/lazyagent/internal/model"
)

func TestReload_DoesNotAutoPopulateNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	provider := fakeProvider{
		sessions: []*model.Session{
			{SessionID: "s1", CWD: "/project1", Name: "My cool session", LastActivity: time.Now()},
			{SessionID: "s2", CWD: "/project2", LastActivity: time.Now()},
		},
	}

	mgr := NewSessionManager(30, provider)
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Agent-provided names should NOT be persisted as custom names.
	// The UI reads s.Name directly as a display fallback.
	if got := mgr.SessionName("s1"); got != "" {
		t.Errorf("SessionName(s1) = %q, want empty (agent name should not be persisted)", got)
	}
	if got := mgr.SessionName("s2"); got != "" {
		t.Errorf("SessionName(s2) = %q, want empty", got)
	}
}

func TestReload_PreservesUserSetNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	provider := fakeProvider{
		sessions: []*model.Session{
			{SessionID: "s1", CWD: "/project1", Name: "From agent", LastActivity: time.Now()},
		},
	}

	mgr := NewSessionManager(30, provider)

	// User sets a custom name
	_ = mgr.SetSessionName("s1", "My custom name")

	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	if got := mgr.SessionName("s1"); got != "My custom name" {
		t.Errorf("SessionName(s1) = %q, want 'My custom name'", got)
	}
}

func TestFilterSessionsLocked_ExcludesCWDSubstrings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	now := time.Now()
	provider := fakeProvider{
		sessions: []*model.Session{
			{SessionID: "normal", CWD: "/home/user/project", LastActivity: now},
			{SessionID: "excluded", CWD: "/home/user/.claude-mem/observer-sessions/abc", LastActivity: now},
			{SessionID: "also-normal", CWD: "/home/user/other-project", LastActivity: now},
		},
	}

	mgr := NewSessionManager(60, provider)
	mgr.SetExcludeCWDSubstrings([]string{".claude-mem/observer-sessions"})
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Sessions() returns all raw sessions (unfiltered by CWD exclusion).
	raw := mgr.Sessions()
	if len(raw) != 3 {
		t.Errorf("Sessions() returned %d sessions, want 3", len(raw))
	}

	// VisibleSessions should exclude the matching session.
	visible := mgr.VisibleSessions()
	for _, s := range visible {
		if s.SessionID == "excluded" {
			t.Error("VisibleSessions() should not contain the excluded session")
		}
	}
	if len(visible) != 2 {
		t.Errorf("VisibleSessions() returned %d sessions, want 2", len(visible))
	}

	// QuerySessions should also exclude the matching session.
	queried := mgr.QuerySessions("", "")
	for _, s := range queried {
		if s.SessionID == "excluded" {
			t.Error("QuerySessions() should not contain the excluded session")
		}
	}
	if len(queried) != 2 {
		t.Errorf("QuerySessions() returned %d sessions, want 2", len(queried))
	}
}

func TestFilterSessionsLocked_HidesEmptyGrokAndKimiSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	now := time.Now()
	// Grok and Kimi both create a session directory the moment the CLI is
	// launched, before any prompt. Such never-used sessions must be hidden.
	provider := fakeProvider{
		sessions: []*model.Session{
			{SessionID: "grok-active", Agent: "grok", CWD: "/p", LastActivity: now, UserMessages: 3},
			{SessionID: "grok-empty", Agent: "grok", CWD: "/p", LastActivity: now, UserMessages: 0},
			{SessionID: "kimi-active", Agent: "kimi", CWD: "/p", LastActivity: now, UserMessages: 2},
			{SessionID: "kimi-empty", Agent: "kimi", CWD: "/p", LastActivity: now, UserMessages: 0},
			{SessionID: "claude-zero", Agent: "claude", CWD: "/p", LastActivity: now, UserMessages: 0},
		},
	}

	mgr := NewSessionManager(60, provider)
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// Discovery keeps every session so prune can reach old never-used ones.
	if len(mgr.Sessions()) != 5 {
		t.Errorf("Sessions() = %d, want 5 (raw discovery keeps empty sessions)", len(mgr.Sessions()))
	}

	// The rendering filter (backing TUI, GUI and API) hides the never-used
	// Grok and Kimi sessions — and only those: a non-Grok/Kimi session with no
	// user messages is left untouched.
	check := func(name string, got []*model.Session) {
		for _, s := range got {
			if s.SessionID == "grok-empty" || s.SessionID == "kimi-empty" {
				t.Errorf("%s must not contain the never-used %s session", name, s.Agent)
			}
		}
		if len(got) != 3 {
			t.Errorf("%s = %d sessions, want 3 (grok-active + kimi-active + claude-zero)", name, len(got))
		}
	}
	check("VisibleSessions", mgr.VisibleSessions()) // TUI + GUI/tray
	check("QuerySessions", mgr.QuerySessions("", "")) // HTTP API
}

func TestQueryAllSessions_IgnoresWindowButNotSubagentsOrExclusions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	now := time.Now()
	provider := fakeProvider{
		sessions: []*model.Session{
			{SessionID: "fresh", CWD: "/home/user/project", LastActivity: now},
			{SessionID: "old", CWD: "/home/user/project", LastActivity: now.Add(-72 * time.Hour)},
			{SessionID: "sub", CWD: "/home/user/project", LastActivity: now, IsSidechain: true},
			{SessionID: "excluded", CWD: "/home/user/.claude-mem/observer-sessions/abc", LastActivity: now},
		},
	}

	mgr := NewSessionManager(30, provider)
	mgr.SetExcludeCWDSubstrings([]string{".claude-mem/observer-sessions"})
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	got := sessionIDSet(mgr.QueryAllSessions())
	want := map[string]bool{"fresh": true, "old": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("QueryAllSessions() = %v, want %v", got, want)
	}
	if visible := sessionIDSet(mgr.QuerySessions("", "")); !reflect.DeepEqual(visible, map[string]bool{"fresh": true}) {
		t.Errorf("QuerySessions() = %v, want only the in-window session", visible)
	}
}

func TestFilterSessionsLocked_NoExcludePatterns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	now := time.Now()
	provider := fakeProvider{
		sessions: []*model.Session{
			{SessionID: "s1", CWD: "/home/user/project", LastActivity: now},
			{SessionID: "s2", CWD: "/home/user/.claude-mem/observer-sessions/abc", LastActivity: now},
		},
	}

	mgr := NewSessionManager(60, provider)
	// No SetExcludeCWDSubstrings call — empty/nil patterns.
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	visible := mgr.VisibleSessions()
	if len(visible) != 2 {
		t.Errorf("VisibleSessions() returned %d sessions, want 2 (no exclusion)", len(visible))
	}
}

func TestFilterSessionsLocked_MultipleExcludePatterns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	now := time.Now()
	provider := fakeProvider{
		sessions: []*model.Session{
			{SessionID: "normal", CWD: "/home/user/project", LastActivity: now},
			{SessionID: "obs", CWD: "/home/user/.claude-mem/observer-sessions/x", LastActivity: now},
			{SessionID: "tmp", CWD: "/tmp/scratch/build", LastActivity: now},
		},
	}

	mgr := NewSessionManager(60, provider)
	mgr.SetExcludeCWDSubstrings([]string{".claude-mem/observer-sessions", "/tmp/scratch"})
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	visible := mgr.VisibleSessions()
	if len(visible) != 1 {
		t.Errorf("VisibleSessions() returned %d sessions, want 1", len(visible))
	}
	if len(visible) > 0 && visible[0].SessionID != "normal" {
		t.Errorf("expected 'normal' session, got %q", visible[0].SessionID)
	}
}

// TestExcludedByCWD_MatchesViewFilterSemantics is the shared-helper test:
// excludedByCWD is the single implementation both the view-level filter
// (filterSessionsLocked) and the discovery-pushdown predicate
// (excludeCWDPredicate, used by Reload) call, so this table is authoritative
// for "is this CWD excluded" everywhere in the manager.
func TestExcludedByCWD_MatchesViewFilterSemantics(t *testing.T) {
	tests := []struct {
		name     string
		cwd      string
		patterns []string
		want     bool
	}{
		{"no patterns", "/home/user/project", nil, false},
		{"empty patterns slice", "/home/user/project", []string{}, false},
		{"single empty-string pattern never matches", "/home/user/project", []string{""}, false},
		{"substring in middle matches", "/home/user/.claude-mem/observer-sessions/x", []string{".claude-mem/observer-sessions"}, true},
		{"substring at start matches", "/tmp/scratch/build", []string{"/tmp/scratch"}, true},
		{"substring at end matches", "/home/user/project/tmp", []string{"/tmp"}, true},
		{"no substring match", "/home/user/project", []string{".claude-mem/observer-sessions"}, false},
		{"case sensitive: differently-cased pattern does not match", "/home/user/Project", []string{"/home/user/project"}, false},
		{"case sensitive: matching case matches", "/home/user/Project", []string{"/home/user/Project"}, true},
		{"multiple patterns: first matches", "/tmp/scratch/build", []string{"/tmp/scratch", ".claude-mem"}, true},
		{"multiple patterns: second matches", "/home/user/.claude-mem/x", []string{"/tmp/scratch", ".claude-mem"}, true},
		{"multiple patterns: none match", "/home/user/other", []string{"/tmp/scratch", ".claude-mem"}, false},
		{"empty pattern among real ones is ignored", "/home/user/project", []string{"", "/tmp/scratch"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := excludedByCWD(tt.cwd, tt.patterns); got != tt.want {
				t.Errorf("excludedByCWD(%q, %v) = %v, want %v", tt.cwd, tt.patterns, got, tt.want)
			}
			// excludeCWDPredicate is Reload's discovery-time pushdown predicate
			// (it returns true = "keep"). It must always be the exact logical
			// negation of excludedByCWD, since it is built directly from it —
			// this is what guarantees the pushdown predicate can never cause a
			// dir-scoped provider to skip a session the view layer would show.
			predicate := excludeCWDPredicate(tt.patterns)
			if got := predicate(tt.cwd); got != !tt.want {
				t.Errorf("excludeCWDPredicate(%v)(%q) = %v, want %v (negation of excludedByCWD)", tt.patterns, tt.cwd, got, !tt.want)
			}
		})
	}
}

// TestReload_NoExcludePatterns_UsesPlainDiscoverSessions proves the "zero
// behavior change when no exclude patterns are configured" binding
// constraint at the mechanism level: with no patterns set, Reload must not
// go through DiscoverMatching's dir-scoped fast path at all -- it must call
// the provider's plain DiscoverSessions(), exactly like before this change.
func TestReload_NoExcludePatterns_UsesPlainDiscoverSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	p := &dirScopedFakeProvider{
		unfiltered: []*model.Session{{SessionID: "plain-path", LastActivity: time.Now()}},
		sessions:   []*model.Session{{SessionID: "fast-path", LastActivity: time.Now()}},
	}
	mgr := NewSessionManager(60, p)
	// No SetExcludeCWDSubstrings call -- patterns are nil/empty.
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	if p.receivedMatcher {
		t.Error("Reload with no exclude patterns must not invoke the dir-scoped fast path (DiscoverMatching)")
	}
	got := mgr.Sessions()
	if len(got) != 1 || got[0].SessionID != "plain-path" {
		t.Fatalf("Sessions() = %#v, want the plain DiscoverSessions() result (zero pushdown when no patterns)", got)
	}
}

// TestReload_PatternChangeAppliesOnNextReload_DirScopedPushdown documents
// and tests the mechanism relied on for "runtime pattern changes": Reload()
// re-reads m.excludeCWDSubstrings on every call, so calling
// SetExcludeCWDSubstrings followed by Reload() picks up the new patterns
// with no extra plumbing. It also proves the exclusion now happens at
// discovery time for a dir-scoped provider: even Sessions() (the "raw",
// unfiltered accessor) shrinks once the pattern is set and a Reload runs --
// not just VisibleSessions()/QuerySessions().
func TestReload_PatternChangeAppliesOnNextReload_DirScopedPushdown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	now := time.Now()
	p := &filteringDirScopedProvider{sessions: []*model.Session{
		{SessionID: "keep", CWD: "/home/user/project", LastActivity: now},
		{SessionID: "heavy", CWD: "/home/user/.claude-mem/observer-sessions/x", LastActivity: now},
	}}
	mgr := NewSessionManager(60, p)

	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload 1 failed: %v", err)
	}
	if len(mgr.Sessions()) != 2 {
		t.Fatalf("before exclusion: Sessions() = %d, want 2 (raw discovery unfiltered)", len(mgr.Sessions()))
	}

	mgr.SetExcludeCWDSubstrings([]string{".claude-mem/observer-sessions"})
	if err := mgr.Reload(); err != nil {
		t.Fatalf("Reload 2 failed: %v", err)
	}

	raw := mgr.Sessions()
	if len(raw) != 1 || raw[0].SessionID != "keep" {
		t.Fatalf("after SetExcludeCWDSubstrings + Reload: Sessions() = %#v, want only 'keep' (pushdown applied at discovery time)", raw)
	}
}

// TestUpdateActivities_AppliesExcludePushdown proves the periodic
// re-discovery path in UpdateActivities (used for providers with
// RefreshInterval > 0, e.g. opencode/kilo/codex) applies the same
// discovery-time pushdown as Reload -- not just at startup. Without the
// fix, mixed setups where a watcher-based provider (e.g. claude) keeps the
// Reload-driven 30s ticker from ever firing would see UpdateActivities'
// ~3s polling loop re-discover unfiltered indefinitely.
func TestUpdateActivities_AppliesExcludePushdown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	now := time.Now()
	p := &filteringDirScopedProvider{
		sessions: []*model.Session{
			{SessionID: "keep", CWD: "/home/user/project", LastActivity: now},
			{SessionID: "heavy", CWD: "/home/user/.claude-mem/observer-sessions/x", LastActivity: now},
		},
		interval: time.Millisecond,
	}
	mgr := NewSessionManager(60, p)
	mgr.SetExcludeCWDSubstrings([]string{".claude-mem/observer-sessions"})

	// First call: m.sessions is empty, so needsRefresh is true regardless of
	// the interval/lastPollDiscover -- this exercises UpdateActivities'
	// periodic-refresh discovery branch on a cold start.
	if !mgr.UpdateActivities() {
		t.Fatal("expected UpdateActivities to report a change on first discovery")
	}
	if !p.receivedMatcher {
		t.Error("UpdateActivities' periodic re-discovery must go through the dir-scoped pushdown path (DiscoverSessionsMatching), not plain DiscoverSessions")
	}
	raw := mgr.Sessions()
	if len(raw) != 1 || raw[0].SessionID != "keep" {
		t.Fatalf("Sessions() = %#v, want only 'keep' (pushdown applied by UpdateActivities)", raw)
	}
}

// TestUpdateActivities_NoExcludePatterns_UsesPlainDiscoverSessions mirrors
// TestReload_NoExcludePatterns_UsesPlainDiscoverSessions for
// UpdateActivities' periodic re-discovery path: with no patterns set, it
// must not invoke the dir-scoped fast path at all.
func TestUpdateActivities_NoExcludePatterns_UsesPlainDiscoverSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	p := &dirScopedFakeProvider{
		unfiltered: []*model.Session{{SessionID: "plain-path", LastActivity: time.Now()}},
		sessions:   []*model.Session{{SessionID: "fast-path", LastActivity: time.Now()}},
		interval:   time.Millisecond,
	}
	mgr := NewSessionManager(60, p)
	// No SetExcludeCWDSubstrings call -- patterns are nil/empty.
	if !mgr.UpdateActivities() {
		t.Fatal("expected UpdateActivities to report a change on first discovery")
	}

	if p.receivedMatcher {
		t.Error("UpdateActivities with no exclude patterns must not invoke the dir-scoped fast path (DiscoverMatching)")
	}
	got := mgr.Sessions()
	if len(got) != 1 || got[0].SessionID != "plain-path" {
		t.Fatalf("Sessions() = %#v, want the plain DiscoverSessions() result (zero pushdown when no patterns)", got)
	}
}

// TestReload_ExcludePushdown_EquivalentToViewFilter is the manager-level
// equivalence property test: for any pattern set, the visible sessions
// (post view-filter) must be identical whether or not discovery-time
// pushdown happened. It uses a fixture provider mix -- one DirScopedProvider
// stub (which genuinely filters using cwdMatch, unlike dirScopedFakeProvider
// in provider_test.go) and one plain stub (which DiscoverMatching cannot
// pre-filter at all) -- to exercise both of DiscoverMatching's code paths at
// once.
func TestReload_ExcludePushdown_EquivalentToViewFilter(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	now := time.Now()
	dirScopedSessions := []*model.Session{
		{SessionID: "ds-keep", CWD: "/home/user/project", LastActivity: now},
		{SessionID: "ds-excluded", CWD: "/home/user/.claude-mem/observer-sessions/x", LastActivity: now},
	}
	plainSessions := []*model.Session{
		{SessionID: "plain-keep", CWD: "/home/user/other-project", LastActivity: now},
		{SessionID: "plain-excluded", CWD: "/tmp/scratch/build", LastActivity: now},
	}

	patternSets := [][]string{
		nil,
		{},
		{".claude-mem/observer-sessions"},
		{"/tmp/scratch"},
		{".claude-mem/observer-sessions", "/tmp/scratch"},
		{"no-match-anywhere"},
	}

	for _, patterns := range patternSets {
		t.Run(strings.Join(patterns, "+"), func(t *testing.T) {
			// Actual: real pushdown, via a MultiProvider mixing a dir-scoped
			// member (fast-path filtering happens at discovery) and a plain
			// member (DiscoverMatching falls back to unfiltered discovery
			// for it, per its documented contract).
			dirScoped := &filteringDirScopedProvider{sessions: cloneSessions(dirScopedSessions)}
			plain := fakeProvider{sessions: cloneSessions(plainSessions)}
			mp := MultiProvider{Providers: []SessionProvider{dirScoped, plain}}

			actualMgr := NewSessionManager(60, mp)
			actualMgr.SetExcludeCWDSubstrings(patterns)
			if err := actualMgr.Reload(); err != nil {
				t.Fatalf("actual Reload failed: %v", err)
			}
			actual := sessionIDSet(actualMgr.VisibleSessions())

			// Expected: no pushdown possible at all (plain provider only,
			// wrapping the exact same full session set) -- so the view
			// filter alone determines what's visible. If pushdown changed
			// the outcome, this would diverge from actual.
			var all []*model.Session
			all = append(all, cloneSessions(dirScopedSessions)...)
			all = append(all, cloneSessions(plainSessions)...)
			wantMgr := NewSessionManager(60, fakeProvider{sessions: all})
			wantMgr.SetExcludeCWDSubstrings(patterns)
			if err := wantMgr.Reload(); err != nil {
				t.Fatalf("want Reload failed: %v", err)
			}
			want := sessionIDSet(wantMgr.VisibleSessions())

			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("patterns=%v: pushdown-visible=%v, view-filter-only-visible=%v (must be identical)", patterns, actual, want)
			}

			// Same equivalence, but driven through UpdateActivities' periodic
			// re-discovery path instead of Reload -- cheap to add since it
			// reuses the same fixtures and the same "want" baseline: only the
			// providers' RefreshInterval needs to be positive (dirScoped2's 1ms)
			// so UpdateActivities' needsRefresh gate re-discovers at all, and
			// the manager's first call always re-discovers (m.sessions starts
			// empty) regardless of interval/lastPollDiscover timing.
			dirScoped2 := &filteringDirScopedProvider{sessions: cloneSessions(dirScopedSessions), interval: time.Millisecond}
			plain2 := fakeProvider{sessions: cloneSessions(plainSessions)}
			mp2 := MultiProvider{Providers: []SessionProvider{dirScoped2, plain2}}

			actualMgr2 := NewSessionManager(60, mp2)
			actualMgr2.SetExcludeCWDSubstrings(patterns)
			if !actualMgr2.UpdateActivities() {
				t.Fatalf("expected UpdateActivities to report a change on first discovery (patterns=%v)", patterns)
			}
			actualUA := sessionIDSet(actualMgr2.VisibleSessions())
			if !reflect.DeepEqual(actualUA, want) {
				t.Fatalf("patterns=%v: UpdateActivities-pushdown-visible=%v, view-filter-only-visible=%v (must be identical)", patterns, actualUA, want)
			}
		})
	}
}

// filteringDirScopedProvider is a DirScopedProvider stub whose
// DiscoverSessionsMatching genuinely applies cwdMatch to its session list
// (unlike dirScopedFakeProvider in provider_test.go, which returns a fixed
// canned result regardless of the matcher it receives). Used to exercise
// the real discovery-time pushdown behavior. interval configures
// RefreshInterval() so it can also exercise UpdateActivities' periodic
// re-discovery path; receivedMatcher/plainCalls record which discovery
// method was actually invoked.
type filteringDirScopedProvider struct {
	sessions        []*model.Session
	interval        time.Duration
	receivedMatcher bool
	plainCalls      int
}

func (p *filteringDirScopedProvider) DiscoverSessions() ([]*model.Session, error) {
	p.plainCalls++
	return p.sessions, nil
}

// DiscoverSessionsMatching honors the same "cwdMatch may be nil, in which
// case every session matches" contract every real DirScopedProvider
// implementation documents (claude, codex, opencode, kilo) -- needed since
// SessionManager.ReloadStreaming's per-member fan-out (unlike Reload, which
// bypasses DiscoverMatching entirely when there are no exclude patterns)
// always calls DiscoverSessionsMatching, passing a literal nil matcher for
// "no patterns configured" rather than skipping the call.
func (p *filteringDirScopedProvider) DiscoverSessionsMatching(cwdMatch func(string) bool) ([]*model.Session, error) {
	p.receivedMatcher = true
	var out []*model.Session
	for _, s := range p.sessions {
		if cwdMatch == nil || cwdMatch(s.CWD) {
			out = append(out, s)
		}
	}
	return out, nil
}

func (p *filteringDirScopedProvider) UseWatcher() bool               { return false }
func (p *filteringDirScopedProvider) RefreshInterval() time.Duration { return p.interval }
func (p *filteringDirScopedProvider) WatchDirs() []string            { return nil }

// cloneSessions returns a shallow copy of the slice (not the pointed-to
// Session structs) so each subtest's providers own an independent slice
// header, even though sessions are shared read-only fixtures.
func cloneSessions(sessions []*model.Session) []*model.Session {
	out := make([]*model.Session, len(sessions))
	copy(out, sessions)
	return out
}

// sessionIDSet returns the set of session IDs present in sessions, for
// order-independent comparison.
func sessionIDSet(sessions []*model.Session) map[string]bool {
	set := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		set[s.SessionID] = true
	}
	return set
}

func TestSessionManager_SetEventBus_PropagatesToTracker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	bus := NewEventBus()
	ch := bus.Subscribe(4)
	defer bus.Unsubscribe(ch)

	now := time.Now()
	p := &mutableStubProvider{sessions: []*model.Session{{SessionID: "s1", Agent: "claude", LastActivity: now, Status: model.StatusThinking}}}
	m := NewSessionManager(60, p)
	m.SetEventBus(bus)

	// First Reload is an observation: no event expected.
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload 1: %v", err)
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event on first observation: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	// Mutate state and reload: now we expect a transition event.
	p.sessions[0].Status = model.StatusExecutingTool
	p.sessions[0].CurrentTool = "Bash"
	p.sessions[0].RecentTools = []model.ToolCall{{Name: "Bash", Timestamp: time.Now()}}
	p.sessions[0].LastActivity = time.Now()
	if err := m.Reload(); err != nil {
		t.Fatalf("Reload 2: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.From != ActivityThinking || ev.To != ActivityRunning {
			t.Fatalf("unexpected transition: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event after real transition")
	}
}

type mutableStubProvider struct{ sessions []*model.Session }

func (p *mutableStubProvider) DiscoverSessions() ([]*model.Session, error) { return p.sessions, nil }
func (p *mutableStubProvider) UseWatcher() bool                            { return false }
func (p *mutableStubProvider) RefreshInterval() time.Duration              { return 0 }
func (p *mutableStubProvider) WatchDirs() []string                         { return nil }
