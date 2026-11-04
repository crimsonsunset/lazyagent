package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/illegalstudio/lazyagent/internal/model"
)

// DesktopCache caches parsed desktop metadata JSON files keyed by file path.
type DesktopCache struct {
	mu      sync.Mutex
	entries map[string]desktopCacheEntry
}

type desktopCacheEntry struct {
	mtime time.Time
	meta  *model.DesktopMeta
	cliID string
}

// NewDesktopCache creates an empty desktop cache.
func NewDesktopCache() *DesktopCache {
	return &DesktopCache{entries: make(map[string]desktopCacheEntry)}
}

// GitTimeout bounds every git call made while discovering sessions, so one
// blocked git (a macOS permission prompt, a dead mount) cannot freeze discovery.
const GitTimeout = 3 * time.Second

// IsWorktree detects if a path is a git worktree and returns the main repo.
// A git that does not answer within GitTimeout counts as "not a worktree".
func IsWorktree(path string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), GitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "--git-dir").Output()
	if err != nil {
		return false, ""
	}
	gitDir := strings.TrimSpace(string(out))

	// In a regular repo: .git
	// In a worktree: absolute path like /repo/.git/worktrees/name
	if filepath.Base(gitDir) == ".git" || !filepath.IsAbs(gitDir) {
		return false, ""
	}

	// It's a worktree — find the main repo
	// gitDir looks like /path/to/main/.git/worktrees/branch-name
	parts := strings.Split(gitDir, string(os.PathSeparator))
	for i, p := range parts {
		if p == ".git" && i+1 < len(parts) && parts[i+1] == "worktrees" {
			mainRepo := filepath.Join(parts[:i]...)
			return true, "/" + mainRepo
		}
	}
	return true, ""
}

