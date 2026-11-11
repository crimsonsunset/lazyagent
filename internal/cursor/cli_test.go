package cursor

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// writeCLIMeta writes a Cursor CLI meta.json for sid under home.
func writeCLIMeta(t *testing.T, home, sid, cwd string, updated time.Time, hasConversation bool) {
	t.Helper()
	dir := filepath.Join(home, ".cursor", "chats", "hash", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"hasConversation":%t,"updatedAtMs":%d,"cwd":%q}`, hasConversation, updated.UnixMilli(), cwd)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverCLISessions checks only fresh CLI sessions with a conversation are listed, with their branch.
func TestDiscoverCLISessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "feature/cli")
	runGit(t, repo, "commit", "--allow-empty", "-m", "init")

	writeCLIMeta(t, home, "fresh", repo, time.Now(), true)
	writeCLIMeta(t, home, "stale", repo, time.Now().Add(-3*RecentWindow), true)
	writeCLIMeta(t, home, "empty", repo, time.Now(), false)

	sessions := DiscoverCLISessions(NewSessionCache())
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want only the fresh one", len(sessions))
	}
	got := sessions[0]
	if got.SessionID != "fresh" || !got.CLI || got.Agent != "cursor" || got.GitBranch != "feature/cli" {
		t.Errorf("unexpected session: %+v", got)
	}
}

// TestCLIBranchRefreshesWithoutNewMessage checks a checkout switch reaches a cached CLI session once the git TTL lapses.
func TestCLIBranchRefreshesWithoutNewMessage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "commit", "--allow-empty", "-m", "init")
	writeCLIMeta(t, home, "s1", repo, time.Now(), true)

	cache := NewSessionCache()
	if got := DiscoverCLISessions(cache)[0].GitBranch; got != "main" {
		t.Fatalf("first branch = %q, want main", got)
	}

	runGit(t, repo, "checkout", "-b", "feature/y")
	cache.mu.Lock()
	for cwd, wt := range cache.gitByCWD {
		wt.at = time.Time{}
		cache.gitByCWD[cwd] = wt
	}
	cache.mu.Unlock()

	if got := DiscoverCLISessions(cache)[0].GitBranch; got != "feature/y" {
		t.Errorf("branch after checkout = %q, want feature/y (meta.json unchanged)", got)
	}
}

// TestOtherBranches checks the current branch is dropped from touched branches.
func TestOtherBranches(t *testing.T) {
	got := otherBranches([]string{"main", "feat/a", "feat/b"}, "feat/a")
	if want := []string{"main", "feat/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
