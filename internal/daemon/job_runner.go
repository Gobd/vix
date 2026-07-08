package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/jobs"
	"github.com/get-vix/vix/internal/protocol"
)

// heartbeatOKToken is the contract for "nothing needs attention": a job run
// whose final text is this token (give or take a short ack) is recorded as
// skipped — no session record, no notification.
const heartbeatOKToken = "HEARTBEAT_OK"

// heartbeatOKSlop is how much text may surround the token before the reply
// stops counting as a bare acknowledgement.
const heartbeatOKSlop = 300

// JobRunner returns the jobs.Runner executing runs in-process: an isolated
// headless session per run, mirroring `vix -p [-w workflow]` semantics.
func (s *Server) JobRunner() jobs.Runner {
	return s.runJob
}

// jobTitleTimeFormat renders job-run timestamps in titles (en_US style).
const jobTitleTimeFormat = "01/02/2006 3:04 PM"

// runJob drives one scheduled job run to completion. ctx carries the per-run
// timeout; cancelling it tears the session down.
func (s *Server) runJob(ctx context.Context, spec jobs.Spec, resolvedPrompt string) jobs.RunResult {
	runID := jobs.RunIDFromContext(ctx)
	if runID == "" {
		runID = generateSessionID()
	}
	session := NewSession(runID, s, nil, s.model, spec.CWD, "", false,
		spec.AutoWrite(), spec.AutoDirs(), true /*headless*/, ctx)
	session.origin = "vix"
	session.trigger = &protocol.TriggerInfo{Type: spec.Trigger.Type, Ref: spec.ID}
	session.title = jobRunTitle(spec, time.Now())
	// Expose the job's own directory (~/.vix/jobs/<id>) to workflow templates as
	// $(workflow.dir), so a run can persist state (e.g. a memory file) alongside
	// its spec. Empty when the home directory is unavailable. Also mark it
	// allowed so the run can read/write there even when the jobs directory lives
	// outside $HOME and cwd (e.g. under a --config-dir override) — this flows to
	// the file-tool path checks and the bash sandbox's writable set alike.
	if jobsRoot := config.NewVixPaths("", s.homeVixDir, "").Jobs(); jobsRoot != "" {
		session.jobDir = filepath.Join(jobsRoot, spec.ID)
		session.addAllowedDir(session.jobDir)
	}

	// Register so the web UI and session.list see the run while it's live.
	s.sessionMu.Lock()
	s.sessions[runID] = session
	s.sessionMu.Unlock()
	s.broadcastSessionsChanged()
	defer func() {
		s.sessionMu.Lock()
		delete(s.sessions, runID)
		s.sessionMu.Unlock()
		session.cancel()
		s.broadcastSessionsChanged()
	}()

	go session.Run()

	// Dispatch exactly like headless, resolving the prompt as $(workflow.prompt)
	// when a workflow is involved:
	//   - inline workflow → session.workflow carrying the definition (the
	//     session registers it transiently and runs it);
	//   - named workflow_id → session.workflow by name;
	//   - neither → plain chat turn.
	var startCmd protocol.SessionCommand
	switch {
	case spec.Workflow != nil:
		raw, _ := json.Marshal(spec.Workflow)
		data, _ := json.Marshal(protocol.SessionWorkflowData{Name: spec.Workflow.Name, Text: resolvedPrompt, Workflow: raw})
		startCmd = protocol.SessionCommand{Type: "session.workflow", Data: data}
	case spec.WorkflowID != "":
		data, _ := json.Marshal(protocol.SessionWorkflowData{Name: spec.WorkflowID, Text: resolvedPrompt})
		startCmd = protocol.SessionCommand{Type: "session.workflow", Data: data}
	default:
		data, _ := json.Marshal(protocol.SessionInputData{Text: resolvedPrompt})
		startCmd = protocol.SessionCommand{Type: "session.input", Data: data}
	}
	if !session.pushCommand(ctx, startCmd) {
		return jobs.RunResult{
			Status: jobs.StatusError,
			Err:    "session refused start command",
			Errors: []jobs.RunError{{Source: "start_refused", Message: "session refused start command"}},
		}
	}

	// Consume the event stream (mandatory: emit blocks once eventChan fills
	// with no reader), answering interactive events with unattended policy:
	// confirmations are denied and recorded, questions take the first option,
	// plans are approved — mirroring headless except for the deny.
	var (
		finalText  strings.Builder
		agentTurns int
		hadError   bool
		errMsg     string
		denials    []string
	)

consume:
	for {
		select {
		case ev := <-session.eventChan:
			switch ev.Type {
			case "event.stream_chunk":
				finalText.WriteString(decodeJobEvent[protocol.EventStreamChunk](ev.Data).Text)
			case "event.stream_done":
				agentTurns++
			case "event.confirm_request":
				cr := decodeJobEvent[protocol.EventConfirmRequest](ev.Data)
				denials = append(denials, cr.ToolName)
				data, _ := json.Marshal(protocol.SessionConfirmData{Approved: false})
				session.pushCommand(ctx, protocol.SessionCommand{Type: "session.confirm", Data: data})
			case "event.user_question":
				uq := decodeJobEvent[protocol.EventUserQuestion](ev.Data)
				answer := ""
				if len(uq.RichOptions) > 0 {
					answer = uq.RichOptions[0].Title
				} else if len(uq.Options) > 0 {
					answer = uq.Options[0]
				}
				data, _ := json.Marshal(protocol.SessionUserAnswerData{Answer: answer})
				session.pushCommand(ctx, protocol.SessionCommand{Type: "session.user_answer", Data: data})
			case "event.plan_proposed":
				data, _ := json.Marshal(protocol.SessionPlanActionData{Action: "approve"})
				session.pushCommand(ctx, protocol.SessionCommand{Type: "session.plan_action", Data: data})
			case "event.error":
				hadError = true
				errMsg = decodeJobEvent[protocol.EventError](ev.Data).Message
			case "event.agent_done":
				break consume
			}
		case <-ctx.Done():
			// Timeout or daemon shutdown: the session ctx (derived from ctx)
			// is collapsing; persist what we have and report.
			session.persist()
			return jobs.RunResult{
				Status:     jobs.StatusTimeout,
				Err:        "run cancelled: " + ctx.Err().Error(),
				SessionID:  runID,
				AgentTurns: agentTurns,
				Errors:     []jobs.RunError{{Source: "timeout", Message: "run cancelled: " + ctx.Err().Error()}},
			}
		case <-session.ctx.Done():
			break consume
		}
	}

	res := jobs.RunResult{Status: jobs.StatusOK, SessionID: runID, AgentTurns: agentTurns, Denials: denials}
	if hadError {
		res.Status = jobs.StatusError
		res.Err = errMsg
		res.Errors = append(res.Errors, jobs.RunError{Source: "agent", Message: errMsg})
	}
	if len(denials) > 0 && res.Err == "" {
		res.Err = "needed approval for: " + strings.Join(denials, "; ")
	}
	if len(denials) > 0 {
		res.Errors = append(res.Errors, jobs.RunError{Source: "denied", Message: "needed approval for: " + strings.Join(denials, "; ")})
	}

	// Skip rules — a skipped run leaves no trace:
	//   cheap-poll: no agent step executed (a poll workflow whose execute_if
	//   gate didn't pass — bash steps never call the LLM);
	//   heartbeat OK: the model said nothing needs attention.
	if res.Status == jobs.StatusOK && (agentTurns == 0 || isHeartbeatOK(finalText.String())) {
		deleteSessionRecord(session.paths, runID)
		return jobs.RunResult{Status: jobs.StatusSkipped, SessionID: runID, AgentTurns: agentTurns}
	}

	// Every other finished run lands in open/: visible in the Vix-initiated
	// sessions group until the user dismisses it (or retention sweeps it).
	session.jobStatus = res.Status
	// Successful GitHub-plan runs open their findings with a deterministic
	// header line naming the item they picked; turn that into a per-item session
	// title (e.g. "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — …").
	// Other jobs (and the "nothing new"/error branches) keep the static title.
	if res.Status == jobs.StatusOK {
		if title, ok := issuePlanTitle(spec, finalText.String()); ok {
			session.mu.Lock()
			session.title = title
			session.mu.Unlock()
		}
	}
	session.persist()
	sweepJobRunRecords(session.paths, spec.ID)

	// Failures nobody saw get a synthetic explainer session on top of the run
	// record, so the next TUI launch surfaces them.
	if res.Status != jobs.StatusOK && !s.hasAttachedInstances() {
		s.writeJobAlertSession(spec, res)
	}
	return res
}

