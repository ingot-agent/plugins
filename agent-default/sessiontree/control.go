package sessiontree

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ingot-agent/plugins/agent-default/sessioncontrol"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

func (t *tree) BeginRoot(ctx context.Context, sessionID session.ID) (sessioncontrol.Handle, error) {
	if ctx == nil || sessionID == "" {
		return sessioncontrol.Handle{}, agent.ErrChildUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return sessioncontrol.Handle{}, err
	}
	if t.config.enabled {
		record, err := t.repository.GetChildSession(ctx, sessionID)
		if err != nil {
			return sessioncontrol.Handle{}, err
		}
		if record.Agent.Kind == agent.ChildSessionKind {
			return sessioncontrol.Handle{}, fmt.Errorf("child Session %q can only run through its accepted task: %w", sessionID, agent.ErrChildInvalidState)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return sessioncontrol.Handle{}, sessioncontrol.ErrClosed
	}
	if t.blockedRoots[sessionID] {
		return sessioncontrol.Handle{}, fmt.Errorf("child branch for root %q is blocked after an unconfirmed interrupt: %w", sessionID, agent.ErrChildInvalidState)
	}
	if _, exists := t.active[sessionID]; exists {
		return sessioncontrol.Handle{}, fmt.Errorf("root Session %q already has an active Turn: %w", sessionID, sessioncontrol.ErrInvalidHandle)
	}
	t.nextToken++
	handle := sessioncontrol.Handle{SessionID: sessionID, Token: t.nextToken}
	t.active[sessionID] = &executionState{
		handle: handle,
		rootID: sessionID,
		ctx:    ctx,
		done:   make(chan struct{}),
		depth:  0,
	}
	return handle, nil
}

func (t *tree) EndRoot(ctx context.Context, handle sessioncontrol.Handle, interrupted bool) error {
	if handle.Child || handle.SessionID == "" {
		return sessioncontrol.ErrInvalidHandle
	}
	gate := t.gate(handle.SessionID)
	gate.Lock()
	defer gate.Unlock()

	t.mu.Lock()
	exec := t.active[handle.SessionID]
	if exec == nil || exec.handle != handle {
		t.mu.Unlock()
		return sessioncontrol.ErrInvalidHandle
	}
	exec.closing = true
	t.mu.Unlock()

	var resultErr error
	if interrupted && t.config.enabled {
		settleCtx, cancel := independentContext(ctx)
		changed, err := t.repository.UpdateChildBranch(settleCtx, handle.SessionID, agent.ChildBranchRequest{
			Mode:            agent.BranchInterrupt,
			Reason:          "parent_user_interrupt",
			DescendantsOnly: true,
		})
		cancel()
		if err != nil {
			resultErr = fmt.Errorf("interrupt descendants of root %q: %w", handle.SessionID, err)
			t.mu.Lock()
			t.blockedRoots[handle.SessionID] = true
			t.cancelRootExecutionsLocked(handle.SessionID, nil, resultErr)
			t.mu.Unlock()
		} else {
			t.applyChangedExecutions(changed)
		}
	}
	t.mu.Lock()
	if current := t.active[handle.SessionID]; current == exec {
		t.closeExecutionLocked(exec, agent.ChildSnapshot{}, resultErr)
	}
	t.mu.Unlock()
	return resultErr
}

func (t *tree) Next(ctx context.Context) (sessioncontrol.Task, error) {
	if ctx == nil {
		return sessioncontrol.Task{}, context.Canceled
	}
	for {
		if err := ctx.Err(); err != nil {
			return sessioncontrol.Task{}, err
		}
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return sessioncontrol.Task{}, sessioncontrol.ErrClosed
		}
		var exec *executionState
		for len(t.ready) != 0 && exec == nil {
			id := t.ready[0]
			t.ready = t.ready[1:]
			candidate := t.active[id]
			if candidate != nil && candidate.handle.Child && !candidate.settled {
				exec = candidate
			}
		}
		t.mu.Unlock()
		if exec == nil {
			select {
			case <-ctx.Done():
				return sessioncontrol.Task{}, ctx.Err()
			case <-t.notify:
				continue
			}
		}

		ready := true
		stopped := false
		startedAt := time.Now().UTC()
		record, updated, err := t.repository.UpdateChildSession(ctx, exec.handle.SessionID, agent.ChildSessionUpdate{
			ExpectedStates:   []agent.ChildState{agent.ChildQueued},
			ExpectedReady:    &ready,
			State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildWorking},
			ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &stopped},
			StartedAt:        agent.UpdateNullable[time.Time]{Set: true, Value: &startedAt},
		})
		if err != nil {
			t.finishUnconfirmed(exec, "state_persistence_failed", fmt.Errorf("start child execution: %w", err))
			continue
		}
		if !updated {
			t.mu.Lock()
			if current := t.active[exec.handle.SessionID]; current == exec {
				t.closeExecutionLocked(exec, snapshotFromRecord(record, true, "not_started"), nil)
			}
			t.mu.Unlock()
			continue
		}
		t.mu.Lock()
		if current := t.active[exec.handle.SessionID]; current != exec || exec.settled {
			t.mu.Unlock()
			continue
		}
		exec.started = true
		definition := cloneDefinition(exec.definition)
		task := sessioncontrol.Task{
			Handle:     exec.handle,
			Context:    exec.ctx,
			Input:      childInput(record.Agent.Task, record.Agent.Context),
			ToolNames:  definition.Tools,
			SubmitTool: submitToolName,
		}
		t.mu.Unlock()
		return task, nil
	}
}

