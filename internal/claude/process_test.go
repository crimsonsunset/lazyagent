package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/illegalstudio/lazyagent/internal/model"
)

func TestClaudeProjectsDirs_WithConfigDirs(t *testing.T) {
	dirs := ClaudeProjectsDirs([]string{"/custom/claude", "/other/path"})
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d", len(dirs))
	}
	if dirs[0] != "/custom/claude/projects" {
		t.Errorf("dirs[0] = %q, want /custom/claude/projects", dirs[0])
	}
	if dirs[1] != "/other/path/projects" {
		t.Errorf("dirs[1] = %q, want /other/path/projects", dirs[1])
	}
}

func TestClaudeProjectsDirs_DefaultFallback(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	dirs := ClaudeProjectsDirs(nil)
	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(dirs))
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".claude", "projects")
	if dirs[0] != want {
		t.Errorf("dirs[0] = %q, want %q", dirs[0], want)
	}
}

func TestClaudeProjectsDirs_EnvVarCustom(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/custom-claude")
	dirs := ClaudeProjectsDirs(nil)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d", len(dirs))
	}
	if dirs[0] != "/tmp/custom-claude/projects" {
		t.Errorf("dirs[0] = %q, want /tmp/custom-claude/projects", dirs[0])
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".claude", "projects")
	if dirs[1] != want {
		t.Errorf("dirs[1] = %q, want %q", dirs[1], want)
	}
}

func TestClaudeProjectsDirs_EnvVarSameAsDefault(t *testing.T) {
	home, _ := os.UserHomeDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	dirs := ClaudeProjectsDirs(nil)
	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir (deduplicated), got %d", len(dirs))
	}
	want := filepath.Join(home, ".claude", "projects")
	if dirs[0] != want {
		t.Errorf("dirs[0] = %q, want %q", dirs[0], want)
	}
}

func TestClaudeProjectsDirs_ConfigDirsOverridesEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/should/be/ignored")
	dirs := ClaudeProjectsDirs([]string{"/explicit/dir"})
	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(dirs))
	}
	if dirs[0] != "/explicit/dir/projects" {
		t.Errorf("dirs[0] = %q, want /explicit/dir/projects", dirs[0])
	}
}

// --- fixture helpers for headCWD / DiscoverSessionsFiltered tests ---

// newTestProjectsRoot creates a fresh temp "config dir" and its "projects"
// subdirectory, mirroring ~/.claude and ~/.claude/projects, and returns
// both — configDir is what DiscoverSessions(Filtered)'s configDirs
// parameter expects (it appends "projects" itself via ClaudeProjectsDirs).
func newTestProjectsRoot(t *testing.T) (configDir, projectsDir string) {
	t.Helper()
	configDir = t.TempDir()
	projectsDir = filepath.Join(configDir, "projects")
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return configDir, projectsDir
}

// writeClaudeSession writes a claude session JSONL file at
// projectsDir/<dirName>/<sessionID>.jsonl (mirroring the real
// ~/.claude/projects/<encoded-cwd>/<sessionID>.jsonl layout) with exactly
// the given raw lines, and returns its path.
func writeClaudeSession(t *testing.T, projectsDir, dirName, sessionID string, lines []string) string {
	t.Helper()
	dir := filepath.Join(projectsDir, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// cwdLine returns a minimal, valid claude JSONL entry carrying cwd on its
// top-level "cwd" field — the field both headCWD and scanEntries key off.
func cwdLine(cwd string) string {
	return fmt.Sprintf(`{"type":"system","cwd":%q,"timestamp":"2026-03-01T11:00:00.000Z"}`, cwd)
}

// noCWDLine returns a valid claude JSONL entry with no "cwd" field at all —
// used to pad a fixture past headCWD's scan window without ever supplying
// a cwd.
func noCWDLine(i int) string {
	return fmt.Sprintf(`{"type":"system","note":"filler-%d","timestamp":"2026-03-01T11:00:00.000Z"}`, i)
}

// cwdLineWithBadCostUSD returns a claude JSONL entry with a well-formed cwd
// but a type-mismatched "costUSD" field (a JSON string where jsonlEntry
// declares it as a float64). scanEntries decodes each line into the full
// jsonlEntry and skips the whole line on ANY unmarshal error, checked
// before the cwd assignment (jsonl.go) — and encoding/json reports a type
// mismatch on costUSD even though cwd itself decoded fine, so a full parse
// never sees this line's cwd at all. This is the exact class of line a
// smaller/different decoder (one that doesn't declare a costUSD field, and
// so never notices the mismatch) would wrongly accept.
func cwdLineWithBadCostUSD(cwd string) string {
	return fmt.Sprintf(`{"type":"system","cwd":%q,"costUSD":"not-a-number","timestamp":"2026-03-01T11:00:00.000Z"}`, cwd)
}

// --- headCWD ---

func TestHeadCWD_FirstLineCWD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := cwdLine("/tmp/project-a") + "\n" + cwdLine("/tmp/project-b") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, ok := headCWD(path)
	if !ok {
		t.Fatal("headCWD ok = false, want true")
	}
	if cwd != "/tmp/project-a" {
		t.Fatalf("headCWD cwd = %q, want /tmp/project-a (first entry wins)", cwd)
	}
}

func TestHeadCWD_CWDOnLaterLineWithinWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{noCWDLine(1), noCWDLine(2), cwdLine("/tmp/project-a")}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, ok := headCWD(path)
	if !ok || cwd != "/tmp/project-a" {
		t.Fatalf("headCWD = (%q, %v), want (/tmp/project-a, true)", cwd, ok)
	}
}

