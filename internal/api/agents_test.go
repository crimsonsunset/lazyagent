package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/illegalstudio/lazyagent/internal/model"
)

// fakeCursorAPI serves a fixed /v1/agents list and counts the calls.
func fakeCursorAPI(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if _, _, ok := r.BasicAuth(); !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"items":[
			{"id":"bc-1","name":"cloud run","status":"ACTIVE","url":"https://cursor.com/agents/bc-1","updatedAt":"2026-11-03T20:00:00Z","repos":[{"url":"https://github.com/o/r"}]},
			{"id":"bc-2","name":"old","status":"IDLE","updatedAt":"2026-11-01T20:00:00Z","repos":[]}
		]}`))
	}))
}

// getAgents fetches GET /api/agents from base with the test token.
func getAgents(t *testing.T, base string) AgentsResponse {
	t.Helper()
	return getAgentsAt(t, base+"/api/agents")
}

// getAgentsAt fetches url with the test token and decodes the AgentsResponse.
func getAgentsAt(t *testing.T, url string) AgentsResponse {
	t.Helper()
	resp := authedGet(t, url)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out AgentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestGetAgentsMergesIDEAndCloud checks IDE and cloud agents are merged, idle ones only with ?all=true.
func TestGetAgentsMergesIDEAndCloud(t *testing.T) {
	now := time.Now()
	srv, ts := newTestServerWithSessions(t, []*model.Session{
		{SessionID: "ide-1", Agent: "cursor", CWD: t.TempDir(), Name: "refactor", LastActivity: now},
	})
	defer ts.Close()

	calls := 0
	cursor := fakeCursorAPI(t, &calls)
	defer cursor.Close()
	srv.cloud = newCloudAgents(cursor.URL, "key", testCloudLimits)

	active := getAgents(t, ts.URL)
	if len(active.Errors) != 0 {
		t.Fatalf("errors = %v", active.Errors)
	}
	if len(active.Agents) != 1 || active.Agents[0].ID != "bc-1" {
		t.Fatalf("default list = %+v, want only the active cloud agent", active.Agents)
	}
	if got := active.Agents[0]; got.Source != "cloud" || got.Status != agentWorking || got.Repo != "https://github.com/o/r" {
		t.Errorf("cloud item = %+v", got)
	}

	byID := map[string]AgentItem{}
	for _, a := range getAgentsAt(t, ts.URL+"/api/agents?all=true").Agents {
		byID[a.ID] = a
	}
	if len(byID) != 3 {
		t.Fatalf("all=true ids = %v, want ide-1, bc-1, bc-2", byID)
	}
	if got := byID["ide-1"]; got.Source != "ide" || got.Name != "refactor" || got.Status != agentIdle {
		t.Errorf("ide item = %+v", got)
	}
}

// TestGetAgentsCachesCloudCalls checks two requests inside the TTL make one upstream call.
func TestGetAgentsCachesCloudCalls(t *testing.T) {
	srv, ts := newTestServer(t)
	defer ts.Close()

	calls := 0
	cursor := fakeCursorAPI(t, &calls)
	defer cursor.Close()
	srv.cloud = newCloudAgents(cursor.URL, "key", testCloudLimits)

	getAgents(t, ts.URL)
	getAgents(t, ts.URL)
	if calls != 1 {
		t.Errorf("cursor api calls = %d, want 1", calls)
	}
}

// TestGetAgentsReportsCloudErrorButKeepsIDE checks a cloud failure is reported and cached while IDE agents still return.
func TestGetAgentsReportsCloudErrorButKeepsIDE(t *testing.T) {
	srv, ts := newTestServer(t)
	defer ts.Close()

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	srv.cloud = newCloudAgents(broken.URL, "key", testCloudLimits)

	out := getAgents(t, ts.URL)
	if len(out.Errors) != 1 {
		t.Errorf("errors = %v, want one cloud error", out.Errors)
	}
}

// TestGetAgentsMergesAccountsAndSurvivesOneFailing checks agents from several accounts merge and one failing account doesn't hide the rest.
func TestGetAgentsMergesAccountsAndSurvivesOneFailing(t *testing.T) {
	srv, ts := newTestServer(t)
	defer ts.Close()

	cursor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, _ := r.BasicAuth()
		switch user {
		case "good-a":
			w.Write([]byte(`{"items":[{"id":"a-1","name":"a","status":"ACTIVE","updatedAt":"2026-11-03T20:00:00Z"},{"id":"a-2","name":"gone","status":"ARCHIVED","updatedAt":"2026-11-03T22:00:00Z"}]}`))
		case "good-b":
			w.Write([]byte(`{"items":[{"id":"b-1","name":"b","status":"ACTIVE","updatedAt":"2026-11-03T21:00:00Z"}]}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer cursor.Close()
	srv.cloud = newCloudAgents(cursor.URL, " good-a, bad ,good-b,", testCloudLimits)

	out := getAgentsAt(t, ts.URL+"/api/agents?all=true")
	if len(out.Agents) != 2 || out.Agents[0].ID != "b-1" || out.Agents[1].ID != "a-1" {
		t.Errorf("agents = %+v, want b-1 then a-1 (archived a-2 dropped)", out.Agents)
	}
	if len(out.Errors) != 1 {
		t.Errorf("errors = %v, want one for the bad account", out.Errors)
	}
}

// TestGetAgentsWithoutKeySkipsCloud checks no Cursor key means no cloud call and no error.
func TestGetAgentsWithoutKeySkipsCloud(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	out := getAgents(t, ts.URL)
	if len(out.Errors) != 0 {
		t.Errorf("errors = %v, want none when no key is set", out.Errors)
	}
}

// TestAgentsRejectsMissingToken checks /api/agents requires the bearer token.
func TestAgentsRejectsMissingToken(t *testing.T) {
	_, ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/agents")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}
