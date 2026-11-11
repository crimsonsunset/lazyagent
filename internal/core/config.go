package core

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/illegalstudio/lazyagent/internal/apiauth"
	"github.com/illegalstudio/lazyagent/internal/claude"
	"github.com/illegalstudio/lazyagent/internal/cursor"
)

// TUIConfig holds TUI-specific settings.
type TUIConfig struct {
	Theme string `json:"theme"` // "auto" (default), "dark", or "light"
}

// WebhookConfig is a single outbound webhook destination.
type WebhookConfig struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Secret  string   `json:"secret,omitempty"`
	Events  []string `json:"events,omitempty"`
	Agents  []string `json:"agents,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"` // absent = true
}

// IsEnabled returns true unless Enabled is explicitly set to false.
func (w WebhookConfig) IsEnabled() bool {
	return w.Enabled == nil || *w.Enabled
}

// knownActivityNames lists the canonical activity names accepted in config.
var knownActivityNames = map[string]ActivityKind{
	"idle":       ActivityIdle,
	"waiting":    ActivityWaiting,
	"thinking":   ActivityThinking,
	"compacting": ActivityCompacting,
	"reading":    ActivityReading,
	"writing":    ActivityWriting,
	"running":    ActivityRunning,
	"searching":  ActivitySearching,
	"browsing":   ActivityBrowsing,
	"spawning":   ActivitySpawning,
}

// knownAgentNames lists the agent names accepted in config.
// Keep in sync with the agent keys in DefaultConfig().Agents.
var knownAgentNames = map[string]struct{}{
	"claude": {}, "codex": {}, "pi": {}, "cursor": {}, "amp": {}, "opencode": {},
	"grok": {}, "kimi": {}, "kilo": {},
}

// validCardDensities lists the accepted desktop card density values.
var validCardDensities = map[string]struct{}{
	"compact": {}, "rich": {}, "live": {},
}

// NormalizeCardDensity returns d if it is a valid card density, "live" otherwise.
func NormalizeCardDensity(d string) string {
	if _, ok := validCardDensities[d]; ok {
		return d
	}
	return "live"
}

// validTerminals lists the terminal emulators lazyagent knows how to
// launch commands in (Resume, terminal $EDITOR, …). Only presets whose
// launch incantation has been verified on a real setup are enabled;
// re-add the parked ones here (and in tray/terminal.go and the Settings
// panel) once tested.
var validTerminals = map[string]struct{}{
	"terminal": {}, "kitty": {},
	// "iterm2": {}, "ghostty": {}, "wezterm": {}, "alacritty": {},
}

// NormalizeTerminal returns t if it is a supported terminal preset,
// "terminal" (the platform default) otherwise.
func NormalizeTerminal(t string) string {
	if _, ok := validTerminals[t]; ok {
		return t
	}
	return "terminal"
}

// NormalizeDetailWidth returns w if it is a plausible desktop detail-panel
// width in pixels (300–2000), 400 otherwise. The frontend applies the
// tighter window-relative clamp (60% of window width) at drag time.
func NormalizeDetailWidth(w int) int {
	if w < 300 || w > 2000 {
		return 400
	}
	return w
}

