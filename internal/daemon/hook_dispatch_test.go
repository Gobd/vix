package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/internal/daemon/hooks"
	"github.com/get-vix/vix/internal/daemon/llm"
	"github.com/get-vix/vix/internal/protocol"
)

// newHookSession builds a Session whose server carries a hook registry loaded
// from dir, plus all tool handlers so executeToolDirect works.
func newHookSession(t *testing.T, cwd, hooksDir, origin string) *Session {
	t.Helper()
	srv := &Server{handlers: make(map[string]HandlerFunc), serverCtx: context.Background()}
	RegisterToolHandlers(srv)
	srv.hookRegistry = hooks.NewRegistry(hooks.NewStore(hooksDir))
	return &Session{
		server:                         srv,
		cwd:                            cwd,
		model:                          "test/model",
		headless:                       true,
		origin:                         origin,
		enableAutomaticWritePermission: true,
		enableAutomaticDirectoryAccess: true,
		readFiles:                      make(map[string]bool),
		startTime:                      time.Now(),
		projectConfig: ProjectConfig{
			ToolTimeouts: ToolTimeouts{Default: 30 * time.Second, Max: 60 * time.Second},
		},
	}
}

func writeHookSpec(t *testing.T, dir, name, body string) {
	t.Helper()
	id := strings.TrimSuffix(name, ".json")
	hookDir := filepath.Join(dir, id)
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDir, "hook.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAnnounceStart_SourceClassification pins the regression where emitReplay
// (which clears attachRecord) ran before the SessionStart source was computed,
// so every resumed session fired as "startup" — wrongly tripping startup-gated
// hooks like the feedback counter. A fresh session must fire only the "startup"
// hook; a resumed session must fire only the "resume" hook.
func TestAnnounceStart_SourceClassification(t *testing.T) {
	for _, tc := range []struct {
		name   string
		resume bool
		want   string
		absent string
	}{
		{"fresh session fires startup", false, "startup.flag", "resume.flag"},
		{"resumed session fires resume", true, "resume.flag", "startup.flag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hd := t.TempDir()
			writeHookSpec(t, hd, "startup.json", `{"id":"startup","enabled":true,"mode":"async","trigger":{"event":"SessionStart","matcher":"startup"},"command":"touch startup.flag"}`)
			writeHookSpec(t, hd, "resume.json", `{"id":"resume","enabled":true,"mode":"async","trigger":{"event":"SessionStart","matcher":"resume"},"command":"touch resume.flag"}`)

			cwd := t.TempDir()
			s := newHookSession(t, cwd, hd, "")
			s.eventChan = make(chan protocol.SessionEvent, 4)
			s.ctx, s.cancel = context.WithCancel(context.Background())
			defer s.cancel()
			if tc.resume {
				s.attachRecord = &sessionRecord{ID: "r1", SessionMode: "chat"}
			}

			s.announceStart()

			// Async hooks fire in goroutines; poll for the expected marker.
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(cwd, tc.want)); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := os.Stat(filepath.Join(cwd, tc.want)); err != nil {
				t.Fatalf("expected %s (the %q hook should have fired)", tc.want, strings.TrimSuffix(tc.want, ".flag"))
			}
			// The other source's hook started concurrently; by now its marker
			// would exist too if it had wrongly matched.
			if _, err := os.Stat(filepath.Join(cwd, tc.absent)); err == nil {
				t.Fatalf("%s should not exist (wrong-source hook fired)", tc.absent)
			}
		})
	}
}

func TestPreToolUseHook_BlockingDeny(t *testing.T) {
	hd := t.TempDir()
	// exit 2 with a reason on stderr → deny.
	writeHookSpec(t, hd, "block.json", `{"id":"block","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse","matcher":"write_file"},"command":"echo blocked-by-test >&2; exit 2"}`)

	s := newHookSession(t, t.TempDir(), hd, "")
	_, reason, denied := s.preToolUseHook(context.Background(), "write_file", map[string]any{"path": "x.txt"})
	if !denied {
		t.Fatal("expected write_file to be denied by hook")
	}
	if reason != "blocked-by-test" {
		t.Fatalf("reason = %q, want blocked-by-test", reason)
	}

	// A non-matching tool is unaffected.
	if _, _, denied := s.preToolUseHook(context.Background(), "read_file", map[string]any{"path": "x"}); denied {
		t.Fatal("read_file should not be denied")
	}
}

