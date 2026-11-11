package cursor

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/illegalstudio/lazyagent/internal/claude"
)

// runGit runs git in dir for a test fixture, isolated from the user's git
// config (e.g. commit signing) and from inherited GIT_DIR/GIT_WORK_TREE, so
// fixture writes can never land in the caller's repository.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Env = claude.GitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestGitBranchFollowsWorktrees(t *testing.T) {
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere.git"))
	main := t.TempDir()
	runGit(t, main, "init", "-b", "main")
	runGit(t, main, "commit", "--allow-empty", "-m", "init")
	worktree := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-b", "feature/x", worktree)

	if got := gitBranch(main); got != "main" {
		t.Errorf("main checkout = %q, want main", got)
	}
	if got := gitBranch(worktree); got != "feature/x" {
		t.Errorf("worktree = %q, want feature/x", got)
	}
	runGit(t, worktree, "checkout", "--detach")
	if got := gitBranch(worktree); got != "" {
		t.Errorf("detached HEAD = %q, want empty", got)
	}
	if got := gitBranch(t.TempDir()); got != "" {
		t.Errorf("not a repo = %q, want empty", got)
	}
}
