package core

import (
	"testing"
	"time"

	"github.com/illegalstudio/lazyagent/internal/model"
)

func TestToolActivity_ClaudeToolNames(t *testing.T) {
	tests := []struct {
		tool string
		want ActivityKind
	}{
		{"Read", ActivityReading},
		{"Write", ActivityWriting},
		{"Edit", ActivityWriting},
		{"NotebookEdit", ActivityWriting},
		{"Bash", ActivityRunning},
		{"Glob", ActivitySearching},
		{"Grep", ActivitySearching},
		{"WebFetch", ActivityBrowsing},
		{"WebSearch", ActivityBrowsing},
		{"Agent", ActivitySpawning},
		{"UnknownTool", ActivityRunning},
		{"", ActivityIdle},
	}
	for _, tt := range tests {
		got := ToolActivity(tt.tool)
		if got != tt.want {
			t.Errorf("ToolActivity(%q) = %q, want %q", tt.tool, got, tt.want)
		}
	}
}

func TestToolActivity_PiToolNames(t *testing.T) {
	tests := []struct {
		tool string
		want ActivityKind
	}{
		{"read", ActivityReading},
		{"write", ActivityWriting},
		{"edit", ActivityWriting},
		{"bash", ActivityRunning},
		{"process", ActivityRunning},
		{"find", ActivitySearching},
		{"lsp", ActivitySearching},
		{"web_search", ActivityBrowsing},
		{"subagent", ActivitySpawning},
	}
	for _, tt := range tests {
		got := ToolActivity(tt.tool)
		if got != tt.want {
			t.Errorf("ToolActivity(%q) = %q, want %q", tt.tool, got, tt.want)
		}
	}
}

func TestActivityTracker_EmitsTransitionOnChange(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe(8)
	defer bus.Unsubscribe(ch)

	tr := NewActivityTracker()
	tr.SetEventBus(bus)

	now := time.Now()
	s := &model.Session{SessionID: "s1", Agent: "claude", CWD: "/p", LastActivity: now, Status: model.StatusThinking}
	tr.Update([]*model.Session{s}, now)

	// First Update for a session is observation, not a transition: no event.
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event on first observation: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	// Same activity: still no event.
	tr.Update([]*model.Session{s}, now)
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event on unchanged state: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	// Status flips to executing a Bash tool: should emit Thinking→Running.
	s.Status = model.StatusExecutingTool
	s.CurrentTool = "Bash"
	s.RecentTools = []model.ToolCall{{Name: "Bash", Timestamp: now}}
	tr.Update([]*model.Session{s}, now)
	select {
	case ev := <-ch:
		if ev.From != ActivityThinking || ev.To != ActivityRunning {
			t.Fatalf("unexpected transition: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event on real transition")
	}
}

func TestActivityTracker_NilBusSafe(t *testing.T) {
	tr := NewActivityTracker()
	// No SetEventBus call. Must not panic.
	s := &model.Session{SessionID: "s1", Agent: "claude", LastActivity: time.Now(), Status: model.StatusThinking}
	tr.Update([]*model.Session{s}, time.Now())
}

// TestResolveActivity_RunningOutlastsTimeout checks a chat Cursor marks as running stays
// active long after its last message, and drops to idle once the flag clears.
func TestResolveActivity_RunningOutlastsTimeout(t *testing.T) {
	now := time.Now()
	s := &model.Session{SessionID: "s1", Agent: "cursor", LastActivity: now.Add(-10 * time.Minute), Running: true}
	if got := ResolveActivity(s, now); got != ActivityThinking {
		t.Fatalf("running, quiet 10m: got %q, want %q", got, ActivityThinking)
	}

	s.Status, s.CurrentTool = model.StatusExecutingTool, "Bash"
	if got := ResolveActivity(s, now); got != ActivityRunning {
		t.Fatalf("running a shell tool: got %q, want %q", got, ActivityRunning)
	}

	s.Running = false
	if got := ResolveActivity(s, now); got != ActivityIdle {
		t.Fatalf("flag cleared: got %q, want %q", got, ActivityIdle)
	}
}

// TestResolveActivity_BlockedWaitsOnUser checks a chat Cursor marks as waiting on the user
// reads as waiting even with a fresh tool call (the pending question) and hours of quiet,
// skips the grace period, and goes idle once the flag clears.
func TestResolveActivity_BlockedWaitsOnUser(t *testing.T) {
	now := time.Now()
	s := &model.Session{
		SessionID: "s1", Agent: "cursor", Blocked: true, Running: true,
		LastActivity: now, RecentTools: []model.ToolCall{{Name: "ask_question", Timestamp: now}},
	}
	if got := ResolveActivity(s, now); got != ActivityWaiting {
		t.Fatalf("blocked with a fresh tool: got %q, want %q", got, ActivityWaiting)
	}

	tracker := NewActivityTracker()
	tracker.Update([]*model.Session{s}, now)
	if got := tracker.Get("s1"); got != ActivityWaiting {
		t.Fatalf("blocked should skip the waiting grace: got %q", got)
	}

	s.LastActivity, s.RecentTools, s.Running = now.Add(-3*time.Hour), nil, false
	if got := ResolveActivity(s, now); got != ActivityWaiting {
		t.Fatalf("blocked, quiet 3h: got %q, want %q", got, ActivityWaiting)
	}

	s.Blocked = false
	if got := ResolveActivity(s, now); got != ActivityIdle {
		t.Fatalf("flag cleared: got %q, want %q", got, ActivityIdle)
	}
}
