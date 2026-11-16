package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// dispatchRunsEnv overrides where fellowship's dispatch run files live.
const dispatchRunsEnv = "LAZYAGENT_DISPATCH_RUNS_DIR"

// incompleteScanRetry is how long an index built from a partly unreadable runs
// dir is trusted before the next request rescans it. Permission fixes do not
// change the dir's mtime, so without this the gap would never close.
const incompleteScanRetry = 30 * time.Second

// Who started an agent: a fellowship dispatch run (Slack, Windmill, CodeRabbit
// fixes) or Joe directly in Cursor.
const (
	originGandalf = "gandalf"
	originYou     = "you"
)

// dispatchOrigins knows which agent ids a dispatch run created, read from the
// run JSON files fellowship writes on this machine. The index is rebuilt only
// when the directory's mtime changes; fellowship writes a run to a temp file
// and renames it, which bumps the mtime, so new runs and late ids show up. A
// scan that could not read every file is retried after incompleteScanRetry.
type dispatchOrigins struct {
	dir string

	mu      sync.Mutex
	mtime   time.Time
	ids     map[string]bool
	retryAt time.Time
}

// newDispatchOrigins resolves the runs dir from LAZYAGENT_DISPATCH_RUNS_DIR,
// then $DISPATCH_HOME/runs, then ~/.cache/dispatch/runs (fellowship's default).
func newDispatchOrigins() *dispatchOrigins {
	if dir := strings.TrimSpace(os.Getenv(dispatchRunsEnv)); dir != "" {
		return &dispatchOrigins{dir: dir}
	}
	if home := strings.TrimSpace(os.Getenv("DISPATCH_HOME")); home != "" {
		return &dispatchOrigins{dir: filepath.Join(home, "runs")}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return &dispatchOrigins{}
	}
	return &dispatchOrigins{dir: filepath.Join(home, ".cache", "dispatch", "runs")}
}

// origin returns "gandalf" when a dispatch run created the agent with this id,
// else "you". A missing or unreadable runs dir means every agent is "you".
func (d *dispatchOrigins) origin(id string) string {
	if d == nil || d.dir == "" {
		return originYou
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refresh()
	if d.ids[id] {
		return originGandalf
	}
	return originYou
}

// refresh rebuilds the id index when the runs dir changed, or when the last
// scan was incomplete and its retry time has passed. Callers hold d.mu.
// ponytail: full rescan on every dir change (~250 small files today). If run
// files pile up into the thousands, parse only files newer than the last scan.
func (d *dispatchOrigins) refresh() {
	info, err := os.Stat(d.dir)
	if err != nil {
		d.ids, d.mtime, d.retryAt = nil, time.Time{}, time.Time{}
		return
	}
	isStale := d.ids == nil || !info.ModTime().Equal(d.mtime)
	isRetryDue := !d.retryAt.IsZero() && !time.Now().Before(d.retryAt)
	if !isStale && !isRetryDue {
		return
	}
	ids, isComplete := readDispatchIDs(d.dir)
	d.ids, d.mtime, d.retryAt = ids, info.ModTime(), time.Time{}
	if !isComplete {
		d.retryAt = time.Now().Add(incompleteScanRetry)
	}
}

// readDispatchIDs collects the agent ids every run in dir created: agentId
// (cloud), localSessionId (CLI) and composerId (Agents window). Runs that only
// injected into Joe's own chat (originChat) are skipped, since that chat is his.
// The dir is read as a literal path, so brackets or wildcards in it are safe.
// isComplete is false when the dir or a run file could not be read, so the
// caller knows the ids may be missing some runs. A file that vanished between
// the listing and the read (fellowship cleaning up) is not a failure.
func readDispatchIDs(dir string) (ids map[string]bool, isComplete bool) {
	ids = map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ids, false
	}
	isComplete = true
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				isComplete = false
			}
			continue
		}
		var run struct {
			AgentID        string `json:"agentId"`
			LocalSessionID string `json:"localSessionId"`
			ComposerID     string `json:"composerId"`
			OriginChat     string `json:"originChat"`
		}
		if json.Unmarshal(raw, &run) != nil || run.OriginChat != "" {
			continue
		}
		for _, id := range []string{run.AgentID, run.LocalSessionID, run.ComposerID} {
			if id != "" {
				ids[id] = true
			}
		}
	}
	return ids, isComplete
}
