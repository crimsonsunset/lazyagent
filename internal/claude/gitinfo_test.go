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