func (t *tree) FinishIntent(handle sessioncontrol.Handle, toolCallID string) (sessioncontrol.FinishIntent, bool, error) {
	if !handle.Child || toolCallID == "" {
		return sessioncontrol.FinishIntent{}, false, sessioncontrol.ErrInvalidHandle
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	exec := t.active[handle.SessionID]
	if exec == nil || exec.handle != handle || exec.settled {
		return sessioncontrol.FinishIntent{}, false, sessioncontrol.ErrInvalidHandle
	}
	if exec.candidate == nil || exec.candidate.ToolCallID != toolCallID {
		return sessioncontrol.FinishIntent{}, false, nil
	}
	return *exec.candidate, true, nil
}

func (t *tree) Settle(ctx context.Context, handle sessioncontrol.Handle, confirmed *sessioncontrol.FinishIntent, execution agent.Execution, runErr error) error {
	if !handle.Child {
		return sessioncontrol.ErrInvalidHandle
	}
	t.mu.Lock()
	exec := t.active[handle.SessionID]
	if exec == nil || exec.handle != handle || exec.settled {
		t.mu.Unlock()
		return sessioncontrol.ErrInvalidHandle
	}
	if confirmed != nil {
		if exec.candidate == nil || *exec.candidate != *confirmed {
			t.mu.Unlock()
			return fmt.Errorf("confirmed finish intent does not match accepted submission: %w", agent.ErrChildSubmission)
		}
		copy := *confirmed
		confirmed = &copy
	}
	t.mu.Unlock()

	settleCtx, cancel := independentContext(ctx)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		gate := t.gate(exec.rootID)
		gate.Lock()
		record, err := t.repository.GetChildSession(settleCtx, handle.SessionID)
		if err == nil {
			if record.Agent.State == agent.ChildCompleted {
				gate.Unlock()
				t.finishConfirmed(exec, snapshotFromRecord(record, true, ""))
				return nil
			}
			update := settlementUpdate(record, confirmed, execution, runErr)
			var updated bool
			record, updated, err = t.repository.UpdateChildSession(settleCtx, handle.SessionID, update)
			if err == nil && updated {
				gate.Unlock()
				t.finishConfirmed(exec, snapshotFromRecord(record, true, ""))
				return nil
			}
			if err == nil && !updated {
				err = errors.New("child state changed during settlement")
			}
		}
		gate.Unlock()
		lastErr = err
		if settleCtx.Err() != nil {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 50 * time.Millisecond)
		select {
		case <-settleCtx.Done():
			timer.Stop()
			lastErr = settleCtx.Err()
			attempt = 3
		case <-timer.C:
		}
	}
	if lastErr == nil {
		lastErr = errors.New("child state settlement could not be confirmed")
	}
	t.finishUnconfirmed(exec, "state_persistence_failed", lastErr)
	return lastErr
}

