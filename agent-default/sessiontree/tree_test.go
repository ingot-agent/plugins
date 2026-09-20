package sessiontree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/plugins/agent-default/sessioncontrol"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

type stateScope string

func (s stateScope) Dir() string { return string(s) }

type memoryRepository struct {
	mu      sync.Mutex
	records map[session.ID]agent.ChildSessionRecord
	next    int
}

type failingGetRepository struct {
	*memoryRepository
	err error
}

func (r *failingGetRepository) GetChildSession(context.Context, session.ID) (agent.ChildSessionRecord, error) {
	return agent.ChildSessionRecord{}, r.err
}

func newMemoryRepository() *memoryRepository {
	root := agent.ChildSessionRecord{
		Session: session.Metadata{ID: "c0_root", Title: "root", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Meta: session.Meta{}},
		Agent:   agent.ChildSessionMeta{ChildSessionIDs: []session.ID{}},
	}
	return &memoryRepository{records: map[session.ID]agent.ChildSessionRecord{root.Session.ID: root}}
}

func (r *memoryRepository) CreateChild(_ context.Context, request agent.ChildSessionCreateRequest) (agent.ChildSessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	parent, ok := r.records[request.ParentSessionID]
	if !ok {
		return agent.ChildSessionRecord{}, session.ErrNotFound
	}
	r.next++
	id := session.ID(fmt.Sprintf("c%d_child%d", request.Depth, r.next))
	now := time.Now().UTC()
	meta := request.Agent
	meta.ParentSessionID = request.ParentSessionID
	meta.Depth = request.Depth
	meta.UpdatedAt = now
	record := agent.ChildSessionRecord{Session: session.Metadata{ID: id, Title: request.Title, CreatedAt: now, UpdatedAt: now, Meta: session.Meta{}}, Agent: meta}
	r.records[id] = record
	parent.Agent.ChildSessionIDs = append(parent.Agent.ChildSessionIDs, id)
	r.records[parent.Session.ID] = parent
	return cloneRecord(record), nil
}

func (r *memoryRepository) GetChildSession(_ context.Context, id session.ID) (agent.ChildSessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	if !ok {
		return agent.ChildSessionRecord{}, session.ErrNotFound
	}
	return cloneRecord(record), nil
}

func (r *memoryRepository) ListChildSessions(_ context.Context, parentID session.ID, request agent.ChildSessionPageRequest) (agent.ChildSessionPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.records[parentID]; !ok {
		return agent.ChildSessionPage{}, session.ErrNotFound
	}
	records := []agent.ChildSessionRecord{}
	for _, record := range r.records {
		if record.Agent.ParentSessionID == parentID {
			records = append(records, cloneRecord(record))
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Session.ID < records[j].Session.ID })
	return agent.ChildSessionPage{Records: records}, nil
}

func (r *memoryRepository) UpdateChildSession(_ context.Context, id session.ID, update agent.ChildSessionUpdate) (agent.ChildSessionRecord, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.records[id]
	if !ok {
		return agent.ChildSessionRecord{}, false, session.ErrNotFound
	}
	if len(update.ExpectedStates) != 0 {
		matched := false
		for _, state := range update.ExpectedStates {
			matched = matched || state == record.Agent.State
		}
		if !matched {
			return cloneRecord(record), false, nil
		}
	}
	if update.ExpectedReady != nil && record.Agent.Ready != *update.ExpectedReady {
		return cloneRecord(record), false, nil
	}
	applyMemoryUpdate(&record.Agent, update)
	record.Agent.UpdatedAt = time.Now().UTC()
	r.records[id] = record
	return cloneRecord(record), true, nil
}

