package runtime

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/soulacy/soulacy/pkg/message"
)

// bindRun prevents a request ID from replaying another user's/session's results
// or restoring outputs into a changed workflow. Run IDs are stable across retries.
func (w *WorkflowExecutor) bindRun(ctx context.Context, msg message.Message, runID string) (func(), error) {
	if w.store == nil {
		return func() {}, nil
	}
	key := msg.AgentID + "\x00" + runID
	release, err := w.store.lockRun(ctx, key)
	if err != nil {
		return nil, err
	}
	principal, _ := PrincipalFromContext(ctx)
	payload, err := json.Marshal(struct {
		User, Session, Channel, Thread string
		Parts                          []message.Part
		Principal                      Principal
		Spec                           any
	}{msg.UserID, msg.SessionID, msg.Channel, msg.ThreadID, msg.Parts, principal, w.spec})
	if err != nil {
		release()
		return nil, err
	}
	hash := sha256.Sum256(payload)
	identity := hex.EncodeToString(hash[:])
	_, err = w.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_run_identity(agent_id,run_id,identity)
SELECT ?,?,? WHERE NOT EXISTS (SELECT 1 FROM workflow_checkpoints WHERE agent_id=? AND run_id=?)`, msg.AgentID, runID, identity, msg.AgentID, runID)
	if err != nil {
		release()
		return nil, fmt.Errorf("persist workflow identity: %w", err)
	}
	var saved string
	if err = w.store.db.QueryRowContext(ctx, `SELECT identity FROM workflow_run_identity WHERE agent_id=? AND run_id=?`, msg.AgentID, runID).Scan(&saved); err != nil {
		release()
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("workflow checkpoints have no verified request identity; reconcile the prior run before retrying")
		}
		return nil, err
	}
	if saved != identity {
		release()
		return nil, fmt.Errorf("workflow run ID belongs to a different request or workflow version")
	}
	// An interrupted call may have committed its external action. Never replay
	// it blindly. A caller must reconcile the external result before retrying.
	var step string
	err = w.store.db.QueryRowContext(ctx, `SELECT step_id FROM workflow_checkpoints WHERE agent_id=? AND run_id=? AND status=? LIMIT 1`, msg.AgentID, runID, CheckpointInProgress).Scan(&step)
	if err == nil {
		release()
		return nil, fmt.Errorf("workflow step %q was interrupted with an unknown outcome; verify its external result before retrying", step)
	}
	if err != sql.ErrNoRows {
		release()
		return nil, err
	}
	return release, nil
}

type workflowRunLock struct {
	token chan struct{}
	users int
}

func (s *CheckpointStore) lockRun(ctx context.Context, key string) (func(), error) {
	s.runMu.Lock()
	if s.runLocks == nil {
		s.runLocks = make(map[string]*workflowRunLock)
	}
	lock := s.runLocks[key]
	if lock == nil {
		lock = &workflowRunLock{token: make(chan struct{}, 1)}
		s.runLocks[key] = lock
	}
	lock.users++
	s.runMu.Unlock()
	forget := func() {
		s.runMu.Lock()
		defer s.runMu.Unlock()
		lock.users--
		if lock.users == 0 {
			delete(s.runLocks, key)
		}
	}
	select {
	case lock.token <- struct{}{}:
		return func() { <-lock.token; forget() }, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}
