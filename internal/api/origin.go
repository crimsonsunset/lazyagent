package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// dispatchRunsEnv overrides where fellowship's dispatch run files live.
const dispatchRunsEnv = "LAZYAGENT_DISPATCH_RUNS_DIR"

// Who started an agent: a fellowship dispatch run (Slack, Windmill, CodeRabbit
// fixes) or Joe directly in Cursor.
const (
	originGandalf = "gandalf"
	originYou     = "you"
)

// dispatchOrigins knows which agent ids a dispatch run created, read from the
// run JSON files fellowship writes on this machine. The index is rebuilt only
// when the directory's mtime changes; fellowship writes a run to a temp file
// and renames it, which bumps the mtime, so new runs and late ids show up.
type dispatchOrigins struct {
	dir string

	mu    sync.Mutex
	mtime time.Time
	ids   map[string]bool
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

// refresh rebuilds the id index when the runs dir changed. Callers hold d.mu.
// ponytail: full rescan on every dir change (~250 small files today). If run
// files pile up into the thousands, parse only files newer than the last scan.
func (d *dispatchOrigins) refresh() {
	info, err := os.Stat(d.dir)
	if err != nil {
		d.ids, d.mtime = nil, time.Time{}
		return
	}
	if d.ids != nil && info.ModTime().Equal(d.mtime) {
		return
	}
	d.ids, d.mtime = readDispatchIDs(d.dir), info.ModTime()
}

// readDispatchIDs collects the agent ids every run in dir created: agentId
// (cloud), localSessionId (CLI) and composerId (Agents window). Runs that only
// injected into Joe's own chat (originChat) are skipped, since that chat is his.
func readDispatchIDs(dir string) map[string]bool {
	ids := map[string]bool{}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
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
	return ids
}