// ClaudeProjectsDirs returns the Claude projects directories to scan.
// If configDirs is non-empty, those are used directly (with /projects appended).
// Otherwise it auto-detects from CLAUDE_CONFIG_DIR (falling back to ~/.claude).
func ClaudeProjectsDirs(configDirs []string) []string {
	if len(configDirs) > 0 {
		dirs := make([]string, 0, len(configDirs))
		for _, d := range configDirs {
			dirs = append(dirs, filepath.Join(d, "projects"))
		}
		return dirs
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	defaultDir := filepath.Join(home, ".claude")
	if configDir := os.Getenv("CLAUDE_CONFIG_DIR"); configDir != "" {
		if configDir == defaultDir {
			return []string{filepath.Join(defaultDir, "projects")}
		}
		return []string{
			filepath.Join(configDir, "projects"),
			filepath.Join(defaultDir, "projects"),
		}
	}
	return []string{filepath.Join(defaultDir, "projects")}
}

// wtInfo caches worktree lookups per CWD to avoid redundant git calls.
type wtInfo struct {
	isWorktree bool
	mainRepo   string
}

// ProjectDirForCWD encodes a CWD path to the ~/.claude/projects directory name.
// Claude replaces every / and . with -, keeping the leading slash as a leading -.
func ProjectDirForCWD(cwd string) string {
	r := strings.NewReplacer("/", "-", ".", "-")
	return r.Replace(cwd)
}

// DiscoverSessions scans Claude projects directories for JSONL session files.
// Every JSONL file is a separate session. Uses caches to skip unchanged files.
func DiscoverSessions(cache *model.SessionCache, desktopCache *DesktopCache, configDirs []string) ([]*model.Session, error) {
	return DiscoverSessionsFiltered(cache, desktopCache, configDirs, nil)
}

// DiscoverSessionsFiltered scans Claude projects directories like
// DiscoverSessions, but skips fully parsing files whose cwd is known — with
// certainty — not to match cwdMatch. session.CWD is first-cwd-wins (see
// scanEntries in jsonl.go: it's set from the first entry that carries a
// non-empty "cwd" field and never overwritten afterward), so a bounded
// forward scan of a file's head (headCWD) that finds that same first
// non-empty cwd IS the value a full parse would assign — a positive,
// non-matching result is a confident skip, with no tail-scan fallback needed
// (unlike codex's turn_context, which is last-wins and can drift over the
// file). A file is skipped ONLY when headCWD positively resolves a
// non-matching cwd; whenever the outcome is uncertain (unreadable, no cwd
// field within the bounded window, etc.) the file is parsed anyway, and
// every parsed session's *actual* cwd is checked against cwdMatch before
// being returned — so a session is never silently dropped, only ever parsed
// unnecessarily in the worst case.
//
// cwdMatch may be nil, in which case every session matches (equivalent to
// DiscoverSessions).
func DiscoverSessionsFiltered(cache *model.SessionCache, desktopCache *DesktopCache, configDirs []string, cwdMatch func(string) bool) ([]*model.Session, error) {
	return DiscoverSessionsFilteredIndexed(cache, desktopCache, configDirs, cwdMatch, nil)
}

// DiscoverSessionsFilteredIndexed is DiscoverSessionsFiltered plus a
// CWDIndex: cwdIdx (may be nil, equivalent to DiscoverSessionsFiltered)
// lets the head-scan prefilter reuse previously-determined results — from
// earlier in this process or reloaded from a persisted index written by a
// previous run — for files whose (mtime, size) haven't changed, avoiding
// the head-scan I/O entirely for files that keep getting skipped run after
// run.
func DiscoverSessionsFilteredIndexed(cache *model.SessionCache, desktopCache *DesktopCache, configDirs []string, cwdMatch func(string) bool, cwdIdx *CWDIndex) ([]*model.Session, error) {
	projectsDirs := ClaudeProjectsDirs(configDirs)
	if len(projectsDirs) == 0 {
		return nil, fmt.Errorf("could not find any Claude projects directories")
	}

	wtCache := make(map[string]wtInfo)

	seen := make(map[string]struct{})
	var sessions []*model.Session
	for _, projectsDir := range projectsDirs {
		sessions = discoverInDir(projectsDir, cache, wtCache, seen, sessions, cwdMatch, cwdIdx)
	}
	cache.Prune(seen)
	if cwdIdx != nil {
		cwdIdx.Prune(seen)
	}

	// Enrich with Claude Desktop metadata. Desktop sessions are a separate,
	// small source (never prefiltered) — this only annotates whichever
	// sessions cwdMatch already let through above, so it stays correct
	// regardless of how cwdMatch filtered the list.
	desktopMeta := loadDesktopMetadata(desktopCache)
	for _, session := range sessions {
		if meta, ok := desktopMeta[session.SessionID]; ok {
			session.Desktop = meta
			if session.Name == "" && meta.Title != "" {
				session.Name = meta.Title
			}
		}
	}

	return sessions, nil
}

// parseJob holds the input for a single JSONL file parse operation.
type parseJob struct {
	jsonlFile string
	dirName   string // project directory name for CWD fallback
	cached    *model.Session
	offset    int64
	mtime     time.Time
}

// parseResult holds the output of a single JSONL file parse operation.
type parseResult struct {
	session   *model.Session
	jsonlFile string
	mtime     time.Time
	newOffset int64
}

// discoverInDir scans a single projects directory for JSONL session files
// and appends discovered sessions to the provided slice.
// Files that need parsing (cache miss or incremental update) are parsed in parallel.
func discoverInDir(projectsDir string, cache *model.SessionCache, wtCache map[string]wtInfo, seen map[string]struct{}, sessions []*model.Session, cwdMatch func(string) bool, cwdIdx *CWDIndex) []*model.Session {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return sessions // Directory may not exist; skip it
	}

	// Phase 1: collect all JSONL files and classify as cache-hit or needs-parse.
	var jobs []parseJob
	for _, projectEntry := range entries {
		if !projectEntry.IsDir() {
			continue
		}
		projectPath := filepath.Join(projectsDir, projectEntry.Name())
		jsonlFiles, err := filepath.Glob(filepath.Join(projectPath, "*.jsonl"))
		if err != nil || len(jsonlFiles) == 0 {
			continue
		}

		for _, jsonlFile := range jsonlFiles {
			seen[jsonlFile] = struct{}{}
			cached, offset, mtime := cache.GetIncremental(jsonlFile)

			if cached != nil && offset == 0 {
				// Full cache hit — cached.CWD is the value a previous parse
				// (full or incremental — either way, fully caught up to
				// this file's current content) assigned: first-cwd-wins, or
				// the decodeDirName fallback (see phase 2 below). Either
				// way it's stable and final, so it's safe to filter on
				// directly without touching the file.
				if cwdMatch != nil && !cwdMatch(cached.CWD) {
					continue
				}
				sessions = append(sessions, cached)
				continue
			}

			if cwdMatch != nil && cached == nil {
				// A genuine first-time full parse (no cached data at all): try
				// to rule the file out before committing to the expensive parse.
				// Incremental jobs (cached != nil, offset > 0) are deliberately
				// NOT prefiltered here — their appended bytes are cheap to parse
				// regardless (only new lines are read), and the uniform
				// post-parse filter below is the single source of truth for
				// whether they're included.
				if !shouldParseForCWDIndexed(jsonlFile, cwdMatch, cwdIdx) {
					continue
				}
			}

			jobs = append(jobs, parseJob{
				jsonlFile: jsonlFile,
				dirName:   projectEntry.Name(),
				cached:    cached,
				offset:    offset,
				mtime:     mtime,
			})
		}
	}

	if len(jobs) == 0 {
		return sessions
	}

	// Phase 2: parse files in parallel.
	workers := runtime.GOMAXPROCS(0)
	if workers > len(jobs) {
		workers = len(jobs)
	}

	results := make([]parseResult, len(jobs))
	var wg sync.WaitGroup
	jobCh := make(chan int, len(jobs))

	for i := range jobs {
		jobCh <- i
	}
	close(jobCh)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobCh {
				j := &jobs[idx]
				var session *model.Session
				var newOffset int64

				if j.cached != nil && j.offset > 0 {
					s, off, err := ParseJSONLIncremental(j.jsonlFile, j.offset, j.cached)
					if err != nil {
						continue
					}
					session = s
					newOffset = off
				} else {
					s, size, err := ParseJSONL(j.jsonlFile)
					if err != nil {
						continue
					}
					session = s
					newOffset = size
				}

				if session.CWD == "" {
					session.CWD = decodeDirName(j.dirName)
				}

				results[idx] = parseResult{
					session:   session,
					jsonlFile: j.jsonlFile,
					mtime:     j.mtime,
					newOffset: newOffset,
				}
			}
		}()
	}
	wg.Wait()

	// Phase 3: enrich worktree info and update cache (sequential — wtCache is not thread-safe
	// and enrichWorktree may fork git processes).
	for _, r := range results {
		if r.session == nil {
			continue
		}
		enrichWorktree(r.session, wtCache)
		// Always cache the fully parsed result, regardless of this call's
		// matcher — the cache is shared across queries.
		cache.Put(r.jsonlFile, r.mtime, r.newOffset, r.session)
		// Uniform post-parse filter: the head-scan prefilter above is purely
		// a skip optimization, so the parsed session's actual CWD (not
		// whatever the prefilter guessed) is the single source of truth for
		// whether it's returned.
		if cwdMatch != nil && !cwdMatch(r.session.CWD) {
			continue
		}
		sessions = append(sessions, r.session)
	}
	return sessions
}

