package coordinator

import (
	"context"
	"path/filepath"

	"coordinate/internal/pilaunch"
	"coordinate/internal/savedpi"
)

type SavedResume struct {
	OperationID string           `json:"operation_id"`
	Target      savedpi.Snapshot `json:"target"`
	SessionDir  string           `json:"session_dir,omitempty"`
}

func (c *Coordinator) StartResumed(ctx context.Context, snapshot savedpi.Snapshot, native pilaunch.Managed, sessionDir string) (JobFacts, error) {
	if validateSnapshot(snapshot) != nil || savedpi.Recheck(snapshot) != nil {
		return JobFacts{}, actionError(2, "saved_session_changed", "saved session is invalid or changed", false, "", "")
	}
	if !filepath.IsAbs(native.Executable) || native.Observation.Version != "1.4.0" || pilaunch.Recheck(native) != nil {
		return JobFacts{}, actionError(2, "invalid_native_pi", "managed Pi observation is invalid or stale", false, "", "")
	}
	if sessionDir != "" && (!filepath.IsAbs(sessionDir) || filepath.Clean(sessionDir) != sessionDir) {
		return JobFacts{}, actionError(2, "invalid_session_dir", "session directory must be canonical and absolute", false, "", "")
	}
	id, err := newID()
	if err != nil {
		return JobFacts{}, actionError(1, "identity_unavailable", "job identity is unavailable", false, "", "")
	}
	resume := &SavedResume{Target: snapshot, SessionDir: sessionDir}
	return c.startTransaction(ctx, "pi-"+id[:8], snapshot.Cwd, nil, &interactiveStart{ID: id, Native: native, Resume: resume}, nil)
}
