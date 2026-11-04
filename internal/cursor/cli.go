package cursor

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/illegalstudio/lazyagent/internal/claude"
	"github.com/illegalstudio/lazyagent/internal/model"
)

// cliMeta is the part of ~/.cursor/chats/<hash>/<session>/meta.json we read.
// The sibling store.db holds protobuf blobs with an encryption key, so it is
// deliberately left alone.
type cliMeta struct {
	HasConversation bool   `json:"hasConversation"`
	UpdatedAtMs     int64  `json:"updatedAtMs"`
	CWD             string `json:"cwd"`
}

// cachedCLI is a parsed CLI session and the meta.json timestamp it came from.
type cachedCLI struct {
	updatedMs int64
	session   *model.Session
}

// worktreeInfo is what lazyagent shows about the checkout a chat ran in.
type worktreeInfo struct {
	isWorktree bool
	mainRepo   string
	branch     string
}

// lookupWorktree reads worktree and branch state for a working directory.
func lookupWorktree(cwd string) worktreeInfo {
	isWT, mainRepo := claude.IsWorktree(cwd)
	return worktreeInfo{isWorktree: isWT, mainRepo: mainRepo, branch: gitBranch(cwd)}
}

// DiscoverCLISessions lists Cursor CLI sessions updated inside recentWindow.
// meta.json is the only source: Cursor records no running state for the CLI, so
// a session counts as active only while its updatedAtMs is fresh (the activity
// timeout in core turns a stale one idle).
func DiscoverCLISessions(cache *SessionCache) []*model.Session {
	dir := cliChatsDir()
	if dir == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*", "*", "meta.json"))
	cutoffMs := time.Now().Add(-recentWindow).UnixMilli()

	var sessions []*model.Session
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var meta cliMeta
		if json.Unmarshal(raw, &meta) != nil || !meta.HasConversation || meta.UpdatedAtMs < cutoffMs {
			continue
		}
		sid := filepath.Base(filepath.Dir(path))
		seen[sid] = struct{}{}

		cache.mu.Lock()
		cached, ok := cache.cli[sid]
		cache.mu.Unlock()
		if ok && cached.updatedMs == meta.UpdatedAtMs {
			sessions = append(sessions, cached.session)
			continue
		}

		wt := lookupWorktree(meta.CWD)
		session := &model.Session{
			SessionID:    sid,
			Agent:        "cursor",
			CLI:          true,
			CWD:          meta.CWD,
			GitBranch:    wt.branch,
			IsWorktree:   wt.isWorktree,
			MainRepo:     wt.mainRepo,
			Status:       model.StatusThinking,
			LastActivity: time.UnixMilli(meta.UpdatedAtMs),
		}
		cache.mu.Lock()
		cache.cli[sid] = &cachedCLI{updatedMs: meta.UpdatedAtMs, session: session}
		cache.mu.Unlock()
		sessions = append(sessions, session)
	}

	cache.mu.Lock()
	for sid := range cache.cli {
		if _, ok := seen[sid]; !ok {
			delete(cache.cli, sid)
		}
	}
	cache.mu.Unlock()
	return sessions
}

// touchedBranches maps a local chat id to the branches it worked on, read from
// Cursor's conversation-search.db. It returns nil when that database is missing
// or unreadable, so the extra column is lost but nothing else is.
func touchedBranches() map[string][]string {
	path := searchDBPath()
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query("SELECT id, branches FROM conversations WHERE source = 'local' AND branches != ''")
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string][]string)
	for rows.Next() {
		var id, branches string
		if rows.Scan(&id, &branches) != nil {
			continue
		}
		result[id] = strings.Fields(branches)
	}
	return result
}

// otherBranches drops the checked-out branch from a chat's touched branches.
func otherBranches(touched []string, current string) []string {
	var others []string
	for _, branch := range touched {
		if branch != current {
			others = append(others, branch)
		}
	}
	return others
}