// enrichWorktree sets worktree info on a session, using a cache to avoid redundant git calls.
func enrichWorktree(session *model.Session, wtCache map[string]wtInfo) {
	if _, ok := wtCache[session.CWD]; !ok {
		isWT, mainRepo := IsWorktree(session.CWD)
		wtCache[session.CWD] = wtInfo{isWT, mainRepo}
	}
	wt := wtCache[session.CWD]
	session.IsWorktree = wt.isWorktree
	session.MainRepo = wt.mainRepo
}

func decodeDirName(name string) string {
	// Reverse of ProjectDirForCWD: dashes → slashes, prepend /
	// This is a best-effort heuristic
	return "/" + strings.ReplaceAll(name, "-", "/")
}

// maxHeadScanBytes and maxHeadScanLines bound how much of a claude session
// JSONL file headCWD will read before giving up, so a pathological file
// (huge lines, or many lines with no cwd field) can't force reading large
// amounts of data into memory or make the prefilter itself expensive.
const (
	maxHeadScanBytes = 64 * 1024
	maxHeadScanLines = 25
)

// headCWD reads forward from the start of a claude session JSONL file,
// looking for the first entry that carries a non-empty top-level "cwd"
// field, stopping as soon as one is found or the bounded window
// (maxHeadScanBytes / maxHeadScanLines, whichever is hit first) is
// exhausted. Because session.CWD is first-cwd-wins (see scanEntries in
// jsonl.go), this is exactly the cwd a full parse would assign to the
// session — so a result found here is not a heuristic, it's the actual
// answer, obtained without paying for the rest of the file.
//
// Each line is unmarshaled into the same jsonlEntry type scanEntries uses,
// with the same "any unmarshal error skips this line" rule (jsonl.go's
// scanEntries: `if err := json.Unmarshal(...); err != nil { continue }`,
// checked before the cwd assignment). This matters: encoding/json fills in
// whatever fields it can from a JSON object before reporting a type
// mismatch on an unrelated field (e.g. a string where jsonlEntry.CostUSD
// expects a float64), so a smaller/different struct that happens not to
// declare that field would silently accept a line scanEntries would have
// skipped — producing a cwd headCWD is confident about but a full parse
// would never have used. Reusing jsonlEntry verbatim makes the two decoders
// equivalent by construction, not by claim.
//
// ok is false whenever no cwd could be determined within the bounded window
// (missing file, empty file, the window closed before any entry supplied a
// cwd) — callers must treat that as "unknown, don't filter it out" rather
// than "no match", since the real cwd might still appear later in the file.
func headCWD(path string) (cwd string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var bytesRead, lines int
	for scanner.Scan() {
		line := scanner.Bytes()
		lines++
		bytesRead += len(line) + 1 // +1 for the stripped newline

		var e jsonlEntry
		if err := json.Unmarshal(line, &e); err == nil && e.CWD != "" {
			return e.CWD, true
		}

		if lines >= maxHeadScanLines || bytesRead >= maxHeadScanBytes {
			return "", false
		}
	}
	return "", false
}

