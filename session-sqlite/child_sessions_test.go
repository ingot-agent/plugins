package sessionsqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/session"
)

func TestChildSessionLifecycleRelationshipsAndDiscovery(t *testing.T) {
	ctx := context.Background()
	created, err := openStore(ctx, filepath.Join(t.TempDir(), "sessions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.close() })

	ids := []session.ID{"root", "child", "grandchild"}
	depths := []uint32{}
	created.generateID = func(depth uint32) (session.ID, error) {
		depths = append(depths, depth)
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	base := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)
	created.now = func() time.Time { return base }

	other := json.RawMessage(`{"preserved":true}`)
	root, err := created.Create(ctx, session.CreateRequest{Title: "Root", Meta: session.Meta{"other": other}})
	if err != nil {
		t.Fatal(err)
	}
	stopped := true
	child, err := created.CreateChild(ctx, agent.ChildSessionCreateRequest{
		ParentSessionID: root.ID,
		Title:           "coder: implement",
		Depth:           1,
		Agent: agent.ChildSessionMeta{
			RootSessionID: root.ID,
			AgentType:     "coder",
			Definition: agent.ChildDefinition{
				SystemPrompt:      "submit once",
				Tools:             []string{"edit_file", "submit_agent_result"},
				AllowedChildTypes: []string{"reviewer"},
			},
			DefinitionDigest: "digest",
			Task:             "implement",
			State:            agent.ChildQueued,
			ExecutionStopped: &stopped,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := depths, []uint32{0, 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("generated depths=%v want=%v", got, want)
	}
	parentRecord, err := created.GetChildSession(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parentRecord.Agent.ChildSessionIDs, []session.ID{child.Session.ID}) {
		t.Fatalf("parent children=%v", parentRecord.Agent.ChildSessionIDs)
	}
	if string(parentRecord.Session.Meta["other"]) != string(other) {
		t.Fatalf("unowned metadata was changed: %s", parentRecord.Session.Meta["other"])
	}
	listed, err := created.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != root.ID {
		t.Fatalf("ordinary List exposed child: %#v", listed)
	}

	ready := true
	child, updated, err := created.UpdateChildSession(ctx, child.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates: []agent.ChildState{agent.ChildQueued},
		Ready:          agent.UpdateValue[bool]{Set: true, Value: ready},
	})
	if err != nil || !updated || !child.Agent.Ready {
		t.Fatalf("ready update=%#v updated=%v err=%v", child, updated, err)
	}
	started := base.Add(time.Minute)
	running := false
	child, updated, err = created.UpdateChildSession(ctx, child.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{agent.ChildQueued},
		ExpectedReady:    &ready,
		State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildWorking},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &running},
		StartedAt:        agent.UpdateNullable[time.Time]{Set: true, Value: &started},
	})
	if err != nil || !updated || child.Agent.State != agent.ChildWorking {
		t.Fatalf("start update=%#v updated=%v err=%v", child, updated, err)
	}
	result := "implemented"
	finished := base.Add(2 * time.Minute)
	succeeded := agent.Outcome{Status: agent.OutcomeSucceeded}
	child, updated, err = created.UpdateChildSession(ctx, child.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{agent.ChildWorking},
		State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildCompleted},
		Result:           agent.UpdateNullable[string]{Set: true, Value: &result},
		Outcome:          agent.UpdateNullable[agent.Outcome]{Set: true, Value: &succeeded},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &stopped},
		FinishedAt:       agent.UpdateNullable[time.Time]{Set: true, Value: &finished},
	})
	if err != nil || !updated || child.Agent.Result == nil || *child.Agent.Result != result {
		t.Fatalf("completion=%#v updated=%v err=%v", child, updated, err)
	}
	page, err := created.ListChildSessions(ctx, root.ID, agent.ChildSessionPageRequest{PageSize: 1})
	if err != nil || len(page.Records) != 1 || page.Records[0].Session.ID != child.Session.ID {
		t.Fatalf("child page=%#v err=%v", page, err)
	}

	grandchild, err := created.CreateChild(ctx, agent.ChildSessionCreateRequest{
		ParentSessionID: child.Session.ID,
		Title:           "reviewer: review",
		Depth:           2,
		Agent: agent.ChildSessionMeta{
			RootSessionID:    root.ID,
			AgentType:        "reviewer",
			DefinitionDigest: "review-digest",
			Definition:       agent.ChildDefinition{SystemPrompt: "review", Tools: []string{"submit_agent_result"}},
			Task:             "review",
			Ready:            true,
			State:            agent.ChildQueued,
			ExecutionStopped: &stopped,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	working := false
	grandchild, updated, err = created.UpdateChildSession(ctx, grandchild.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{agent.ChildQueued},
		State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildWorking},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &working},
	})
	if err != nil || !updated {
		t.Fatalf("start grandchild updated=%v err=%v", updated, err)
	}
	changed, err := created.UpdateChildBranch(ctx, root.ID, agent.ChildBranchRequest{
		Mode:            agent.BranchInterrupt,
		Reason:          "parent_user_interrupt",
		DescendantsOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Session.ID != grandchild.Session.ID || changed[0].Agent.State != agent.ChildInterrupted {
		t.Fatalf("branch changed=%#v", changed)
	}
	completed, err := created.GetChildSession(ctx, child.Session.ID)
	if err != nil || completed.Agent.State != agent.ChildCompleted {
		t.Fatalf("completed intermediary changed=%#v err=%v", completed, err)
	}
	interrupted, err := created.GetChildSession(ctx, grandchild.Session.ID)
	if err != nil || interrupted.Agent.PreviousState == nil || *interrupted.Agent.PreviousState != agent.ChildWorking || interrupted.Agent.ExecutionStopped == nil || *interrupted.Agent.ExecutionStopped {
		t.Fatalf("interrupted grandchild=%#v err=%v", interrupted, err)
	}
}

func TestDeleteMaintainsParentRelationshipAndRejectsBrokenChains(t *testing.T) {
	ctx := context.Background()
	created, err := openStore(ctx, filepath.Join(t.TempDir(), "sessions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.close() })
	ids := []session.ID{"root", "child", "canceled-child"}
	created.generateID = func(uint32) (session.ID, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	root, err := created.Create(ctx, session.CreateRequest{Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	stopped := true
	child, err := created.CreateChild(ctx, agent.ChildSessionCreateRequest{
		ParentSessionID: root.ID,
		Depth:           1,
		Agent:           agent.ChildSessionMeta{RootSessionID: root.ID, AgentType: "reviewer", DefinitionDigest: "d", Definition: agent.ChildDefinition{Tools: []string{"submit_agent_result"}}, State: agent.ChildQueued, ExecutionStopped: &stopped},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Delete(ctx, child.Session.ID); !errors.Is(err, agent.ErrChildInvalidState) {
		t.Fatalf("delete queued child error=%v", err)
	}
	running := false
	child, updated, err := created.UpdateChildSession(ctx, child.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates: []agent.ChildState{agent.ChildQueued},
		State:          agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildWorking},
		ExecutionStopped: agent.UpdateNullable[bool]{
			Set:   true,
			Value: &running,
		},
	})
	if err != nil || !updated {
		t.Fatalf("start child updated=%v err=%v", updated, err)
	}
	if err := created.Delete(ctx, child.Session.ID); !errors.Is(err, agent.ErrChildInvalidState) {
		t.Fatalf("delete working child error=%v", err)
	}
	child, updated, err = created.UpdateChildSession(ctx, child.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates: []agent.ChildState{agent.ChildWorking},
		State:          agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildFailed},
		ExecutionStopped: agent.UpdateNullable[bool]{
			Set:   true,
			Value: &stopped,
		},
	})
	if err != nil || !updated {
		t.Fatalf("fail child updated=%v err=%v", updated, err)
	}
	if err := created.Delete(ctx, root.ID); !errors.Is(err, ErrSessionHasChildren) {
		t.Fatalf("delete parent error=%v", err)
	}
	if err := created.Delete(ctx, child.Session.ID); err != nil {
		t.Fatal(err)
	}
	parent, err := created.GetChildSession(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(parent.Agent.ChildSessionIDs) != 0 {
		t.Fatalf("deleted child retained in parent: %v", parent.Agent.ChildSessionIDs)
	}

	canceledChild, err := created.CreateChild(ctx, agent.ChildSessionCreateRequest{
		ParentSessionID: root.ID,
		Depth:           1,
		Agent:           agent.ChildSessionMeta{RootSessionID: root.ID, AgentType: "reviewer", DefinitionDigest: "d", Definition: agent.ChildDefinition{Tools: []string{"submit_agent_result"}}, State: agent.ChildQueued, ExecutionStopped: &stopped},
	})
	if err != nil {
		t.Fatal(err)
	}
	canceledChild, updated, err = created.UpdateChildSession(ctx, canceledChild.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{agent.ChildQueued},
		State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildWorking},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &running},
	})
	if err != nil || !updated {
		t.Fatalf("start canceled child updated=%v err=%v", updated, err)
	}
	changed, err := created.UpdateChildBranch(ctx, canceledChild.Session.ID, agent.ChildBranchRequest{Mode: agent.BranchCancel})
	if err != nil || len(changed) != 1 || changed[0].Agent.State != agent.ChildCanceled {
		t.Fatalf("cancel child changed=%#v err=%v", changed, err)
	}
	if err := created.Delete(ctx, canceledChild.Session.ID); !errors.Is(err, agent.ErrChildInvalidState) {
		t.Fatalf("delete child before execution stopped error=%v", err)
	}
	_, updated, err = created.UpdateChildSession(ctx, canceledChild.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{agent.ChildCanceled},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &stopped},
	})
	if err != nil || !updated {
		t.Fatalf("stop canceled child updated=%v err=%v", updated, err)
	}
	if err := created.Delete(ctx, canceledChild.Session.ID); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateChildSessionRejectsTerminalReactivation(t *testing.T) {
	ctx := context.Background()
	created, err := openStore(ctx, filepath.Join(t.TempDir(), "sessions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.close() })
	nextID := 0
	created.generateID = func(uint32) (session.ID, error) {
		nextID++
		return session.ID(fmt.Sprintf("session-%d", nextID)), nil
	}
	root, err := created.Create(ctx, session.CreateRequest{Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	stopped := true
	running := false
	createTerminal := func(state agent.ChildState) agent.ChildSessionRecord {
		t.Helper()
		record, err := created.CreateChild(ctx, agent.ChildSessionCreateRequest{
			ParentSessionID: root.ID,
			Depth:           1,
			Agent: agent.ChildSessionMeta{
				RootSessionID: root.ID, AgentType: "coder", DefinitionDigest: "digest",
				Definition: agent.ChildDefinition{Tools: []string{"submit_agent_result"}},
				State:      agent.ChildQueued, ExecutionStopped: &stopped,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		switch state {
		case agent.ChildFailed:
			record, _, err = created.UpdateChildSession(ctx, record.Session.ID, agent.ChildSessionUpdate{
				ExpectedStates: []agent.ChildState{agent.ChildQueued},
				State:          agent.UpdateValue[agent.ChildState]{Set: true, Value: state},
			})
		case agent.ChildCanceled, agent.ChildInterrupted:
			mode := agent.BranchCancel
			if state == agent.ChildInterrupted {
				mode = agent.BranchInterrupt
			}
			var changed []agent.ChildSessionRecord
			changed, err = created.UpdateChildBranch(ctx, record.Session.ID, agent.ChildBranchRequest{Mode: mode})
			if len(changed) == 1 {
				record = changed[0]
			}
		case agent.ChildCompleted:
			record, _, err = created.UpdateChildSession(ctx, record.Session.ID, agent.ChildSessionUpdate{
				ExpectedStates:   []agent.ChildState{agent.ChildQueued},
				State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildWorking},
				ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &running},
			})
			if err == nil {
				result := "done"
				record, _, err = created.UpdateChildSession(ctx, record.Session.ID, agent.ChildSessionUpdate{
					ExpectedStates:   []agent.ChildState{agent.ChildWorking},
					State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: state},
					Result:           agent.UpdateNullable[string]{Set: true, Value: &result},
					ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &stopped},
				})
			}
		default:
			t.Fatalf("unsupported terminal state %q", state)
		}
		if err != nil || record.Agent.State != state {
			t.Fatalf("create terminal state %q record=%#v err=%v", state, record, err)
		}
		return record
	}

	for _, terminal := range []agent.ChildState{agent.ChildFailed, agent.ChildCanceled, agent.ChildInterrupted, agent.ChildCompleted} {
		record := createTerminal(terminal)
		for _, target := range []agent.ChildState{agent.ChildQueued, agent.ChildWorking} {
			executionStopped := stopped
			if target == agent.ChildWorking {
				executionStopped = running
			}
			_, updated, err := created.UpdateChildSession(ctx, record.Session.ID, agent.ChildSessionUpdate{
				ExpectedStates:   []agent.ChildState{terminal},
				State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: target},
				Result:           agent.UpdateNullable[string]{Set: true},
				ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &executionStopped},
			})
			if !errors.Is(err, agent.ErrChildInvalidState) || updated {
				t.Fatalf("transition %s -> %s updated=%v err=%v", terminal, target, updated, err)
			}
			persisted, getErr := created.GetChildSession(ctx, record.Session.ID)
			if getErr != nil || persisted.Agent.State != terminal {
				t.Fatalf("persisted after %s -> %s: state=%s err=%v", terminal, target, persisted.Agent.State, getErr)
			}
		}
	}
}

func TestRecoverChildSessionsAddsRestartDiagnosticAndPreservesStopCertainty(t *testing.T) {
	ctx := context.Background()
	created, err := openStore(ctx, filepath.Join(t.TempDir(), "sessions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.close() })
	ids := []session.ID{"root", "queued", "working", "working-with-error", "failed"}
	created.generateID = func(uint32) (session.ID, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	root, err := created.Create(ctx, session.CreateRequest{Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	stopped := true
	createChild := func() agent.ChildSessionRecord {
		t.Helper()
		record, err := created.CreateChild(ctx, agent.ChildSessionCreateRequest{
			ParentSessionID: root.ID,
			Depth:           1,
			Agent: agent.ChildSessionMeta{
				RootSessionID: root.ID, AgentType: "coder", DefinitionDigest: "digest",
				Definition: agent.ChildDefinition{Tools: []string{"submit_agent_result"}},
				State:      agent.ChildQueued, ExecutionStopped: &stopped,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	queued := createChild()
	working := createChild()
	workingWithError := createChild()
	failed := createChild()
	running := false
	working, updated, err := created.UpdateChildSession(ctx, working.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{agent.ChildQueued},
		State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildWorking},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &running},
	})
	if err != nil || !updated {
		t.Fatalf("start working child updated=%v err=%v", updated, err)
	}
	existing := agent.ChildDiagnostic{Code: "original_failure", Message: "preserve me"}
	workingWithError, updated, err = created.UpdateChildSession(ctx, workingWithError.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{agent.ChildQueued},
		State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildWorking},
		Error:            agent.UpdateNullable[agent.ChildDiagnostic]{Set: true, Value: &existing},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &running},
	})
	if err != nil || !updated {
		t.Fatalf("start diagnosed working child updated=%v err=%v", updated, err)
	}
	failed, updated, err = created.UpdateChildSession(ctx, failed.Session.ID, agent.ChildSessionUpdate{
		ExpectedStates:   []agent.ChildState{agent.ChildQueued},
		State:            agent.UpdateValue[agent.ChildState]{Set: true, Value: agent.ChildFailed},
		Error:            agent.UpdateNullable[agent.ChildDiagnostic]{Set: true, Value: &existing},
		ExecutionStopped: agent.UpdateNullable[bool]{Set: true, Value: &running},
	})
	if err != nil || !updated {
		t.Fatalf("fail child updated=%v err=%v", updated, err)
	}
	if err := created.RecoverChildSessions(ctx, agent.ChildRecoveryRequest{Reason: "runtime_restart"}); err != nil {
		t.Fatal(err)
	}

	queued, err = created.GetChildSession(ctx, queued.Session.ID)
	if err != nil || queued.Agent.State != agent.ChildInterrupted || queued.Agent.PreviousState == nil || *queued.Agent.PreviousState != agent.ChildQueued ||
		queued.Agent.ExecutionStopped == nil || !*queued.Agent.ExecutionStopped || queued.Agent.Error == nil ||
		queued.Agent.Error.Code != restartDiagnosticCode || queued.Agent.Error.Message != restartDiagnosticMessage {
		t.Fatalf("recovered queued=%#v err=%v", queued, err)
	}
	working, err = created.GetChildSession(ctx, working.Session.ID)
	if err != nil || working.Agent.State != agent.ChildInterrupted || working.Agent.PreviousState == nil || *working.Agent.PreviousState != agent.ChildWorking ||
		working.Agent.ExecutionStopped != nil || working.Agent.Error == nil || working.Agent.Error.Code != restartDiagnosticCode ||
		working.Agent.Error.Message != restartDiagnosticMessage {
		t.Fatalf("recovered working=%#v err=%v", working, err)
	}
	workingWithError, err = created.GetChildSession(ctx, workingWithError.Session.ID)
	if err != nil || workingWithError.Agent.State != agent.ChildInterrupted || workingWithError.Agent.ExecutionStopped != nil ||
		workingWithError.Agent.Error == nil || *workingWithError.Agent.Error != existing {
		t.Fatalf("recovered diagnosed working=%#v err=%v", workingWithError, err)
	}
	failed, err = created.GetChildSession(ctx, failed.Session.ID)
	if err != nil || failed.Agent.State != agent.ChildFailed || failed.Agent.ExecutionStopped != nil || failed.Agent.Error == nil || *failed.Agent.Error != existing {
		t.Fatalf("calibrated failed=%#v err=%v", failed, err)
	}
}

func TestSchemaV2MigratesMetaInPlace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.sqlite3")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `
CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, archived_at INTEGER);
CREATE TABLE entries (session_id TEXT NOT NULL, sequence INTEGER NOT NULL, kind TEXT NOT NULL, version INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY (session_id, sequence), FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE);
CREATE TABLE session_workspaces (session_id TEXT PRIMARY KEY, root TEXT NOT NULL, FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE);
INSERT INTO sessions (id, title, created_at, updated_at, archived_at) VALUES ('legacy', 'Legacy', 1, 1, NULL);
PRAGMA user_version = 2;`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	created, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.close() })
	metadata, err := created.Get(ctx, "legacy")
	if err != nil || len(metadata.Meta) != 0 {
		t.Fatalf("migrated metadata=%#v err=%v", metadata, err)
	}
	var version int
	if err := created.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
}