func (t *tree) ValidateTools(definitions []tool.Definition) error {
	if !t.config.enabled {
		return nil
	}
	available := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		available[definition.Name] = struct{}{}
	}
	for name, entry := range t.config.definitions {
		for _, toolName := range entry.definition.Tools {
			if _, exists := available[toolName]; !exists {
				return fmt.Errorf("child agent type %q references unavailable tool %q: %w", name, toolName, ErrInvalidConfig)
			}
		}
	}
	return nil
}

func (t *tree) Shutdown(ctx context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	roots := make(map[session.ID]struct{})
	for _, exec := range t.active {
		exec.closing = true
		if exec.rootID != "" {
			roots[exec.rootID] = struct{}{}
		}
	}
	t.mu.Unlock()
	t.signal()

	var resultErr error
	if t.config.enabled {
		for rootID := range roots {
			gate := t.gate(rootID)
			gate.Lock()
			settleCtx, cancel := independentContext(ctx)
			changed, err := t.repository.UpdateChildBranch(settleCtx, rootID, agent.ChildBranchRequest{
				Mode:            agent.BranchInterrupt,
				Reason:          "runtime_shutdown",
				DescendantsOnly: true,
			})
			cancel()
			if err != nil {
				gate.Unlock()
				resultErr = errors.Join(resultErr, err)
				continue
			}
			t.applyChangedExecutions(changed)
			gate.Unlock()
		}
	}
	t.mu.Lock()
	for _, exec := range t.active {
		if !exec.handle.Child {
			continue
		}
		if exec.cancel != nil {
			exec.cancel()
		}
		if !exec.started {
			snapshot := agent.ChildSnapshot{SessionID: exec.handle.SessionID, StateConfirmed: false, ExecutionStopped: boolPointer(true), Reason: "runtime_shutdown"}
			t.closeExecutionLocked(exec, snapshot, resultErr)
		}
	}
	t.mu.Unlock()
	return resultErr
}

func settlementUpdate(record agent.ChildSessionRecord, confirmed *sessioncontrol.FinishIntent, execution agent.Execution, runErr error) agent.ChildSessionUpdate {
	stopped := true
	finished := time.Now().UTC()
	update := agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{record.Agent.State},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &stopped},
		FinishedAt:       agent.UpdateNullable[time.Time]{Set: true, Value: &finished},
	}
	if execution.Outcome.Status != 0 {
		outcome := execution.Outcome
		update.Outcome = agent.UpdateNullable[agent.Outcome]{Set: true, Value: &outcome}
	}
	if record.Agent.State == agent.ChildCanceled || record.Agent.State == agent.ChildInterrupted {
		if runErr != nil {
			diagnostic := diagnosticFor(runErr)
			update.Error = agent.UpdateNullable[agent.ChildDiagnostic]{Set: true, Value: &diagnostic}
		}
		return update
	}
	if runErr == nil && confirmed != nil {
		result := confirmed.Result
		update.State = agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildCompleted}
		update.Result = agent.UpdateNullable[string]{Set: true, Value: &result}
		update.Error = agent.UpdateNullable[agent.ChildDiagnostic]{Set: true}
		return update
	}
	diagnostic := diagnosticFor(runErr)
	if runErr == nil {
		diagnostic = agent.ChildDiagnostic{Code: "missing_submission", Message: "child Turn ended without submit_agent_result"}
	}
	update.State = agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildFailed}
	update.Error = agent.UpdateNullable[agent.ChildDiagnostic]{Set: true, Value: &diagnostic}
	return update
}

func diagnosticFor(err error) agent.ChildDiagnostic {
	if err == nil {
		return agent.ChildDiagnostic{Code: "execution_failed", Message: "child execution failed"}
	}
	code := "execution_failed"
	if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	} else if errors.Is(err, context.Canceled) {
		code = "canceled"
	}
	return agent.ChildDiagnostic{Code: code, Message: err.Error()}
}

