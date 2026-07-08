package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/get-vix/vix/internal/daemon"
	"github.com/get-vix/vix/internal/protocol"
)

// vixSessionsMsg carries the persisted vix-initiated session records (job
// runs, synthetic alerts) for this cwd, shown as their own group in the
// Sessions tab.
type vixSessionsMsg struct {
	sums []protocol.SessionSummary
}

// fetchVixSessions lists the persisted open sessions and keeps the
// vix-initiated, not-currently-attached ones. Triggered on Init, on entering
// the Sessions tab, and on event.sessions_changed broadcasts.
func fetchVixSessions(socketPath, cwd, configDir, authToken string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		sums, err := client.ListSessions(cwd, configDir)
		if err != nil {
			return vixSessionsMsg{}
		}
		var out []protocol.SessionSummary
		for _, s := range sums {
			if s.Origin == "vix" && !s.Attached {
				out = append(out, s)
			}
		}
		return vixSessionsMsg{sums: out}
	}
}

// dismissVixSession archives a vix-initiated record (open/ → closed/) without
// attaching it, then refreshes the list.
func dismissVixSession(socketPath, cwd, configDir, authToken, id string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		client.DismissSession(cwd, configDir, id)
		return fetchVixSessions(socketPath, cwd, configDir, authToken)()
	}
}

// jobsListMsg carries the scheduled jobs and lifecycle hooks shown in the Jobs
// & Triggers tab.
type jobsListMsg struct {
	jobs  []protocol.JobSummary
	hooks []protocol.HookSummary
}

// fetchJobsAndHooks lists the scheduled jobs and lifecycle hooks. Triggered on
// entering the Jobs & Triggers tab and on event.jobs_changed broadcasts.
func fetchJobsAndHooks(socketPath, authToken string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		jobs, err := client.ListJobs()
		if err != nil {
			return jobsListMsg{}
		}
		hooks, err := client.ListHooks()
		if err != nil {
			return jobsListMsg{jobs: jobs}
		}
		return jobsListMsg{jobs: jobs, hooks: hooks}
	}
}

// setJobEnabled toggles a job's enabled flag, then refreshes the list.
func setJobEnabled(socketPath, authToken, id string, enabled bool) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		client.SetJobEnabled(id, enabled)
		return fetchJobsAndHooks(socketPath, authToken)()
	}
}

// setHookEnabled toggles a hook's enabled flag, then refreshes the list.
func setHookEnabled(socketPath, authToken, id string, enabled bool) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		client.SetHookEnabled(id, enabled)
		return fetchJobsAndHooks(socketPath, authToken)()
	}
}

// jobDoneStatusText renders the transient status-bar line for a finished job.
func jobDoneStatusText(ev protocol.EventJobDone) (string, StatusMsgKind) {
	name := ev.Name
	if name == "" {
		name = ev.JobID
	}
	switch ev.Status {
	case "ok":
		return fmt.Sprintf("Job %s finished", name), StatusMsgInfo
	default:
		text := fmt.Sprintf("Job %s failed (%s)", name, ev.Status)
		if ev.Error != "" {
			text += ": " + ev.Error
		}
		return text, StatusMsgWarning
	}
}
