package appcomponent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/plugins/app-webui/internal/projection"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/session"
)

// turnRegistry is a Web projection adapter. The Agent owns all queue state,
// cancellation and scheduling; a disconnected Web watcher never stops execution.
type turnRegistry struct {
	appCtx   context.Context
	cancel   context.CancelFunc
	agent    agentController
	events   appbackend.EventSink
	done     chan struct{}
	identity string
}

func newTurnRegistry(ctx context.Context, controller agentController, events appbackend.EventSink) *turnRegistry {
	ctx, cancel := context.WithCancel(ctx)
	r := &turnRegistry{appCtx: ctx, cancel: cancel, agent: controller, events: events, done: make(chan struct{}), identity: rand.Text()}
	go r.watch()
	return r
}
func (r *turnRegistry) stop() { r.cancel() }
func (r *turnRegistry) watch() {
	defer close(r.done)
	var revision uint64
	for {
		update, err := r.agent.Watch(r.appCtx, revision)
		if err != nil {
			return
		}
		revision = update.Revision
		// Publication is advisory. State/bootstrap reads still query the Agent.
		if p := update.Progress; p != nil {
			tools := make([]any, 0, len(p.Tools))
			for _, delta := range p.Tools {
				tools = append(tools, map[string]any{"toolCallId": delta.ToolCallID, "content": projection.Content(delta.Content)})
			}
			_ = r.events.Publish(appbackend.Event{Type: "agent.runtime.progress",
				Scope: &appbackend.Scope{Agent: &appbackend.AgentScope{SessionID: string(p.SessionID), TurnID: p.TurnID, RoundIndex: &p.RoundIndex}},
				Data:  map[string]any{"id": p.MessageID, "revision": revision, "outputDelta": p.OutputDelta, "reasoningDelta": p.ReasoningDelta, "tools": tools}})
		} else {
			_ = r.events.Publish(appbackend.Event{Type: "agent.runtime.updated", Data: map[string]any{"turns": projectSubmissions(update.RuntimeState), "turnQueues": projectQueues(update.RuntimeState)}})
		}
	}
}
func (r *turnRegistry) Submit(ctx context.Context, request agent.MessageRequest) (appbackend.TurnSnapshot, error) {
	// Legacy clients may omit requestId. Supply the existing request key so the
	// accepted submission can be located through the asynchronous state API.
	if request.RequestID == "" {
		request.RequestID = rand.Text()
	}
	if err := r.agent.SendMessage(ctx, request); err != nil {
		return appbackend.TurnSnapshot{}, err
	}
	state, err := r.agent.State(r.appCtx)
	if err != nil {
		return appbackend.TurnSnapshot{}, err
	}
	for _, item := range projectSubmissions(state) {
		if item.RequestID == request.RequestID {
			return item, nil
		}
	}
	return appbackend.TurnSnapshot{}, agent.ErrMessageNotFound
}
func (r *turnRegistry) Snapshots() []appbackend.TurnSnapshot {
	state, _ := r.agent.State(r.appCtx)
	return projectSubmissions(state)
}
func (r *turnRegistry) QueueSnapshots() []appbackend.TurnQueueSnapshot {
	state, _ := r.agent.State(r.appCtx)
	return projectQueues(state)
}
func (r *turnRegistry) SetPaused(id session.ID, paused bool) (appbackend.TurnQueueSnapshot, error) {
	if err := r.agent.SetPaused(r.appCtx, id, paused); err != nil {
		return appbackend.TurnQueueSnapshot{}, err
	}
	for _, q := range r.QueueSnapshots() {
		if q.SessionID == string(id) {
			return q, nil
		}
	}
	return appbackend.TurnQueueSnapshot{SessionID: string(id), Paused: paused}, nil
}
func (r *turnRegistry) Cancel(id string) error { return r.agent.CancelMessage(r.appCtx, id, false) }
func (r *turnRegistry) CancelQueued(id string) error {
	return r.agent.CancelMessage(r.appCtx, id, true)
}
func (r *turnRegistry) CachedHistory(id session.ID) ([]projection.Message, bool) {
	messages, ok, err := r.agent.CachedHistory(r.appCtx, id)
	if err != nil || !ok {
		return nil, false
	}
	return projection.Messages(messages), true
}
func (r *turnRegistry) PauseForArchive(id session.ID) (bool, bool) {
	for _, q := range r.QueueSnapshots() {
		if q.SessionID == string(id) {
			_, err := r.SetPaused(id, true)
			return q.Paused, err == nil
		}
	}
	return false, false
}
func (r *turnRegistry) PrepareDelete(id session.ID) error {
	for _, q := range r.QueueSnapshots() {
		if q.SessionID == string(id) && q.ActiveID != "" {
			return errSessionRunning
		}
	}
	_, err := r.SetPaused(id, true)
	return err
}
func (r *turnRegistry) ForgetSession(id session.ID) { _ = r.agent.ForgetSession(r.appCtx, id) }

