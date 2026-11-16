package api

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDispatchOrigins checks each dispatch run kind maps its agent id to
// "gandalf", origin-chat runs and unknown ids stay "you", and a run written
// later is picked up.
func TestDispatchOrigins(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cloud.json", `{"backend":"cursor","agentId":"bc-cloud"}`)
	write("cli.json", `{"backend":"cpa","localSessionId":"cli-session"}`)
	write("ide.json", `{"backend":"cpa","runtime":"ide","composerId":"ide-chat"}`)
	write("origin.json", `{"backend":"cpa","originChat":"joes-chat","composerId":"joes-chat"}`)
	write("broken.json", `{not json`)
	write("cloud.stream.ndjson", `{"agentId":"not-a-run"}`)

	d := &dispatchOrigins{dir: dir}
	cases := map[string]string{
		"bc-cloud":    originGandalf,
		"cli-session": originGandalf,
		"ide-chat":    originGandalf,
		"joes-chat":   originYou,
		"not-a-run":   originYou,
		"unknown":     originYou,
	}
	for id, want := range cases {
		if got := d.origin(id); got != want {
			t.Errorf("origin(%q) = %q, want %q", id, got, want)
		}
	}

	write("late.json", `{"backend":"cpa","localSessionId":"late-session"}`)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(dir, later, later); err != nil {
		t.Fatal(err)
	}
	if got := d.origin("late-session"); got != originGandalf {
		t.Errorf("origin(late-session) = %q after a new run, want gandalf", got)
	}

	missing := &dispatchOrigins{dir: filepath.Join(dir, "nope")}
	if got := missing.origin("bc-cloud"); got != originYou {
		t.Errorf("missing dir origin = %q, want you", got)
	}
}
