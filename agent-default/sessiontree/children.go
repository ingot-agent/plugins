package sessiontree

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ingot-agent/plugins/agent-default/sessioncontrol"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/session"
)

func (t *tree) Types(ctx context.Context, scope execution.Scope) ([]agent.AgentTypeInfo, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !t.config.enabled {
		return nil, agent.ErrChildUnsupported
	}
	exec, err := t.callerExecution(scope)
	if err != nil {
		return nil, err
	}
	names := t.config.rootAllowed
	if exec.handle.Child {
		names = exec.definition.AllowedChildTypes
	}
	result := make([]agent.AgentTypeInfo, 0, len(names))
	for _, name := range names {
		entry := t.config.definitions[name]
		result = append(result, entry.info)
	}
	return result, nil
}

func (t *tree) CreateChild(ctx context.Context, scope execution.Scope, request agent.ChildRequest) (agent.ChildSnapshot, error) {
	if ctx == nil {
		return agent.ChildSnapshot{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return agent.ChildSnapshot{}, err
	}
	if !t.config.enabled {
		return agent.ChildSnapshot{}, agent.ErrChildUnsupported
	}
	if request.Workspace == nil || request.AgentType == "" || request.Task == "" || !utf8.ValidString(request.Task) || !utf8.ValidString(request.Context) {
		return agent.ChildSnapshot{}, fmt.Errorf("child request requires type, UTF-8 task, and workspace: %w", agent.ErrChildInvalidState)
	}
	if len(request.Task)+len(request.Context) > defaultMaxTaskBytes {
		return agent.ChildSnapshot{}, fmt.Errorf("child task exceeds %d bytes: %w", defaultMaxTaskBytes, agent.ErrChildInvalidState)
	}
	parent, err := t.callerExecution(scope)
	if err != nil {
		return agent.ChildSnapshot{}, err
	}
	allowed := t.config.rootAllowed
	if parent.handle.Child {
		allowed = parent.definition.AllowedChildTypes
	}
	if !slices.Contains(allowed, request.AgentType) {
		return agent.ChildSnapshot{}, fmt.Errorf("agent type %q is not allowed for Session %q: %w", request.AgentType, scope.SessionID, agent.ErrChildUnauthorized)
	}
	definition := t.config.definitions[request.AgentType]
	if parent.depth == ^uint32(0) || parent.depth+1 > defaultMaxDepth {
		return agent.ChildSnapshot{}, fmt.Errorf("child depth limit %d exceeded: %w", defaultMaxDepth, agent.ErrChildCapacity)
	}
	depth := parent.depth + 1
	rootID := parent.rootID

	t.mu.Lock()
	if t.closed || t.blockedRoots[rootID] || t.active[scope.SessionID] != parent || parent.closing || parent.ctx.Err() != nil {
		t.mu.Unlock()
		return agent.ChildSnapshot{}, agent.ErrChildUnauthorized
	}
	if t.childActive+t.reserved >= defaultMaxActive || len(t.ready)+t.reserved >= defaultMaxQueue {
		t.mu.Unlock()
		return agent.ChildSnapshot{}, agent.ErrChildCapacity
	}
	t.reserved++
	t.mu.Unlock()
	releaseReservation := true
	defer func() {
		if !releaseReservation {
			return
		}
		t.mu.Lock()
		if t.reserved > 0 {
			t.reserved--
		}
		t.mu.Unlock()
	}()

	gate := t.gate(rootID)
	gate.Lock()
	gateLocked := true
	defer func() {
		if gateLocked {
			gate.Unlock()
		}
	}()
	t.mu.Lock()
	validParent := !t.closed && !t.blockedRoots[rootID] && t.active[scope.SessionID] == parent && !parent.closing && parent.ctx.Err() == nil
	t.mu.Unlock()
	if !validParent {
		return agent.ChildSnapshot{}, agent.ErrChildUnauthorized
	}
	parentRecord, err := t.repository.GetChildSession(ctx, scope.SessionID)
	if err != nil {
		return agent.ChildSnapshot{}, err
	}
	if parent.handle.Child {
		if parentRecord.Agent.Kind != agent.ChildSessionKind || parentRecord.Agent.RootSessionID != rootID || parentRecord.Agent.Depth != parent.depth || parentRecord.Agent.State != agent.ChildWorking {
			return agent.ChildSnapshot{}, agent.ErrChildUnauthorized
		}
	} else if parentRecord.Agent.Kind == agent.ChildSessionKind {
		return agent.ChildSnapshot{}, agent.ErrChildUnauthorized
	}
	stopped := true
	record, err := t.repository.CreateChild(ctx, agent.ChildSessionCreateRequest{
		ParentSessionID: scope.SessionID,
		Title:           childTitle(request.AgentType, request.Task),
		Depth:           depth,
		Agent: agent.ChildSessionMeta{
			SchemaVersion:    agent.ChildMetaSchemaVersion,
			Kind:             agent.ChildSessionKind,
			ParentSessionID:  scope.SessionID,
			RootSessionID:    rootID,
			ChildSessionIDs:  []session.ID{},
			Depth:            depth,
			AgentType:        request.AgentType,
			DefinitionDigest: definition.digest,
			Definition:       cloneDefinition(definition.definition),
			Task:             request.Task,
			Context:          request.Context,
			Ready:            false,
			State:            agent.ChildQueued,
			ExecutionStopped: &stopped,
		},
	})
	if err != nil {
		return agent.ChildSnapshot{}, err
	}
	if err := t.workspace.Assign(ctx, record.Session.ID, *request.Workspace); err != nil {
		snapshot, settleErr := t.failCreatedChild(ctx, record, "workspace_assignment_failed", err)
		if settleErr != nil {
			return snapshot, errors.Join(err, settleErr)
		}
		return snapshot, err
	}
	ready := true
	record, updated, err := t.repository.UpdateChildSession(ctx, record.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates: []agent.ChildState{agent.ChildQueued},
		ExpectedReady:  boolPointer(false),
		Ready:          agent.UpdateValue[bool]{Set: true, Value: ready},
	})
	if err != nil || !updated {
		cause := err
		if cause == nil {
			cause = errors.New("child ready transition was rejected")
		}
		snapshot, settleErr := t.failCreatedChild(ctx, record, "initialization_failed", cause)
		if settleErr != nil {
			return snapshot, errors.Join(cause, settleErr)
		}
		return snapshot, cause
	}

	t.mu.Lock()
	validParent = !t.closed && !t.blockedRoots[rootID] && t.active[scope.SessionID] == parent && !parent.closing && parent.ctx.Err() == nil
	if !validParent {
		t.mu.Unlock()
		return snapshotFromRecord(record, false, "parent_execution_ended"), agent.ErrChildUnauthorized
	}
	t.nextToken++
	childCtx, cancel := context.WithTimeout(context.Background(), childTaskTimeout)
	exec := &executionState{
		handle:     sessioncontrol.Handle{SessionID: record.Session.ID, Token: t.nextToken, Child: true},
		rootID:     rootID,
		parentID:   scope.SessionID,
		depth:      depth,
		ctx:        childCtx,
		cancel:     cancel,
		done:       make(chan struct{}),
		definition: cloneDefinition(definition.definition),
	}
	t.active[record.Session.ID] = exec
	t.ready = append(t.ready, record.Session.ID)
	t.childActive++
	if t.reserved > 0 {
		t.reserved--
	}
	releaseReservation = false
	t.mu.Unlock()
	gate.Unlock()
	gateLocked = false
	t.signal()

	if !request.Wait {
		return snapshotFromRecord(record, false, "accepted"), nil
	}
	return t.waitExecution(ctx, exec)
}

