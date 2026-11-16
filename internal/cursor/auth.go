package cursor

import (
	"database/sql"
	"encoding/json"
	"os"
)

// ReadAuth returns Cursor's locally-stored OAuth access token, read from
// state.vscdb's ItemTable.
//
// ok is false (with a nil error) when Cursor simply isn't installed or the user
// isn't logged in — no database, or no cursorAuth/accessToken entry. Real failures
// (DB open/query errors) are returned as err so callers can surface them.
func ReadAuth() (accessToken string, ok bool, err error) {
	path := stateDBPath()
	if path == "" {
		return "", false, nil
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return "", false, nil
	}

	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return "", false, err
	}
	defer db.Close()

	accessToken = readItemValue(db, "cursorAuth/accessToken")
	if accessToken == "" {
		return "", false, nil
	}
	return accessToken, true, nil
}

// PinnedIDs returns the chat ids pinned in Cursor's Agents window, in the
// order they were pinned (ItemTable cursor/pinnedComposers). Nil when Cursor
// isn't installed or nothing is pinned.
func PinnedIDs() []string { return pinnedIDsAt(pinsDBPath()) }

// pinnedIDsAt reads the pinned chat ids from the state.vscdb at path.
func pinnedIDsAt(path string) []string {
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
	var ids []string
	if json.Unmarshal([]byte(readItemValue(db, "cursor/pinnedComposers")), &ids) != nil {
		return nil
	}
	return ids
}

// readItemValue fetches a single ItemTable value, returning "" when the key is
// absent or unreadable (the caller treats both as "not set").
func readItemValue(db *sql.DB, key string) string {
	var v string
	if err := db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", key).Scan(&v); err != nil {
		return ""
	}
	return v
}