func projectQueues(state agent.RuntimeState) []appbackend.TurnQueueSnapshot {
	result := make([]appbackend.TurnQueueSnapshot, 0, len(state.Queues))
	for _, q := range state.Queues {
		result = append(result, appbackend.TurnQueueSnapshot{SessionID: string(q.SessionID), ActiveID: q.ActiveID, Paused: q.Paused, Revision: q.Revision})
	}
	return result
}
func projectSubmissions(state agent.RuntimeState) []appbackend.TurnSnapshot {
	result := make([]appbackend.TurnSnapshot, len(state.Messages))
	byTurn := make(map[string][]int)
	for i, m := range state.Messages {
		result[i] = appbackend.TurnSnapshot{TurnReceipt: appbackend.TurnReceipt{ID: m.ID, SessionID: string(m.Request.SessionID), RequestID: m.Request.RequestID, Status: m.Status}, Revision: state.Revision, Sequence: m.Sequence, Started: m.Started, Input: m.Request.Input.Text, Attachments: projectInputAttachments(m.Request.Input), SDKTurnID: m.TurnID, Persisted: m.Persisted, Blocks: []any{}}
		if m.Error != "" {
			result[i].Error = &appbackend.ErrorDetail{Code: "input_failed", Message: m.Error}
		}
		result[i].HistoryEnd = m.HistoryEnd
		if m.TurnID != "" && m.Persisted {
			byTurn[m.TurnID] = append(byTurn[m.TurnID], i)
		}
	}
	for _, turn := range state.Turns {
		indices := byTurn[turn.TurnID]
		if len(indices) == 0 {
			continue
		}
		current := 0
		for _, block := range turn.Blocks {
			if block.Kind == "input" {
				current = block.InputIndex
				continue
			}
			if current >= len(indices) {
				continue
			}
			target := &result[indices[current]]
			target.Retry = nil
			if block.Retry != nil && turn.Status == "running" {
				target.Retry = &appbackend.TurnRetry{Attempt: block.Retry.Attempt, DelayMS: block.Retry.DelayMS}
			}
			boundary := block.RoundIndex
			if block.Reasoning != "" {
				target.Reasoning += block.Reasoning
				target.Blocks = append(target.Blocks, map[string]any{"id": fmt.Sprintf("round-%d-reasoning", boundary), "kind": "reasoning", "text": block.Reasoning, "boundary": boundary})
			}
			output := block.Output
			var media []projection.Part
			if len(block.Messages) > 0 {
				var text strings.Builder
				for _, part := range block.Messages[0].Content {
					if part.Kind == content.KindText {
						text.WriteString(part.Text)
					} else {
						media = projection.Content(block.Messages[0].Content)
					}
				}
				output = text.String()
			}
			if output != "" || len(media) > 0 {
				target.Output += output
				target.Blocks = append(target.Blocks, map[string]any{"id": fmt.Sprintf("round-%d-output", boundary), "kind": "output", "text": output, "content": media, "boundary": boundary})
			}
			for _, item := range block.Tools {
				target.Blocks = append(target.Blocks, map[string]any{"id": "tool-" + item.Call.ID, "kind": "tool", "call": projection.ToolCall(item.Call), "status": item.Status, "content": projection.Content(item.Content), "error": item.Error})
			}
		}
		for _, index := range indices {
			result[index].HistoryEnd = turn.HistoryEnd
			result[index].Outcome = projection.Outcome(turn.Execution.Outcome)
		}
		if turn.Execution.Result != nil {
			result[indices[len(indices)-1]].Result = map[string]any{"output": projection.Content(turn.Execution.Result.Output)}
		}
	}
	return result
}
func projectInputAttachments(input agent.Input) []appbackend.Attachment {
	result := make([]appbackend.Attachment, 0, len(input.Attachments))
	for _, a := range input.Attachments {
		value := appbackend.Attachment{Kind: "file", Name: a.Name, MIMEType: a.MIMEType, Size: uint64(a.Size), Path: a.Path}
		if a.Media != nil && a.Media.Kind == content.KindImage {
			value.Kind = "image"
			if a.Media.Media.Source.Kind == content.SourceAsset {
				value.AssetID = a.Media.Media.Source.Asset.ID
			}
		}
		result = append(result, value)
	}
	return result
}
func executionStatus(err error) string {
	if err == nil {
		return "succeeded"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "failed"
}
