package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
)

// RegisterBuiltinHandlers registers ping, init, and force_init handlers.
func RegisterBuiltinHandlers(s *Server) {
	RegisterCredentialHandlers(s)
	RegisterLocalProviderHandlers(s)

	s.RegisterHandler("ping", func(data map[string]any) (map[string]any, error) {
		return map[string]any{"status": "ok", "message": "pong", "version": s.version}, nil
	})

	// daemon.stop performs a coordinated shutdown: every attached vix instance
	// is told to quit, then the daemon exits. Used by `vix daemon stop` and
	// deliberately version-gate-exempt (one-shot RPCs carry no version): the
	// stop command must work precisely when client and daemon versions differ.
	s.RegisterHandler("daemon.stop", func(data map[string]any) (map[string]any, error) {
		go s.QuitAll()
		return map[string]any{"status": "ok", "message": "stopping"}, nil
	})

	s.RegisterHandler("init", func(data map[string]any) (map[string]any, error) {
		path, _ := data["path"].(string)
		if path == "" {
			return map[string]any{"status": "error", "message": "missing 'path'"}, nil
		}
		handler := s.GetHandler("brain.init")
		if handler == nil {
			return map[string]any{"status": "error", "message": "brain.init handler not registered"}, nil
		}
		return handler(map[string]any{"params": map[string]any{"project_path": path}})
	})

	s.RegisterHandler("force_init", func(data map[string]any) (map[string]any, error) {
		path, _ := data["path"].(string)
		if path == "" {
			return map[string]any{"status": "error", "message": "missing 'path'"}, nil
		}
		brainDir := filepath.Join(path, ".vix")

		// Only remove generated artifacts, preserve user config (settings.json, etc.)
		os.RemoveAll(filepath.Join(brainDir, "context"))

		handler := s.GetHandler("brain.init")
		if handler == nil {
			return map[string]any{"status": "error", "message": "brain.init handler not registered"}, nil
		}
		return handler(map[string]any{"params": map[string]any{"project_path": path}})
	})

	// session.list returns the persisted open sessions for the requesting cwd,
	// so a freshly launched TUI can reopen them. Filtering by cwd keeps the
	// global store (~/.vix/sessions) project-scoped at the UI layer.
	s.RegisterHandler("session.list", func(data map[string]any) (map[string]any, error) {
		cwd, _ := data["cwd"].(string)
		configDir, _ := data["config_dir"].(string)
		paths := config.NewVixPaths(configDir, s.homeVixDir, cwd)
		recs := listOpenSessionRecords(paths)
		summaries := make([]protocol.SessionSummary, 0, len(recs))
		for _, r := range recs {
			// Vix-initiated records (job runs, alerts) are global: they run
			// from the job's cwd, not the TUI's, and must surface in every
			// instance regardless of where it was launched.
			if cwd != "" && r.CWD != cwd && r.Origin != "vix" {
				continue
			}
			sum := r.summary()
			// Mark sessions currently live in this daemon so the launching
			// client can skip the ones another instance already owns.
			s.sessionMu.Lock()
			_, sum.Attached = s.sessions[r.ID]
			s.sessionMu.Unlock()
			summaries = append(summaries, sum)
		}
		return map[string]any{"status": "ok", "sessions": summaries}, nil
	})

	// session.dismiss archives a persisted session record (open/ → closed/)
	// without attaching it. Used by the TUI to dismiss vix-initiated run
	// records from the sessions list. Refuses sessions currently live in a
	// connection.
	s.RegisterHandler("session.dismiss", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		s.sessionMu.Lock()
		_, live := s.sessions[id]
		s.sessionMu.Unlock()
		if live {
			return map[string]any{"status": "error", "message": "session is open in another connection"}, nil
		}
		cwd, _ := data["cwd"].(string)
		configDir, _ := data["config_dir"].(string)
		paths := config.NewVixPaths(configDir, s.homeVixDir, cwd)
		if err := moveSessionToClosed(paths, id); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		s.broadcastSessionsChanged()
		return map[string]any{"status": "ok"}, nil
	})

	// message.create materialises a Vix-initiated message session from a whole
	// JSON spec (MessageSessionSpec) carried in the "session" field. It backs
	// `vix session create`, letting external callers (notably command hooks)
	// surface a one-message conversation under "Vix-initiated" without
	// re-encoding the on-disk session record.
	s.RegisterHandler("message.create", func(data map[string]any) (map[string]any, error) {
		raw, ok := data["session"]
		if !ok {
			return map[string]any{"status": "error", "message": "missing 'session'"}, nil
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return map[string]any{"status": "error", "message": "invalid session payload"}, nil
		}
		var spec MessageSessionSpec
		if err := json.Unmarshal(b, &spec); err != nil {
			return map[string]any{"status": "error", "message": fmt.Sprintf("invalid session payload: %v", err)}, nil
		}
		id, err := s.createMessageSession(spec)
		if err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok", "session_id": id}, nil
	})

	// job.run fires a scheduled job immediately by id, out of band from its
	// schedule (backs `vix job run <id>`). Returns the run's session id; the run
	// proceeds in the background and lands under "Vix-initiated".
	s.RegisterHandler("job.run", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		sessionID, err := s.RunJob(id)
		if err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok", "session_id": sessionID}, nil
	})

	// hook.trigger fires a lifecycle hook immediately by id, out of band from its
	// event (backs `vix hook trigger <id>`). Workflow/prompt hooks run in an
	// isolated session (its id is returned); command hooks have no session, so
	// only the fire id is returned.
	s.RegisterHandler("hook.trigger", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		sessionID, fireID, err := s.TriggerHook(id)
		if err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok", "session_id": sessionID, "fire_id": fireID}, nil
	})

	// job.list returns the scheduled jobs (enabled and disabled), powering the
	// TUI's Jobs & Triggers tab. Jobs are daemon-global (~/.vix/jobs), so the
	// list is not cwd-filtered.
	s.RegisterHandler("job.list", func(data map[string]any) (map[string]any, error) {
		return map[string]any{"status": "ok", "jobs": s.JobSummaries()}, nil
	})

	// hook.list returns the lifecycle hooks (enabled and disabled), powering the
	// TUI's Jobs & Triggers tab.
	s.RegisterHandler("hook.list", func(data map[string]any) (map[string]any, error) {
		return map[string]any{"status": "ok", "hooks": s.HookSummaries()}, nil
	})

	// job.set_enabled toggles a job's `enabled` field (surgical in-place edit of
	// its job.json) and reschedules. Backs the Space toggle in the Jobs &
	// Triggers tab.
	s.RegisterHandler("job.set_enabled", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		enabled, _ := data["enabled"].(bool)
		if err := s.SetJobEnabled(id, enabled); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok"}, nil
	})

	// hook.set_enabled toggles a hook's `enabled` field (surgical in-place edit
	// of its hook.json) and reloads. Backs the Space toggle in the Jobs &
	// Triggers tab.
	s.RegisterHandler("hook.set_enabled", func(data map[string]any) (map[string]any, error) {
		id, _ := data["id"].(string)
		if id == "" {
			return map[string]any{"status": "error", "message": "missing 'id'"}, nil
		}
		enabled, _ := data["enabled"].(bool)
		if err := s.SetHookEnabled(id, enabled); err != nil {
			return map[string]any{"status": "error", "message": err.Error()}, nil
		}
		return map[string]any{"status": "ok"}, nil
	})
}