func TestPreToolUseHook_NonBlockingDenyDowngraded(t *testing.T) {
	hd := t.TempDir()
	// Same exit 2, but not blocking → must NOT deny (downgraded to context).
	writeHookSpec(t, hd, "warn.json", `{"id":"warn","enabled":true,"mode":"sync","trigger":{"event":"PreToolUse","matcher":"write_file"},"command":"echo nope >&2; exit 2"}`)
	s := newHookSession(t, t.TempDir(), hd, "")
	if _, _, denied := s.preToolUseHook(context.Background(), "write_file", map[string]any{"path": "x"}); denied {
		t.Fatal("non-blocking hook must not deny")
	}
}

func TestPreToolUseHook_Modify(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "mod.json", `{"id":"mod","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse","matcher":"write_file"},"command":"echo '{\"behavior\":\"modify\",\"input\":{\"path\":\"safe.txt\"}}'"}`)
	s := newHookSession(t, t.TempDir(), hd, "")
	newInput, _, denied := s.preToolUseHook(context.Background(), "write_file", map[string]any{"path": "danger.txt"})
	if denied {
		t.Fatal("modify should not deny")
	}
	if newInput == nil || newInput["path"] != "safe.txt" {
		t.Fatalf("expected rewritten path, got %v", newInput)
	}
}

func TestPostToolUseHook_AppendsContext(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "ctx.json", `{"id":"ctx","enabled":true,"mode":"sync","trigger":{"event":"PostToolUse","matcher":"bash"},"command":"echo extra-note"}`)
	s := newHookSession(t, t.TempDir(), hd, "")
	res := &ToolResult{Output: "original"}
	s.postToolUseHook(context.Background(), "bash", map[string]any{"command": "ls"}, res)
	if res.Output == "original" || !strings.Contains(res.Output, "extra-note") {
		t.Fatalf("expected context appended, got %q", res.Output)
	}
}

func TestUserPromptSubmitHook_Veto(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "veto.json", `{"id":"veto","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"UserPromptSubmit"},"command":"exit 2"}`)
	s := newHookSession(t, t.TempDir(), hd, "")
	_, _, denied := s.userPromptSubmitHook(context.Background(), "do something")
	if !denied {
		t.Fatal("expected prompt to be vetoed")
	}
}

func TestHooks_RecursionGuard(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "block.json", `{"id":"block","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse"},"command":"exit 2"}`)
	// origin "vix" marks a hook/job session: it must not fire hooks.
	s := newHookSession(t, t.TempDir(), hd, "vix")
	if _, _, denied := s.preToolUseHook(context.Background(), "write_file", map[string]any{"path": "x"}); denied {
		t.Fatal("vix-origin sessions must not fire hooks (recursion guard)")
	}
}

// TestDispatch_PreToolUseDenyShortCircuits drives the real dispatcher to confirm
// a blocking deny prevents the tool from executing at all.
func TestDispatch_PreToolUseDenyShortCircuits(t *testing.T) {
	hd := t.TempDir()
	writeHookSpec(t, hd, "block.json", `{"id":"block","enabled":true,"mode":"sync","blocking":true,"trigger":{"event":"PreToolUse","matcher":"write_file"},"command":"exit 2"}`)
	s := newHookSession(t, t.TempDir(), hd, "")

	executed := false
	opts := dispatchOptions{
		executeTool: func(name string, input map[string]any) *ToolResult {
			executed = true
			return &ToolResult{Output: "ran"}
		},
		beforeTool: s.preToolUseHook,
		afterTool:  s.postToolUseHook,
	}
	msg := &llm.Message{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "write_file", Input: map[string]any{"path": "x"}}}}
	results := dispatchToolCalls(context.Background(), msg, opts)
	if executed {
		t.Fatal("executeTool must not run when a blocking hook denies")
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}
