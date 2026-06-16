package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestGithubPlanJobAccessBranches is a staged acceptance spec for the mission
// control "Plan GitHub issues" job (inline workflow githubIssuePlanWorkflow).
// It pins the runtime GitHub-access contract the workflow must honour:
//
//	detect → deny | fetch → nag | plan
//
//   - gh signed in   → fetch via `gh`, then `plan` (the plan appears in the
//     session; nothing is posted back to GitHub).
//   - gh missing/unauth but the public API reachable → fetch via `curl`, a `nag`
//     reminding the user to install + `gh auth login`, then `plan`.
//   - no access at all → `deny` prints a clear error and exits non-zero, so the
//     run is recorded as failed with that message and no plan is attempted.
//
// The `plan` step also maintains a memory file at $(workflow.dir)/memory.md — the
// run's own job directory (~/.vix/jobs/<id>) surfaced to the workflow as the
// daemon-resolved $(workflow.dir) variable. The model reads it, claims EXACTLY
// ONE not-yet-addressed open item per run (marking it before it starts), drafts
// a plan for that single item, marks it addressed when done, and trims numbers
// that have dropped off the open list (closed/merged). The planning
// instructions are baked into the plan step itself (the job's prompt is only a
// label), so the run does not depend on a user-supplied prompt.
//
// Skipped: scheduled-job runs execute in the daemon's scheduler, not through the
// TUI, and the harness has no job-run primitive (no /api/jobs driver, no way to
// fire a trigger and read back the Vix-initiated session). The branch logic is
// covered today by the builder's vitest unit tests
// (internal/daemon/web/source/src/data/jobWorkflows.test.ts), and $(workflow.dir)
// resolution + the memory-file-aware watcher by the workflow engine's Go tests.
// Enable this once the harness can create + run a job and surface its session
// transcript.
//
// When enabled it proves, per branch, that the right path runs and the plan (or
// error/nag) lands in the run's session, and that the memory file under
// $(workflow.dir) is created/updated/trimmed across consecutive runs. The body
// below seeds the gh/curl shims the three branches switch on; the trigger +
// transcript read-back is the missing piece.
func TestGithubPlanJobAccessBranches(t *testing.T) {
	meta := harness.Meta{
		Category:    "jobs",
		Subcategory: "jobs.github_plan",
		Description: "the GitHub plan job picks gh/API/none, shows its framed findings in a per-item-titled session, and tracks planned items in $(workflow.dir)/memory.md",
		Wire:        harness.WireMessages,
	}
	harness.SkipScenario(t, meta, "branch matrix needs the frontend-generated githubIssuePlanWorkflow JSON + gh/curl shims; the daemon-side title/transcript/chat-mode are covered live by jobs.plan_session (jobs_plan_session_test.go)")

	// "no access" shim: gh absent (not installed) and curl always fails — the
	// detect step must resolve to `none` and route to `deny`.
	ghStub := "#!/bin/sh\nexit 127\n"
	curlFail := "#!/bin/sh\nexit 7\n"

	h := harness.Start(t, meta,
		harness.WithWorkdirFile("bin/gh", ghStub),
		harness.WithWorkdirFile("bin/curl", curlFail),
		harness.WithEnv("PATH", "./bin:/usr/bin:/bin"),
	)

	h.UI.WaitStable(400 * time.Millisecond)

	// The plan step (only reached on the gh/api branches) streams the framed
	// findings, which open with the deterministic header the daemon parses to
	// title the session.
	h.Mock.Enqueue(harness.Text("Hi, I investigated issue #29 — ANTHROPIC_BASE_URL not resolved from .env files — on GitHub. Here are my findings:\n\nhttps://github.com/get-vix/vix/issues/29\n\n**Summary**\nThe base URL isn't read from .env.\n\n**My take**\nLegit, actionable bug.\n\n**Plan**\n1. Resolve ANTHROPIC_BASE_URL in config loading."))

	// TODO(jobs-harness): create the job (inline githubIssuePlanWorkflow) and
	// fire its trigger, then assert the resulting Vix-initiated session:
	//   - for the no-access shims above, shows the deny error ("can't reach
	//     GitHub"); for an api shim, the nag + framed findings; for a gh shim,
	//     the framed findings only;
	//   - is titled after the picked item, e.g.
	//     "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — ANTHROPIC_BASE_URL not resolved from .env files"
	//     (parsed from the findings' deterministic header line);
	//   - keeps the FULL working transcript — the plan-step prompt, the agent's
	//     tool_use/tool_result turns, and the final findings — not a text-only
	//     summary, so a follow-up turn is grounded in the real tool calls.
	// Also assert that after a run the memory file at ~/.vix/jobs/<id>/memory.md
	// records exactly the one item the run claimed, that a second run claims a
	// different open item, and that a closed item's number is trimmed from the
	// file.
	_ = h.UI.Snapshot()
}
