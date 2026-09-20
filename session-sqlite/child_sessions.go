package sessionsqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/session"
)

const (
	defaultChildPageSize     = 50
	maxChildPageSize         = 100
	restartDiagnosticCode    = "runtime_restarted"
	restartDiagnosticMessage = "previous child execution cannot be resumed; spawn a new child to retry; if execution_stopped is unknown, external writers may still exist"
)

type childCursor struct {
	CreatedAt int64      `json:"created_at"`
	ID        session.ID `json:"id"`
}

func (s *store) CreateChild(ctx context.Context, request agent.ChildSessionCreateRequest) (agent.ChildSessionRecord, error) {
	if request.ParentSessionID == "" || request.Depth == 0 {
		return agent.ChildSessionRecord{}, fmt.Errorf("create child session: missing parent or depth: %w", agent.ErrChildInvalidState)
	}
	childAgent := cloneChildMeta(request.Agent)
	childAgent.SchemaVersion = agent.ChildMetaSchemaVersion
	childAgent.Kind = agent.ChildSessionKind
	childAgent.ParentSessionID = request.ParentSessionID
	childAgent.Depth = request.Depth
	childAgent.ChildSessionIDs = []session.ID{}
	if childAgent.State != agent.ChildQueued || childAgent.RootSessionID == "" || childAgent.AgentType == "" {
		return agent.ChildSessionRecord{}, fmt.Errorf("create child session: invalid initial metadata: %w", agent.ErrChildInvalidState)
	}
	if err := validateChildMeta(childAgent); err != nil {
		return agent.ChildSessionRecord{}, err
	}

	tx, err := s.begin(ctx)
	if err != nil {
		return agent.ChildSessionRecord{}, err
	}
	defer tx.Rollback()
	parent, err := metadataByID(ctx, tx, request.ParentSessionID)
	if err != nil {
		return agent.ChildSessionRecord{}, err
	}
	if parent.ArchivedAt != nil {
		return agent.ChildSessionRecord{}, fmt.Errorf("create child under archived session %q: %w", request.ParentSessionID, session.ErrArchived)
	}
	id, err := s.generateID(request.Depth)
	if err != nil {
		return agent.ChildSessionRecord{}, fmt.Errorf("generate child session ID: %w", err)
	}
	now := s.now().UTC()
	childAgent.UpdatedAt = now
	childMeta, err := metaWithAgent(nil, childAgent)
	if err != nil {
		return agent.ChildSessionRecord{}, err
	}
	encodedChildMeta, err := encodeMeta(childMeta)
	if err != nil {
		return agent.ChildSessionRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sessions (id, title, created_at, updated_at, archived_at, meta)
VALUES (?, ?, ?, ?, NULL, ?)`, string(id), request.Title, encodeTime(now), encodeTime(now), encodedChildMeta); err != nil {
		return agent.ChildSessionRecord{}, fmt.Errorf("create child session: %w", err)
	}

	parentMeta, err := appendParentChild(ctx, tx, parent.Meta, parent.ID, id)
	if err != nil {
		return agent.ChildSessionRecord{}, err
	}
	encodedParentMeta, err := encodeMeta(parentMeta)
	if err != nil {
		return agent.ChildSessionRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET meta = ? WHERE id = ?", encodedParentMeta, string(parent.ID)); err != nil {
		return agent.ChildSessionRecord{}, fmt.Errorf("register child %q on parent %q: %w", id, parent.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return agent.ChildSessionRecord{}, fmt.Errorf("commit child session creation: %w", err)
	}
	metadata := session.Metadata{ID: id, Title: request.Title, CreatedAt: now, UpdatedAt: now, Meta: childMeta}
	return agent.ChildSessionRecord{Session: metadata, Agent: childAgent}, nil
}

func (s *store) GetChildSession(ctx context.Context, id session.ID) (agent.ChildSessionRecord, error) {
	if ctx == nil {
		return agent.ChildSessionRecord{}, context.Canceled
	}
	metadata, err := metadataByID(ctx, s.db, id)
	if err != nil {
		return agent.ChildSessionRecord{}, err
	}
	childMeta, err := childMetaFromMetadata(metadata)
	if err != nil {
		return agent.ChildSessionRecord{}, err
	}
	return agent.ChildSessionRecord{Session: metadata, Agent: childMeta}, nil
}

func (s *store) ListChildSessions(ctx context.Context, parentID session.ID, request agent.ChildSessionPageRequest) (agent.ChildSessionPage, error) {
	if ctx == nil {
		return agent.ChildSessionPage{}, context.Canceled
	}
	if parentID == "" {
		return agent.ChildSessionPage{}, fmt.Errorf("list child sessions: missing parent: %w", session.ErrNotFound)
	}
	pageSize := request.PageSize
	if pageSize == 0 {
		pageSize = defaultChildPageSize
	}
	if pageSize < 1 || pageSize > maxChildPageSize {
		return agent.ChildSessionPage{}, fmt.Errorf("page_size must be in [1,%d]", maxChildPageSize)
	}
	cursor, err := decodeChildCursor(request.Cursor)
	if err != nil {
		return agent.ChildSessionPage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, title, created_at, updated_at, archived_at, meta
FROM sessions
WHERE json_extract(meta, '$.agent.parent_session_id') = ?
  AND (? IS NULL OR created_at > ? OR (created_at = ? AND id > ?))
ORDER BY created_at ASC, id ASC
LIMIT ?`, string(parentID), nullableCursorTime(cursor), cursor.CreatedAt, cursor.CreatedAt, string(cursor.ID), pageSize+1)
	if err != nil {
		return agent.ChildSessionPage{}, fmt.Errorf("list children of session %q: %w", parentID, err)
	}
	defer rows.Close()
	records := make([]agent.ChildSessionRecord, 0, pageSize+1)
	for rows.Next() {
		metadata, err := scanMetadata(rows)
		if err != nil {
			return agent.ChildSessionPage{}, fmt.Errorf("scan child of session %q: %w", parentID, err)
		}
		childMeta, err := childMetaFromMetadata(metadata)
		if err != nil {
			return agent.ChildSessionPage{}, err
		}
		records = append(records, agent.ChildSessionRecord{Session: metadata, Agent: childMeta})
	}
	if err := rows.Err(); err != nil {
		return agent.ChildSessionPage{}, fmt.Errorf("iterate children of session %q: %w", parentID, err)
	}
	page := agent.ChildSessionPage{}
	if len(records) > pageSize {
		records = records[:pageSize]
		last := records[len(records)-1].Session
		page.NextCursor, err = encodeChildCursor(childCursor{CreatedAt: encodeTime(last.CreatedAt), ID: last.ID})
		if err != nil {
			return agent.ChildSessionPage{}, err
		}
	}
	page.Records = records
	return page, nil
}

