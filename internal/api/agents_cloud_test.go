package api

import (
	"net/http"
	"net/http/httptest"
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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if isRefresh.Load() {
			<-release
		}
		w.Write([]byte(`{"items":[{"id":"bc-1","name":"run","status":"ACTIVE","updatedAt":"2026-11-03T20:00:00Z","repos":[]}]}`))
	}))
	defer upstream.Close()

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
	c.list()
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stale list blocked %v on the upstream refresh", elapsed)
	}
	if len(stale) != 2 {
		t.Fatalf("stale list = %d items, want the cached 2", len(stale))
	}

	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		done := !c.refreshing
		c.mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("upstream calls = %d, want 4 (2 accounts, initial fetch plus exactly one refresh)", got)
	}
}
