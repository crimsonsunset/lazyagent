package claude

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// GitReadTimeout bounds one ReadGitInfo call, so a blocked open() (a dead
// mount, an iCloud placeholder, a macOS privacy prompt) cannot freeze discovery.
var GitReadTimeout = 3 * time.Second

// gitRead is one in-flight read of a path. Concurrent callers for the same
// path share it instead of starting a second read.
type gitRead struct {
	started time.Time
	done    chan struct{}
	info    GitInfo
}

// gitReadsInFlight maps path to its *gitRead, so a path that never answers
// leaks one goroutine, not one per retry.
var gitReadsInFlight sync.Map

// ReadGitInfo reads worktree and branch state for path from the .git files
// with go-git, so discovery never spawns a git process (#114). A path that no
// longer exists, is not inside a checkout, cannot be read or does not answer
// within GitReadTimeout gives a zero GitInfo. Branch is "" on a detached HEAD.
// A caller that finds a read of the same path running waits for its result
// (up to GitReadTimeout), except when that read is already past the timeout:
// then it returns a zero GitInfo at once, so a dead path costs one stall.
//
// @param path - session cwd, any directory inside a checkout
// @returns GitInfo for the checkout, or zero
func ReadGitInfo(path string) GitInfo {
	fresh := &gitRead{started: time.Now(), done: make(chan struct{})}
	existing, isRunning := gitReadsInFlight.LoadOrStore(path, fresh)
	read := existing.(*gitRead)
	if !isRunning {
		go func() {
			read.info = readGitInfo(path)
			gitReadsInFlight.Delete(path)
			close(read.done)
		}()
	}
	wait := GitReadTimeout - time.Since(read.started)
	if wait <= 0 {
		return GitInfo{}
	}
	select {
	case <-read.done:
		return read.info
	case <-time.After(wait):
		if !isRunning {
			log.Printf("git read of %s blocked for %s, showing no branch until it answers", path, GitReadTimeout)
		}
		return GitInfo{}
	}
}

// readGitInfo is ReadGitInfo without the timeout or sharing.
//
// @param path - session cwd
// @returns GitInfo for the checkout, or zero
func readGitInfo(path string) GitInfo {
	if _, err := os.Stat(path); err != nil {
		return GitInfo{}
	}
	repo, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true, EnableDotGitCommonDir: true})
	if errors.Is(err, git.ErrUnknownExtension) || errors.Is(err, git.ErrUnsupportedExtensionRepositoryFormatVersion) {
		return readGitInfoByHand(path)
	}
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

// readGitInfoByHand reads HEAD straight from the .git files, for repos go-git
// refuses to open over a config extension it doesn't know (e.g. worktreeConfig
// on format v1). Branch and worktree state don't depend on any extension.
//
// @param path - session cwd
// @returns GitInfo for the checkout, or zero
func readGitInfoByHand(path string) GitInfo {
	dir := path
	stat, err := os.Stat(filepath.Join(dir, ".git"))
	for err != nil {
		parent := filepath.Dir(dir)
		if parent == dir {
			return GitInfo{}
		}
		dir = parent
		stat, err = os.Stat(filepath.Join(dir, ".git"))
	}

	gitDir := filepath.Join(dir, ".git")
	if !stat.IsDir() {
		data, err := os.ReadFile(gitDir)
		target, isGitFile := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if err != nil || !isGitFile {
			return GitInfo{}
		}
		gitDir = strings.TrimSpace(target)
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
	}

	var info GitInfo
	info.IsWorktree, info.MainRepo = worktreeMainRepo(filepath.Clean(gitDir))
	if head, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		if branch, isSymbolic := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/"); isSymbolic {
			info.Branch = branch
		}
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
