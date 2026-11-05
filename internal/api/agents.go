package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/illegalstudio/lazyagent/internal/core"
	"github.com/illegalstudio/lazyagent/internal/model"
)

const (
	// cursorKeyEnv holds the Cursor API keys (comma separated, one per account)
	// used to list cloud agents. Unset disables the cloud half of /api/agents.
	cursorKeyEnv  = "LAZYAGENT_CURSOR_API_KEY"
	cursorAPIBase = "https://api.cursor.com"
)

// Normalized agent states shared by IDE and cloud agents.
const (
	agentWorking  = "working"
	agentNeedsYou = "needs_you"
	agentIdle     = "idle"
)

// AgentItem is one coding agent, IDE chat or Cursor cloud agent, in a common shape.
type AgentItem struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"` // "ide", "cli" or "cloud"
	Agent     string    `json:"agent"`  // "cursor", "claude", ... (IDE, CLI) or "cursor" (cloud)
	Name      string    `json:"name"`
	Status    string    `json:"status"` // working | needs_you | idle
	Branch    string    `json:"branch,omitempty"`
	Touched   []string  `json:"touched,omitempty"` // other branches the chat worked on
	Repo      string    `json:"repo,omitempty"`    // cwd for IDE and CLI, repo URL for cloud
	URL       string    `json:"url,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentsResponse is returned by GET /api/agents. Errors lists sources that
// failed, so a consumer can tell "no agents" from "cloud lookup broke".
type AgentsResponse struct {
	Agents []AgentItem `json:"agents"`
	Errors []string    `json:"errors"`
}

// cloudAgents lists Cursor cloud agents and caches the result briefly, so
// several consumers polling /api/agents make one upstream call per TTL.
type cloudAgents struct {
	baseURL string
	apiKeys []string
	client  *http.Client
	ttl     time.Duration
	limit   int

	mu         sync.Mutex
	cached     []AgentItem
	cachedErr  error
	fetched    time.Time
	refreshing bool
}

// newCloudAgents takes a comma separated list of Cursor API keys, one per account,
// and the cache, timeout and page limits from config.json "cloud".
func newCloudAgents(baseURL, apiKeys string, limits core.CloudConfig) *cloudAgents {
	var keys []string
	for _, key := range strings.Split(apiKeys, ",") {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	return &cloudAgents{
		baseURL: baseURL,
		apiKeys: keys,
		client:  &http.Client{Timeout: time.Duration(limits.FetchTimeoutSec) * time.Second},
		ttl:     time.Duration(limits.CacheTTLSec) * time.Second,
		limit:   limits.PageLimit,
	}
}

// enabled reports whether at least one Cursor API key is configured.
func (c *cloudAgents) enabled() bool { return len(c.apiKeys) > 0 }

// list returns cloud agents from every account. Fresh cache is returned as is;
// stale cache is returned immediately while one background refresh runs, so a
// consumer with a short timeout never waits on Cursor after the first call. A
// failing account is reported in the error while the others still return.
func (c *cloudAgents) list() ([]AgentItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fetched.IsZero() {
		c.store(c.fetchAll())
		return c.cached, c.cachedErr
	}
	if time.Since(c.fetched) >= c.ttl && !c.refreshing {
		c.refreshing = true
		go c.refresh()
	}
	return c.cached, c.cachedErr
}

// refresh refetches every account and replaces the cache.
func (c *cloudAgents) refresh() {
	items, err := c.fetchAll()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store(items, err)
	c.refreshing = false
}

// store replaces the cache. Callers hold c.mu.
func (c *cloudAgents) store(items []AgentItem, err error) {
	c.cached, c.cachedErr, c.fetched = items, err, time.Now()
}

// fetchAll lists every account in parallel and joins the results in key order.
func (c *cloudAgents) fetchAll() ([]AgentItem, error) {
	results := make([][]AgentItem, len(c.apiKeys))
	errs := make([]error, len(c.apiKeys))
	var wg sync.WaitGroup
	for i, key := range c.apiKeys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.fetchAccount(key)
		}()
	}
	wg.Wait()

	var items []AgentItem
	var failures []string
	for i := range c.apiKeys {
		if errs[i] != nil {
			failures = append(failures, fmt.Sprintf("account %d: %v", i+1, errs[i]))
			continue
		}
		items = append(items, results[i]...)
	}
	if len(failures) > 0 {
		return items, errors.New(strings.Join(failures, "; "))
	}
	return items, nil
}

// fetchAccount lists the cloud agents of one Cursor account.
func (c *cloudAgents) fetchAccount(apiKey string) ([]AgentItem, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("%s/v1/agents?limit=%d", c.baseURL, c.limit), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(apiKey, "")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cursor api returned %d", resp.StatusCode)
	}

	var body struct {
		Items []struct {
			ID        string    `json:"id"`
			Name      string    `json:"name"`
			Status    string    `json:"status"`
			URL       string    `json:"url"`
			UpdatedAt time.Time `json:"updatedAt"`
			Repos     []struct {
				URL string `json:"url"`
			} `json:"repos"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	items := make([]AgentItem, 0, len(body.Items))
	for _, a := range body.Items {
		if strings.EqualFold(a.Status, "ARCHIVED") {
			continue
		}
		item := AgentItem{
			ID:        a.ID,
			Source:    "cloud",
			Agent:     "cursor",
			Name:      a.Name,
			Status:    cloudStatus(a.Status),
			URL:       a.URL,
			UpdatedAt: a.UpdatedAt,
		}
		if len(a.Repos) > 0 {
			item.Repo = a.Repos[0].URL
		}
		items = append(items, item)
	}
	return items, nil
}

