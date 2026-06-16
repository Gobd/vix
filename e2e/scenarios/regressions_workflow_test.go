package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestPlanWorkflowRunsOnConfiguredProvider is a staged acceptance spec for issue
// #34 ("Plan doesn't work — requires ANTHROPIC_API_KEY"). The Plan workflow is
// entered with Shift+Tab (not a slash command) and its agent steps inherit the
// session model via buildRunnerClient. With an OpenAI-pinned session the
// explore/plan steps must run on OpenAI, never failing with "no credential for
// anthropic".
//
// Skipped: #34 is open, and driving the workflow (Shift+Tab to the Plan mode,
// then the explore→plan→review step machine) is unvalidated here. Enable and
// refine after the first gate run.
//
// When enabled it proves: the plan workflow starts on the session's provider and
// does not raise an anthropic-credential error. T1.1 · asserts screen (workflow
// starts, no anthropic-cred error) + wire (a request reached the OpenAI mock).
func TestPlanWorkflowRunsOnConfiguredProvider(t *testing.T) {
	meta := harness.Meta{
		Category:    "workflow",
		Subcategory: "workflow.plan_provider",
		Description: "the Plan workflow runs on the session's provider, not anthropic (#34)",
		Wire:        harness.WireResponses,
		Variant:     "responses",
	}
	harness.SkipScenario(t, meta, "acceptance spec for #34 (open) + unvalidated Shift+Tab workflow driving; enable when fixed/validated")

	h := harness.Start(t, meta, harness.WithModel("openai/gpt-4o"))

	h.UI.WaitStable(400 * time.Millisecond)

	// Cycle workflow mode (Shift+Tab) until the Plan workflow is active.
	planActive := false
	for range 6 {
		h.UI.Key("shift-tab")
		h.UI.WaitStable(250 * time.Millisecond)
		if h.UI.Contains("Plan") {
			planActive = true
			break
		}
	}
	if !planActive {
		t.Fatalf("could not switch to the Plan workflow; screen:\n%s", h.UI.Snapshot())
	}

	// The explore + plan agent steps stream text; script a couple of replies so
	// the steps can complete up to the user-review gate.
	h.Mock.Enqueue(
		harness.Text("Explored the repository structure."),
		harness.Text("Drafted a plan with three steps."),
	)
	h.UI.Type("come up with a plan to add a greeting command")
	h.UI.Enter()
	h.UI.WaitStable(700 * time.Millisecond)
	h.UI.Shot("plan-running")

	if h.UI.Contains("no credential for anthropic") {
		t.Fatalf("Plan workflow demanded an anthropic credential; screen:\n%s", h.UI.Snapshot())
	}
	if len(h.Mock.Requests()) == 0 {
		t.Fatalf("Plan workflow made no request to the (OpenAI) mock")
	}
}
