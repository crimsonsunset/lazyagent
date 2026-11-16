package cursor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGit runs git in dir for a test fixture, isolated from the user's git
// config (e.g. commit signing) and from inherited GIT_DIR/GIT_WORK_TREE, so
// fixture writes can never land in the caller's repository.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Env = []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestLookupWorktreeReadsGitFiles checks branch and worktree state per
// checkout, on detached HEAD, outside a repo, in a subdirectory, with a stray
// GIT_DIR, and for a deleted worktree (which must not report its parent repo).
func TestLookupWorktreeReadsGitFiles(t *testing.T) {
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere.git"))
	main, _ := filepath.EvalSymlinks(t.TempDir())
	runGit(t, main, "init", "-b", "main")
	runGit(t, main, "commit", "--allow-empty", "-m", "init")
	worktree := filepath.Join(main, ".worktrees", "wt")
	runGit(t, main, "worktree", "add", "-b", "feature/x", worktree)
	sub := filepath.Join(worktree, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		want worktreeInfo
	}{
		{"main checkout", main, worktreeInfo{branch: "main"}},
		{"worktree", worktree, worktreeInfo{isWorktree: true, mainRepo: main, branch: "feature/x"}},
		{"worktree subdir", sub, worktreeInfo{isWorktree: true, mainRepo: main, branch: "feature/x"}},
		{"not a repo", t.TempDir(), worktreeInfo{}},
		{"missing dir", filepath.Join(main, ".worktrees", "gone"), worktreeInfo{}},
	}
	for _, c := range cases {
		if got := lookupWorktree(c.path); got != c.want {
			t.Errorf("%s = %+v, want %+v", c.name, got, c.want)
		}
	}

	runGit(t, worktree, "checkout", "--detach")
	if got := lookupWorktree(worktree); got.branch != "" || !got.isWorktree {
		t.Errorf("detached HEAD = %+v, want worktree with no branch", got)
	}
}