func (r *memoryRepository) UpdateChildBranch(_ context.Context, targetID session.ID, request agent.ChildBranchRequest) ([]agent.ChildSessionRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.records[targetID]; !ok {
		return nil, session.ErrNotFound
	}
	changed := []agent.ChildSessionRecord{}
	for id, record := range r.records {
		if record.Agent.Kind != agent.ChildSessionKind || !memoryInBranch(r.records, id, targetID, request.DescendantsOnly) {
			continue
		}
		previous := record.Agent.State
		switch request.Mode {
		case agent.BranchCancel:
			if previous != agent.ChildQueued && previous != agent.ChildWorking {
				continue
			}
			record.Agent.State = agent.ChildCanceled
		case agent.BranchInterrupt:
			if previous == agent.ChildCompleted {
				continue
			}
			record.Agent.State = agent.ChildInterrupted
		}
		if record.Agent.PreviousState == nil {
			record.Agent.PreviousState = &previous
		}
		reason := request.Reason
		record.Agent.InterruptReason = &reason
		if previous == agent.ChildQueued {
			stopped := true
			record.Agent.ExecutionStopped = &stopped
		}
		r.records[id] = record
		changed = append(changed, cloneRecord(record))
	}
	return changed, nil
}

func (*memoryRepository) RecoverChildSessions(context.Context, agent.ChildRecoveryRequest) error {
	return nil
}

type memoryWorkspace struct {
	mu       sync.Mutex
	assigned map[session.ID]workspace.Binding
}

func (w *memoryWorkspace) Assign(_ context.Context, id session.ID, binding workspace.Binding) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.assigned[id] = binding
	return nil
}

type blockingCreateRepository struct {
	*memoryRepository
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingCreateRepository) CreateChild(ctx context.Context, request agent.ChildSessionCreateRequest) (agent.ChildSessionRecord, error) {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-ctx.Done():
		return agent.ChildSessionRecord{}, ctx.Err()
	case <-r.release:
		return r.memoryRepository.CreateChild(ctx, request)
	}
}

