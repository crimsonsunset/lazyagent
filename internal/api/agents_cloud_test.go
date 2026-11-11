package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/illegalstudio/lazyagent/internal/core"
)

var testCloudLimits = core.DefaultConfig().Cloud

func TestCloudLimitsComeFromConfig(t *testing.T) {
	var query atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.RawQuery)
		w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()

	c := newCloudAgents(upstream.URL, "key", core.CloudConfig{CacheTTLSec: 7, FetchTimeoutSec: 2, PageLimit: 25})
	if _, err := c.list(); err != nil {
		t.Fatal(err)
	}
	if got := query.Load(); got != "limit=25" {
		t.Errorf("query = %v, want limit=25", got)
	}
	if c.ttl != 7*time.Second || c.client.Timeout != 2*time.Second {
		t.Errorf("ttl = %v, timeout = %v, want 7s and 2s", c.ttl, c.client.Timeout)
	}
}

func TestCloudListServesStaleWhileRefreshing(t *testing.T) {
	var calls atomic.Int32
	var isRefresh atomic.Bool
	release := make(chan struct{})
	refreshStarted := make(chan struct{}, 2)
	var releaseOnce sync.Once
	releaseHandlers := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		name := "run"
		if isRefresh.Load() {
			refreshStarted <- struct{}{}
			<-release
			name = "refreshed"
		}
		fmt.Fprintf(w, `{"items":[{"id":"bc-1","name":%q,"status":"ACTIVE","updatedAt":"2026-11-03T20:00:00Z","repos":[]}]}`, name)
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(releaseHandlers)

	c := newCloudAgents(upstream.URL, "key-a,key-b", testCloudLimits)
	first, err := c.list()
	if err != nil || len(first) != 2 {
		t.Fatalf("first list = %d items, err %v; want both accounts", len(first), err)
	}

	isRefresh.Store(true)
	c.mu.Lock()
	c.fetched = time.Now().Add(-2 * c.ttl)
	c.mu.Unlock()

	started := time.Now()
	stale, _ := c.list()
	select {
	case <-refreshStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("background refresh never reached upstream")
	}
	if again, _ := c.list(); len(again) != 2 || again[0].Name != "run" {
		t.Fatalf("list during refresh = %+v, want the cached 2", again)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stale list blocked %v on the upstream refresh", elapsed)
	}
	if len(stale) != 2 {
		t.Fatalf("stale list = %d items, want the cached 2", len(stale))
	}

	releaseHandlers()
	waitForRefresh(t, c)
	if got := calls.Load(); got != 4 {
		t.Fatalf("upstream calls = %d, want 4 (2 accounts, initial fetch plus exactly one refresh)", got)
	}
	fresh, err := c.list()
	if err != nil || len(fresh) != 2 || fresh[0].Name != "refreshed" {
		t.Fatalf("after refresh = %+v, err %v; want 2 refreshed items", fresh, err)
	}
}

func TestCloudRefreshFailureKeepsLastGoodAgents(t *testing.T) {
	var failB atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, _, _ := r.BasicAuth()
		if key == "key-b" && failB.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"items":[{"id":%q,"name":"run","status":"ACTIVE","updatedAt":"2026-11-03T20:00:00Z","repos":[]}]}`, key)
	}))
	t.Cleanup(upstream.Close)

	c := newCloudAgents(upstream.URL, "key-a,key-b", testCloudLimits)
	if first, err := c.list(); err != nil || len(first) != 2 {
		t.Fatalf("first list = %d items, err %v; want both accounts", len(first), err)
	}

	failB.Store(true)
	c.mu.Lock()
	c.fetched = time.Now().Add(-2 * c.ttl)
	c.mu.Unlock()
	c.list()
	waitForRefresh(t, c)

	got, err := c.list()
	if err == nil {
		t.Error("want the refresh error reported")
	}
	ids := make([]string, 0, len(got))
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	if !slices.Equal(ids, []string{"key-a", "key-b"}) {
		t.Errorf("after failed refresh ids = %v, want key-a and key-b's last good agent", ids)
	}
}

// waitForRefresh blocks until c's background refresh finishes, failing the
// test if it is still running after two seconds.
func waitForRefresh(t *testing.T, c *cloudAgents) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		done := !c.refreshing
		c.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background refresh did not finish within 2s")
}
