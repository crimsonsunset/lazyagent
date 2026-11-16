package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/illegalstudio/lazyagent/internal/cursor"
)

// haWebhookEnv holds the Home Assistant webhook URL that receives the
// /api/agents?pinned=true payload whenever it changes. Unset disables the push.
const haWebhookEnv = "LAZYAGENT_HA_WEBHOOK_URL"

// haPush POSTs the agents list to a Home Assistant webhook when it changes, so
// a consumer like the desk screen updates in about a second instead of on its
// poll. A missed or failed send is retried on the next check; the consumer's
// own poll covers anything still missed.
type haPush struct {
	url    string
	client *http.Client

	mu       sync.Mutex
	sent     string // fingerprint of the last payload HA accepted
	inFlight bool
	pending  bool // a check arrived while a send was running
	failing  bool // last send failed, so the next success is logged
}

// newHAPush returns a pusher for the URL in LAZYAGENT_HA_WEBHOOK_URL, or nil
// when it is unset or not an http(s) URL.
func newHAPush() *haPush {
	raw := strings.TrimSpace(os.Getenv(haWebhookEnv))
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		log.Printf("Warning: %s is not an http(s) URL, Home Assistant push disabled", haWebhookEnv)
		return nil
	}
	return &haPush{url: raw, client: &http.Client{Timeout: 5 * time.Second}}
}

// check builds the payload and sends it if it changed, in the background.
// Only one send runs at a time; a check during a send runs again after it.
func (p *haPush) check(build func() AgentsResponse) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.inFlight {
		p.pending = true
		p.mu.Unlock()
		return
	}
	p.inFlight = true
	p.mu.Unlock()

	go func() {
		for {
			p.deliver(build())
			p.mu.Lock()
			if !p.pending {
				p.inFlight = false
				p.mu.Unlock()
				return
			}
			p.pending = false
			p.mu.Unlock()
		}
	}()
}

// deliver POSTs resp unless HA already has the same content. The fingerprint
// only advances on a 2xx, so a failed send is retried by the next check. With
// cursor.LogTimings on, each send logs the fingerprint and POST durations.
func (p *haPush) deliver(resp AgentsResponse) {
	start := time.Now()
	fp := agentsFingerprint(resp)
	p.mu.Lock()
	unchanged := fp == p.sent
	p.mu.Unlock()
	if unchanged {
		return
	}

	fingerprinted := time.Now()
	err := p.post(resp)
	if cursor.LogTimings {
		log.Printf("timing push: fingerprint=%dms post=%dms ok=%t",
			fingerprinted.Sub(start).Milliseconds(), time.Since(fingerprinted).Milliseconds(), err == nil)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		if !p.failing {
			log.Printf("Home Assistant push failed, retrying on the next change check: %v", err)
		}
		p.failing = true
		return
	}
	if p.failing {
		log.Printf("Home Assistant push recovered")
	}
	p.failing = false
	p.sent = fp
}

// post sends resp as JSON and fails on any non-2xx status.
func (p *haPush) post(resp AgentsResponse) error {
	body, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	r, err := p.client.Post(p.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode > 299 {
		return fmt.Errorf("webhook returned %d", r.StatusCode)
	}
	return nil
}

// agentsFingerprint identifies what a consumer would show: every agent field
// except UpdatedAt (it ticks on every message while an agent works), in an
// order that doesn't depend on it, plus the errors.
func agentsFingerprint(resp AgentsResponse) string {
	agents := make([]AgentItem, len(resp.Agents))
	for i, a := range resp.Agents {
		a.UpdatedAt = time.Time{}
		agents[i] = a
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Source != agents[j].Source {
			return agents[i].Source < agents[j].Source
		}
		return agents[i].ID < agents[j].ID
	})
	b, _ := json.Marshal(struct {
		Agents []AgentItem
		Errors []string
	}{agents, resp.Errors})
	return string(b)
}
