package claude

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// GitInfo is what lazyagent shows about the checkout a session ran in.
type GitInfo struct {
	IsWorktree bool
	MainRepo   string
	Branch     string
}

// ReadGitInfo reads worktree and branch state for path from the .git files
// with go-git, so discovery never spawns a git process (#114). A path that no
// longer exists, is not inside a checkout or cannot be read gives a zero GitInfo.
// Branch is "" on a detached HEAD.
// ponytail: go-git refuses repos with extensions it does not know (e.g.
// worktreeConfig on format v1); those show no branch. Fall back to reading
// HEAD by hand if that ever shows up.
func ReadGitInfo(path string) GitInfo {
	if _, err := os.Stat(path); err != nil {
		return GitInfo{}
	}
	repo, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
	if err != nil {
		return GitInfo{}
	}
	var info GitInfo
	if head, err := repo.Reference(plumbing.HEAD, false); err == nil && head.Type() == plumbing.SymbolicReference {
		info.Branch = head.Target().Short()
	}
	if storage, ok := repo.Storer.(*filesystem.Storage); ok {
		info.IsWorktree, info.MainRepo = worktreeMainRepo(storage.Filesystem().Root())
	}
	return info
}

// worktreeMainRepo reports whether gitDir is a linked worktree's git dir
// (<main>/.git/worktrees/<name>) and, if so, returns <main>.
func worktreeMainRepo(gitDir string) (bool, string) {
	sep := string(filepath.Separator)
	mainRepo, _, isWorktree := strings.Cut(gitDir, sep+".git"+sep+"worktrees"+sep)
	if !isWorktree {
		return false, ""
	}
	return true, mainRepo
}

// IsWorktree detects if a path is a git worktree and returns the main repo.
func IsWorktree(path string) (bool, string) {
	info := ReadGitInfo(path)
	return info.IsWorktree, info.MainRepo
}
