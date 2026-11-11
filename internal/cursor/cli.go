package cursor

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// gitMetaTTL is how long a working directory's git state is reused before git
// is asked again. ponytail: a checkout switch shows up within this window, not
// instantly; watch .git/HEAD if that ever matters.
const gitMetaTTL = 30 * time.Second

// cachedWorktree is a working directory's git state and when it was read.
type cachedWorktree struct {
	info worktreeInfo
	at   time.Time
}

// lookupWorktree reads worktree and branch state for a working directory.
func lookupWorktree(cwd string) worktreeInfo {
	isWT, mainRepo := claude.IsWorktree(cwd)
	return worktreeInfo{isWorktree: isWT, mainRepo: mainRepo, branch: gitBranch(cwd)}
}

// worktree returns cwd's git state, asking git at most once per gitMetaTTL.
func (c *SessionCache) worktree(cwd string) worktreeInfo {
	c.mu.Lock()
	cached, ok := c.worktrees[cwd]
	c.mu.Unlock()
	if ok && time.Since(cached.at) < gitMetaTTL {
		return cached.info
	}
	info := lookupWorktree(cwd)
	c.mu.Lock()
	c.worktrees[cwd] = cachedWorktree{info: info, at: time.Now()}
	c.mu.Unlock()
	return info
}

// touched returns touchedBranches, re-reading conversation-search.db only when
// its WAL (or the db itself) has changed since the last read.
func (c *SessionCache) touched() map[string][]string {
	var mtime time.Time
	var size int64
	if path := searchDBPath(); path != "" {
		mtime, size = walStats(path)
	}
	c.mu.Lock()
	isFresh := c.touchedLoaded && mtime.Equal(c.touchedMtime) && size == c.touchedSize
	cached := c.touchedByID
	c.mu.Unlock()
	if isFresh {
		return cached
	}
	result := touchedBranches()
	c.mu.Lock()
	c.touchedByID, c.touchedMtime, c.touchedSize, c.touchedLoaded = result, mtime, size, true
	c.mu.Unlock()
	return result
}

// applyGitMeta sets each session's branch, worktree and touched branches from
// git and conversation-search.db, independently of the chat caches, so a
// checkout switch or a new touched branch shows up without a new message.
// Changed sessions are replaced by updated copies (also stored back in the
// cache), never mutated, since the manager may hold the old pointers.
func applyGitMeta(cache *SessionCache, sessions []*model.Session) []*model.Session {
	touched := cache.touched()
	for i, s := range sessions {
		var wt worktreeInfo
		if s.CWD != "" {
			wt = cache.worktree(s.CWD)
		}
		others := otherBranches(touched[s.SessionID], wt.branch)
		isUnchanged := s.GitBranch == wt.branch && s.IsWorktree == wt.isWorktree &&
			s.MainRepo == wt.mainRepo && slices.Equal(s.TouchedBranches, others)
		if isUnchanged {
			continue
		}
		clone := *s
		clone.GitBranch, clone.IsWorktree, clone.MainRepo, clone.TouchedBranches = wt.branch, wt.isWorktree, wt.mainRepo, others
		sessions[i] = &clone
		cache.replace(&clone)
	}
	return sessions
}

// replace swaps the cached copy of s (IDE or CLI) for s, if one is cached.
func (c *SessionCache) replace(s *model.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[s.SessionID]; ok {
		entry.session = s
	}
	if entry, ok := c.cli[s.SessionID]; ok {
		entry.session = s
	}
}

// DiscoverCLISessions lists Cursor CLI sessions updated inside RecentWindow.
// meta.json is the only source: Cursor records no running state for the CLI, so
// a session counts as active only while its updatedAtMs is fresh (the activity
// timeout in core turns a stale one idle).
func DiscoverCLISessions(cache *SessionCache) []*model.Session {
	dir := cliChatsDir()
	if dir == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*", "*", "meta.json"))
	cutoffMs := time.Now().Add(-RecentWindow).UnixMilli()

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

		session := &model.Session{
			SessionID:    sid,
			Agent:        "cursor",
			CLI:          true,
			CWD:          meta.CWD,
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
	return applyGitMeta(cache, sessions)
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
