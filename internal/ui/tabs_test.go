package ui

import (
	"strings"
	"testing"

	"github.com/get-vix/vix/internal/protocol"
)

// TestVixRowTitle covers the bare-title Sessions-tab rendering: a clean title
// for ok/skipped runs and a ⚠️ marker for failures. The job-id/status prefix is
// gone for titled rows.
func TestVixRowTitle(t *testing.T) {
	title := "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — env bug"
	cases := []struct {
		name   string
		status string
		want   string
	}{
		{"ok run is bare title", "ok", title},
		{"skipped run is bare title", "skipped", title},
		{"empty status is bare title", "", title},
		{"error run is flagged", "error", "⚠️  " + title},
		{"timeout run is flagged", "timeout", "⚠️  " + title},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sum := protocol.SessionSummary{Title: title, JobStatus: c.status}
			got := vixRowTitle(sum)
			if got != c.want {
				t.Errorf("vixRowTitle(status=%q) = %q, want %q", c.status, got, c.want)
			}
			// The job-id badge and "· ok" prefix never appear for a titled row.
			if strings.Contains(got, " · ") {
				t.Errorf("titled row should not contain the job-id/status prefix: %q", got)
			}
		})
	}
}
func TestTruncateLabel(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"short", 10, "short"},
		{"exactfit!!", 10, "exactfit!!"},
		{"toolongforcell", 10, "toolongfo…"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
		{"abc", -3, ""},
		{"héllo wörld", 6, "héllo…"},
	}
	for _, c := range cases {
		if got := truncateLabel(c.in, c.w); got != c.want {
			t.Errorf("truncateLabel(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
}
