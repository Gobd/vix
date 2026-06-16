package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// Lifecycle-hooks e2e scenarios. Hooks are enabled by default (the harness only
// disables jobs), so each test seeds one or more ~/.vix/hooks/<id>/hook.json
// specs and drives the model into the matching event. Hook commands use
// jq/grep/echo/touch (all present in the e2e image) the way real hooks are
// written.
//
// Coverage: every event (PreToolUse, PostToolUse, UserPromptSubmit, SessionStart,
// Stop), every decision (deny, modify, context), both modes (sync, async), the
// command and prompt forms, plus matcher scoping, multi-hook combine
// (most-restrictive-wins), the blocking gate, and the kill switch.

// blockWriteHook is a blocking PreToolUse hook: it parses the tool-input path
// with jq and denies (exit 2) any write to "blocked.txt" — the same idiom the
// docs' protect-files example uses.
const blockWriteHook = `{
  "id": "block-write",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "p=$(jq -r .tool_input.path); case \"$p\" in *blocked.txt*) echo 'blocked by hook' >&2; exit 2;; esac"
}`

// TestHookBlocksWrite proves a blocking PreToolUse hook vetoes a tool call: the
// file is never written and the deny reason is fed back to the model.
func TestHookBlocksWrite(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.pre_tool_deny",
		Description: "a blocking PreToolUse hook denies a write_file; the file never lands and the reason reaches the model",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/block-write/hook.json", blockWriteHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"blocked.txt","content":"nope"}`),
		harness.Text("I could not write that file."),
	)
	h.UI.Type("write blocked.txt")
	h.UI.Enter()
	h.WaitForLLMRequests(2) // denied write turn + final turn
	h.UI.Shot("after-deny")

	if h.FS.Exists("blocked.txt") {
		t.Fatalf("hook deny breached: blocked.txt was written")
	}
	if !anyToolResultContains(h, "blocked by hook") {
		t.Fatalf("deny reason did not reach the model (requests=%d)", len(h.Mock.Requests()))
	}
}

// postContextHook is a non-blocking sync PostToolUse hook on bash: its stdout
// text is injected into the tool result the model sees next.
const postContextHook = `{
  "id": "post-ctx",
  "enabled": true,
  "mode": "sync",
  "trigger": { "event": "PostToolUse", "matcher": "bash" },
  "command": "echo HOOK_SAW_BASH"
}`

// TestHookPostToolUseContext proves a PostToolUse hook can append context to a
// tool result, which then flows to the model on the wire.
func TestHookPostToolUseContext(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.post_tool_context",
		Description: "a PostToolUse hook appends context to a bash result; the note reaches the model",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/post-ctx/hook.json", postContextHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo hi"}`),
		harness.Text("Done."),
	)
	h.UI.Type("run echo hi")
	h.UI.Enter()
	h.WaitForLLMRequests(2) // bash turn + final turn
	h.UI.Shot("after-bash")

	if !anyToolResultContains(h, "HOOK_SAW_BASH") {
		t.Fatalf("PostToolUse context not injected into the tool result (requests=%d)", len(h.Mock.Requests()))
	}
}

// vetoPromptHook is a blocking UserPromptSubmit hook that denies any prompt
// containing "forbidden" (grep over the context JSON on stdin).
const vetoPromptHook = `{
  "id": "veto-prompt",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "UserPromptSubmit" },
  "command": "if grep -q forbidden; then echo 'policy violation' >&2; exit 2; fi"
}`

// TestHookVetoesPrompt proves a blocking UserPromptSubmit hook aborts the turn
// before any LLM request is made.
func TestHookVetoesPrompt(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.prompt_veto",
		Description: "a blocking UserPromptSubmit hook aborts the turn before the model is ever called",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/veto-prompt/hook.json", vetoPromptHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.UI.Type("please do the forbidden thing")
	h.UI.Enter()
	h.UI.WaitFor("blocked by hook")
	h.UI.Shot("prompt-vetoed")

	if n := len(h.Mock.Requests()); n != 0 {
		t.Fatalf("a vetoed prompt must not reach the model, got %d request(s)", n)
	}
}

// TestHookKillSwitch proves VIX_DISABLE_HOOKS=1 turns the engine off: the same
// blocking hook is inert and the write succeeds.
func TestHookKillSwitch(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.kill_switch",
		Description: "VIX_DISABLE_HOOKS=1 disables hooks; a would-be blocking hook does nothing",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/hooks/block-write/hook.json", blockWriteHook),
		harness.WithEnv("VIX_DISABLE_HOOKS", "1"),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"blocked.txt","content":"data"}`),
		harness.Text("Wrote blocked.txt."),
	)
	h.UI.Type("write blocked.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote blocked.txt")
	h.UI.Shot("kill-switch")

	if got := string(h.FS.Read("blocked.txt")); got != "data" {
		t.Fatalf("with hooks disabled the write should land, got %q", got)
	}
}

