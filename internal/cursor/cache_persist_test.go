package cursor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/illegalstudio/lazyagent/internal/model"
)

// TestSessionCacheRoundTrip checks a saved cache reloads its parsed chats and
// that a wrong format version leaves the cache cold.
func TestSessionCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovery-cursor.json")
	src := NewSessionCache()
	src.entries["sid-1"] = &cachedSession{bubbleCount: 7, session: &model.Session{SessionID: "sid-1", CWD: "/repo", UserMessages: 3}}
	src.entries["sid-nil"] = &cachedSession{bubbleCount: 1}
	if err := src.SaveTo(path); err != nil {
		t.Fatal(err)
	}

	dst := NewSessionCache()
	if err := dst.LoadFrom(path); err != nil {
		t.Fatal(err)
	}
	got, ok := dst.entries["sid-1"]
	if !ok || got.bubbleCount != 7 || got.session.CWD != "/repo" || got.session.UserMessages != 3 {
		t.Fatalf("sid-1 not restored: %+v", got)
	}
	if _, ok := dst.entries["sid-nil"]; ok {
		t.Fatal("nil session entry should be skipped")
	}

	if err := os.WriteFile(path, []byte(`{"formatVersion":99,"entries":{"x":{"bubbleCount":1,"session":{}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cold := NewSessionCache()
	if err := cold.LoadFrom(path); err == nil || len(cold.entries) != 0 {
		t.Fatalf("wrong version should fail and load nothing, err=%v entries=%d", err, len(cold.entries))
	}
}
