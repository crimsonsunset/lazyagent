package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeWebhook records the payloads it receives and answers with status().
type fakeWebhook struct {
	mu       sync.Mutex
	received []AgentsResponse
	status   int
}

// count returns how many payloads arrived.
func (f *fakeWebhook) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

// newFakeWebhook starts a server for f and points LAZYAGENT_HA_WEBHOOK_URL at it.
func newFakeWebhook(t *testing.T, f *fakeWebhook) *haPush {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body AgentsResponse
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		f.mu.Lock()
		f.received = append(f.received, body)
		status := f.status
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(ts.Close)
	t.Setenv(haWebhookEnv, ts.URL)
	p := newHAPush()
	if p == nil {
		t.Fatal("newHAPush returned nil for a valid URL")
	}
	return p
}

// checkAndWait runs one check and waits for its send to finish.
func checkAndWait(t *testing.T, p *haPush, resp AgentsResponse) {
	t.Helper()
	p.check(func() AgentsResponse { return resp })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		busy := p.inFlight
		p.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("push did not finish")
}

// TestHAPushSendsOnlyOnChange checks a rename or status change is pushed, an
// UpdatedAt-only change is not, and a failed send is retried next check.
func TestHAPushSendsOnlyOnChange(t *testing.T) {
	hook := &fakeWebhook{status: http.StatusOK}
	p := newFakeWebhook(t, hook)

	now := time.Now()
	agent := AgentItem{ID: "a", Source: "ide", Name: "old", Status: agentWorking, UpdatedAt: now}
	resp := func(a AgentItem) AgentsResponse {
		return AgentsResponse{Agents: []AgentItem{a}, Errors: []string{}, GeneratedAt: time.Now()}
	}

	checkAndWait(t, p, resp(agent))
	if hook.count() != 1 {
		t.Fatalf("first check sent %d, want 1", hook.count())
	}

	agent.UpdatedAt = now.Add(time.Minute)
	checkAndWait(t, p, resp(agent))
	if hook.count() != 1 {
		t.Fatalf("UpdatedAt-only change sent, count = %d", hook.count())
	}

	agent.Name = "renamed"
	checkAndWait(t, p, resp(agent))
	if hook.count() != 2 || hook.received[1].Agents[0].Name != "renamed" {
		t.Fatalf("rename not pushed: %+v", hook.received)
	}

	hook.mu.Lock()
	hook.status = http.StatusInternalServerError
	hook.mu.Unlock()
	agent.Status = agentIdle
	checkAndWait(t, p, resp(agent))
	hook.mu.Lock()
	hook.status = http.StatusOK
	hook.mu.Unlock()
	checkAndWait(t, p, resp(agent))
	if hook.count() != 4 {
		t.Fatalf("failed send not retried, count = %d, want 4", hook.count())
	}
	checkAndWait(t, p, resp(agent))
	if hook.count() != 4 {
		t.Fatalf("resent after success, count = %d", hook.count())
	}
}

// TestNewHAPushRejectsBadURL checks unset and non-http URLs disable the push.
func TestNewHAPushRejectsBadURL(t *testing.T) {
	for _, raw := range []string{"", "  ", "ftp://ha/hook", "not a url"} {
		t.Setenv(haWebhookEnv, raw)
		if newHAPush() != nil {
			t.Errorf("%q: want nil", raw)
		}
	}
	var p *haPush
	p.check(func() AgentsResponse { t.Fatal("nil pusher built a payload"); return AgentsResponse{} })
}