// ── PreToolUse: modify (rewrite tool input) ─────────────────────────────────

// rewriteWriteHook rewrites a write_file's path to safe.txt via a modify
// decision (jq builds the replacement input from the original tool_input).
const rewriteWriteHook = `{
  "id": "rewrite-write",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "jq -c '{behavior:\"modify\", input:(.tool_input | .path=\"safe.txt\")}'"
}`

// TestHookRewritesToolInput proves a blocking PreToolUse hook can rewrite a
// tool call: the write lands at the hook-chosen path, not the model's.
func TestHookRewritesToolInput(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.pre_tool_modify",
		Description: "a PreToolUse hook rewrites write_file's path; the write lands at the hook-chosen path",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/rewrite-write/hook.json", rewriteWriteHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"danger.txt","content":"payload"}`),
		harness.Text("Wrote the file."),
	)
	h.UI.Type("write danger.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote the file")
	h.UI.Shot("after-rewrite")

	if h.FS.Exists("danger.txt") {
		t.Fatalf("modify hook failed: original path danger.txt was written")
	}
	if got := string(h.FS.Read("safe.txt")); got != "payload" {
		t.Fatalf("rewritten path safe.txt = %q, want %q", got, "payload")
	}
}

// ── UserPromptSubmit: modify (rewrite prompt) ───────────────────────────────

const rewritePromptHook = `{
  "id": "rewrite-prompt",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "UserPromptSubmit" },
  "command": "jq -c '{behavior:\"modify\", input:{prompt:\"REWRITTEN_BY_HOOK\"}}'"
}`

// TestHookRewritesPrompt proves a blocking UserPromptSubmit hook can rewrite the
// prompt before it reaches the model.
func TestHookRewritesPrompt(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.prompt_modify",
		Description: "a UserPromptSubmit hook rewrites the prompt; the model sees the rewritten text",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/rewrite-prompt/hook.json", rewritePromptHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("ok"))
	h.UI.Type("the original user request")
	h.UI.Enter()
	h.WaitForLLMRequests(1)
	h.UI.Shot("after-prompt-rewrite")

	if !anyRequestBodyContains(h, "REWRITTEN_BY_HOOK") {
		t.Fatalf("rewritten prompt did not reach the model")
	}
	if anyRequestBodyContains(h, "the original user request") {
		t.Fatalf("original prompt leaked to the model despite the rewrite")
	}
}

// ── Blocking gate: a non-blocking sync hook cannot veto ─────────────────────

// warnWriteHook exits 2 (a deny) but is NOT blocking, so its veto is downgraded
// and the tool proceeds.
const warnWriteHook = `{
  "id": "warn-write",
  "enabled": true,
  "mode": "sync",
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "echo 'just a warning' >&2; exit 2"
}`

// TestHookNonBlockingDoesNotVeto proves a sync hook without "blocking": true
// cannot stop a tool — its exit-2 is downgraded and the write lands.
func TestHookNonBlockingDoesNotVeto(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.non_blocking",
		Description: "a non-blocking sync hook that exits 2 cannot veto; the write still lands",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/warn-write/hook.json", warnWriteHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"kept.txt","content":"kept"}`),
		harness.Text("Wrote kept.txt."),
	)
	h.UI.Type("write kept.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote kept.txt")
	h.UI.Shot("non-blocking")

	if got := string(h.FS.Read("kept.txt")); got != "kept" {
		t.Fatalf("non-blocking hook should not veto; kept.txt = %q", got)
	}
}

// ── Matcher scoping: a hook only fires for its matched tool ──────────────────

// editOnlyBlockHook blocks edit_file only; a write_file must slip past it.
const editOnlyBlockHook = `{
  "id": "edit-only-block",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "edit_file" },
  "command": "exit 2"
}`

// TestHookMatcherScopesToTool proves the matcher scopes a hook to specific
// tools: an edit_file-scoped block leaves write_file untouched.
func TestHookMatcherScopesToTool(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.matcher_scope",
		Description: "a PreToolUse hook scoped to edit_file does not fire for write_file",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/edit-only-block/hook.json", editOnlyBlockHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"unscoped.txt","content":"ok"}`),
		harness.Text("Wrote unscoped.txt."),
	)
	h.UI.Type("write unscoped.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Wrote unscoped.txt")
	h.UI.Shot("matcher-scope")

	if got := string(h.FS.Read("unscoped.txt")); got != "ok" {
		t.Fatalf("write_file should be unaffected by an edit_file-only hook; got %q", got)
	}
}

// ── Combine: most-restrictive-wins across multiple matching hooks ────────────

const allowWriteHook = `{
  "id": "allow-write",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "exit 0"
}`

const denyWriteHook = `{
  "id": "deny-write",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "command": "echo 'denied by policy' >&2; exit 2"
}`

