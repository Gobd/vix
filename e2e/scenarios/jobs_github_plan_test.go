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
// daemon-resolved $(workflow.dir) variable. The model reads it, plans each open
// item exactly once (skipping numbers already recorded), appends newly planned
// numbers, and trims numbers that have dropped off the open list (closed/merged).
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
		Description: "the GitHub plan job picks gh/API/none, shows the plan in its session, and tracks planned items in $(workflow.dir)/memory.md",
		Wire:        harness.WireMessages,
	}
	harness.SkipScenario(t, meta, "no job-run harness primitive yet; branch logic covered by jobWorkflows vitest + engine Go tests")

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

	// The plan step (only reached on the gh/api branches) streams the drafted
	// plan; deny never reaches the model.
	h.Mock.Enqueue(harness.Text("Drafted a step-by-step plan for each open item."))

	// TODO(jobs-harness): create the job (inline githubIssuePlanWorkflow) and
	// fire its trigger, then assert the resulting Vix-initiated session shows
	// the deny error ("can't reach GitHub") for the no-access shims above, and
	// the nag + plan for an api shim, and the plan only for a gh shim. Also
	// assert that after a run the memory file at ~/.vix/jobs/<id>/memory.md
	// records the planned item numbers, and that a second run with a closed item
	// trims that number from the file.
	_ = h.UI.Snapshot()
}