// shouldParseForCWD reports whether a session file might still end up
// matching cwdMatch and should therefore be parsed. It returns false —
// "skip, don't parse" — ONLY when headCWD positively resolves a cwd (the
// same value a full parse would assign to session.CWD, since claude is
// first-cwd-wins) and cwdMatch rejects it. Whenever headCWD can't determine
// a cwd within its bounded window, this conservatively returns true, so a
// session is never skipped without certainty; the actual parsed cwd is
// still checked afterward by the caller's uniform post-parse filter.
//
// This is shouldParseForCWDIndexed with a nil index (always live I/O) — see
// that function for the cache-aware version used by discovery once a
// CWDIndex is available.
func shouldParseForCWD(path string, cwdMatch func(string) bool) bool {
	return shouldParseForCWDIndexed(path, cwdMatch, nil)
}

// shouldParseForCWDIndexed is shouldParseForCWD's cache-aware counterpart:
// the identical decision (see shouldParseForCWD's doc comment), but headCWD
// is looked up through cwdIdx when cwdIdx is non-nil, instead of always
// re-reading the file. headCWD is a pure function of a file's content (it
// takes no matcher), so a cached result is exactly what fresh I/O would
// return for an unchanged file, regardless of which matcher originally
// populated the cache entry. cwdIdx == nil reproduces shouldParseForCWD's
// plain (always-live-I/O) behavior exactly.
func shouldParseForCWDIndexed(path string, cwdMatch func(string) bool, cwdIdx *CWDIndex) bool {
	if cwdIdx == nil {
		cwd, ok := headCWD(path)
		if !ok {
			return true // can't tell within the window — parse it
		}
		return cwdMatch(cwd)
	}

	info, err := os.Stat(path)
	if err != nil {
		return true // can't stat — conservative parse, same as headCWD's own "can't open" -> not ok -> true
	}
	cwd, ok := cwdIdx.headCWDIndexed(path, info.ModTime(), info.Size())
	if !ok {
		return true
	}
	return cwdMatch(cwd)
}
