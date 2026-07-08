package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/config"
)

func TestSessionsIncludesPersistedOpenRecords(t *testing.T) {
	home := t.TempDir()
	paths := config.NewVixPaths("", home, "")
	started := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	last := time.Date(2026, 6, 17, 12, 5, 0, 0, time.UTC)

	if err := saveSessionRecord(paths, sessionRecord{
		ID:                "persisted",
		CWD:               "/tmp/project",
		Model:             "openai/gpt-5.5",
		Title:             "Persisted session",
		StartedAt:         started,
		LastRequestAt:     last,
		TotalInputTokens:  12,
		TotalOutputTokens: 34,
		Unread:            true,
	}); err != nil {
		t.Fatalf("saveSessionRecord: %v", err)
	}

	srv := &Server{
		homeVixDir: home,
		sessions:   map[string]*Session{},
	}

	infos := srv.Sessions()
	if len(infos) != 1 {
		t.Fatalf("len(Sessions()) = %d, want 1 (%+v)", len(infos), infos)
	}
	info := infos[0]
	if info.ID != "persisted" || info.CWD != "/tmp/project" {
		t.Fatalf("session info = %+v", info)
	}
	if info.Model != "openai/gpt-5.5" || info.Title != "Persisted session" {
		t.Fatalf("session metadata = %+v", info)
	}
	if info.InputTokens != 12 || info.OutputTokens != 34 {
		t.Fatalf("token counts = (%d,%d), want (12,34)", info.InputTokens, info.OutputTokens)
	}
	if info.LastRequestAt == nil || *info.LastRequestAt != last.Format(time.RFC3339) {
		t.Fatalf("LastRequestAt = %v, want %s", info.LastRequestAt, last.Format(time.RFC3339))
	}
	if info.Attached {
		t.Fatal("Attached = true, want false for persisted-only record")
	}
	if !info.Unread {
		t.Fatal("Unread = false, want true")
	}
}

func TestSessionsPrefersLiveSessionOverPersistedRecord(t *testing.T) {
	home := t.TempDir()
	paths := config.NewVixPaths("", home, "")
	started := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)

	if err := saveSessionRecord(paths, sessionRecord{
		ID:                "same",
		CWD:               "/tmp/persisted",
		Model:             "openai/gpt-5.5",
		StartedAt:         started,
		TotalInputTokens:  1,
		TotalOutputTokens: 2,
	}); err != nil {
		t.Fatalf("saveSessionRecord: %v", err)
	}

	srv := &Server{
		homeVixDir: home,
		sessions: map[string]*Session{
			"same": {
				id:                "same",
				cwd:               "/tmp/live",
				model:             "anthropic/claude-sonnet-4-6",
				startTime:         started.Add(time.Minute),
				totalInputTokens:  10,
				totalOutputTokens: 20,
			},
		},
	}

	infos := srv.Sessions()
	if len(infos) != 1 {
		t.Fatalf("len(Sessions()) = %d, want 1 (%+v)", len(infos), infos)
	}
	info := infos[0]
	if !info.Attached {
		t.Fatal("Attached = false, want true for live session")
	}
	if info.CWD != "/tmp/live" || info.Model != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("live session did not override persisted record: %+v", info)
	}
	if info.InputTokens != 10 || info.OutputTokens != 20 {
		t.Fatalf("token counts = (%d,%d), want live counts (10,20)", info.InputTokens, info.OutputTokens)
	}
}

func TestSessionForWebCallRestoresPersistedOpenRecord(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	agentsDir := filepath.Join(home, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll agents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "general.md"), []byte("---\nname: general\n---\nExplore the project files.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile agent: %v", err)
	}

	paths := config.NewVixPaths("", home, "")
	if err := saveSessionRecord(paths, sessionRecord{
		ID:        "persisted-web",
		CWD:       cwd,
		Model:     "openai/gpt-5.5",
		StartedAt: time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("saveSessionRecord: %v", err)
	}

	srv := NewServer("", config.Credential{}, "test-session", "openai/gpt-5.5", &config.DaemonConfig{HomeVixDir: home}, nil)
	sess, cleanup, err := srv.sessionForWebCall("persisted-web")
	if err != nil {
		t.Fatalf("sessionForWebCall: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want cleanup for restored web-only session")
	}
	defer cleanup()
	if sess == nil {
		t.Fatal("sessionForWebCall returned nil session")
	}
	if sess.id != "persisted-web" || sess.cwd != cwd {
		t.Fatalf("restored session = id %q cwd %q, want persisted-web %q", sess.id, sess.cwd, cwd)
	}
	if !sess.headless {
		t.Fatal("restored web session should be headless")
	}
	if _, ok := sess.customAgents["general"]; !ok {
		t.Fatalf("restored web session did not load general agent: %#v", sess.customAgents)
	}
	if live := srv.getSession("persisted-web"); live != nil {
		t.Fatalf("web-only restore registered a live session: %#v", live)
	}
}

func TestSessionForWebCallPrefersLiveSession(t *testing.T) {
	home := t.TempDir()
	live := &Session{id: "same", cwd: "/tmp/live"}
	srv := &Server{
		homeVixDir: home,
		sessions: map[string]*Session{
			"same": live,
		},
	}

	sess, cleanup, err := srv.sessionForWebCall("same")
	if err != nil {
		t.Fatalf("sessionForWebCall: %v", err)
	}
	if sess != live {
		t.Fatalf("sessionForWebCall returned %#v, want live session %#v", sess, live)
	}
	if cleanup != nil {
		t.Fatal("cleanup should be nil for live sessions")
	}
}

func TestRunExplorationReturnsConfigErrorWithoutLLM(t *testing.T) {
	sess := &Session{
		model:     "openai/gpt-5.5",
		configErr: errors.New("missing model credentials"),
		customAgents: map[string]SubagentConfig{
			"general": {Name: "general"},
		},
	}

	_, err := sess.RunExploration(context.Background(), "general", "read the project")
	if err == nil {
		t.Fatal("RunExploration error = nil, want config error")
	}
	if !strings.Contains(err.Error(), "missing model credentials") {
		t.Fatalf("RunExploration error = %q, want config error", err.Error())
	}
}