func (s *store) UpdateChildSession(ctx context.Context, id session.ID, update agent.ChildSessionUpdate) (agent.ChildSessionRecord, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return agent.ChildSessionRecord{}, false, err
	}
	defer tx.Rollback()
	metadata, err := metadataByID(ctx, tx, id)
	if err != nil {
		return agent.ChildSessionRecord{}, false, err
	}
	childMeta, err := childMetaFromMetadata(metadata)
	if err != nil {
		return agent.ChildSessionRecord{}, false, err
	}
	if childMeta.Kind != agent.ChildSessionKind {
		return agent.ChildSessionRecord{}, false, fmt.Errorf("session %q is not a child: %w", id, agent.ErrChildInvalidState)
	}
	if len(update.ExpectedStates) != 0 && !slices.Contains(update.ExpectedStates, childMeta.State) {
		return agent.ChildSessionRecord{Session: metadata, Agent: childMeta}, false, nil
	}
	if update.ExpectedReady != nil && childMeta.Ready != *update.ExpectedReady {
		return agent.ChildSessionRecord{Session: metadata, Agent: childMeta}, false, nil
	}
	if update.State.Set && !validChildTransition(childMeta.State, update.State.Value) {
		return agent.ChildSessionRecord{}, false, fmt.Errorf("transition child session %q from %q to %q: %w", id, childMeta.State, update.State.Value, agent.ErrChildInvalidState)
	}
	applyChildUpdate(&childMeta, update)
	childMeta.UpdatedAt = s.now().UTC()
	if err := validateChildMeta(childMeta); err != nil {
		return agent.ChildSessionRecord{}, false, err
	}
	updatedMeta, err := metaWithAgent(metadata.Meta, childMeta)
	if err != nil {
		return agent.ChildSessionRecord{}, false, err
	}
	encoded, err := encodeMeta(updatedMeta)
	if err != nil {
		return agent.ChildSessionRecord{}, false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET meta = ? WHERE id = ?", encoded, string(id)); err != nil {
		return agent.ChildSessionRecord{}, false, fmt.Errorf("update child session %q: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return agent.ChildSessionRecord{}, false, fmt.Errorf("commit child session %q update: %w", id, err)
	}
	metadata.Meta = updatedMeta
	return agent.ChildSessionRecord{Session: metadata, Agent: childMeta}, true, nil
}

