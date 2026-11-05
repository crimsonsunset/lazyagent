package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig_ExcludeCWDSubstrings(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ExcludeCWDSubstrings == nil {
		t.Fatal("DefaultConfig().ExcludeCWDSubstrings should not be nil")
	}
	if len(cfg.ExcludeCWDSubstrings) != 0 {
		t.Errorf("DefaultConfig().ExcludeCWDSubstrings = %v, want empty slice", cfg.ExcludeCWDSubstrings)
	}
}

func TestLoadConfig_Limits(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	cfgDir := filepath.Join(tmpDir, "lazyagent")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfgDir, "config.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"window_minutes": 30}`)
	cfg := LoadConfig()
	if cfg.Discovery.RecentDays != 30 || cfg.Discovery.GitTimeoutSec != 3 || cfg.Cloud.CacheTTLSec != 30 || cfg.Cloud.FetchTimeoutSec != 10 || cfg.Cloud.PageLimit != 100 {
		t.Errorf("missing limits did not default: %+v %+v", cfg.Discovery, cfg.Cloud)
	}
	var onDisk map[string]json.RawMessage
	data, _ := os.ReadFile(path)
	_ = json.Unmarshal(data, &onDisk)
	if _, ok := onDisk["discovery"]; !ok {
		t.Error("LoadConfig did not write the discovery block back, so the file never shows the knobs")
	}
	if _, ok := onDisk["cloud"]; !ok {
		t.Error("LoadConfig did not write the cloud block back")
	}

	write(`{"discovery": {"recent_days": 7, "git_timeout_sec": 0}, "cloud": {"page_limit": -5}}`)
	cfg = LoadConfig()
	if cfg.Discovery.RecentDays != 7 {
		t.Errorf("RecentDays = %d, want the configured 7", cfg.Discovery.RecentDays)
	}
	if cfg.Discovery.GitTimeoutSec != 3 || cfg.Cloud.PageLimit != 100 {
		t.Errorf("zero or negative limits must fall back to defaults: %+v %+v", cfg.Discovery, cfg.Cloud)
	}
}

func TestLoadConfig_BackfillsExcludeCWDSubstrings(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Write a config file without ExcludeCWDSubstrings.
	cfgDir := filepath.Join(tmpDir, "lazyagent")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	minimalCfg := map[string]interface{}{
		"window_minutes": 30,
		"agents":         map[string]bool{"claude": true},
	}
	data, _ := json.Marshal(minimalCfg)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig()
	if cfg.ExcludeCWDSubstrings == nil {
		t.Fatal("LoadConfig() did not backfill ExcludeCWDSubstrings")
	}
	if len(cfg.ExcludeCWDSubstrings) != 0 {
		t.Errorf("LoadConfig().ExcludeCWDSubstrings = %v, want []", cfg.ExcludeCWDSubstrings)
	}
}

func TestLoadConfig_PreservesExistingExcludeCWDSubstrings(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Write a config file with custom ExcludeCWDSubstrings.
	cfgDir := filepath.Join(tmpDir, "lazyagent")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	customCfg := map[string]interface{}{
		"window_minutes":         30,
		"agents":                 map[string]bool{"claude": true},
		"exclude_cwd_substrings": []string{"/custom/path", "/another"},
	}
	data, _ := json.Marshal(customCfg)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig()
	if len(cfg.ExcludeCWDSubstrings) != 2 {
		t.Fatalf("LoadConfig() ExcludeCWDSubstrings length = %d, want 2", len(cfg.ExcludeCWDSubstrings))
	}
	if cfg.ExcludeCWDSubstrings[0] != "/custom/path" || cfg.ExcludeCWDSubstrings[1] != "/another" {
		t.Errorf("LoadConfig().ExcludeCWDSubstrings = %v, want [/custom/path /another]", cfg.ExcludeCWDSubstrings)
	}
}

func TestWebhookConfig_ValidateOK(t *testing.T) {
	tr := true
	w := WebhookConfig{
		Name:    "slack",
		URL:     "https://example.com/hook",
		Events:  []string{"waiting"},
		Agents:  []string{"claude"},
		Enabled: &tr,
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !w.IsEnabled() {
		t.Fatal("IsEnabled should be true")
	}
}

func TestWebhookConfig_Validate_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		w    WebhookConfig
	}{
		{"no name", WebhookConfig{URL: "https://x"}},
		{"no url", WebhookConfig{Name: "x"}},
		{"bad scheme", WebhookConfig{Name: "x", URL: "ftp://x"}},
		{"unparseable", WebhookConfig{Name: "x", URL: "::"}},
		{"unknown event", WebhookConfig{Name: "x", URL: "https://x", Events: []string{"nope"}}},
		{"unknown agent", WebhookConfig{Name: "x", URL: "https://x", Agents: []string{"nope"}}},
		{"no host (http)", WebhookConfig{Name: "x", URL: "http://"}},
		{"no host (https with path)", WebhookConfig{Name: "x", URL: "https:///path"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.w.Validate(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestWebhookConfig_IsEnabled_DefaultTrue(t *testing.T) {
	w := WebhookConfig{Name: "x", URL: "https://x"}
	if !w.IsEnabled() {
		t.Fatal("absent Enabled should default to true")
	}
}

func TestConfig_ValidWebhooks_SkipsInvalid(t *testing.T) {
	cfg := Config{Webhooks: []WebhookConfig{
		{Name: "ok", URL: "https://x"},
		{Name: "bad", URL: "ftp://x"},
	}}
	got := cfg.ValidWebhooks()
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("got %+v, want only 'ok'", got)
	}
}

func TestDefaultConfigThemeIsAuto(t *testing.T) {
	if got := DefaultConfig().TUI.Theme; got != "auto" {
		t.Errorf("DefaultConfig().TUI.Theme = %q, want %q", got, "auto")
	}
}

// Existing installs already carry "theme": "dark" — written by LoadConfig
// itself on first run, not chosen by the user. Switching the default to "auto"
// must not migrate them, neither in the returned config nor in the file
// LoadConfig rewrites when it backfills other keys.
func TestLoadConfigDoesNotMigrateExistingTheme(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfgDir := filepath.Join(tmpDir, "lazyagent")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := map[string]interface{}{
		"window_minutes": 30,
		"agents":         map[string]bool{"claude": true},
		"tui":            map[string]string{"theme": "dark"},
	}
	data, _ := json.Marshal(existing)
	path := filepath.Join(cfgDir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := LoadConfig().TUI.Theme; got != "dark" {
		t.Errorf("LoadConfig().TUI.Theme = %q, want %q — existing configs must not be migrated", got, "dark")
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reread Config
	if err := json.Unmarshal(written, &reread); err != nil {
		t.Fatal(err)
	}
	if reread.TUI.Theme != "dark" {
		t.Errorf("config file on disk has theme %q after LoadConfig, want %q", reread.TUI.Theme, "dark")
	}
}

// A config file with no "tui" block at all expresses no choice, so it picks up
// the new default rather than the old hardcoded dark.
func TestLoadConfigAbsentThemeGetsAuto(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfgDir := filepath.Join(tmpDir, "lazyagent")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	minimal := map[string]interface{}{
		"window_minutes": 30,
		"agents":         map[string]bool{"claude": true},
	}
	data, _ := json.Marshal(minimal)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := LoadConfig().TUI.Theme; got != "auto" {
		t.Errorf("LoadConfig().TUI.Theme = %q, want %q", got, "auto")
	}
}
