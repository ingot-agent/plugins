package sessiontree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/plugins/agent-default/sessioncontrol"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/workspace"
)

func beginSetupRoot(t *testing.T, exports Exports) sessioncontrol.Handle {
	t.Helper()
	root, err := exports.Control.BeginRoot(context.Background(), "c0_root", "c0_root")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func createSetupChild(t *testing.T, exports Exports, parent session.ID, name string) agent.ChildSnapshot {
	t.Helper()
	child, err := exports.Children.CreateChild(context.Background(), execution.Scope{SessionID: parent}, agent.ChildRequest{
		AgentType: name, Task: "test live configuration", Workspace: &workspace.Binding{Root: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	return child
}

func nextSetupChild(t *testing.T, exports Exports, child agent.ChildSnapshot) sessioncontrol.Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	task, err := exports.Control.Next(ctx)
	if err != nil || task.Handle.SessionID != child.SessionID {
		t.Fatalf("next task = %#v, error = %v, want %s", task, err, child.SessionID)
	}
	return task
}

func assertChildPrompt(t *testing.T, exports Exports, id session.ID, expected string) {
	t.Helper()
	blocks, err := exports.Contributor.Contribute(context.Background(), prompt.Request{SessionID: id})
	if err != nil || len(blocks) != 2 || blocks[1].Content[0].Text != expected {
		t.Fatalf("child prompt = %#v, error = %v, want %q", blocks, err, expected)
	}
}

func TestSubagentSetupNewChildrenUseUpdatesAndExistingChildrenKeepDefinitions(t *testing.T) {
	ctx := context.Background()
	exports := setupExports(t, t.TempDir(), true, submitToolName, "read_file", "spawn_agent")
	op := exports.Operations[0]
	answers := customAnswers()
	invokeSetup(t, op, "custom", &answers)
	root := beginSetupRoot(t, exports)
	running := createSetupChild(t, exports, root.SessionID, "leader")
	runningTask := nextSetupChild(t, exports, running)
	queued := createSetupChild(t, exports, root.SessionID, "leader")

	updated := customAnswers()
	fields := updated.Values[1].Value.Items[0].Entries
	fields[1].Value = interaction.StringValue("Updated leader")
	fields[2].Value = interaction.StringValue("New prompt")
	fields[3].Value = interaction.StringsValue([]string{submitToolName})
	fields[4].Value = namesValue(nil)
	invokeSetup(t, op, "custom", &updated)
	types, err := exports.Children.Types(ctx, execution.Scope{SessionID: root.SessionID})
	if err != nil || len(types) != 1 || types[0].Description != "Updated leader" {
		t.Fatalf("live types = %#v, %v", types, err)
	}
	// The old parent's frozen permission still allows a currently defined type.
	nested := createSetupChild(t, exports, running.SessionID, "researcher")
	newChild := createSetupChild(t, exports, root.SessionID, "leader")
	queuedTask := nextSetupChild(t, exports, queued)
	if !slices.Equal(queuedTask.ToolNames, runningTask.ToolNames) {
		t.Fatalf("queued tools changed: %v, want %v", queuedTask.ToolNames, runningTask.ToolNames)
	}
	nextSetupChild(t, exports, nested)
	newTask := nextSetupChild(t, exports, newChild)
	if !slices.Equal(newTask.ToolNames, []string{submitToolName}) {
		t.Fatalf("new child tools = %v", newTask.ToolNames)
	}
	types, err = exports.Children.Types(ctx, execution.Scope{SessionID: newChild.SessionID})
	if err != nil || len(types) != 0 {
		t.Fatalf("new child retained old dispatch permissions: %#v, %v", types, err)
	}
	assertChildPrompt(t, exports, running.SessionID, "Delegate a bounded question.\nSubmit a report.")
	assertChildPrompt(t, exports, queued.SessionID, "Delegate a bounded question.\nSubmit a report.")
	assertChildPrompt(t, exports, newChild.SessionID, "New prompt")
	if err := exports.Control.EndRoot(ctx, root, true); err != nil {
		t.Fatal(err)
	}
}

func TestSubagentSetupDisabledKeepsExistingChildLifecycle(t *testing.T) {
	ctx := context.Background()
	exports := setupExports(t, t.TempDir(), true, submitToolName, "read_file")
	root := beginSetupRoot(t, exports)
	running := createSetupChild(t, exports, root.SessionID, "coder")
	runningTask := nextSetupChild(t, exports, running)
	queued := createSetupChild(t, exports, root.SessionID, "explorer")
	record, err := exports.Control.(*tree).repository.GetChildSession(ctx, running.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	invokeSetup(t, exports.Operations[0], "disabled", nil)
	scope := execution.Scope{SessionID: root.SessionID}
	if _, err := exports.Children.Types(ctx, scope); !errors.Is(err, agent.ErrChildUnsupported) {
		t.Fatalf("disabled types error = %v", err)
	}
	if _, err := exports.Children.CreateChild(ctx, scope, agent.ChildRequest{AgentType: "coder"}); !errors.Is(err, agent.ErrChildUnsupported) {
		t.Fatalf("disabled create error = %v", err)
	}
	if _, err := exports.Control.BeginRoot(ctx, running.SessionID, running.SessionID); !errors.Is(err, agent.ErrChildInvalidState) {
		t.Fatalf("disabled child accepted as root: %v", err)
	}
	assertChildPrompt(t, exports, running.SessionID, record.Agent.Definition.SystemPrompt)
	nextSetupChild(t, exports, queued) // Accepted work still starts while disabled.
	page, err := exports.Children.List(ctx, scope, agent.ChildrenPageRequest{})
	if err != nil || len(page.Children) != 2 {
		t.Fatalf("disabled list = %#v, %v", page, err)
	}
	if _, err := exports.Children.Cancel(ctx, scope, queued.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := exports.Children.SubmitResult(ctx, execution.Scope{SessionID: running.SessionID}, "submit", "done"); err != nil {
		t.Fatal(err)
	}
	intent, ok, err := exports.Control.FinishIntent(runningTask.Handle, "submit")
	if err != nil || !ok {
		t.Fatalf("finish intent = %#v, %v", intent, err)
	}
	if err := exports.Control.Settle(ctx, runningTask.Handle, &intent, agent.Execution{}, nil); err != nil {
		t.Fatal(err)
	}
	completed, err := exports.Children.Wait(ctx, scope, running.SessionID)
	if err != nil || completed.State == nil || *completed.State != agent.ChildCompleted || completed.Result == nil || *completed.Result != "done" {
		t.Fatalf("completed while disabled = %#v, %v", completed, err)
	}
	if err := exports.Control.EndRoot(ctx, root, true); err != nil {
		t.Fatal(err)
	}
}

func TestSubagentSetupDisabledStillInterruptsChildren(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown=%v", shutdown), func(t *testing.T) {
			exports := setupExports(t, t.TempDir(), true, submitToolName)
			root := beginSetupRoot(t, exports)
			child := createSetupChild(t, exports, root.SessionID, "coder")
			invokeSetup(t, exports.Operations[0], "disabled", nil)
			var err error
			if shutdown {
				err = exports.Control.Shutdown(context.Background())
			} else {
				err = exports.Control.EndRoot(context.Background(), root, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			record, err := exports.Control.(*tree).repository.GetChildSession(context.Background(), child.SessionID)
			if err != nil || record.Agent.State != agent.ChildInterrupted {
				t.Fatalf("child survived interrupt while disabled: %#v, %v", record, err)
			}
		})
	}
}

func TestSubagentSetupRemovedTypesCannotBeDispatchedByExistingChildren(t *testing.T) {
	exports := setupExports(t, t.TempDir(), true, submitToolName, "read_file", "spawn_agent")
	answers := customAnswers()
	invokeSetup(t, exports.Operations[0], "custom", &answers)
	root := beginSetupRoot(t, exports)
	leader := createSetupChild(t, exports, root.SessionID, "leader")
	nextSetupChild(t, exports, leader)
	updated := customAnswers()
	updated.Values[1].Value.Items = updated.Values[1].Value.Items[:1]
	updated.Values[1].Value.Items[0].Entries[4].Value = namesValue(nil)
	invokeSetup(t, exports.Operations[0], "custom", &updated)
	scope := execution.Scope{SessionID: leader.SessionID}
	types, err := exports.Children.Types(context.Background(), scope)
	if err != nil || len(types) != 0 {
		t.Fatalf("removed type still advertised: %#v, %v", types, err)
	}
	_, err = exports.Children.CreateChild(context.Background(), scope, agent.ChildRequest{
		AgentType: "researcher", Task: "removed type", Workspace: &workspace.Binding{Root: t.TempDir()},
	})
	if !errors.Is(err, agent.ErrChildUnauthorized) {
		t.Fatalf("removed type create error = %v", err)
	}
}

func TestSubagentSetupEnableFromDisabledDoesNotRepeatRecovery(t *testing.T) {
	root := t.TempDir()
	if err := saveConfiguration(root, []byte("subagents_config_version = 1\n")); err != nil {
		t.Fatal(err)
	}
	repository := &recordingRecoveryRepository{memoryRepository: newMemoryRepository()}
	exports, cleanup, err := New(context.Background(), Dependencies{
		State: stateScope(root), Repository: ingotabi.Some[agent.ChildSessionRepository](repository),
		Workspace: ingotabi.Some[workspace.Manager](&memoryWorkspace{assigned: make(map[session.ID]workspace.Binding)}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup(context.Background()) })
	if repository.recoveries != 1 {
		t.Fatalf("disabled startup recoveries = %d", repository.recoveries)
	}
	if err := exports.Control.ValidateTools([]tool.Definition{{Name: submitToolName}}); err != nil {
		t.Fatal(err)
	}
	handle := beginSetupRoot(t, exports)
	invokeSetup(t, exports.Operations[0], "builtin", nil)
	child := createSetupChild(t, exports, handle.SessionID, "coder")
	nextSetupChild(t, exports, child)
	invokeSetup(t, exports.Operations[0], "disabled", nil)
	invokeSetup(t, exports.Operations[0], "builtin", nil)
	if repository.recoveries != 1 {
		t.Fatalf("live updates repeated startup recovery: %d", repository.recoveries)
	}
	snapshot, err := exports.Children.Check(context.Background(), execution.Scope{SessionID: handle.SessionID}, child.SessionID, false)
	if err != nil || snapshot.State == nil || *snapshot.State != agent.ChildWorking {
		t.Fatalf("live update interrupted child: %#v, %v", snapshot, err)
	}
}

func TestSubagentSetupUpdateDuringCreationKeepsOneDefinitionSnapshot(t *testing.T) {
	exports := setupExports(t, t.TempDir(), true, submitToolName)
	root := beginSetupRoot(t, exports)
	tree := exports.Control.(*tree)
	repository := &blockingCreateRepository{memoryRepository: tree.repository.(*memoryRepository), entered: make(chan struct{}), release: make(chan struct{})}
	tree.repository = repository
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workspaceRoot := t.TempDir()
	type result struct {
		child agent.ChildSnapshot
		err   error
	}
	done := make(chan result, 1)
	go func() {
		child, err := exports.Children.CreateChild(ctx, execution.Scope{SessionID: root.SessionID}, agent.ChildRequest{
			AgentType: "coder", Task: "concurrent creation", Workspace: &workspace.Binding{Root: workspaceRoot},
		})
		done <- result{child, err}
	}()
	select {
	case <-repository.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	invokeSetup(t, exports.Operations[0], "disabled", nil)
	close(repository.release)
	created := <-done
	if created.err != nil {
		t.Fatal(created.err)
	}
	record, err := repository.GetChildSession(ctx, created.child.SessionID)
	if err != nil || record.Agent.DefinitionDigest == "" || record.Agent.Definition.SystemPrompt == "" || !slices.Equal(record.Agent.Definition.Tools, []string{submitToolName}) {
		t.Fatalf("mixed configuration snapshot: %#v, %v", record, err)
	}
	nextSetupChild(t, exports, created.child)
}

func TestSubagentSetupConcurrentDiscoveryObservesCompleteSnapshots(t *testing.T) {
	exports := setupExports(t, t.TempDir(), true, submitToolName, "read_file", "spawn_agent")
	root := beginSetupRoot(t, exports)
	stop := make(chan struct{})
	defer close(stop)
	done := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			types, err := exports.Children.Types(context.Background(), execution.Scope{SessionID: root.SessionID})
			if err != nil || !(len(types) == 3 && types[0].Name == "coder" || len(types) == 1 && types[0].Name == "leader") {
				done <- fmt.Errorf("partial type snapshot: %#v, %v", types, err)
				return
			}
		}
	}()
	for range 20 {
		answers := customAnswers()
		invokeSetup(t, exports.Operations[0], "custom", &answers)
		invokeSetup(t, exports.Operations[0], "builtin", nil)
	}
	// Wait for the reader before the test's cleanup shuts down the tree.
	select {
	case stop <- struct{}{}:
	case err := <-done:
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSubagentSetupFailedSaveDoesNotPublish(t *testing.T) {
	root := t.TempDir()
	exports := setupExports(t, root, true, submitToolName)
	handle := beginSetupRoot(t, exports)
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if probe, err := os.CreateTemp(root, ".write-probe-"); err == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
		t.Skip("filesystem permissions do not prevent writes")
	}
	interacted := false
	_, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: setupChannel{request: func(context.Context, interaction.Request) (interaction.Response, error) {
		interacted = true
		return modeAnswers("disabled"), nil
	}}})
	if !interacted || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected persistence permission failure after interaction, got %v (interacted=%v)", err, interacted)
	}
	types, err := exports.Children.Types(context.Background(), execution.Scope{SessionID: handle.SessionID})
	if err != nil || len(types) != 3 {
		t.Fatalf("failed save changed active types: %#v, %v", types, err)
	}
}