func TestSingleTurnChildLifecycleAndPrompt(t *testing.T) {
	root := t.TempDir()
	config := `subagents_config_version = 1
root_allowed_types = ["coder"]

[[agents]]
name = "coder"
description = "implement"
system_prompt = "Complete one task and submit exactly once."
tools = ["echo", "submit_agent_result"]
allowed_child_types = ["reviewer"]

[[agents]]
name = "reviewer"
description = "review"
system_prompt = "Review one task and submit exactly once."
tools = ["submit_agent_result"]
allowed_child_types = []
`
	if err := os.WriteFile(filepath.Join(root, configFileName), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	repository := newMemoryRepository()
	workspaces := &memoryWorkspace{assigned: map[session.ID]workspace.Binding{}}
	exports, cleanup, err := New(context.Background(), Dependencies{
		State:      stateScope(root),
		Repository: ingotabi.Some[agent.ChildSessionRepository](repository),
		Workspace:  ingotabi.Some[workspace.Manager](workspaces),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup(context.Background()) })
	if err := exports.Control.ValidateTools([]tool.Definition{{Name: "echo"}, {Name: submitToolName}}); err != nil {
		t.Fatal(err)
	}

	rootHandle, err := exports.Control.BeginRoot(context.Background(), "c0_root")
	if err != nil {
		t.Fatal(err)
	}
	types, err := exports.Children.Types(context.Background(), execution.Scope{SessionID: "c0_root"})
	if err != nil || len(types) != 1 || types[0].Name != "coder" {
		t.Fatalf("types=%#v err=%v", types, err)
	}
	snapshot, err := exports.Children.CreateChild(context.Background(), execution.Scope{SessionID: "c0_root"}, agent.ChildRequest{
		AgentType: "coder", Task: "implement", Workspace: &workspace.Binding{Root: t.TempDir()}, Wait: false,
	})
	if err != nil || snapshot.SessionID == "" {
		t.Fatalf("create snapshot=%#v err=%v", snapshot, err)
	}
	task, err := exports.Control.Next(context.Background())
	if err != nil || task.Handle.SessionID != snapshot.SessionID {
		t.Fatalf("task=%#v err=%v", task, err)
	}
	if err := exports.Children.SubmitResult(context.Background(), execution.Scope{SessionID: task.Handle.SessionID}, "submit-call", "implemented"); err != nil {
		t.Fatal(err)
	}
	intent, ok, err := exports.Control.FinishIntent(task.Handle, "submit-call")
	if err != nil || !ok || intent.Result != "implemented" {
		t.Fatalf("intent=%#v ok=%v err=%v", intent, ok, err)
	}
	executionResult := agent.Execution{Result: &agent.Result{Output: content.FromText("implemented")}, Outcome: agent.Outcome{Status: agent.OutcomeSucceeded}}
	if err := exports.Control.Settle(task.Context, task.Handle, &intent, executionResult, nil); err != nil {
		t.Fatal(err)
	}
	completed, err := exports.Children.Wait(context.Background(), execution.Scope{SessionID: "c0_root"}, snapshot.SessionID)
	if err != nil || completed.State == nil || *completed.State != agent.ChildCompleted || completed.Result == nil || *completed.Result != "implemented" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	blocks, err := exports.Contributor.Contribute(context.Background(), prompt.Request{SessionID: snapshot.SessionID})
	if err != nil || len(blocks) != 2 || blocks[0].Content[0].Text != childManagementPrompt || blocks[1].Content[0].Text != "Complete one task and submit exactly once." {
		t.Fatalf("blocks=%#v err=%v", blocks, err)
	}
	rootBlocks, err := exports.Contributor.Contribute(context.Background(), prompt.Request{SessionID: "c0_root"})
	if err != nil || len(rootBlocks) != 1 || rootBlocks[0].Content[0].Text != childManagementPrompt {
		t.Fatalf("root blocks=%#v err=%v", rootBlocks, err)
	}
	if err := exports.Control.EndRoot(context.Background(), rootHandle, false); err != nil {
		t.Fatal(err)
	}
}

func TestCreateChildWaitReleasesRootGateBeforeSettlement(t *testing.T) {
	tree, rootHandle := newTestTree(t)
	type createResult struct {
		snapshot agent.ChildSnapshot
		err      error
	}
	workspaceRoot := t.TempDir()
	created := make(chan createResult, 1)
	go func() {
		snapshot, err := tree.CreateChild(context.Background(), execution.Scope{SessionID: "c0_root"}, agent.ChildRequest{
			AgentType: "coder", Task: "implement", Workspace: &workspace.Binding{Root: workspaceRoot}, Wait: true,
		})
		created <- createResult{snapshot: snapshot, err: err}
	}()

	task, err := tree.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.SubmitResult(context.Background(), execution.Scope{SessionID: task.Handle.SessionID}, "submit-call", "implemented"); err != nil {
		t.Fatal(err)
	}
	intent, ok, err := tree.FinishIntent(task.Handle, "submit-call")
	if err != nil || !ok {
		t.Fatalf("intent=%#v ok=%v err=%v", intent, ok, err)
	}
	settled := make(chan error, 1)
	go func() {
		settled <- tree.Settle(task.Context, task.Handle, &intent, agent.Execution{
			Result:  &agent.Result{Output: content.FromText("implemented")},
			Outcome: agent.Outcome{Status: agent.OutcomeSucceeded},
		}, nil)
	}()
	select {
	case err := <-settled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("child settlement blocked while synchronous create was waiting")
	}
	select {
	case result := <-created:
		if result.err != nil || result.snapshot.State == nil || *result.snapshot.State != agent.ChildCompleted || result.snapshot.Result == nil || *result.snapshot.Result != "implemented" {
			t.Fatalf("snapshot=%#v err=%v", result.snapshot, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("synchronous create did not return after settlement")
	}
	if err := tree.EndRoot(context.Background(), rootHandle, false); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownSerializesWithChildCreation(t *testing.T) {
	repository := &blockingCreateRepository{
		memoryRepository: newMemoryRepository(),
		entered:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	workspaces := &memoryWorkspace{assigned: map[session.ID]workspace.Binding{}}
	tree, _ := newTestTreeWithDependencies(t, repository, workspaces)
	type createResult struct {
		snapshot agent.ChildSnapshot
		err      error
	}
	created := make(chan createResult, 1)
	workspaceRoot := t.TempDir()
	go func() {
		snapshot, err := tree.CreateChild(context.Background(), execution.Scope{SessionID: "c0_root"}, agent.ChildRequest{
			AgentType: "coder", Task: "implement", Workspace: &workspace.Binding{Root: workspaceRoot}, Wait: false,
		})
		created <- createResult{snapshot: snapshot, err: err}
	}()
	select {
	case <-repository.entered:
	case <-time.After(time.Second):
		t.Fatal("child creation did not reach persistent creation")
	}

	shutdown := make(chan error, 1)
	go func() { shutdown <- tree.Shutdown(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for {
		tree.mu.Lock()
		closed := tree.closed
		tree.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			close(repository.release)
			t.Fatal("shutdown did not close the scheduler")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-shutdown:
		close(repository.release)
		t.Fatalf("shutdown completed before in-flight child creation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(repository.release)

	var create createResult
	select {
	case create = <-created:
	case <-time.After(time.Second):
		t.Fatal("child creation did not finish")
	}
	if !errors.Is(create.err, agent.ErrChildUnauthorized) || create.snapshot.SessionID == "" {
		t.Fatalf("create snapshot=%#v err=%v", create.snapshot, create.err)
	}
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after child creation released the root gate")
	}
	persisted, err := repository.GetChildSession(context.Background(), create.snapshot.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Agent.State != agent.ChildInterrupted || !persisted.Agent.Ready || persisted.Agent.ExecutionStopped == nil || !*persisted.Agent.ExecutionStopped {
		t.Fatalf("persisted child after shutdown=%#v", persisted.Agent)
	}
}

func TestCancelKeepsStateSeparateFromPhysicalStop(t *testing.T) {
	tree, rootHandle := newTestTree(t)
	snapshot, err := tree.CreateChild(context.Background(), execution.Scope{SessionID: "c0_root"}, agent.ChildRequest{
		AgentType: "coder", Task: "implement", Workspace: &workspace.Binding{Root: t.TempDir()}, Wait: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := tree.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := tree.Cancel(context.Background(), execution.Scope{SessionID: "c0_root"}, snapshot.SessionID)
	if err != nil || canceled.Snapshot.State == nil || *canceled.Snapshot.State != agent.ChildCanceled || canceled.Snapshot.ExecutionStopped == nil || *canceled.Snapshot.ExecutionStopped {
		t.Fatalf("cancel=%#v err=%v", canceled, err)
	}
	if !errors.Is(task.Context.Err(), context.Canceled) {
		t.Fatalf("task context error=%v", task.Context.Err())
	}
	if err := tree.Settle(task.Context, task.Handle, nil, agent.Execution{Outcome: agent.Outcome{Status: agent.OutcomeCanceled}}, context.Canceled); err != nil {
		t.Fatal(err)
	}
	final, err := tree.Check(context.Background(), execution.Scope{SessionID: "c0_root"}, snapshot.SessionID, false)
	if err != nil || final.ExecutionStopped == nil || !*final.ExecutionStopped || final.State == nil || *final.State != agent.ChildCanceled {
		t.Fatalf("final=%#v err=%v", final, err)
	}
	if err := tree.EndRoot(context.Background(), rootHandle, false); err != nil {
		t.Fatal(err)
	}
}

func TestWaitExecutionReturnsSettlementFailure(t *testing.T) {
	settleErr := errors.New("settlement persistence failed")
	stopped := true
	exec := &executionState{
		handle: sessioncontrol.Handle{SessionID: "c1_child", Token: 1, Child: true},
		done:   make(chan struct{}),
		final: agent.ChildSnapshot{
			SessionID:        "c1_child",
			StateConfirmed:   false,
			ExecutionStopped: &stopped,
			Reason:           "state_persistence_failed",
		},
		settleErr: settleErr,
	}
	close(exec.done)

	snapshot, err := (&tree{}).waitExecution(context.Background(), exec)
	if !errors.Is(err, settleErr) {
		t.Fatalf("wait error=%v", err)
	}
	if snapshot.StateConfirmed || snapshot.State != nil || snapshot.ExecutionStopped == nil || !*snapshot.ExecutionStopped {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestWaitExecutionReturnsMetaQueryFailure(t *testing.T) {
	queryErr := errors.New("meta read failed")
	exec := &executionState{
		handle: sessioncontrol.Handle{SessionID: "c1_child", Token: 1, Child: true},
		done:   make(chan struct{}),
		final:  agent.ChildSnapshot{SessionID: "c1_child", StateConfirmed: true},
	}
	close(exec.done)
	tree := &tree{repository: &failingGetRepository{memoryRepository: newMemoryRepository(), err: queryErr}}

	snapshot, err := tree.waitExecution(context.Background(), exec)
	if !errors.Is(err, queryErr) {
		t.Fatalf("wait error=%v", err)
	}
	if snapshot.StateConfirmed || snapshot.State != nil || snapshot.Reason != "query_failed" || snapshot.Error == nil || snapshot.Error.Code != "query_failed" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func newTestTree(t *testing.T) (*tree, sessioncontrol.Handle) {
	t.Helper()
	repository := newMemoryRepository()
	workspaces := &memoryWorkspace{assigned: map[session.ID]workspace.Binding{}}
	return newTestTreeWithDependencies(t, repository, workspaces)
}

func newTestTreeWithDependencies(t *testing.T, repository agent.ChildSessionRepository, workspaces workspace.Manager) (*tree, sessioncontrol.Handle) {
	t.Helper()
	root := t.TempDir()
	config := `subagents_config_version = 1
root_allowed_types = ["coder"]
[[agents]]
name = "coder"
description = "implement"
system_prompt = "submit"
tools = ["submit_agent_result"]
allowed_child_types = []
`
	if err := os.WriteFile(filepath.Join(root, configFileName), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	exports, cleanup, err := New(context.Background(), Dependencies{
		State: stateScope(root), Repository: ingotabi.Some[agent.ChildSessionRepository](repository), Workspace: ingotabi.Some[workspace.Manager](workspaces),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup(context.Background()) })
	tree := exports.Control.(*tree)
	handle, err := tree.BeginRoot(context.Background(), "c0_root")
	if err != nil {
		t.Fatal(err)
	}
	return tree, handle
}

func applyMemoryUpdate(meta *agent.ChildSessionMeta, update agent.ChildSessionUpdate) {
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

func memoryInBranch(records map[session.ID]agent.ChildSessionRecord, id, target session.ID, descendantsOnly bool) bool {
	if id == target {
		return !descendantsOnly
	}
	current := records[id]
	for current.Agent.ParentSessionID != "" {
		if current.Agent.ParentSessionID == target {
			return true
		}
		parent, ok := records[current.Agent.ParentSessionID]
		if !ok {
			return false
		}
		current = parent
	}
	return false
}

func cloneRecord(record agent.ChildSessionRecord) agent.ChildSessionRecord {
	record.Agent.ChildSessionIDs = append([]session.ID{}, record.Agent.ChildSessionIDs...)
	record.Agent.Definition = cloneDefinition(record.Agent.Definition)
	record.Agent.Result = clonePointer(record.Agent.Result)
	record.Agent.Error = clonePointer(record.Agent.Error)
	record.Agent.Outcome = clonePointer(record.Agent.Outcome)
	record.Agent.PreviousState = clonePointer(record.Agent.PreviousState)
	record.Agent.InterruptReason = clonePointer(record.Agent.InterruptReason)
	record.Agent.ExecutionStopped = clonePointer(record.Agent.ExecutionStopped)
	record.Agent.StartedAt = clonePointer(record.Agent.StartedAt)
	record.Agent.FinishedAt = clonePointer(record.Agent.FinishedAt)
	return record
}

var _ agent.ChildSessionRepository = (*memoryRepository)(nil)
var _ workspace.Manager = (*memoryWorkspace)(nil)