func TestHeadCWD_GarbageLinesSkippedUntilCWDFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{"not valid json at all", "{also not json", cwdLine("/tmp/project-a")}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, ok := headCWD(path)
	if !ok || cwd != "/tmp/project-a" {
		t.Fatalf("headCWD = (%q, %v), want (/tmp/project-a, true)", cwd, ok)
	}
}

func TestHeadCWD_NoCWDAnywhereInFile_NotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{noCWDLine(1), noCWDLine(2), noCWDLine(3)}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := headCWD(path); ok {
		t.Fatal("headCWD ok = true, want false (no cwd anywhere in the file)")
	}
}

func TestHeadCWD_LineCountCapExceeded_NotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	var lines []string
	for i := 0; i < maxHeadScanLines+5; i++ {
		lines = append(lines, noCWDLine(i))
	}
	lines = append(lines, cwdLine("/tmp/project-a")) // lies beyond the cap
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := headCWD(path); ok {
		t.Fatal("headCWD ok = true, want false (cwd lies beyond the line-count cap)")
	}
}

func TestHeadCWD_ByteSizeCapExceeded_NotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	pad := strings.Repeat("x", 15000)
	// 5 lines (well under the 25-line cap) already exceed the 64 KiB byte
	// cap, so this isolates byte-cap behavior from line-count-cap behavior.
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"system","note":%q}`, pad))
	}
	lines = append(lines, cwdLine("/tmp/project-a")) // lies beyond the byte cap
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := headCWD(path); ok {
		t.Fatal("headCWD ok = true, want false (cwd lies beyond the byte-size cap, well within the line-count cap)")
	}
}

func TestHeadCWD_EmptyFile_NotOk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := headCWD(path); ok {
		t.Fatal("headCWD ok = true, want false for empty file")
	}
}

func TestHeadCWD_NonexistentFile_NotOk(t *testing.T) {
	if _, ok := headCWD(filepath.Join(t.TempDir(), "missing.jsonl")); ok {
		t.Fatal("headCWD ok = true, want false for missing file")
	}
}

// TestHeadCWD_TypeMismatchOnUnrelatedFieldSkipsLine reproduces a review-
// caught bug: headCWD used to decode each line into a minimal {cwd string}
// struct, which — unlike scanEntries' full jsonlEntry decode — has no field
// for costUSD at all, so it silently ignores a type-mismatched costUSD
// instead of erroring the whole line the way scanEntries does. That let
// headCWD confidently return a cwd a full parse would never actually have
// assigned. Line 1 here has a well-formed cwd plus a bad costUSD (the exact
// class scanEntries would skip); line 2 has a different, clean cwd. headCWD
// must skip line 1 (matching scanEntries) and return line 2's cwd.
func TestHeadCWD_TypeMismatchOnUnrelatedFieldSkipsLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{cwdLineWithBadCostUSD("/tmp/wrong"), cwdLine("/tmp/right")}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cwd, ok := headCWD(path)
	if !ok || cwd != "/tmp/right" {
		t.Fatalf("headCWD = (%q, %v), want (/tmp/right, true) — line 1's cwd must be skipped like scanEntries would skip it (type mismatch on costUSD)", cwd, ok)
	}
}

// --- shouldParseForCWD ---

func TestShouldParseForCWD_HeadMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(cwdLine("/tmp/project-a")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	match := func(cwd string) bool { return cwd == "/tmp/project-a" }
	if !shouldParseForCWD(path, match) {
		t.Fatal("shouldParseForCWD = false, want true (head matches)")
	}
}

func TestShouldParseForCWD_HeadConclusivelyRulesOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(cwdLine("/tmp/project-a")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	match := func(cwd string) bool { return cwd == "/tmp/project-b" }
	if shouldParseForCWD(path, match) {
		t.Fatal("shouldParseForCWD = true, want false (head conclusively shows a different, non-matching cwd)")
	}
}

func TestShouldParseForCWD_NoCWDInWindowFallsBackToParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(noCWDLine(1)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	match := func(cwd string) bool { return false }
	if !shouldParseForCWD(path, match) {
		t.Fatal("shouldParseForCWD = false, want true (no cwd within the window — conservative parse)")
	}
}

// --- DiscoverSessionsFiltered ---

func TestDiscoverSessionsFiltered_ScopesToMatchingCWD(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	pathA := writeClaudeSession(t, projectsDir, "dir-a", "sess-a", []string{cwdLine("/tmp/project-a")})
	pathB := writeClaudeSession(t, projectsDir, "dir-b", "sess-b", []string{cwdLine("/tmp/project-b")})

	matchA := func(cwd string) bool { return cwd == "/tmp/project-a" }
	sessions, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, matchA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gotPaths := make(map[string]bool)
	for _, s := range sessions {
		gotPaths[s.JSONLPath] = true
	}
	if !gotPaths[pathA] {
		t.Errorf("expected matching cwd file %s to be included", pathA)
	}
	if gotPaths[pathB] {
		t.Errorf("did not expect non-matching cwd file %s to be included", pathB)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
}

func TestDiscoverSessionsFiltered_NilMatcherReturnsAll(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	writeClaudeSession(t, projectsDir, "dir-a", "sess-a", []string{cwdLine("/tmp/project-a")})
	writeClaudeSession(t, projectsDir, "dir-b", "sess-b", []string{cwdLine("/tmp/project-b")})
	writeClaudeSession(t, projectsDir, "dir-c", "sess-c", []string{noCWDLine(1)})

	sessions, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3 (nil matcher must not filter anything)", len(sessions))
	}
}

func TestDiscoverSessions_StillReturnsEverything(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	writeClaudeSession(t, projectsDir, "dir-a", "sess-a", []string{cwdLine("/tmp/project-a")})
	writeClaudeSession(t, projectsDir, "dir-b", "sess-b", []string{cwdLine("/tmp/project-b")})

	sessions, err := DiscoverSessions(model.NewSessionCache(), NewDesktopCache(), []string{configDir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
}

// TestDiscoverSessionsFiltered_FullCacheHitUsesCachedCWDWithoutRereadingFile
// asserts the full-cache-hit fast path actually relies on cached.CWD instead
// of touching the file: it corrupts pathA's on-disk content after priming
// the cache (while preserving its exact mtime, so GetIncremental still
// reports a full hit) and confirms the filtered result is still correct —
// which is only possible if the cached CWD, not a re-read of the now-garbage
// file, is what got checked.
func TestDiscoverSessionsFiltered_FullCacheHitUsesCachedCWDWithoutRereadingFile(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	pathA := writeClaudeSession(t, projectsDir, "dir-a", "sess-a", []string{cwdLine("/tmp/project-a")})
	pathB := writeClaudeSession(t, projectsDir, "dir-b", "sess-b", []string{cwdLine("/tmp/project-b")})

	cache := model.NewSessionCache()
	// Prime the cache with a full parse (nil matcher) so both files are cached.
	if _, err := DiscoverSessionsFiltered(cache, NewDesktopCache(), []string{configDir}, nil); err != nil {
		t.Fatalf("priming pass: unexpected error: %v", err)
	}

	infoA, err := os.Stat(pathA)
	if err != nil {
		t.Fatal(err)
	}
	mtimeA := infoA.ModTime()

	// Corrupt pathA on disk (a re-read would find no cwd at all), but
	// restore its exact mtime so the cache still treats it as unchanged.
	if err := os.WriteFile(pathA, []byte("garbage, not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(pathA, mtimeA, mtimeA); err != nil {
		t.Fatal(err)
	}

	matchA := func(cwd string) bool { return cwd == "/tmp/project-a" }
	sessions, err := DiscoverSessionsFiltered(cache, NewDesktopCache(), []string{configDir}, matchA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gotPaths := make(map[string]bool)
	for _, s := range sessions {
		gotPaths[s.JSONLPath] = true
	}
	if !gotPaths[pathA] {
		t.Errorf("expected cached matching-cwd file %s to be included via cached.CWD despite corrupted on-disk content", pathA)
	}
	if gotPaths[pathB] {
		t.Errorf("did not expect cached non-matching-cwd file %s to be included", pathB)
	}
}

// TestDiscoverSessionsFiltered_ConservativeWhenNoCWDInWindow covers the
// conservative branch: headCWD can't determine a cwd within its bounded
// window, so the file must still be parsed rather than skipped. It's then
// included via the existing decodeDirName(dirName) fallback that phase 2
// applies whenever a fully parsed session ends up with an empty CWD.
func TestDiscoverSessionsFiltered_ConservativeWhenNoCWDInWindow(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	path := writeClaudeSession(t, projectsDir, "dir-x", "sess-no-cwd", []string{noCWDLine(1), noCWDLine(2)})

	match := func(cwd string) bool { return cwd == decodeDirName("dir-x") }
	sessions, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, match)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].JSONLPath != path {
		t.Fatalf("sessions = %#v, want the no-cwd-in-window session included via the decodeDirName fallback", sessions)
	}
}

// TestDiscoverSessionsFiltered_ImmuneToEncoderMismatch_UnderscoreViolation
// reproduces violation class (a) found by Task 8's empirical check: a real
// project directory whose name does not round-trip through
// ProjectDirForCWD, because Claude's real directory naming folds "_" into
// "-" too (ProjectDirForCWD doesn't). A directory-name-based skip rule would
// get this wrong; the head-scan design never looks at the directory name at
// all, so it can't be fooled by an encoder mismatch — it reads the real cwd
// straight from the file.
func TestDiscoverSessionsFiltered_ImmuneToEncoderMismatch_UnderscoreViolation(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	path := writeClaudeSession(t, projectsDir, "totally-unrelated-dirname", "sess-underscore", []string{cwdLine("/tmp/nltk_data")})

	match := func(cwd string) bool { return cwd == "/tmp/nltk_data" }
	sessions, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, match)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 || sessions[0].JSONLPath != path {
		t.Fatalf("sessions = %#v, want the session under the mismatched-name directory included", sessions)
	}
}

// TestDiscoverSessionsFiltered_ImmuneToResumedElsewhereViolation reproduces
// violation class (b) found by Task 8's empirical check: a session resumed
// from a different directory than where it started ends up filed under a
// project directory named for the LATEST cwd, while its (first-cwd-wins)
// session.CWD is still the ORIGINAL cwd. A query for the original cwd must
// still find it, matching today's unfiltered discovery; a query for the
// directory it's actually filed under must NOT return it, since that's not
// what session.CWD resolves to.
func TestDiscoverSessionsFiltered_ImmuneToResumedElsewhereViolation(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	path := writeClaudeSession(t, projectsDir, "dir-for-project-b", "sess-resumed", []string{
		cwdLine("/tmp/project-a"), // first cwd — wins, becomes session.CWD
		cwdLine("/tmp/project-b"), // later cwd (session resumed elsewhere) — ignored
	})

	matchA := func(cwd string) bool { return cwd == "/tmp/project-a" }
	sessionsA, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, matchA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessionsA) != 1 || sessionsA[0].JSONLPath != path {
		t.Fatalf("sessions for project-a = %#v, want the resumed session included (its first cwd is project-a)", sessionsA)
	}

	matchB := func(cwd string) bool { return cwd == "/tmp/project-b" }
	sessionsB, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, matchB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessionsB) != 0 {
		t.Fatalf("sessions for project-b = %#v, want empty: session.CWD is project-a (first-wins), even though the file is filed under a project-b-named directory", sessionsB)
	}
}

// TestDiscoverSessionsFiltered_TypeMismatchLineSkippedLikeFullParse
// reproduces a review-caught bug at the DiscoverSessionsFiltered level:
// headCWD used to decode into a minimal {cwd string} struct that silently
// accepted a line with a type-mismatched unrelated field (costUSD as a
// string) — a line the full jsonlEntry decode scanEntries uses would skip
// entirely. That let the prefilter confidently report a cwd
// ("/tmp/wrong") a full parse would never actually assign to session.CWD
// (which ends up "/tmp/right", from the next clean line) — a silent false
// negative for any query matching "/tmp/right" and a silent false positive
// for any query matching "/tmp/wrong".
func TestDiscoverSessionsFiltered_TypeMismatchLineSkippedLikeFullParse(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	path := writeClaudeSession(t, projectsDir, "dir-type-mismatch", "sess-type-mismatch", []string{
		cwdLineWithBadCostUSD("/tmp/wrong"), // scanEntries skips this whole line
		cwdLine("/tmp/right"),               // ...so this is what session.CWD actually becomes
	})

	matchRight := func(cwd string) bool { return cwd == "/tmp/right" }
	sessionsRight, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, matchRight)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessionsRight) != 1 || sessionsRight[0].JSONLPath != path {
		t.Fatalf("sessions for /tmp/right = %#v, want the session included (its real, post-parse cwd, once the bad line is skipped, is /tmp/right)", sessionsRight)
	}

	matchWrong := func(cwd string) bool { return cwd == "/tmp/wrong" }
	sessionsWrong, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, matchWrong)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessionsWrong) != 0 {
		t.Fatalf("sessions for /tmp/wrong = %#v, want empty: a full parse never assigns session.CWD=/tmp/wrong (scanEntries skips that whole line, exactly like headCWD now does)", sessionsWrong)
	}
}

// TestDiscoverSessionsFiltered_IncrementalAppendAlwaysReparsedPostParseFilterDecides
// covers the offset>0 branch: discoverInDir deliberately never prefilters
// incremental jobs (see the comment there) — they must always be enqueued
// and reparsed, with the uniform post-parse filter as the sole arbiter of
// inclusion, even when queried with a matcher that doesn't match. This
// primes a cache, appends a real user-message line (observable via
// UserMessages, independent of CWD), and issues a NON-matching query —
// then inspects the cache directly (via GetIncremental) to confirm that
// non-matching query alone already triggered the reparse (the cache now
// reports a full hit with the appended line's effect applied), rather than
// silently skipping the file because its cwd didn't match.
func TestDiscoverSessionsFiltered_IncrementalAppendAlwaysReparsedPostParseFilterDecides(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	path := writeClaudeSession(t, projectsDir, "dir-a", "sess-a", []string{cwdLine("/tmp/project-a")})

	cache := model.NewSessionCache()
	matchA := func(cwd string) bool { return cwd == "/tmp/project-a" }

	primed, err := DiscoverSessionsFiltered(cache, NewDesktopCache(), []string{configDir}, matchA)
	if err != nil {
		t.Fatalf("priming pass: unexpected error: %v", err)
	}
	if len(primed) != 1 || primed[0].UserMessages != 0 {
		t.Fatalf("primed session = %#v, want 1 session with 0 user messages", primed)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"user","cwd":"/tmp/project-a","timestamp":"2026-03-01T11:00:01.000Z","message":{"role":"user","content":"hello"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// GetIncremental treats same-mtime as a full hit (mtime-only by
	// design). On 1-second filesystems the append can share the priming
	// write's mtime, so force a later timestamp.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	later := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}

	matchOther := func(cwd string) bool { return cwd == "/tmp/does-not-exist" }
	sessionsOther, err := DiscoverSessionsFiltered(cache, NewDesktopCache(), []string{configDir}, matchOther)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessionsOther) != 0 {
		t.Fatalf("sessions = %#v, want empty (post-parse filter must exclude a real non-match)", sessionsOther)
	}

	// The non-matching query above must already have enqueued and reparsed
	// the grown file — proven by asking the cache directly, with no further
	// disk changes: a full hit (offset 0) whose cached session reflects the
	// appended line means the reparse happened, not a skip.
	cachedAfter, offsetAfter, _ := cache.GetIncremental(path)
	if offsetAfter != 0 {
		t.Fatalf("cache offset after the non-matching query = %d, want 0 (a full hit) — the incremental branch must always be enqueued and reparsed, never skipped by the prefilter, even for a non-matching cwdMatch", offsetAfter)
	}
	if cachedAfter == nil || cachedAfter.UserMessages != 1 {
		t.Fatalf("cachedAfter = %#v, want a session with UserMessages=1 (the appended line must have been parsed by the earlier non-matching query)", cachedAfter)
	}
}

// TestDiscoverSessionsFiltered_EquivalentToManualFilterOfFullDiscovery is an
// equivalence-property test: for a tree covering a plain match, a plain
// non-match, a no-cwd-anywhere file, and both real violation classes found
// by Task 8's empirical check (encoder mismatch, resumed-elsewhere),
// DiscoverSessionsFiltered's result must equal filtering a full (unfiltered)
// discovery pass by the same matcher — proving the fast path never silently
// drops a session the slow path would have returned.
func TestDiscoverSessionsFiltered_EquivalentToManualFilterOfFullDiscovery(t *testing.T) {
	configDir, projectsDir := newTestProjectsRoot(t)
	writeClaudeSession(t, projectsDir, "dir-a", "sess-a", []string{cwdLine("/tmp/project-a")})
	writeClaudeSession(t, projectsDir, "dir-b", "sess-b", []string{cwdLine("/tmp/project-b")})
	writeClaudeSession(t, projectsDir, "dir-no-cwd", "sess-no-cwd", []string{noCWDLine(1)})
	writeClaudeSession(t, projectsDir, "totally-unrelated-dirname", "sess-underscore", []string{cwdLine("/tmp/nltk_data")})
	writeClaudeSession(t, projectsDir, "dir-for-project-b", "sess-resumed", []string{
		cwdLine("/tmp/project-a"),
		cwdLine("/tmp/project-b"),
	})
	// Review-caught regression fixture: a well-formed cwd sharing a line
	// with a type-mismatched unrelated field, followed by a clean different
	// cwd — scanEntries skips the whole first line, so session.CWD ends up
	// "/tmp/right", never "/tmp/wrong".
	writeClaudeSession(t, projectsDir, "dir-type-mismatch", "sess-type-mismatch", []string{
		cwdLineWithBadCostUSD("/tmp/wrong"),
		cwdLine("/tmp/right"),
	})

	matchers := map[string]func(string) bool{
		"project-a":           func(cwd string) bool { return cwd == "/tmp/project-a" },
		"project-b":           func(cwd string) bool { return cwd == "/tmp/project-b" },
		"nltk_data":           func(cwd string) bool { return cwd == "/tmp/nltk_data" },
		"type-mismatch-right": func(cwd string) bool { return cwd == "/tmp/right" },
		"type-mismatch-wrong": func(cwd string) bool { return cwd == "/tmp/wrong" },
		"nothing-at-all":      func(cwd string) bool { return cwd == "/tmp/does-not-exist" },
	}

	for name, matcher := range matchers {
		t.Run(name, func(t *testing.T) {
			full, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, nil)
			if err != nil {
				t.Fatalf("full discovery: unexpected error: %v", err)
			}
			var want []string
			for _, s := range full {
				if matcher(s.CWD) {
					want = append(want, s.JSONLPath)
				}
			}
			sort.Strings(want)

			got, err := DiscoverSessionsFiltered(model.NewSessionCache(), NewDesktopCache(), []string{configDir}, matcher)
			if err != nil {
				t.Fatalf("filtered discovery: unexpected error: %v", err)
			}
			var gotPaths []string
			for _, s := range got {
				gotPaths = append(gotPaths, s.JSONLPath)
			}
			sort.Strings(gotPaths)

			if !slices.Equal(want, gotPaths) {
				t.Fatalf("filtered discovery = %v, want %v (must equal manual filter of full discovery)", gotPaths, want)
			}
		})
	}
}
