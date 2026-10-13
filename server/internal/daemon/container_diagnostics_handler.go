package daemon

import (
	"encoding/json"
	"net/http"
)

// Container diagnostics endpoint
//
// POST /containers/diagnostics on the daemon's localhost health listener.
// Authenticated exactly like /repo/checkout: the bearer token must be the
// task-scoped token of a task currently running in THIS daemon. The response
// is served to the calling task only, so capability availability is always
// visible to the agent that asks — including "not configured", which names
// the configuration an operator must add instead of masquerading as a missing
// binary.
//
// Deliberately NOT a general container API: only three read-only operations,
// an exact-name allowlist, and size/time caps (see container_diagnostics.go).

type containerDiagnosticsRequest struct {
	// Operation is "status", "logs" or "inspect".
	Operation string `json:"operation"`
	// Container is the exact allowlisted container name (logs/inspect).
	Container string `json:"container"`
	// Tail caps log lines (server clamps to the capability maximum).
	Tail int `json:"tail"`
	// Since bounds the log window ("30m", "2h", "24h"; clamped server-side).
	Since string `json:"since"`
}

func (d *Daemon) containerDiagnosticsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		activeTask, authResult := d.activeRepoCheckoutTask(r)
		if authResult != repoCheckoutAuthOK {
			d.writeContainerDiagnosticsAuthError(w, authResult)
			return
		}

		capability := d.containerDiag
		if capability == nil {
			writeContainerDiagnosticsJSON(w, http.StatusNotFound, map[string]any{
				"error": "container diagnostics capability is not configured on this runtime; " +
					"this is an operator configuration, not a missing binary in the task shell. " +
					"Ask the runtime admin to run `multica containers configure --container <name> ...` on the daemon host " +
					"(or add the container_diagnostics section to ~/.multica/config.json) and restart the daemon.",
				"configured": false,
			})
			return
		}

		var req containerDiagnosticsRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
			http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		audit := containerDiagAudit{
			TaskID:    activeTask.TaskID,
			AgentID:   activeTask.AgentID,
			AgentName: activeTask.AgentName,
			Workspace: activeTask.WorkspaceID,
		}

		var result any
		var err error
		switch req.Operation {
		case "status":
			result, err = capability.Status(r.Context(), audit)
		case "logs":
			result, err = capability.Logs(r.Context(), req.Container, req.Since, req.Tail, audit)
		case "inspect":
			result, err = capability.Inspect(r.Context(), req.Container, audit)
		case "":
			http.Error(w, "operation is required (status | logs | inspect)", http.StatusBadRequest)
			return
		default:
			http.Error(w, "unknown operation "+req.Operation+": only status, logs and inspect are available, and all are read-only", http.StatusBadRequest)
			return
		}

		var notAllowed containerDiagNotAllowedError
		if errorsAs(err, &notAllowed) {
			if d.logger != nil {
				d.logger.Warn("container diagnostics refused",
					"reason", "container_not_allowed",
					"requested", notAllowed.requested,
					"task_id", audit.TaskID,
					"agent_id", audit.AgentID,
					"agent_name", audit.AgentName,
				)
			}
			writeContainerDiagnosticsJSON(w, http.StatusForbidden, map[string]any{
				"error":      notAllowed.Error(),
				"configured": true,
				"allowed":    notAllowed.allowed,
			})
			return
		}
		if err != nil {
			if d.logger != nil {
				d.logger.Warn("container diagnostics operation failed",
					"op", req.Operation,
					"container", req.Container,
					"task_id", audit.TaskID,
					"error", err.Error(),
				)
			}
			writeContainerDiagnosticsJSON(w, http.StatusBadGateway, map[string]any{
				"error":      err.Error(),
				"configured": true,
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

// writeContainerDiagnosticsAuthError mirrors writeRepoCheckoutAuthError's
// audit trail with capability-specific wording: the caller is an agent
// diagnosing containers, and the message is all it will ever see.
func (d *Daemon) writeContainerDiagnosticsAuthError(w http.ResponseWriter, result repoCheckoutAuthResult) {
	message := "container diagnostics requires an active task credential, and this request carried none. " +
		"The `multica containers` commands are intended to run inside a Multica task, where MULTICA_TOKEN and MULTICA_DAEMON_PORT are set automatically."
	if result == repoCheckoutAuthUnknownCredential {
		message = "container diagnostics credential is not bound to a task running in this daemon: " +
			"the task it belongs to has already finished, or the daemon restarted after this agent started. " +
			"Container diagnostics is only available while the task that owns the credential is still running."
	}
	reason := "no_credential"
	if result == repoCheckoutAuthUnknownCredential {
		reason = "unknown_credential"
	}
	if d.logger != nil {
		d.logger.Warn("container diagnostics rejected", "reason", reason)
	}
	http.Error(w, message, http.StatusUnauthorized)
}

func writeContainerDiagnosticsJSON(w http.ResponseWriter, status int, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

// errorsAs is errors.As behind a short name so the handler reads linearly.
func errorsAs(err error, target *containerDiagNotAllowedError) bool {
	if err == nil {
		return false
	}
	if na, ok := err.(containerDiagNotAllowedError); ok {
		*target = na
		return true
	}
	return false
}
