//go:build !windows

package claude

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestReadGitInfoTimesOutOnBlockedOpen uses a FIFO as .git: opening it blocks
// until a writer shows up, like a dead mount would.
func TestReadGitInfoTimesOutOnBlockedOpen(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, ".git")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	defer func(old time.Duration) { GitReadTimeout = old }(GitReadTimeout)
	GitReadTimeout = 100 * time.Millisecond

	start := time.Now()
	if got := ReadGitInfo(dir); got != (GitInfo{}) {
		t.Errorf("blocked read = %+v, want zero", got)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("blocked read took %s, want about %s", took, GitReadTimeout)
	}
	start = time.Now()
	ReadGitInfo(dir)
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Errorf("second read of a blocked path took %s, want an immediate return", took)
	}

	unblock, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err == nil {
		unblock.Close()
	}
}

// TestReadGitInfoSharesRunningRead checks a caller that arrives while a read
// of the same path is running gets that read's result, not a zero GitInfo.
func TestReadGitInfoSharesRunningRead(t *testing.T) {
	dir := t.TempDir()
	read := &gitRead{started: time.Now(), done: make(chan struct{})}
	gitReadsInFlight.Store(dir, read)
	defer gitReadsInFlight.Delete(dir)

	go func() {
		time.Sleep(20 * time.Millisecond)
		read.info = GitInfo{Branch: "main"}
		close(read.done)
	}()
	if got := ReadGitInfo(dir); got.Branch != "main" {
		t.Errorf("concurrent read = %+v, want the running read's result", got)
	}
}

// TestReadGitInfoWorktreeConfigV1 checks a repo go-git refuses
// (extensions.worktreeConfig on format v1) still reports branch and worktree.
func TestReadGitInfoWorktreeConfigV1(t *testing.T) {
	root := t.TempDir()
	mainGit := filepath.Join(root, "repo", ".git")
	wtGit := filepath.Join(mainGit, "worktrees", "wt")
	wt := filepath.Join(root, "wt", "sub")
	for _, d := range []string{filepath.Join(mainGit, "refs", "heads"), wtGit, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(mainGit, "config"):  "[core]\n\trepositoryformatversion = 1\n[extensions]\n\tworktreeConfig = true\n",
		filepath.Join(mainGit, "HEAD"):    "ref: refs/heads/main\n",
		filepath.Join(wtGit, "HEAD"):      "ref: refs/heads/feature/x\n",
		filepath.Join(wtGit, "commondir"): "../..\n",
		filepath.Join(root, "wt", ".git"): "gitdir: " + wtGit + "\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := ReadGitInfo(wt)
	want := GitInfo{IsWorktree: true, MainRepo: filepath.Join(root, "repo"), Branch: "feature/x"}
	if got != want {
		t.Errorf("ReadGitInfo(worktree) = %+v, want %+v", got, want)
	}
	if got := ReadGitInfo(filepath.Join(root, "repo")); got != (GitInfo{Branch: "main"}) {
		t.Errorf("ReadGitInfo(main) = %+v, want branch main", got)
	}
}
