package sessiontree

import (
	"context"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/prompt"
)

const childManagementPrompt = "A child Session interrupted by runtime_restart cannot be resumed. Spawn a new child Session if its task is still required. When execution_stopped is unknown, do not assume external writers have exited or reuse or clean up that workspace without separate confirmation."

func (t *tree) Contribute(ctx context.Context, request prompt.Request) ([]prompt.Block, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if !t.config.enabled || request.SessionID == "" {
		return nil, nil
	}
	record, err := t.repository.GetChildSession(ctx, request.SessionID)
	if err != nil {
		return nil, err
	}
	blocks := []prompt.Block{{Name: "Child Agent Management", Content: content.FromText(childManagementPrompt)}}
	if record.Agent.Kind != agent.ChildSessionKind {
		return blocks, nil
	}
	return append(blocks, prompt.Block{Name: "Child Agent", Content: content.FromText(record.Agent.Definition.SystemPrompt)}), nil
}