// Validate returns nil if the webhook is well-formed.
func (w WebhookConfig) Validate() error {
	if strings.TrimSpace(w.Name) == "" {
		return fmt.Errorf("webhook: name is required")
	}
	if strings.TrimSpace(w.URL) == "" {
		return fmt.Errorf("webhook %q: url is required", w.Name)
	}
	u, err := url.Parse(w.URL)
	if err != nil {
		return fmt.Errorf("webhook %q: url parse: %w", w.Name, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook %q: url scheme must be http or https, got %q", w.Name, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("webhook %q: url is missing host", w.Name)
	}
	for _, ev := range w.Events {
		if _, ok := knownActivityNames[strings.ToLower(ev)]; !ok {
			return fmt.Errorf("webhook %q: unknown event %q", w.Name, ev)
		}
	}
	for _, ag := range w.Agents {
		if _, ok := knownAgentNames[strings.ToLower(ag)]; !ok {
			return fmt.Errorf("webhook %q: unknown agent %q", w.Name, ag)
		}
	}
	return nil
}

// ValidWebhooks returns the subset of webhooks that pass Validate and are enabled.
func (c Config) ValidWebhooks() []WebhookConfig {
	out := make([]WebhookConfig, 0, len(c.Webhooks))
	for _, w := range c.Webhooks {
		if err := w.Validate(); err == nil && w.IsEnabled() {
			out = append(out, w)
		}
	}
	return out
}

// DiscoveryConfig bounds how much session history lazyagent reads.
type DiscoveryConfig struct {
	// RecentDays is how far back Cursor chats and CLI sessions are listed.
	RecentDays int `json:"recent_days"`
	// GitTimeoutSec bounds each git call made to read a branch or worktree.
	GitTimeoutSec int `json:"git_timeout_sec"`
}

// CloudConfig bounds how /api/agents reads Cursor cloud agents.
type CloudConfig struct {
	// CacheTTLSec is how long a cloud agent list is served before a background refresh.
	CacheTTLSec int `json:"cache_ttl_sec"`
	// FetchTimeoutSec bounds one Cursor API call (one per account).
	FetchTimeoutSec int `json:"fetch_timeout_sec"`
	// PageLimit is the most cloud agents read per account.
	PageLimit int `json:"page_limit"`
}

// Config holds application settings shared by TUI and GUI.
type Config struct {
	WindowMinutes        int             `json:"window_minutes"`
	DefaultFilter        string          `json:"default_filter"`
	Editor               string          `json:"editor"`
	LaunchAtLogin        bool            `json:"launch_at_login"`
	Notifications        bool            `json:"notifications"`
	NotifyAfterSec       int             `json:"notify_after_sec"`
	Agents               map[string]bool `json:"agents"`
	ClaudeDirs           []string        `json:"claude_dirs,omitempty"`
	ExcludeCWDSubstrings []string        `json:"exclude_cwd_substrings"`
	Discovery            DiscoveryConfig `json:"discovery"`
	Cloud                CloudConfig     `json:"cloud"`
	TUI                  TUIConfig       `json:"tui"`
	CardDensity          string          `json:"card_density,omitempty"`
	// DetailWidth is the GUI desktop-mode detail panel width in pixels.
	// 0 or out-of-range values mean the 400px default.
	DetailWidth int `json:"detail_width,omitempty"`
	// Terminal picks the terminal emulator for actions that open one
	// (Resume, terminal $EDITOR): terminal or kitty. Empty/unknown means
	// the platform default.
	Terminal string          `json:"terminal,omitempty"`
	Webhooks []WebhookConfig `json:"webhooks,omitempty"`
	// APIPassphrase is the secret used to derive the bearer token that
	// protects the HTTP API. Empty means the API has not been configured yet
	// — `lazyagent --api` will prompt for one on first run.
	APIPassphrase string `json:"api_passphrase,omitempty"`
	// APISalt is a public, per-install salt used with APIPassphrase when
	// deriving the bearer token. It is not secret, but must stay stable so
	// clients can derive the same token from the same passphrase.
	APISalt string `json:"api_salt,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		WindowMinutes:  30,
		DefaultFilter:  "",
		Editor:         "",
		LaunchAtLogin:  false,
		Notifications:  false,
		NotifyAfterSec: 30,
		Agents: map[string]bool{
			"claude":   true,
			"pi":       true,
			"opencode": true,
			"cursor":   true,
			"codex":    true,
			"amp":      true,
			"grok":     true,
			"kilo":     true,
			"kimi":     true,
		},
		ExcludeCWDSubstrings: []string{},
		Discovery:            DiscoveryConfig{RecentDays: 30, GitTimeoutSec: 3},
		Cloud:                CloudConfig{CacheTTLSec: 30, FetchTimeoutSec: 10, PageLimit: 100},
		TUI:                  TUIConfig{Theme: "auto"},
	}
}

// normalizeLimits puts the default back on any discovery or cloud limit that
// is zero or negative, so a typo cannot turn a bound off.
func normalizeLimits(cfg *Config) {
	defaults := DefaultConfig()
	for _, limit := range []struct{ value, fallback *int }{
		{&cfg.Discovery.RecentDays, &defaults.Discovery.RecentDays},
		{&cfg.Discovery.GitTimeoutSec, &defaults.Discovery.GitTimeoutSec},
		{&cfg.Cloud.CacheTTLSec, &defaults.Cloud.CacheTTLSec},
		{&cfg.Cloud.FetchTimeoutSec, &defaults.Cloud.FetchTimeoutSec},
		{&cfg.Cloud.PageLimit, &defaults.Cloud.PageLimit},
	} {
		if *limit.value <= 0 {
			*limit.value = *limit.fallback
		}
	}
}

// ApplyDiscovery pushes the discovery limits into the packages that read
// sessions. Call it once at startup, before discovery runs.
func ApplyDiscovery(cfg Config) {
	cursor.RecentWindow = time.Duration(cfg.Discovery.RecentDays) * 24 * time.Hour
	claude.GitTimeout = time.Duration(cfg.Discovery.GitTimeoutSec) * time.Second
}

// AgentEnabled returns whether an agent is enabled in config.
// Defaults to true if the agent key is missing from the map.
func (c Config) AgentEnabled(name string) bool {
	if c.Agents == nil {
		return true
	}
	enabled, ok := c.Agents[name]
	if !ok {
		return true
	}
	return enabled
}

// ConfigDir returns the config directory path (~/.config/lazyagent).
func ConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "lazyagent")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "lazyagent")
}

// ConfigPath returns the full path to the config file.
func ConfigPath() string {
	dir := ConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.json")
}

// readConfig reads and normalizes the config purely in memory: defaults,
// file contents when present, and backfill of missing generated keys. It
// NEVER writes to disk — persistence of a needed backfill is LoadConfig's
// job (under the config lock), so a stale snapshot can't clobber a
// concurrent writer's fields. needsSave reports whether the on-disk file
// is missing or incomplete.
func readConfig() (cfg Config, needsSave bool) {
	cfg = DefaultConfig()
	path := ConfigPath()
	if path == "" {
		return cfg, false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist — defaults plus a fresh salt.
		ensureAPISalt(&cfg)
		return cfg, true
	}

	_ = json.Unmarshal(data, &cfg)

	changed := ensureAPISalt(&cfg)
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(data, &keys)
	for _, key := range []string{"discovery", "cloud"} {
		if _, ok := keys[key]; !ok {
			changed = true
		}
	}
	normalizeLimits(&cfg)
	defaults := DefaultConfig()
	if cfg.Agents == nil {
		cfg.Agents = defaults.Agents
		changed = true
	} else {
		for k, v := range defaults.Agents {
			if _, ok := cfg.Agents[k]; !ok {
				cfg.Agents[k] = v
				changed = true
			}
		}
	}
	if cfg.ExcludeCWDSubstrings == nil {
		cfg.ExcludeCWDSubstrings = defaults.ExcludeCWDSubstrings
		changed = true
	}
	return cfg, changed
}

// LoadConfig reads the config file, creating or completing it (under the
// config lock) if keys are missing.
func LoadConfig() Config {
	cfg, needsSave := readConfig()
	if needsSave {
		// Persist through UpdateConfig so the backfill save can't race
		// another writer. The no-op mutate is enough: UpdateConfig
		// re-runs readConfig under the lock, which recomputes the
		// backfill. Re-read afterwards so what we return (salt
		// included) is exactly what was persisted.
		if err := UpdateConfig(func(*Config) {}); err == nil {
			cfg, _ = readConfig()
		}
	}

	for _, w := range cfg.Webhooks {
		if err := w.Validate(); err != nil {
			log.Printf("config: %v (skipped)", err)
		}
	}

	return cfg
}

// EnsureAPISalt backfills the public per-install API salt when missing.
// It returns true when cfg was changed.
func EnsureAPISalt(cfg *Config) bool {
	salt := strings.TrimSpace(cfg.APISalt)
	if salt != "" {
		if salt == cfg.APISalt {
			return false
		}
		cfg.APISalt = salt
		return true
	}
	generated, err := apiauth.NewSalt()
	if err != nil {
		// The salt is public and only needs uniqueness, not secrecy. crypto/rand
		// failure is extremely unlikely; this fallback avoids silently reverting
		// to the global legacy salt in constrained environments.
		generated = fmt.Sprintf("%s-%d-%d", apiauth.SaltPrefix, os.Getpid(), time.Now().UnixNano())
	}
	cfg.APISalt = generated
	return true
}

func ensureAPISalt(cfg *Config) bool {
	return EnsureAPISalt(cfg)
}

// SaveConfig writes the config to disk. The file is created with mode 0o600
// because it carries the API passphrase: anyone who can read it can derive
// the bearer token. Existing files are chmod'ed back to 0o600 on every save
// so that historical files (originally 0o644) get tightened on first write
// after upgrading.
func SaveConfig(cfg Config) error {
	path := ConfigPath()
	if path == "" {
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	// os.WriteFile only honors the mode at file creation; force-tighten any
	// pre-existing file that may have been created before this change.
	return os.Chmod(path, 0o600)
}
