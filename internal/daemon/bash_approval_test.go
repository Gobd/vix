package daemon

import (
	"context"
	"strings"
	"testing"
)

func TestIsCommandApproved(t *testing.T) {
	s := &Session{
		approvedBashPrefixes: []string{"go test", "git stash"},
	}
	cases := []struct {
		cmd  string
		want bool
	}{
		{"go test ./...", true},
		{"go test", true},
		{"git stash list", true},
		{"git push origin main", false},
		{"curl https://example.com", false},
		{"", false},
	}
	for _, c := range cases {
		got := s.isCommandApproved(c.cmd)
		if got != c.want {
			t.Errorf("isCommandApproved(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestIsURLApproved(t *testing.T) {
	s := &Session{
		approvedURLPrefixes: []string{"https://api.github.com/"},
	}
	cases := []struct {
		url  string
		want bool
	}{
		{"https://api.github.com/repos/foo", true},
		{"https://api.github.com/", true},
		{"https://evil.com", false},
		{"", false},
	}
	for _, c := range cases {
		got := s.isURLApproved(c.url)
		if got != c.want {
			t.Errorf("isURLApproved(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestSuggestBashPattern(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"curl https://api.github.com/repos/foo?token=abc -H 'Authorization: Bearer xyz'", "curl https://api.github.com/repos/foo"},
		{"git stash list --all", "git stash"},
		{"go test ./... -v", "go test"},
		{"make build ARCH=arm64", "make build"},
		{"python3 script.py --secret=abc", "python3"},
	}
	for _, c := range cases {
		got := suggestBashPattern(c.cmd)
		if got != c.want {
			t.Errorf("suggestBashPattern(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestHeadlessBashGate_HardFailsWhenNotApproved(t *testing.T) {
	s := &Session{
		enableAutomaticBashExecution: false,
		headless:                     true,
		approvedBashPrefixes:         []string{"go test"},
	}

	res := s.executeToolDirect(context.Background(), "bash", map[string]any{"command": "psql -c 'DROP TABLE users'"})
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if res.NeedsConfirmation {
		t.Error("headless should hard-fail, not request confirmation")
	}
	if !res.IsError {
		t.Error("expected IsError=true for unapproved headless bash")
	}
	if !strings.Contains(res.Output, "Permission denied") {
		t.Errorf("expected 'Permission denied' in output, got %q", res.Output)
	}
}

func TestHeadlessWebFetchGate_HardFailsWhenNotApproved(t *testing.T) {
	s := &Session{
		enableAutomaticBashExecution: false,
		headless:                     true,
		approvedURLPrefixes:          []string{"https://api.github.com/"},
	}

	res := s.executeToolDirect(context.Background(), "web_fetch", map[string]any{"url": "https://evil.com/steal"})
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if res.NeedsConfirmation {
		t.Error("headless should hard-fail, not request confirmation")
	}
	if !res.IsError {
		t.Error("expected IsError=true for unapproved headless web_fetch")
	}
	if !strings.Contains(res.Output, "Permission denied") {
		t.Errorf("expected 'Permission denied' in output, got %q", res.Output)
	}
}