func (t *tree) Check(ctx context.Context, scope execution.Scope, targetID session.ID, includeResult bool) (agent.ChildSnapshot, error) {
	if ctx == nil {
		return agent.ChildSnapshot{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return agent.ChildSnapshot{}, err
	}
	record, err := t.authorize(ctx, scope, targetID)
	if err != nil {
		return agent.ChildSnapshot{}, err
	}
	if record.Agent.Kind != agent.ChildSessionKind {
		return agent.ChildSnapshot{}, fmt.Errorf("session %q is not a child: %w", targetID, agent.ErrChildInvalidState)
	}
	return snapshotFromRecord(record, includeResult, ""), nil
}

func (t *tree) Wait(ctx context.Context, scope execution.Scope, targetID session.ID) (agent.ChildSnapshot, error) {
	if ctx == nil {
		return agent.ChildSnapshot{}, context.Canceled
	}
	if _, err := t.authorize(ctx, scope, targetID); err != nil {
		return agent.ChildSnapshot{}, err
	}
	t.mu.Lock()
	exec := t.active[targetID]
	t.mu.Unlock()
	if exec == nil || !exec.handle.Child {
		snapshot, err := t.Check(ctx, scope, targetID, true)
		if err != nil {
			return snapshot, err
		}
		if snapshot.State != nil && (*snapshot.State == agent.ChildQueued || *snapshot.State == agent.ChildWorking) {
			snapshot.Reason = "no_process_execution"
		}
		return snapshot, nil
	}
	return t.waitExecution(ctx, exec)
}

func (t *tree) List(ctx context.Context, scope execution.Scope, request agent.ChildrenPageRequest) (agent.ChildrenPage, error) {
	if ctx == nil {
		return agent.ChildrenPage{}, context.Canceled
	}
	parentID := request.ParentSessionID
	if parentID == "" {
		parentID = scope.SessionID
	}
	if _, err := t.authorize(ctx, scope, parentID); err != nil {
		return agent.ChildrenPage{}, err
	}
	page, err := t.repository.ListChildSessions(ctx, parentID, agent.ChildSessionPageRequest{Cursor: request.Cursor, PageSize: request.PageSize})
	if err != nil {
		return agent.ChildrenPage{}, err
	}
	result := agent.ChildrenPage{Children: make([]agent.ChildSnapshot, 0, len(page.Records)), NextCursor: page.NextCursor}
	for _, record := range page.Records {
		result.Children = append(result.Children, snapshotFromRecord(record, false, ""))
	}
	return result, nil
}

func (t *tree) Cancel(ctx context.Context, scope execution.Scope, targetID session.ID) (agent.CancelResult, error) {
	if ctx == nil {
		return agent.CancelResult{}, context.Canceled
	}
	target, err := t.authorize(ctx, scope, targetID)
	if err != nil {
		return agent.CancelResult{}, err
	}
	if target.Agent.Kind != agent.ChildSessionKind {
		return agent.CancelResult{}, fmt.Errorf("session %q is not a child: %w", targetID, agent.ErrChildInvalidState)
	}
	gate := t.gate(target.Agent.RootSessionID)
	gate.Lock()
	changed, err := t.repository.UpdateChildBranch(ctx, targetID, agent.ChildBranchRequest{Mode: agent.BranchCancel, Reason: "explicit_cancel"})
	gate.Unlock()
	if err != nil {
		return agent.CancelResult{}, err
	}
	t.applyChangedExecutions(changed)
	changedTarget := false
	for _, record := range changed {
		if record.Session.ID == targetID {
			target = record
			changedTarget = true
			break
		}
	}
	if !changedTarget {
		target, err = t.repository.GetChildSession(ctx, targetID)
		if err != nil {
			return agent.CancelResult{}, err
		}
	}
	return agent.CancelResult{Snapshot: snapshotFromRecord(target, true, ""), Changed: changedTarget}, nil
}

func (t *tree) SubmitResult(ctx context.Context, scope execution.Scope, toolCallID, result string) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if scope.SessionID == "" || toolCallID == "" || result == "" || !utf8.ValidString(result) || len(result) > defaultMaxResultSize {
		return agent.ErrChildSubmission
	}
	t.mu.Lock()
	exec := t.active[scope.SessionID]
	if exec == nil || !exec.handle.Child || !exec.started || exec.settled || exec.closing || exec.ctx.Err() != nil {
		t.mu.Unlock()
		return agent.ErrChildUnauthorized
	}
	rootID := exec.rootID
	t.mu.Unlock()

	gate := t.gate(rootID)
	gate.Lock()
	defer gate.Unlock()
	record, err := t.repository.GetChildSession(ctx, scope.SessionID)
	if err != nil {
		return err
	}
	if record.Agent.State != agent.ChildWorking {
		return fmt.Errorf("child Session %q is %s: %w", scope.SessionID, record.Agent.State, agent.ErrChildInvalidState)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if current := t.active[scope.SessionID]; current != exec || exec.settled || exec.closing || exec.ctx.Err() != nil {
		return agent.ErrChildUnauthorized
	}
	if exec.candidate != nil {
		return fmt.Errorf("child Session %q already submitted a result: %w", scope.SessionID, agent.ErrChildSubmission)
	}
	exec.candidate = &sessioncontrol.FinishIntent{ToolCallID: toolCallID, Result: result}
	return nil
}

func (t *tree) callerExecution(scope execution.Scope) (*executionState, error) {
	if scope.SessionID == "" {
		return nil, agent.ErrChildUnauthorized
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	exec := t.active[scope.SessionID]
	if exec == nil || exec.settled || exec.closing || exec.ctx == nil || exec.ctx.Err() != nil {
		return nil, agent.ErrChildUnauthorized
	}
	return exec, nil
}

func (t *tree) authorize(ctx context.Context, scope execution.Scope, targetID session.ID) (agent.ChildSessionRecord, error) {
	if !t.config.enabled || t.repository == nil {
		return agent.ChildSessionRecord{}, agent.ErrChildUnsupported
	}
	if targetID == "" {
		return agent.ChildSessionRecord{}, agent.ErrChildUnauthorized
	}
	if _, err := t.callerExecution(scope); err != nil {
		return agent.ChildSessionRecord{}, err
	}
	current := targetID
	var target agent.ChildSessionRecord
	for steps := 0; steps <= defaultMaxDepth+1; steps++ {
		record, err := t.repository.GetChildSession(ctx, current)
		if err != nil {
			return agent.ChildSessionRecord{}, err
		}
		if steps == 0 {
			target = record
		}
		if current == scope.SessionID {
			return target, nil
		}
		if record.Agent.Kind != agent.ChildSessionKind || record.Agent.ParentSessionID == "" {
			break
		}
		current = record.Agent.ParentSessionID
	}
	return agent.ChildSessionRecord{}, agent.ErrChildUnauthorized
}

func (t *tree) waitExecution(ctx context.Context, exec *executionState) (agent.ChildSnapshot, error) {
	select {
	case <-ctx.Done():
		return agent.ChildSnapshot{}, ctx.Err()
	case <-exec.done:
	}
	t.mu.Lock()
	final := cloneSnapshot(exec.final)
	settleErr := exec.settleErr
	t.mu.Unlock()
	if !final.StateConfirmed {
		return final, settleErr
	}
	record, err := t.repository.GetChildSession(ctx, exec.handle.SessionID)
	if err != nil {
		return queryFailureSnapshot(exec.handle.SessionID, err), err
	}
	return snapshotFromRecord(record, true, ""), nil
}

func (t *tree) failCreatedChild(ctx context.Context, record agent.ChildSessionRecord, code string, cause error) (agent.ChildSnapshot, error) {
	stopped := true
	finished := time.Now().UTC()
	diagnostic := agent.ChildDiagnostic{Code: code, Message: cause.Error()}
	settleCtx, cancel := independentContext(ctx)
	defer cancel()
	updated, changed, err := t.repository.UpdateChildSession(settleCtx, record.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{agent.ChildQueued},
		State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildFailed},
		Error:            agent.UpdateNullable[agent.ChildDiagnostic]{Set: true, Value: &diagnostic},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &stopped},
		FinishedAt:       agent.UpdateNullable[time.Time]{Set: true, Value: &finished},
	})
	if err != nil || !changed {
		if err == nil {
			err = errors.New("created child failure state was not persisted")
		}
		return queryFailureSnapshot(record.Session.ID, err), err
	}
	return snapshotFromRecord(updated, false, ""), nil
}

func queryFailureSnapshot(id session.ID, err error) agent.ChildSnapshot {
	return agent.ChildSnapshot{
		SessionID:      id,
		StateConfirmed: false,
		Reason:         "query_failed",
		Error:          &agent.ChildDiagnostic{Code: "query_failed", Message: err.Error()},
	}
}

func childTitle(agentType, task string) string {
	const max = 96
	trimmed := strings.TrimSpace(task)
	if len(trimmed) > max {
		trimmed = trimmed[:max]
	}
	return agentType + ": " + trimmed
}
