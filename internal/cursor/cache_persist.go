package cursor

import (
	"fmt"

	"github.com/illegalstudio/lazyagent/internal/diskcache"
	"github.com/illegalstudio/lazyagent/internal/model"
)

// cacheFormatVersion is bumped when the persisted shape changes. LoadFrom
// rejects any other version so a mismatched binary starts cold.
const cacheFormatVersion = 1

// persistedCache is the on-disk shape of the parsed IDE chat entries.
type persistedCache struct {
	FormatVersion int                         `json:"formatVersion"`
	Entries       map[string]persistedSession `json:"entries"`
}

// persistedSession is one parsed chat plus the bubble count it was parsed at.
type persistedSession struct {
	BubbleCount int            `json:"bubbleCount"`
	Session     *model.Session `json:"session"`
}

// SaveTo atomically writes the parsed IDE chats to path (0600, it holds
// transcript snippets) so the next process skips re-decoding their bubbles.
//
// @param path - destination file
// @returns error from the write, if any
func (c *SessionCache) SaveTo(path string) error {
	c.mu.Lock()
	snapshot := persistedCache{FormatVersion: cacheFormatVersion, Entries: make(map[string]persistedSession, len(c.entries))}
	for sid, e := range c.entries {
		snapshot.Entries[sid] = persistedSession{BubbleCount: e.bubbleCount, Session: e.session}
	}
	c.mu.Unlock()
	return diskcache.AtomicWriteJSON(path, snapshot)
}

// LoadFrom merges a snapshot written by SaveTo into the cache. Entries are
// reused only while their chat's bubble count still matches (the same check
// discoverIDESessions applies in-process), so a chat that changed while the
// daemon was down is re-parsed. Any file-level error leaves the cache
// untouched and the caller continues cold.
//
// @param path - snapshot file
// @returns error when the file is missing, malformed or a different version
func (c *SessionCache) LoadFrom(path string) error {
	var snapshot persistedCache
	if err := diskcache.ReadJSON(path, &snapshot); err != nil {
		return err
	}
	if snapshot.FormatVersion != cacheFormatVersion {
		return fmt.Errorf("cursor cache %s: format version %d, want %d", path, snapshot.FormatVersion, cacheFormatVersion)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for sid, e := range snapshot.Entries {
		if e.Session == nil {
			continue
		}
		c.entries[sid] = &cachedSession{bubbleCount: e.BubbleCount, session: e.Session}
	}
	return nil
}
