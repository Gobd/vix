package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// This file exercises the Sessions tab: its chrome and shortcuts, the per-row
// loading / unread / waiting indicators, navigation + open, the tab-title
// highlight (a background message) vs. blink (a session waiting for input), and
// the Vix-initiated group produced by a scheduled job. Each test drives the
// real TUI through tmux and acts as the model via the mock LLM.

const askQuestion = `{"questions":[{"id":"q1","category":"Choose","question":"Pick one please?","options":["Yes","No"]}]}`

func sessionsMeta(desc string) harness.Meta {
	return harness.Meta{
		Category:    "ui",
		Subcategory: "ui.sessions",
		Description: desc,
		Wire:        harness.WireMessages,
	}
}

// pollUntil polls cond up to timeout, returning true as soon as it holds.
func pollUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// distinctFgOf samples the foreground color in effect where label renders, over
// a window, and returns the set of distinct values seen. Used to tell a static
// tint (one value) from a blink (two values, toggling). The sampling window is
// inherent to the behaviour under test — the tab blink toggles on a real timer.
func distinctFgOf(h *harness.Harness, label string, samples int, interval time.Duration) map[string]int {
	seen := map[string]int{}
	for i := 0; i < samples; i++ {
		if c, ok := h.UI.FgColorOf(label); ok {
			seen[c]++
		}
		time.Sleep(interval)
	}
	return seen
}

