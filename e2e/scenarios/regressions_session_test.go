package scenarios

import (
	"os"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestConversationSurvivesDaemonRestart guards issue #22: session state used to
// live only in memory, so a daemon crash/restart lost the conversation. The
// daemon now persists each turn to ~/.vix/sessions/open/<id>.json; a freshly
// launched TUI auto-attaches the open session for the workdir and replays it.
//
// T1.8 · asserts disk (record written) + screen (conversation replayed after
// restart).
func TestConversationSurvivesDaemonRestart(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "session",
		Subcategory: "session.persistence",
		Description: "a conversation is replayed after the daemon restarts (#22)",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// One full turn so the daemon persists the conversation (persist runs per
	// turn, right before event.agent_done).
	h.Mock.Enqueue(harness.Text("Acknowledged: the number is 42."))
	h.UI.Type("please remember the number 42")
	h.UI.Enter()
	h.UI.WaitFor("Acknowledged: the number is 42.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("before-restart")

	// Disk: the open session record exists before we restart.
	openDir := h.HomePath(".vix", "sessions", "open")
	if entries, err := os.ReadDir(openDir); err != nil || len(entries) == 0 {
		t.Fatalf("no persisted session record under %s (err=%v)", openDir, err)
	}

	// Restart the whole stack on the same HOME + socket.
	h.Daemon.Restart()
	h.UI.WaitStable(700 * time.Millisecond)
	h.UI.Shot("after-restart")

	// Screen: the freshly launched TUI auto-attached the open session and
	// replayed the prior turn.
	h.UI.WaitFor("Acknowledged: the number is 42.")
	if !h.UI.Contains("please remember the number 42") {
		t.Fatalf("user message not replayed after restart; screen:\n%s", h.UI.Snapshot())
	}
}