func (t *tree) finishConfirmed(exec *executionState, snapshot agent.ChildSnapshot) {
	t.mu.Lock()
	if current := t.active[exec.handle.SessionID]; current == exec {
		t.closeExecutionLocked(exec, snapshot, nil)
	}
	t.mu.Unlock()
}

func (t *tree) finishUnconfirmed(exec *executionState, reason string, err error) {
	snapshot := agent.ChildSnapshot{
		SessionID:        exec.handle.SessionID,
		StateConfirmed:   false,
		ExecutionStopped: boolPointer(true),
		Reason:           reason,
		Error:            &agent.ChildDiagnostic{Code: reason, Message: err.Error()},
	}
	t.mu.Lock()
	if current := t.active[exec.handle.SessionID]; current == exec {
		t.closeExecutionLocked(exec, snapshot, err)
	}
	t.mu.Unlock()
}

func (t *tree) applyChangedExecutions(changed []agent.ChildSessionRecord) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, record := range changed {
		exec := t.active[record.Session.ID]
		if exec == nil || !exec.handle.Child {
			continue
		}
		exec.closing = true
		if exec.cancel != nil {
			exec.cancel()
		}
		if !exec.started {
			t.closeExecutionLocked(exec, snapshotFromRecord(record, true, ""), nil)
		}
	}
}

func (t *tree) cancelRootExecutionsLocked(rootID session.ID, changed map[session.ID]agent.ChildSessionRecord, cause error) {
	for _, exec := range t.active {
		if !exec.handle.Child || exec.rootID != rootID {
			continue
		}
		exec.closing = true
		if exec.cancel != nil {
			exec.cancel()
		}
		if !exec.started {
			snapshot := agent.ChildSnapshot{SessionID: exec.handle.SessionID, StateConfirmed: false, ExecutionStopped: boolPointer(true), Reason: "state_persistence_failed", Error: &agent.ChildDiagnostic{Code: "state_persistence_failed", Message: cause.Error()}}
			if record, exists := changed[exec.handle.SessionID]; exists {
				snapshot = snapshotFromRecord(record, true, "")
			}
			t.closeExecutionLocked(exec, snapshot, cause)
		}
	}
}

func snapshotFromRecord(record agent.ChildSessionRecord, includeResult bool, reason string) agent.ChildSnapshot {
	state := record.Agent.State
	hasResult := record.Agent.State == agent.ChildCompleted && record.Agent.Result != nil
	snapshot := agent.ChildSnapshot{
		SessionID:        record.Session.ID,
		ParentSessionID:  record.Agent.ParentSessionID,
		RootSessionID:    record.Agent.RootSessionID,
		Depth:            record.Agent.Depth,
		AgentType:        record.Agent.AgentType,
		State:            &state,
		StateConfirmed:   true,
		ExecutionStopped: clonePointer(record.Agent.ExecutionStopped),
		HasResult:        &hasResult,
		Reason:           reason,
		Error:            clonePointer(record.Agent.Error),
		PreviousState:    clonePointer(record.Agent.PreviousState),
		StartedAt:        clonePointer(record.Agent.StartedAt),
		FinishedAt:       clonePointer(record.Agent.FinishedAt),
	}
	if record.Agent.InterruptReason != nil {
		snapshot.Reason = *record.Agent.InterruptReason
	}
	if includeResult && hasResult {
		snapshot.Result = clonePointer(record.Agent.Result)
	}
	return snapshot
}

func independentContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, settlementTimeout)
}

func childInput(task, taskContext string) string {
	if taskContext == "" {
		return task
	}
	return task + "\n\nContext:\n" + taskContext
}

func cloneDefinition(value agent.ChildDefinition) agent.ChildDefinition {
	value.Tools = append([]string{}, value.Tools...)
	value.AllowedChildTypes = append([]string{}, value.AllowedChildTypes...)
	return value
}

func boolPointer(value bool) *bool { return &value }