func (s *store) UpdateChildBranch(ctx context.Context, targetID session.ID, request agent.ChildBranchRequest) ([]agent.ChildSessionRecord, error) {
	if request.Mode != agent.BranchCancel && request.Mode != agent.BranchInterrupt {
		return nil, fmt.Errorf("unknown child branch mode %q", request.Mode)
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := metadataByID(ctx, tx, targetID); err != nil {
		return nil, err
	}
	records, err := loadChildBranch(ctx, tx, targetID, request.DescendantsOnly)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	changed := make([]agent.ChildSessionRecord, 0, len(records))
	for _, record := range records {
		meta := cloneChildMeta(record.Agent)
		if !applyBranchUpdate(&meta, request, now) {
			continue
		}
		updatedMeta, err := metaWithAgent(record.Session.Meta, meta)
		if err != nil {
			return nil, err
		}
		encoded, err := encodeMeta(updatedMeta)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET meta = ? WHERE id = ?", encoded, string(record.Session.ID)); err != nil {
			return nil, fmt.Errorf("update child branch session %q: %w", record.Session.ID, err)
		}
		record.Session.Meta = updatedMeta
		record.Agent = meta
		changed = append(changed, record)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit child branch update: %w", err)
	}
	return changed, nil
}

func (s *store) RecoverChildSessions(ctx context.Context, request agent.ChildRecoveryRequest) error {
	if ctx == nil {
		return context.Canceled
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	reason := request.Reason
	if reason == "" {
		reason = "runtime_restart"
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions
SET meta = json_set(
    meta,
    '$.agent.previous_state', json_extract(meta, '$.agent.state'),
    '$.agent.state', 'interrupted',
    '$.agent.interrupt_reason', ?,
    '$.agent.execution_stopped', CASE json_extract(meta, '$.agent.state') WHEN 'queued' THEN json('true') ELSE json('null') END,
	'$.agent.error', CASE
		WHEN json_type(meta, '$.agent.error') IS NULL OR json_type(meta, '$.agent.error') = 'null'
		THEN json_object('code', ?, 'message', ?)
		ELSE json_extract(meta, '$.agent.error')
	END,
    '$.agent.finished_at', COALESCE(json_extract(meta, '$.agent.finished_at'), ?),
    '$.agent.updated_at', ?
)
WHERE json_extract(meta, '$.agent.kind') = ?
  AND json_extract(meta, '$.agent.state') IN ('queued', 'working')`, reason, restartDiagnosticCode, restartDiagnosticMessage, now, now, agent.ChildSessionKind); err != nil {
		return fmt.Errorf("recover active child sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE sessions
SET meta = json_set(meta, '$.agent.execution_stopped', json('null'), '$.agent.updated_at', ?)
WHERE json_extract(meta, '$.agent.kind') = ?
  AND json_extract(meta, '$.agent.state') IN ('failed', 'canceled', 'interrupted')
  AND json_extract(meta, '$.agent.execution_stopped') = 0`, now, agent.ChildSessionKind); err != nil {
		return fmt.Errorf("calibrate stopped child sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit child session recovery: %w", err)
	}
	return nil
}

func appendParentChild(ctx context.Context, tx *sql.Tx, meta session.Meta, parentID, childID session.ID) (session.Meta, error) {
	result := cloneMeta(meta)
	agentObject, present, err := agentObject(result)
	if err != nil {
		return nil, err
	}
	ids, idsPresent, err := childIDsFromObject(agentObject)
	if err != nil {
		return nil, err
	}
	if !present || !idsPresent {
		ids, err = directChildIDs(ctx, tx, parentID)
		if err != nil {
			return nil, err
		}
	} else if !slices.Contains(ids, childID) {
		ids = append(ids, childID)
	}
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	agentObject["child_session_ids"] = encodedIDs
	encodedAgent, err := json.Marshal(agentObject)
	if err != nil {
		return nil, err
	}
	result[agent.AgentMetaNamespace] = encodedAgent
	return result, nil
}

func removeParentChild(meta session.Meta, childID session.ID) (session.Meta, error) {
	result := cloneMeta(meta)
	agentFields, present, err := agentObject(result)
	if err != nil || !present {
		return result, err
	}
	ids, idsPresent, err := childIDsFromObject(agentFields)
	if err != nil || !idsPresent {
		return result, err
	}
	filtered := make([]session.ID, 0, len(ids))
	for _, id := range ids {
		if id != childID {
			filtered = append(filtered, id)
		}
	}
	rawIDs, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	agentFields["child_session_ids"] = rawIDs
	rawAgent, err := json.Marshal(agentFields)
	if err != nil {
		return nil, err
	}
	result[agent.AgentMetaNamespace] = rawAgent
	return result, nil
}

func directChildIDs(ctx context.Context, tx *sql.Tx, parentID session.ID) ([]session.ID, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id FROM sessions
WHERE json_extract(meta, '$.agent.parent_session_id') = ?
ORDER BY created_at ASC, id ASC`, string(parentID))
	if err != nil {
		return nil, fmt.Errorf("load existing children of %q: %w", parentID, err)
	}
	defer rows.Close()
	ids := []session.ID{}
	for rows.Next() {
		var id session.ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func agentObject(meta session.Meta) (map[string]json.RawMessage, bool, error) {
	raw, present := meta[agent.AgentMetaNamespace]
	if !present {
		return map[string]json.RawMessage{}, false, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, true, fmt.Errorf("decode agent metadata object: %w", err)
	}
	if object == nil {
		return nil, true, errors.New("agent metadata must be a JSON object")
	}
	return object, true, nil
}

func childIDsFromObject(object map[string]json.RawMessage) ([]session.ID, bool, error) {
	raw, present := object["child_session_ids"]
	if !present {
		return nil, false, nil
	}
	var ids []session.ID
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, true, fmt.Errorf("decode child_session_ids: %w", err)
	}
	if ids == nil {
		return nil, true, errors.New("child_session_ids must be a JSON array")
	}
	return ids, true, nil
}

func childMetaFromMetadata(metadata session.Metadata) (agent.ChildSessionMeta, error) {
	raw, present := metadata.Meta[agent.AgentMetaNamespace]
	if !present {
		return agent.ChildSessionMeta{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value agent.ChildSessionMeta
	if err := decoder.Decode(&value); err != nil {
		return agent.ChildSessionMeta{}, fmt.Errorf("decode session %q agent metadata: %w", metadata.ID, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return agent.ChildSessionMeta{}, fmt.Errorf("session %q agent metadata contains multiple values", metadata.ID)
	}
	if value.Kind == agent.ChildSessionKind {
		if err := validateChildMeta(value); err != nil {
			return agent.ChildSessionMeta{}, fmt.Errorf("session %q: %w", metadata.ID, err)
		}
	}
	return cloneChildMeta(value), nil
}

func metaWithAgent(meta session.Meta, value agent.ChildSessionMeta) (session.Meta, error) {
	result := cloneMeta(meta)
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode child agent metadata: %w", err)
	}
	result[agent.AgentMetaNamespace] = raw
	return result, nil
}

func validateChildMeta(value agent.ChildSessionMeta) error {
	if value.SchemaVersion != agent.ChildMetaSchemaVersion || value.Kind != agent.ChildSessionKind || value.ParentSessionID == "" || value.RootSessionID == "" || value.Depth == 0 || value.AgentType == "" {
		return fmt.Errorf("invalid child identity metadata: %w", agent.ErrChildInvalidState)
	}
	if !validChildState(value.State) {
		return fmt.Errorf("unknown child state %q: %w", value.State, agent.ErrChildInvalidState)
	}
	if value.State == agent.ChildCompleted && (value.Result == nil || value.ExecutionStopped == nil || !*value.ExecutionStopped) {
		return fmt.Errorf("completed child lacks result or stopped fact: %w", agent.ErrChildInvalidState)
	}
	if value.State != agent.ChildCompleted && value.Result != nil {
		return fmt.Errorf("non-completed child has a result: %w", agent.ErrChildInvalidState)
	}
	return nil
}

func validChildState(state agent.ChildState) bool {
	return state == agent.ChildQueued || state == agent.ChildWorking || state == agent.ChildInterrupted || state == agent.ChildCanceled || state == agent.ChildFailed || state == agent.ChildCompleted
}

func validChildTransition(previous, next agent.ChildState) bool {
	if previous == next {
		return validChildState(next)
	}
	switch previous {
	case agent.ChildQueued:
		return next == agent.ChildWorking || next == agent.ChildInterrupted || next == agent.ChildCanceled || next == agent.ChildFailed
	case agent.ChildWorking:
		return next == agent.ChildInterrupted || next == agent.ChildCanceled || next == agent.ChildFailed || next == agent.ChildCompleted
	case agent.ChildFailed, agent.ChildCanceled:
		return next == agent.ChildInterrupted
	default:
		return false
	}
}

func applyChildUpdate(meta *agent.ChildSessionMeta, update agent.ChildSessionUpdate) {
	if update.Ready.Set {
		meta.Ready = update.Ready.Value
	}
	if update.State.Set {
		meta.State = update.State.Value
	}
	if update.Result.Set {
		meta.Result = clonePointer(update.Result.Value)
	}
	if update.Error.Set {
		meta.Error = clonePointer(update.Error.Value)
	}
	if update.Outcome.Set {
		meta.Outcome = clonePointer(update.Outcome.Value)
	}
	if update.PreviousState.Set {
		meta.PreviousState = clonePointer(update.PreviousState.Value)
	}
	if update.InterruptReason.Set {
		meta.InterruptReason = clonePointer(update.InterruptReason.Value)
	}
	if update.ExecutionStopped.Set {
		meta.ExecutionStopped = clonePointer(update.ExecutionStopped.Value)
	}
	if update.StartedAt.Set {
		meta.StartedAt = clonePointer(update.StartedAt.Value)
	}
	if update.FinishedAt.Set {
		meta.FinishedAt = clonePointer(update.FinishedAt.Value)
	}
}

func applyBranchUpdate(meta *agent.ChildSessionMeta, request agent.ChildBranchRequest, now time.Time) bool {
	previous := meta.State
	switch request.Mode {
	case agent.BranchCancel:
		if previous != agent.ChildQueued && previous != agent.ChildWorking {
			return false
		}
		meta.State = agent.ChildCanceled
	case agent.BranchInterrupt:
		if previous == agent.ChildCompleted {
			return false
		}
		meta.State = agent.ChildInterrupted
	}
	if meta.PreviousState == nil {
		meta.PreviousState = &previous
	}
	if request.Reason != "" {
		reason := request.Reason
		meta.InterruptReason = &reason
	}
	if previous == agent.ChildQueued {
		stopped := true
		meta.ExecutionStopped = &stopped
	}
	if meta.FinishedAt == nil {
		finished := now
		meta.FinishedAt = &finished
	}
	meta.UpdatedAt = now
	return true
}

func loadChildBranch(ctx context.Context, tx *sql.Tx, targetID session.ID, descendantsOnly bool) ([]agent.ChildSessionRecord, error) {
	seed := "SELECT id FROM sessions WHERE id = ?"
	if descendantsOnly {
		seed = "SELECT id FROM sessions WHERE json_extract(meta, '$.agent.parent_session_id') = ?"
	}
	query := `WITH RECURSIVE branch(id) AS (` + seed + `
UNION ALL
SELECT child.id
FROM sessions AS child
JOIN branch AS parent ON json_extract(child.meta, '$.agent.parent_session_id') = parent.id
)
SELECT id, title, created_at, updated_at, archived_at, meta
FROM sessions
WHERE id IN (SELECT id FROM branch)
ORDER BY created_at ASC, id ASC`
	rows, err := tx.QueryContext(ctx, query, string(targetID))
	if err != nil {
		return nil, fmt.Errorf("load child branch %q: %w", targetID, err)
	}
	defer rows.Close()
	records := []agent.ChildSessionRecord{}
	for rows.Next() {
		metadata, err := scanMetadata(rows)
		if err != nil {
			return nil, err
		}
		childMeta, err := childMetaFromMetadata(metadata)
		if err != nil {
			return nil, err
		}
		if childMeta.Kind == agent.ChildSessionKind {
			records = append(records, agent.ChildSessionRecord{Session: metadata, Agent: childMeta})
		}
	}
	return records, rows.Err()
}

func cloneChildMeta(value agent.ChildSessionMeta) agent.ChildSessionMeta {
	if value.ChildSessionIDs != nil {
		value.ChildSessionIDs = append([]session.ID{}, value.ChildSessionIDs...)
	}
	if value.Definition.Tools != nil {
		value.Definition.Tools = append([]string{}, value.Definition.Tools...)
	}
	if value.Definition.AllowedChildTypes != nil {
		value.Definition.AllowedChildTypes = append([]string{}, value.Definition.AllowedChildTypes...)
	}
	value.Result = clonePointer(value.Result)
	value.Error = clonePointer(value.Error)
	value.Outcome = clonePointer(value.Outcome)
	value.PreviousState = clonePointer(value.PreviousState)
	value.InterruptReason = clonePointer(value.InterruptReason)
	value.ExecutionStopped = clonePointer(value.ExecutionStopped)
	value.StartedAt = clonePointer(value.StartedAt)
	value.FinishedAt = clonePointer(value.FinishedAt)
	return value
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func encodeChildCursor(cursor childCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeChildCursor(value string) (childCursor, error) {
	if value == "" {
		return childCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return childCursor{}, fmt.Errorf("decode child cursor: %w", err)
	}
	var cursor childCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.ID == "" || cursor.CreatedAt <= 0 {
		return childCursor{}, errors.New("invalid child cursor")
	}
	return cursor, nil
}

func nullableCursorTime(cursor childCursor) any {
	if cursor.ID == "" {
		return nil
	}
	return cursor.CreatedAt
}

var _ agent.ChildSessionRepository = (*store)(nil)