// TestSessionsTabChrome verifies F1 opens the Sessions tab and that its static
// chrome renders: the group header, the column headers, and the footer
// shortcuts that advertise the tab's keys.
func TestSessionsTabChrome(t *testing.T) {
	h := harness.Start(t, sessionsMeta("F1 opens the Sessions tab; group/column headers and shortcut hints render"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("sessions-tab")

	for _, want := range []string{
		"Sessions [F1]", "Workspace [F2]", // tab bar
		"User-initiated",              // group header
		"Session", "Title", "Running", // column headers
		"New Session", "Duplicate Session", "Delete Session", "Open In Workspace", // footer hints (Title Case as rendered)
	} {
		if !h.UI.Contains(want) {
			t.Fatalf("Sessions tab missing %q; screen:\n%s", want, h.UI.Snapshot())
		}
	}
}

// TestSessionsLoadingSpinner verifies a session that is actively working shows
// the loading spinner glyph in its row (and no waiting badge) on the Sessions
// tab, and that the spinner clears once the turn completes.
func TestSessionsLoadingSpinner(t *testing.T) {
	h := harness.Start(t, sessionsMeta("a busy session shows the loading spinner on the Sessions tab"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Start a turn but do not reply yet: the session stays in-flight (busy).
	h.UI.Type("do something slow")
	h.UI.Enter()
	h.Mock.Next() // the request is received; the mock handler now parks.

	h.UI.Key("f1")
	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("⠋") }) {
		t.Fatalf("loading spinner glyph not shown for the busy session; screen:\n%s", h.UI.Snapshot())
	}
	if h.UI.Contains("Waiting for input") {
		t.Fatalf("busy session must not show the waiting badge; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("busy-spinner")

	// Finish the turn; the spinner must clear.
	h.Mock.Reply(harness.Text("all done now"))
	if !pollUntil(8*time.Second, func() bool { return !h.UI.Contains("⠋") }) {
		t.Fatalf("spinner did not clear after the turn completed; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("spinner-cleared")
}

// TestSessionsWaitingBadge verifies a session waiting for user input shows the
// "Waiting for input" badge on the Sessions tab.
func TestSessionsWaitingBadge(t *testing.T) {
	h := harness.Start(t, sessionsMeta("a session waiting for input shows the waiting badge on the Sessions tab"))

	h.UI.WaitStable(500 * time.Millisecond)

	h.Mock.Enqueue(harness.ToolUse("ask_question_to_user", askQuestion))
	h.UI.Type("ask me a question")
	h.UI.Enter()
	h.UI.WaitFor("Pick one please?") // the question panel is up (StateUserQuestion).

	h.UI.Key("f1")
	h.UI.WaitFor("Waiting for input")
	h.UI.Shot("waiting-badge")
}

// TestSessionsUnreadIndicatorClears verifies the unread dot appears when a turn
// completes while the conversation isn't being viewed, and is removed once the
// conversation is selected and we return to the Sessions list.
func TestSessionsUnreadIndicatorClears(t *testing.T) {
	h := harness.Start(t, sessionsMeta("unread dot appears off-view and clears after the conversation is opened"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Start a turn, then leave the conversation (go to the Sessions tab) before
	// it completes, so the completion lands as unread.
	h.UI.Type("background turn")
	h.UI.Enter()
	h.Mock.Next()
	h.UI.Key("f1")
	h.Mock.Reply(harness.Text("turn finished"))

	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("●") }) {
		t.Fatalf("unread dot not shown after off-view completion; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("unread-dot")

	// Open the conversation (enter on the selected row), then come back.
	h.UI.Enter()
	h.UI.WaitFor("turn finished")
	h.UI.Key("f1")
	if !pollUntil(8*time.Second, func() bool { return !h.UI.Contains("●") }) {
		t.Fatalf("unread dot not cleared after opening the conversation; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("unread-cleared")
}

// TestSessionsNavigateAndOpen verifies ↑/↓ move the selection and Enter opens
// the highlighted session in the workspace.
func TestSessionsNavigateAndOpen(t *testing.T) {
	h := harness.Start(t, sessionsMeta("arrow keys navigate rows; Enter opens the selected session in the workspace"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Session A (the initial one): one completed turn with a distinctive reply.
	h.Mock.Enqueue(harness.Text("ALPHA-REPLY"))
	h.UI.Type("alpha prompt")
	h.UI.Enter()
	h.UI.WaitFor("ALPHA-REPLY")

	// Session B: a second session with its own distinctive reply.
	h.UI.Ctrl('t')
	h.UI.WaitStable(800 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("BRAVO-REPLY"))
	h.UI.Type("bravo prompt")
	h.UI.Enter()
	h.UI.WaitFor("BRAVO-REPLY")

	// On the Sessions tab, go to the top row (A) and open it.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	h.UI.Key("up")
	h.UI.Key("up")
	h.UI.Enter()
	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("ALPHA-REPLY") }) {
		t.Fatalf("Enter on the top row did not open session A; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("opened-alpha")

	// Back to the list, move down one row (B) and open it.
	h.UI.Key("f1")
	h.UI.Key("down")
	h.UI.Enter()
	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("BRAVO-REPLY") }) {
		t.Fatalf("down+Enter did not open session B; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("opened-bravo")
}

// TestSessionsTabHighlightOnBackgroundMessage verifies that, while the user is
// in the workspace on one session, a message completing on another session
// statically highlights (tints) the Sessions tab title — and does not blink.
func TestSessionsTabHighlightOnBackgroundMessage(t *testing.T) {
	h := harness.Start(t, sessionsMeta("a background message statically highlights the Sessions tab title"))

	h.UI.WaitStable(500 * time.Millisecond)
	fgBase, ok := h.UI.FgColorOf("Sessions [F1]")
	if !ok {
		t.Fatalf("could not read the Sessions tab title color; screen:\n%s", h.UI.Snapshot())
	}

	// Create session B, start a turn on it, then switch back to A (still in the
	// workspace) and let B's turn complete in the background.
	h.UI.Ctrl('t')
	h.UI.WaitStable(800 * time.Millisecond)
	h.UI.Type("background work")
	h.UI.Enter()
	h.Mock.Next()
	h.UI.Ctrl('p') // back to session A; still on the Workspace tab.
	h.Mock.Reply(harness.Text("background finished"))

	// The Sessions title should tint (differ from the inactive baseline).
	if !pollUntil(8*time.Second, func() bool {
		c, ok := h.UI.FgColorOf("Sessions [F1]")
		return ok && c != fgBase
	}) {
		t.Fatalf("Sessions tab title was not highlighted after a background message (base=%q); screen:\n%s", fgBase, h.UI.Snapshot())
	}
	h.UI.Shot("tab-highlighted")

	// A plain background message tints statically — it must not blink: sampled
	// over a blink period the title color stays constant, and no session is
	// waiting for input.
	seen := distinctFgOf(h, "Sessions [F1]", 8, 150*time.Millisecond)
	if len(seen) != 1 {
		t.Fatalf("expected a stable (non-blinking) highlight, saw %d distinct colors: %v", len(seen), seen)
	}
}

// TestSessionsTabBlinkOnWaitingInput verifies that, with the same workspace
// setup, a background session waiting for input makes the Sessions tab title
// blink (its color toggles) — distinct from the static highlight above.
func TestSessionsTabBlinkOnWaitingInput(t *testing.T) {
	h := harness.Start(t, sessionsMeta("a background session waiting for input blinks the Sessions tab title"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Session B asks a question, then we switch back to A in the workspace,
	// leaving B waiting for input.
	h.UI.Ctrl('t')
	h.UI.WaitStable(800 * time.Millisecond)
	h.Mock.Enqueue(harness.ToolUse("ask_question_to_user", askQuestion))
	h.UI.Type("please ask")
	h.UI.Enter()
	h.UI.WaitFor("Pick one please?")
	h.UI.Ctrl('p') // back to session A; B stays in the waiting state.

	// Sampled across a blink period, the title color toggles → ≥2 distinct
	// values. (A static highlight would yield exactly one.)
	seen := distinctFgOf(h, "Sessions [F1]", 18, 120*time.Millisecond)
	if len(seen) < 2 {
		t.Fatalf("expected the Sessions tab title to blink (≥2 colors), saw %d: %v; screen:\n%s", len(seen), seen, h.UI.Snapshot())
	}
	h.UI.Shot("tab-blinking")

	// And the waiting state is what drives it: the badge shows on the tab.
	h.UI.Key("f1")
	h.UI.WaitFor("Waiting for input")
	h.UI.Shot("waiting-on-list")
}

// TestSessionsTitle verifies the Title column reflects the conversation: with no
// auto-title yet (a single turn is below the threshold), it falls back to the
// first user message.
func TestSessionsTitle(t *testing.T) {
	h := harness.Start(t, sessionsMeta("the Title column falls back to the first user message before auto-titling"))

	h.UI.WaitStable(500 * time.Millisecond)

	h.Mock.Enqueue(harness.Text("acknowledged"))
	h.UI.Type("RENAME-THE-WIDGET")
	h.UI.Enter()
	h.UI.WaitFor("acknowledged")

	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if !pollUntil(8*time.Second, func() bool { return h.UI.Contains("RENAME-THE-WIDGET") }) {
		t.Fatalf("Title column did not show the first user message; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("title-from-message")
}

// TestSessionsNewAndOrdering verifies `t` adds a session from the Sessions tab
// and that user-initiated rows render in creation order.
func TestSessionsNewAndOrdering(t *testing.T) {
	h := harness.Start(t, sessionsMeta("`t` adds a session; user rows render in creation order"))

	h.UI.WaitStable(500 * time.Millisecond)

	// Give the initial session (A) a distinctive first message.
	h.Mock.Enqueue(harness.Text("ack-one"))
	h.UI.Type("FIRST-SESSION")
	h.UI.Enter()
	h.UI.WaitFor("ack-one")

	// Add a second session from the Sessions tab with `t`, then give it a
	// distinctive first message too.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	h.UI.Type("t")
	h.UI.WaitStable(800 * time.Millisecond)
	h.Mock.Enqueue(harness.Text("ack-two"))
	h.UI.Type("SECOND-SESSION")
	h.UI.Enter()
	h.UI.WaitFor("ack-two")

	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	snap := ""
	ok := pollUntil(8*time.Second, func() bool {
		snap = h.UI.Snapshot()
		return strings.Contains(snap, "FIRST-SESSION") && strings.Contains(snap, "SECOND-SESSION")
	})
	if !ok {
		t.Fatalf("both sessions not listed; screen:\n%s", snap)
	}
	// Creation order: the first session must appear above the second.
	if strings.Index(snap, "FIRST-SESSION") > strings.Index(snap, "SECOND-SESSION") {
		t.Fatalf("user rows not in creation order; screen:\n%s", snap)
	}
	h.UI.Shot("two-sessions-ordered")
}

// TestSessionsDuplicate verifies `d` duplicates the selected session: a second
// session record lands on disk whose conversation is identical to the source's.
func TestSessionsDuplicate(t *testing.T) {
	h := harness.Start(t, sessionsMeta("`d` writes a duplicate session record identical to the source on disk"))

	h.UI.WaitStable(500 * time.Millisecond)

	// One completed turn is required before a session can be duplicated, and it
	// gives the record a non-trivial conversation to compare.
	h.Mock.Enqueue(harness.Text("ok-to-fork"))
	h.UI.Type("seed turn")
	h.UI.Enter()
	h.UI.WaitFor("ok-to-fork")

	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	h.UI.Key("up") // ensure the seeded session is selected (top row).
	h.UI.Type("d")

	openDir := h.HomePath(".vix", "sessions", "open")
	var recs []sessionRec
	if !pollUntil(8*time.Second, func() bool {
		recs = readSessionRecords(openDir)
		return len(recs) == 2 && len(recs[0].Messages) > 0 && len(recs[1].Messages) > 0
	}) {
		t.Fatalf("expected two session records on disk after duplicate, got %d in %s", len(recs), openDir)
	}

	// The duplicate's conversation must be identical to the source's.
	if !jsonEqual(recs[0].Messages, recs[1].Messages) {
		t.Fatalf("duplicated session is not identical to the source:\nA=%s\nB=%s", recs[0].Messages, recs[1].Messages)
	}
	h.UI.Shot("duplicated-session")
}

// unreadSessionRecord is a persisted open session marked unread, seeded into
// open/ before launch. {{WORKDIR}} is expanded to the per-test cwd so
// session.list (cwd-scoped) returns it.
const unreadSessionRecord = `{
  "schema_version": 1,
  "id": "22222222-2222-2222-2222-222222222222",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "unread": true,
  "started_at": "2024-01-02T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "UNREAD-ONE"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "unread reply"}]}
  ]
}`

// readSessionRecordSeed is an older, already-read session. Being the oldest, it
// becomes the focused initial session on launch, leaving the unread one to be
// restored in the background (where its unread state survives).
const readSessionRecordSeed = `{
  "schema_version": 1,
  "id": "11111111-1111-1111-1111-111111111111",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "started_at": "2024-01-01T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "OLD-READ"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "old reply"}]}
  ]
}`

// TestSessionsUnreadOnLaunch verifies that launching with an unread session in
// open/ highlights the Sessions tab title and shows the unread marker for that
// session in the list.
func TestSessionsUnreadOnLaunch(t *testing.T) {
	h := harness.Start(t, sessionsMeta("an unread session in open/ highlights the Sessions tab and marks the row on launch"),
		harness.WithHomeFile(".vix/sessions/open/11111111-1111-1111-1111-111111111111.json", readSessionRecordSeed),
		harness.WithHomeFile(".vix/sessions/open/22222222-2222-2222-2222-222222222222.json", unreadSessionRecord),
	)

	// On launch the TUI opens on the Workspace tab; the background-restored
	// unread session tints the (inactive) Sessions title. Compare against the
	// Models tab title, which stays the plain inactive color — they must differ.
	highlighted := pollUntil(12*time.Second, func() bool {
		fgSessions, ok1 := h.UI.FgColorOf("Sessions [F1]")
		fgModels, ok2 := h.UI.FgColorOf("Models [F3]")
		return ok1 && ok2 && fgSessions != fgModels
	})
	if !highlighted {
		fs, _ := h.UI.FgColorOf("Sessions [F1]")
		fm, _ := h.UI.FgColorOf("Models [F3]")
		t.Fatalf("Sessions tab not highlighted on launch (Sessions fg=%q, Models fg=%q); screen:\n%s", fs, fm, h.UI.Snapshot())
	}
	h.UI.Shot("tab-highlighted-on-launch")

	// Visiting the Sessions tab clears the title highlight but keeps the per-row
	// unread marker, which must show on the restored unread session.
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")
	if !pollUntil(8*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "●") && strings.Contains(s, "UNREAD-ONE")
	}) {
		t.Fatalf("unread marker not shown for the restored session; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("unread-marker-in-list")
}

// sessionRec is the slice of a persisted session record this suite inspects.
type sessionRec struct {
	ID       string          `json:"id"`
	ParentID string          `json:"parent_id"`
	Messages json.RawMessage `json:"messages"`
}

// readSessionRecords parses every *.json session record in dir.
func readSessionRecords(dir string) []sessionRec {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []sessionRec
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r sessionRec
		if json.Unmarshal(data, &r) != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// jsonEqual reports whether two JSON documents are semantically equal
// (independent of key order / formatting).
func jsonEqual(a, b json.RawMessage) bool {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// TestSessionsDeleteConfirm verifies `x` opens the close-confirmation dialog and
// that declining keeps the session.
func TestSessionsDeleteConfirm(t *testing.T) {
	h := harness.Start(t, sessionsMeta("`x` opens the close-confirmation dialog; declining keeps the session"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("User-initiated")

	h.UI.Type("x")
	h.UI.WaitFor("Close session?")
	if !h.UI.Contains("The session will be terminated.") {
		t.Fatalf("close dialog body not shown; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("close-confirm")

	// "No" is the default; Enter dismisses without closing.
	h.UI.Enter()
	if !pollUntil(5*time.Second, func() bool { return !h.UI.Contains("Close session?") }) {
		t.Fatalf("close dialog did not dismiss; screen:\n%s", h.UI.Snapshot())
	}
	if !h.UI.Contains("User-initiated") {
		t.Fatalf("session row gone after declining close; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("close-declined")
}

// jobSpec is a one-shot scheduled job whose fire time is in the past, so the
// scheduler runs it immediately at startup. The run executes against the mock
// and persists a Vix-initiated session record.
const jobSpec = `{
  "id": "e2e-demo",
  "name": "E2E Demo",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "Say hello.",
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`

// TestSessionsVixInitiated verifies that a scheduled job run lands in the
// Sessions tab's Vix-initiated group, labelled with its run title.
func TestSessionsVixInitiated(t *testing.T) {
	h := harness.Start(t, sessionsMeta("a scheduled job run appears in the Vix-initiated group"),
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-demo/job.json", jobSpec),
	)

	// The job fires at startup and runs against the mock (one turn → persisted).
	h.Mock.Enqueue(harness.Text("hello from the scheduled job"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	// The Vix-initiated row is labelled with the job's run title ("<name> -
	// <timestamp>"), derived from the job's name "E2E Demo".
	if !pollUntil(20*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "Vix-initiated") && strings.Contains(s, "E2E Demo")
	}) {
		t.Fatalf("Vix-initiated job run not listed; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("vix-initiated")
}

// vixAlignErrorRecord and vixAlignOkRecord are two persisted Vix-initiated run
// records seeded into open/: a failed run (job_status "error" → the ⚠ marker in
// the Title column) and a successful one (no marker). They guard the Sessions-tab
// column alignment fix — the warning marker is exactly one display cell wide, so
// the Running column must line up across both rows. {{WORKDIR}} is the per-test
// cwd so session.list returns them.
const vixAlignErrorRecord = `{
  "schema_version": 1,
  "id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "origin": "vix",
  "trigger": {"type": "cron", "ref": "align-job"},
  "job_status": "error",
  "title": "ALIGN-ERR-ROW",
  "started_at": "2024-01-01T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "err run"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "boom"}]}
  ]
}`

const vixAlignOkRecord = `{
  "schema_version": 1,
  "id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
  "cwd": "{{WORKDIR}}",
  "session_mode": "chat",
  "origin": "vix",
  "trigger": {"type": "cron", "ref": "align-job"},
  "job_status": "ok",
  "title": "ALIGN-OK-ROW",
  "started_at": "2024-01-02T00:00:00Z",
  "messages": [
    {"role": "user", "content": [{"type": "text", "text": "ok run"}]},
    {"role": "assistant", "content": [{"type": "text", "text": "fine"}]}
  ]
}`

// runeCol returns the display column (rune offset) at which sub first appears in
// line, or -1. The Sessions-list rows it inspects contain only width-1 glyphs
// (ASCII plus the one-cell ⚠ marker), so a rune offset equals the screen column.
func runeCol(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return len([]rune(line[:i]))
}

// TestSessionsVixInitiatedAlignment guards the Sessions-tab column alignment: a
// failed Vix-initiated run is flagged with a ⚠ marker in the Title column, and
// the marker must not shift the Running column relative to an unflagged row. The
// marker is the plain warning sign (U+26A0, no U+FE0F): one display cell that
// lipgloss and the terminal agree on. The emoji-presentation "⚠️" measures two
// cells in lipgloss but renders as one here, which used to push the Running
// column left on flagged rows.
func TestSessionsVixInitiatedAlignment(t *testing.T) {
	h := harness.Start(t, sessionsMeta("the ⚠ marker keeps the Running column aligned across failed and successful Vix-initiated rows"),
		harness.WithHomeFile(".vix/sessions/open/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa.json", vixAlignErrorRecord),
		harness.WithHomeFile(".vix/sessions/open/bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb.json", vixAlignOkRecord),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	h.UI.WaitFor("Vix-initiated")

	var errLine, okLine string
	if !pollUntil(8*time.Second, func() bool {
		errLine, okLine = "", ""
		for _, ln := range strings.Split(h.UI.Snapshot(), "\n") {
			if strings.Contains(ln, "ALIGN-ERR-ROW") {
				errLine = ln
			}
			if strings.Contains(ln, "ALIGN-OK-ROW") {
				okLine = ln
			}
		}
		return errLine != "" && okLine != ""
	}) {
		t.Fatalf("both Vix-initiated rows not listed; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("vix-initiated-alignment")

	// Only the failed row carries the warning marker.
	if !strings.Contains(errLine, "⚠") {
		t.Fatalf("failed row missing the ⚠ marker: %q", errLine)
	}
	if strings.Contains(okLine, "⚠") {
		t.Fatalf("successful row should not carry the ⚠ marker: %q", okLine)
	}

	// The Running column ("0s ago", frozen by VIX_TEST_RENDER) must start at the
	// same screen column on both rows — the regression under test.
	errCol := runeCol(errLine, "0s ago")
	okCol := runeCol(okLine, "0s ago")
	if errCol < 0 || okCol < 0 {
		t.Fatalf("Running column not found (err=%d ok=%d):\nERR %q\nOK  %q", errCol, okCol, errLine, okLine)
	}
	if errCol != okCol {
		t.Fatalf("Running column misaligned: ⚠ row at col %d, clean row at col %d\nERR %q\nOK  %q", errCol, okCol, errLine, okLine)
	}
}

// inlineWorkflowJobSpec is a one-shot job that runs a self-contained inline
// workflow (no entry in config/workflow.json). The single agent step streams a
// reply through the mock, so the run produces a persisted Vix-initiated session
// — proving the inline-workflow dispatch path end-to-end.
const inlineWorkflowJobSpec = `{
  "id": "e2e-inline",
  "name": "E2E Inline",
  "enabled": true,
  "trigger": {"type": "at", "time": "2000-01-01T00:00:00Z"},
  "prompt": "Say hello from the inline workflow.",
  "workflow": {
    "name": "e2e-inline-wf",
    "entry_point": {"id": "do"},
    "steps": {
      "do": {"type": "agent", "agent": "general", "prompt": "$(workflow.prompt)"}
    }
  },
  "cwd": "{{WORKDIR}}",
  "created_by": "vix"
}`

// TestSessionsVixInitiatedInlineWorkflow verifies that a scheduled job carrying
// an inline workflow definition (rather than a named workflow_id) runs that
// workflow and lands in the Vix-initiated group.
func TestSessionsVixInitiatedInlineWorkflow(t *testing.T) {
	h := harness.Start(t, sessionsMeta("a scheduled job with an inline workflow runs and appears in the Vix-initiated group"),
		harness.WithEnv("VIX_DISABLE_JOBS", "0"),
		harness.WithHomeFile(".vix/jobs/e2e-inline/job.json", inlineWorkflowJobSpec),
	)

	// The inline workflow's single agent step calls the mock once.
	h.Mock.Enqueue(harness.Text("hello from the inline workflow step"))

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Key("f1")
	// The Vix-initiated row is labelled with the job's run title ("<name> -
	// <timestamp>"), derived from the job's name "E2E Inline".
	if !pollUntil(20*time.Second, func() bool {
		s := h.UI.Snapshot()
		return strings.Contains(s, "Vix-initiated") && strings.Contains(s, "E2E Inline")
	}) {
		t.Fatalf("Vix-initiated inline-workflow run not listed; screen:\n%s", h.UI.Snapshot())
	}
	h.UI.Shot("vix-initiated-inline")
}