// cloudStatus maps a Cursor agent status to the shared states. ACTIVE means a
// turn is running; everything else (IDLE, FINISHED, ...) is not working.
func cloudStatus(status string) string {
	if strings.EqualFold(status, "ACTIVE") {
		return agentWorking
	}
	return agentIdle
}

// ideStatus maps a lazyagent activity to the shared states, matching the
// split Helm uses: waiting = needs_you, idle = idle, anything else = working.
func ideStatus(activity core.ActivityKind) string {
	switch activity {
	case core.ActivityWaiting:
		return agentNeedsYou
	case core.ActivityIdle:
		return agentIdle
	default:
		return agentWorking
	}
}

// localSource names where a local session came from: the Cursor CLI or an IDE chat.
func localSource(sess *model.Session) string {
	if sess.CLI {
		return "cli"
	}
	return "ide"
}

// localSessions returns the local sessions to list. Without includeAll that is
// what the manager's time window shows (30 minutes by default); with it, every
// discovered session except sub-agents, however old.
func (s *Server) localSessions(includeAll bool) []*model.Session {
	if includeAll {
		return s.manager.QueryAllSessions()
	}
	return s.manager.QuerySessions("", "")
}

// ideAgents returns local sessions (IDE chats and CLI sessions) as AgentItems.
func (s *Server) ideAgents(includeAll bool) []AgentItem {
	visible := s.localSessions(includeAll)
	items := make([]AgentItem, 0, len(visible))
	for _, sess := range visible {
		name := s.manager.SessionName(sess.SessionID)
		if name == "" {
			name = sess.Name
		}
		if name == "" {
			name = core.ShortName(sess.CWD, 60)
		}
		items = append(items, AgentItem{
			ID:        sess.SessionID,
			Source:    localSource(sess),
			Agent:     sess.Agent,
			Name:      name,
			Status:    ideStatus(s.manager.ActivityFor(sess.SessionID)),
			Branch:    sess.GitBranch,
			Touched:   sess.TouchedBranches,
			Repo:      sess.CWD,
			UpdatedAt: sess.LastActivity,
		})
	}
	return items
}

// handleGetAgents serves GET /api/agents: IDE chats plus Cursor cloud agents
// in one list. Idle agents and sessions older than the manager's time window
// are left out unless ?all=true.
func (s *Server) handleGetAgents(w http.ResponseWriter, r *http.Request) {
	includeIdle := r.URL.Query().Get("all") == "true"
	resp := AgentsResponse{Agents: []AgentItem{}, Errors: []string{}}

	all := s.ideAgents(includeIdle)
	if s.cloud.enabled() {
		cloud, err := s.cloud.list()
		if err != nil {
			resp.Errors = append(resp.Errors, "cloud: "+err.Error())
		}
		all = append(all, cloud...)
	}

	for _, a := range all {
		if includeIdle || a.Status != agentIdle {
			resp.Agents = append(resp.Agents, a)
		}
	}
	sort.SliceStable(resp.Agents, func(i, j int) bool {
		return resp.Agents[i].UpdatedAt.After(resp.Agents[j].UpdatedAt)
	})
	writeJSON(w, http.StatusOK, resp)
}

// cursorKeyFromEnv returns the configured Cursor API key, trimmed.
func cursorKeyFromEnv() string { return strings.TrimSpace(os.Getenv(cursorKeyEnv)) }