// jobRunTitle builds the display title of a job-run session, e.g.
// "Heartbeat - 06/12/2026 9:30 AM".
func jobRunTitle(spec jobs.Spec, t time.Time) string {
	name := spec.Name
	if name == "" {
		name = spec.ID
	}
	return name + " - " + t.Format(jobTitleTimeFormat)
}

// issuePlanHeaderRe matches the deterministic first line of a GitHub-plan run's
// findings (built by the plan step in githubIssuePlanWorkflow):
//
//	Hi, I investigated <issue|pull request> #<n> — <item title> — on GitHub. …
//
// The title is non-greedy and anchored on " — on GitHub." so item titles that
// themselves contain dashes survive. `.` never spans newlines, so the match
// stays on the header line.
var issuePlanHeaderRe = regexp.MustCompile(`Hi, I investigated (issue|pull request) #(\d+) — (.+?) — on GitHub\.`)

// issuePlanTitle derives a per-item session title from a GitHub-plan run's
// final text, e.g. "[Plan GitHub issues (get-vix/vix)] Addressing issue #29 — …".
// Returns ok=false when the deterministic header is absent (any non-plan job, or
// the "nothing new to plan"/error branches), so the caller keeps the static
// jobRunTitle.
func issuePlanTitle(spec jobs.Spec, finalText string) (string, bool) {
	m := issuePlanHeaderRe.FindStringSubmatch(finalText)
	if m == nil {
		return "", false
	}
	kind, number, itemTitle := m[1], m[2], strings.TrimSpace(m[3])
	if itemTitle == "" {
		return "", false
	}
	name := spec.Name
	if name == "" {
		name = spec.ID
	}
	return fmt.Sprintf("[%s] Addressing %s #%s — %s", name, kind, number, itemTitle), true
}

