package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/daemon/jobs"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

func TestCountEndTurns(t *testing.T) {
	msgs := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("u0")),
		llm.NewAssistantMessage(llm.NewToolUseBlock("t1", "read_file", map[string]any{"path": "/x"})), // tool turn: not counted
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "contents", false)),
		llm.NewAssistantMessage(llm.NewTextBlock("a0")), // end turn 1
		llm.NewUserMessage(llm.NewTextBlock("u1")),
		llm.NewAssistantMessage(llm.NewTextBlock("a1")), // end turn 2
	}
	if got := countEndTurns(msgs); got != 2 {
		t.Errorf("countEndTurns = %d, want 2", got)
	}
	if got := countEndTurns(nil); got != 0 {
		t.Errorf("countEndTurns(nil) = %d, want 0", got)
	}
}

func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Fixing the heartbeat scheduler", "Fixing the heartbeat scheduler"},
		{`"Quoted title"`, "Quoted title"},
		{"  Title line\nsecond line", "Title line"},
		{"", ""},
		{"\n\n", ""},
		{strings.Repeat("x", 150), strings.Repeat("x", 100)},
	}
	for _, c := range cases {
		if got := sanitizeTitle(c.in); got != c.want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJobRunTitle(t *testing.T) {
	ts := time.Date(2026, 6, 12, 9, 30, 0, 0, time.UTC)
	spec := jobs.Spec{ID: "heartbeat", Name: "Heartbeat"}
	if got := jobRunTitle(spec, ts); got != "Heartbeat - 06/12/2026 9:30 AM" {
		t.Errorf("jobRunTitle = %q", got)
	}
	spec.Name = ""
	if got := jobRunTitle(spec, ts); got != "heartbeat - 06/12/2026 9:30 AM" {
		t.Errorf("jobRunTitle (no name) = %q", got)
	}
}

func TestTitleTranscriptSkipsToolBlocksAndCaps(t *testing.T) {
	msgs := []llm.MessageParam{
		llm.NewUserMessage(llm.NewTextBlock("question")),
		llm.NewAssistantMessage(llm.NewToolUseBlock("t1", "bash", map[string]any{"command": "ls"})),
		llm.NewUserMessage(llm.NewToolResultBlock("t1", "noise", false)),
		llm.NewAssistantMessage(llm.NewTextBlock("answer")),
	}
	got := titleTranscript(msgs)
	if !strings.Contains(got, "User: question") || !strings.Contains(got, "Assistant: answer") {
		t.Errorf("transcript missing text blocks: %q", got)
	}
	if strings.Contains(got, "noise") || strings.Contains(got, "bash") {
		t.Errorf("transcript leaked tool blocks: %q", got)
	}

	long := []llm.MessageParam{llm.NewUserMessage(llm.NewTextBlock(strings.Repeat("a", 2*titleTranscriptBudget)))}
	if n := len(titleTranscript(long)); n > titleTranscriptBudget+16 {
		t.Errorf("transcript not capped: %d bytes", n)
	}
}

// TestMaybeGenerateTitle exercises the async pass end to end against a fake
// LLM: qualifying session → title set, persisted state updated, and
// event.title_updated emitted.
func TestMaybeGenerateTitle(t *testing.T) {
	fake := &fakeCompactionLLM{summary: `"Refactoring the session store"`}
	s, events := newCompactionTestSession(t, fake)
	s.endTurnCount = titleEndTurnThreshold

	s.maybeGenerateTitle()

	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type != "event.title_updated" {
				continue
			}
			tu := ev.Data.(protocol.EventTitleUpdated)
			if tu.Title != "Refactoring the session store" {
				t.Errorf("event title = %q", tu.Title)
			}
			s.mu.Lock()
			got := s.title
			s.mu.Unlock()
			if got != "Refactoring the session store" {
				t.Errorf("session title = %q", got)
			}
			// The one-shot call must be tool-free.
			if len(fake.gotMsgs) != 1 || fake.gotMsgs[0].Role != llm.RoleUser {
				t.Errorf("unexpected request shape: %+v", fake.gotMsgs)
			}
			if !strings.Contains(fake.gotMsgs[0].Content[0].Text, "User: u0") {
				t.Errorf("prompt missing transcript: %q", fake.gotMsgs[0].Content[0].Text)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for event.title_updated")
		}
	}
}

func TestMaybeGenerateTitleSkips(t *testing.T) {
	cases := []struct {
		name string
		prep func(s *Session)
	}{
		{"below threshold", func(s *Session) { s.endTurnCount = titleEndTurnThreshold - 1 }},
		{"vix origin", func(s *Session) { s.endTurnCount = titleEndTurnThreshold; s.origin = "vix" }},
		{"already titled", func(s *Session) { s.endTurnCount = titleEndTurnThreshold; s.title = "set" }},
		{"in flight", func(s *Session) { s.endTurnCount = titleEndTurnThreshold; s.titleGenInFlight = true }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeCompactionLLM{summary: "nope"}
			s, _ := newCompactionTestSession(t, fake)
			c.prep(s)
			s.maybeGenerateTitle()
			time.Sleep(20 * time.Millisecond)
			if fake.callCount != 0 {
				t.Errorf("LLM called %d times, want 0", fake.callCount)
			}
		})
	}
}
