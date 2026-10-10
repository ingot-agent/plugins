package sessiontree

import (
	"context"
	"maps"
	"testing"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/workspace"
)

func TestNestedTasksKeepOutermostTokenRootSeparateFromLifecycleRoot(t *testing.T) {
	for _, tokenRoot := range []session.ID{"c0_root", "main_source"} {
		t.Run(string(tokenRoot), func(t *testing.T) {
			ctx := context.Background()
			tree, initial := newTestTree(t)
			if err := tree.EndRoot(ctx, initial, false); err != nil {
				t.Fatal(err)
			}
			root, err := tree.BeginRoot(ctx, tokenRoot, "c0_root")
			if err != nil {
				t.Fatal(err)
			}
			defer tree.EndRoot(ctx, root, true)
			config := *tree.config.Load()
			config.definitions = maps.Clone(config.definitions)
			definition := config.definitions["coder"]
			definition.definition.AllowedChildTypes = []string{"coder"}
			config.definitions["coder"] = definition
			tree.config.Store(&config)
			parent := session.ID("c0_root")
			for depth := 1; depth <= 2; depth++ {
				child, err := tree.CreateChild(ctx, execution.Scope{SessionID: parent}, agent.ChildRequest{AgentType: "coder", Task: "task", Workspace: &workspace.Binding{Root: t.TempDir()}})
				if err != nil {
					t.Fatal(err)
				}
				task, err := tree.Next(ctx)
				if err != nil || task.RootSessionID != tokenRoot || task.Handle.SessionID != child.SessionID {
					t.Fatalf("task=%+v err=%v", task, err)
				}
				record, err := tree.repository.GetChildSession(ctx, child.SessionID)
				if err != nil || record.Agent.RootSessionID != "c0_root" {
					t.Fatalf("lifecycle root changed: %+v err=%v", record.Agent, err)
				}
				parent = child.SessionID
			}
		})
	}
}
