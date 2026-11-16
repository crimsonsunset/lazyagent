package cursor

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/illegalstudio/lazyagent/internal/model"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		t.Fatal(err)
	}
	return db
}

func put(t *testing.T, db *sql.DB, key, value string) {
	t.Helper()
	if _, err := db.Exec("INSERT INTO cursorDiskKV (key, value) VALUES (?, ?)", key, value); err != nil {
		t.Fatal(err)
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }

// seedChunked writes a composer in the newer layout: no inline headers,
// headers split across composerConvHeaders chunks, workspace on the composer.
func seedChunked(t *testing.T, db *sql.DB, sid string, updatedMs int64) {
	t.Helper()
	put(t, db, "composerData:"+sid, fmt.Sprintf(
		`{"composerId":%q,"fullConversationHeadersOnly":[],"lastUpdatedAt":%d,`+
			`"headerLog":{"chunkSize":2,"chunkCount":2,"count":3},`+
			`"workspaceIdentifier":{"uri":{"fsPath":"/Users/me/proj"}}}`, sid, updatedMs))
	put(t, db, chunkedHeaderKey(sid, 0), `[{"bubbleId":"b1","type":1},{"bubbleId":"b2/t:tool","type":2}]`)
	put(t, db, chunkedHeaderKey(sid, 1), `[{"bubbleId":"b3","type":2}]`)
	put(t, db, "bubbleId:"+sid+":b1", `{"type":1,"text":"hi","createdAt":"2026-11-03T10:00:00.000Z"}`)
	put(t, db, "bubbleId:"+sid+":b2/t:tool", `{"type":2,"toolFormerData":{"name":"read_file_v2"},"createdAt":"2026-11-03T10:00:01.000Z"}`)
	put(t, db, "bubbleId:"+sid+":b3", `{"type":2,"text":"done","createdAt":"2026-11-03T10:00:02.000Z"}`)
}

func TestQueryComposersChunkedHeaders(t *testing.T) {
	db := newTestDB(t)
	seedChunked(t, db, "new", nowMs())

	got, err := queryComposers(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].sid != "new" || got[0].count != 3 || got[0].lastBubble != "b3" {
		t.Fatalf("got %+v, want one composer {new 3 b3}", got)
	}
}

func TestQueryComposersDoesNotResolveStaleChunked(t *testing.T) {
	db := newTestDB(t)
	seedChunked(t, db, "old", time.Now().Add(-RecentWindow-time.Hour).UnixMilli())

	got, err := queryComposers(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].count != 0 || got[0].lastBubble != "" {
		t.Fatalf("stale chunked composer should pass through unresolved (count 0), got %+v", got)
	}
}

func TestNormalizeToolNameCurrentCursor(t *testing.T) {
	cases := map[string]string{
		"run_terminal_command_v2": "Bash",
		"read_file_v2":            "Read",
		"edit_file_v2":            "Edit",
		"glob_file_search":        "Glob",
		"ripgrep_raw_search":      "Grep",
		"web_fetch":               "WebFetch",
		"task_v2":                 "Agent",
	}
	for in, want := range cases {
		if got := normalizeToolName(in); got != want {
			t.Errorf("normalizeToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestQueryComposersInlineHeadersStillWork(t *testing.T) {
	db := newTestDB(t)
	put(t, db, "composerData:legacy",
		`{"composerId":"legacy","fullConversationHeadersOnly":[{"bubbleId":"a","type":1},{"bubbleId":"b","type":2}]}`)

	got, err := queryComposers(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].count != 2 || got[0].lastBubble != "b" {
		t.Fatalf("got %+v, want one composer with count 2, last bubble b", got)
	}
}

// TestDiscoverIDESessionsPicksUpRename checks a rename with no new message reaches a cached session.
func TestDiscoverIDESessionsPicksUpRename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	dbPath := stateDBPath()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		t.Fatal(err)
	}
	seed := func(name string) {
		put(t, db, "composerData:s1", fmt.Sprintf(
			`{"composerId":"s1","name":%q,"fullConversationHeadersOnly":[{"bubbleId":"b1","type":1}]}`, name))
	}
	seed("Old title")
	put(t, db, "bubbleId:s1:b1", fmt.Sprintf(`{"type":1,"text":"hi","createdAt":%q}`,
		time.Now().UTC().Format("2006-01-02T15:04:05.000Z")))

	cache := NewSessionCache()
	first, _ := discoverIDESessions(cache)
	if len(first) != 1 || first[0].Name != "Old title" {
		t.Fatalf("first discover = %+v, want one session named Old title", first)
	}

	seed("New title")
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(dbPath, future, future); err != nil {
		t.Fatal(err)
	}
	second, _ := discoverIDESessions(cache)
	if len(second) != 1 || second[0].Name != "New title" {
		t.Fatalf("second discover = %+v, want the renamed session", second)
	}
	if first[0].Name != "Old title" {
		t.Errorf("first session mutated to %q, want a clone", first[0].Name)
	}
}

func TestBuildStateSessionChunked(t *testing.T) {
	db := newTestDB(t)
	seedChunked(t, db, "new", nowMs())

	session := &model.Session{SessionID: "new"}
	if err := buildStateSession(db, "new", session); err != nil {
		t.Fatal(err)
	}

	if session.CWD != "/Users/me/proj" {
		t.Errorf("CWD = %q, want /Users/me/proj", session.CWD)
	}
	if session.UserMessages != 1 || session.AssistantMessages != 1 {
		t.Errorf("messages user=%d assistant=%d, want 1/1", session.UserMessages, session.AssistantMessages)
	}
	if len(session.RecentTools) != 1 || session.RecentTools[0].Name != "Read" {
		t.Errorf("RecentTools = %+v, want one Read", session.RecentTools)
	}
}

func TestBuildStateSessionFallsBackToBubbleWorkspace(t *testing.T) {
	db := newTestDB(t)
	put(t, db, "composerData:legacy",
		`{"composerId":"legacy","fullConversationHeadersOnly":[{"bubbleId":"a","type":1}]}`)
	put(t, db, "bubbleId:legacy:a",
		`{"type":1,"text":"hi","workspaceUris":["file:///Users/me/legacy-proj"],"createdAt":"2026-11-03T10:00:00.000Z"}`)

	session := &model.Session{SessionID: "legacy"}
	if err := buildStateSession(db, "legacy", session); err != nil {
		t.Fatal(err)
	}

	if session.CWD != "/Users/me/legacy-proj" {
		t.Errorf("CWD = %q, want bubble workspace /Users/me/legacy-proj", session.CWD)
	}
}

func TestBuildStateSessionBubbleWorkspaceBeatsComposer(t *testing.T) {
	db := newTestDB(t)
	put(t, db, "composerData:both",
		`{"composerId":"both","fullConversationHeadersOnly":[{"bubbleId":"a","type":1}],`+
			`"workspaceIdentifier":{"uri":{"fsPath":"/Users/me/composer-proj"}}}`)
	put(t, db, "bubbleId:both:a",
		`{"type":1,"text":"hi","workspaceUris":["file:///Users/me/bubble-proj"],"createdAt":"2026-11-03T10:00:00.000Z"}`)

	session := &model.Session{SessionID: "both"}
	if err := buildStateSession(db, "both", session); err != nil {
		t.Fatal(err)
	}

	if session.CWD != "/Users/me/bubble-proj" {
		t.Errorf("CWD = %q, want bubble workspace /Users/me/bubble-proj", session.CWD)
	}
}

func TestBuildStateSessionErrorsOnMissingChunk(t *testing.T) {
	db := newTestDB(t)
	seedChunked(t, db, "partial", nowMs())
	if _, err := db.Exec("DELETE FROM cursorDiskKV WHERE key = ?", chunkedHeaderKey("partial", 1)); err != nil {
		t.Fatal(err)
	}

	session := &model.Session{SessionID: "partial"}
	if err := buildStateSession(db, "partial", session); err == nil {
		t.Error("want error for unreadable header chunk so the partial session is not cached")
	}
}

func TestQueryComposersNullableInlineHeaders(t *testing.T) {
	for _, field := range []string{"", `"fullConversationHeadersOnly":null,`} {
		t.Run(field, func(t *testing.T) {
			db := newTestDB(t)
			seedChunked(t, db, "nullable", nowMs())
			put(t, db, "composerData:nullable", fmt.Sprintf(
				`{"composerId":"nullable",%s"lastUpdatedAt":%d,"headerLog":{"chunkCount":2,"count":3}}`, field, nowMs()))
			got, err := queryComposers(db)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].sid != "nullable" || got[0].count != 3 || got[0].lastBubble != "b3" {
				t.Fatalf("got %+v, want one composer {nullable 3 b3}", got)
			}
		})
	}
}

func TestBuildStateSessionPreservesPartialChunks(t *testing.T) {
	for _, failure := range []string{"missing", "malformed", "null", "huge count"} {
		t.Run(failure, func(t *testing.T) {
			db := newTestDB(t)
			seedChunked(t, db, "partial", nowMs())
			switch failure {
			case "missing":
				if _, err := db.Exec("DELETE FROM cursorDiskKV WHERE key = ?", chunkedHeaderKey("partial", 1)); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				put(t, db, chunkedHeaderKey("partial", 1), `{`)
			case "null":
				if _, err := db.Exec("UPDATE cursorDiskKV SET value = NULL WHERE key = ?", chunkedHeaderKey("partial", 1)); err != nil {
					t.Fatal(err)
				}
			case "huge count":
				put(t, db, "composerData:partial", `{"headerLog":{"chunkCount":2147483647},"workspaceIdentifier":{"uri":{"fsPath":"/Users/me/proj"}}}`)
			}
			session := &model.Session{SessionID: "partial"}
			if err := buildStateSession(db, "partial", session); err == nil {
				t.Fatal("want error so partial data is retried")
			}
			if session.UserMessages != 1 || len(session.RecentMessages) == 0 || session.RecentMessages[0].Text != "hi" {
				t.Fatalf("lost readable messages: %+v", session)
			}
			if len(session.RecentTools) != 1 || session.RecentTools[0].Name != "Read" || session.CWD != "/Users/me/proj" {
				t.Fatalf("lost readable tool or workspace: %+v", session)
			}
			wantStatus := model.StatusExecutingTool
			if failure == "huge count" {
				wantStatus = determineBubbleStatus(2, false)
				if session.AssistantMessages != 1 {
					t.Fatal("lost second readable chunk")
				}
			}
			if session.Status != wantStatus {
				t.Errorf("status = %v, want %v", session.Status, wantStatus)
			}
		})
	}
}

// TestQueryRunningReadsUnfinishedRunAt checks only chats with unfinishedRunAt set count as running.
func TestQueryRunningReadsUnfinishedRunAt(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Exec("CREATE TABLE composerHeaders (composerId TEXT PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO composerHeaders VALUES
		('busy', '{"name":"busy","unfinishedRunAt":1794811464561}'),
		('done', '{"name":"done"}')`); err != nil {
		t.Fatal(err)
	}

	got, err := queryRunning(db)
	if err != nil {
		t.Fatal(err)
	}
	if !got["busy"] || got["done"] || len(got) != 1 {
		t.Fatalf("queryRunning = %v, want only busy", got)
	}
}

// TestQueryRunningWithoutHeadersTable checks an older Cursor DB reports nothing running.
func TestQueryRunningWithoutHeadersTable(t *testing.T) {
	got, err := queryRunning(newTestDB(t))
	if err != nil || len(got) != 0 {
		t.Fatalf("queryRunning = %v, %v, want empty and no error", got, err)
	}
}

// TestQueryRunningReportsReadFailure checks a failed read errors instead of clearing every run flag.
func TestQueryRunningReportsReadFailure(t *testing.T) {
	db := newTestDB(t)
	db.Close()
	if _, err := queryRunning(db); err == nil {
		t.Fatal("queryRunning on a closed DB returned no error")
	}
}

// TestPinnedIDsAt checks the pinned list is read in order, and a missing DB or key reads as none.
func TestPinnedIDsAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if got := pinnedIDsAt(path); got != nil {
		t.Fatalf("no key: got %v, want nil", got)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable VALUES ('cursor/pinnedComposers', '["b","a"]')`); err != nil {
		t.Fatal(err)
	}
	if got := pinnedIDsAt(path); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("got %v, want [b a]", got)
	}
	if got := pinnedIDsAt(filepath.Join(t.TempDir(), "none.vscdb")); got != nil {
		t.Fatalf("missing db: got %v, want nil", got)
	}
}