// TestHookMostRestrictiveWins proves that when two hooks match one tool, a deny
// beats an allow.
func TestHookMostRestrictiveWins(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.combine",
		Description: "two PreToolUse hooks match one write; the deny wins over the allow",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile(".vix/hooks/allow-write/hook.json", allowWriteHook),
		harness.WithHomeFile(".vix/hooks/deny-write/hook.json", denyWriteHook),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"combined.txt","content":"x"}`),
		harness.Text("Could not write."),
	)
	h.UI.Type("write combined.txt")
	h.UI.Enter()
	h.WaitForLLMRequests(2)
	h.UI.Shot("combine-deny-wins")

	if h.FS.Exists("combined.txt") {
		t.Fatalf("most-restrictive-wins failed: combined.txt was written despite a deny")
	}
	if !anyToolResultContains(h, "denied by policy") {
		t.Fatalf("deny reason did not reach the model")
	}
}

// ── async mode: fire-and-forget out of band ─────────────────────────────────

const asyncPostHook = `{
  "id": "async-post",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "PostToolUse", "matcher": "bash" },
  "command": "touch hooked-async.flag"
}`

// TestHookAsyncFireAndForget proves an async hook runs out of band: the turn
// completes and the hook's side effect appears shortly after.
func TestHookAsyncFireAndForget(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.async",
		Description: "an async PostToolUse hook runs fire-and-forget; its side effect lands out of band",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/async-post/hook.json", asyncPostHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("bash", `{"command":"echo hi"}`),
		harness.Text("Done."),
	)
	h.UI.Type("run echo hi")
	h.UI.Enter()
	h.WaitForLLMRequests(2)
	h.UI.Shot("after-async")

	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("hooked-async.flag") }) {
		t.Fatalf("async hook side effect (hooked-async.flag) never appeared")
	}
}

// ── SessionStart event ──────────────────────────────────────────────────────

const sessionStartHook = `{
  "id": "session-start",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "SessionStart" },
  "command": "touch sessionstart.flag"
}`

// TestHookSessionStartFires proves a SessionStart hook runs when the session
// begins, with no user prompt needed.
func TestHookSessionStartFires(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.session_start",
		Description: "a SessionStart hook fires when the session begins",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/session-start/hook.json", sessionStartHook))

	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("session-start")

	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("sessionstart.flag") }) {
		t.Fatalf("SessionStart hook never fired (sessionstart.flag missing)")
	}
}

// ── Stop event ──────────────────────────────────────────────────────────────

const stopHook = `{
  "id": "stop",
  "enabled": true,
  "mode": "async",
  "trigger": { "event": "Stop" },
  "command": "touch stop.flag"
}`

// TestHookStopFires proves a Stop hook fires when a turn finishes.
func TestHookStopFires(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.stop",
		Description: "a Stop hook fires when a turn completes",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/stop/hook.json", stopHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("All done."))
	h.UI.Type("say hello")
	h.UI.Enter()
	h.UI.WaitFor("All done.")
	h.UI.Shot("after-stop")

	if !pollUntil(10*time.Second, func() bool { return h.FS.Exists("stop.flag") }) {
		t.Fatalf("Stop hook never fired (stop.flag missing)")
	}
}

// ── prompt form: a blocking hook backed by an LLM turn ───────────────────────

// promptFormHook is a sync blocking PreToolUse hook whose decision comes from an
// LLM turn (run in an isolated session). The model answers with the BLOCK:
// sentinel, which the engine reads as a deny.
const promptFormHook = `{
  "id": "prompt-form",
  "enabled": true,
  "mode": "sync",
  "blocking": true,
  "timeout": "30s",
  "trigger": { "event": "PreToolUse", "matcher": "write_file" },
  "prompt": "Reply with exactly: BLOCK: not allowed"
}`

// TestHookPromptFormBlocks proves the prompt/workflow execution form: a sync
// blocking hook runs an isolated LLM turn whose BLOCK: output vetoes the tool.
// The mock serves the hook turn's reply between the two main-turn requests.
func TestHookPromptFormBlocks(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "hooks",
		Subcategory: "hooks.prompt_form",
		Description: "a sync blocking prompt-form hook runs an LLM turn whose BLOCK: output vetoes the write",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/hooks/prompt-form/hook.json", promptFormHook))

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("write_file", `{"path":"llm-blocked.txt","content":"x"}`), // main turn 1
		harness.Text("BLOCK: not allowed"),                                        // hook turn
		harness.Text("Could not write."),                                          // main turn 2
	)
	h.UI.Type("write llm-blocked.txt")
	h.UI.Enter()
	h.WaitForLLMRequests(3)
	h.UI.Shot("prompt-form-block")

	if h.FS.Exists("llm-blocked.txt") {
		t.Fatalf("prompt-form hook failed to block: llm-blocked.txt was written")
	}
}