// pushCommand feeds a command to the session loop, giving up when either
// context dies. Returns false when the command was not delivered.
func (s *Session) pushCommand(ctx context.Context, cmd protocol.SessionCommand) bool {
	select {
	case s.commandChan <- cmd:
		return true
	case <-ctx.Done():
		return false
	case <-s.ctx.Done():
		return false
	}
}

// hasAttachedInstances reports whether any vix process is currently attached.
func (s *Server) hasAttachedInstances() bool {
	s.instanceMu.Lock()
	defer s.instanceMu.Unlock()
	return s.instanceCount > 0
}

// broadcastSessionsChanged tells attached clients (and web UI subscribers) the
// persisted sessions list changed outside their own connection.
func (s *Server) broadcastSessionsChanged() {
	s.BroadcastEvent(protocol.SessionEvent{Type: "event.sessions_changed", Data: protocol.EventSessionsChanged{}})
	s.notifySubscribers()
}

// broadcastJobsChanged tells attached clients (and web UI subscribers) the jobs
// or hooks list changed — a run started/finished, a spec was enabled/disabled,
// or the spec directory was reloaded — so the Jobs & Triggers tab re-fetches.
func (s *Server) broadcastJobsChanged() {
	s.BroadcastEvent(protocol.SessionEvent{Type: "event.jobs_changed", Data: protocol.EventJobsChanged{}})
	s.notifySubscribers()
}

// writeJobAlertSession persists a synthetic one-message session explaining a
// failed job run. Zero tokens: the text is canned. It lands in open/ so the
// next TUI launch lists it under Vix-initiated sessions.
func (s *Server) writeJobAlertSession(spec jobs.Spec, res jobs.RunResult) {
	name := spec.Name
	if name == "" {
		name = spec.ID
	}
	text := fmt.Sprintf(
		"Your job %q failed at %s (%s).",
		name, time.Now().Format("15:04"), res.Status)
	if res.Err != "" {
		text += "\n\nError: " + res.Err
	}
	if res.SessionID != "" {
		text += fmt.Sprintf("\n\nThe full run is in session %s.", res.SessionID)
	}
	if _, err := s.createMessageSession(MessageSessionSpec{
		Message: text,
		CWD:     spec.CWD,
		Title:   jobRunTitle(spec, time.Now()),
		Trigger: &protocol.TriggerInfo{Type: spec.Trigger.Type, Ref: spec.ID},
	}); err != nil {
		LogError("job alert session: %v", err)
	}
}

// isHeartbeatOK reports whether text is a bare "nothing needs attention"
// acknowledgement: the HEARTBEAT_OK token at the start or end with at most
// heartbeatOKSlop other characters around it.
func isHeartbeatOK(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if !strings.HasPrefix(t, heartbeatOKToken) && !strings.HasSuffix(t, heartbeatOKToken) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(t, heartbeatOKToken), heartbeatOKToken))
	return len(rest) <= heartbeatOKSlop
}

// decodeJobEvent unmarshals an event payload into the given type.
func decodeJobEvent[T any](data any) T {
	var out T
	raw, err := json.Marshal(data)
	if err != nil {
		return out
	}
	json.Unmarshal(raw, &out)
	return out
}
