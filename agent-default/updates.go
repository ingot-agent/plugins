package agentdefault

import (
	"context"
	"sort"
	"time"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
)

const (
	runtimeUpdateRetention = 128
	progressInterval       = 50 * time.Millisecond
	progressBatchBytes     = 4096
)

// Only slice headers/elements that writers mutate are copied under queueMu.
// Input payloads, messages, tool calls/content and outcomes are immutable once
// installed. The expensive recursive copies for API callers happen after unlock.
func captureTurnState(value agent.TurnState) agent.TurnState {
	value.Turn.Inputs = append([]agent.Input(nil), value.Turn.Inputs...)
	value.Blocks = append([]agent.ExecutionBlock(nil), value.Blocks...)
	for i := range value.Blocks {
		value.Blocks[i].Tools = append([]agent.ToolState(nil), value.Blocks[i].Tools...)
	}
	return value
}

func captureQueue(queue *messageQueue) agent.QueueState {
	value := agent.QueueState{SessionID: queue.id, Paused: queue.paused, PauseReason: queue.pauseReason}
	if queue.active != nil {
		value.ActiveID = queue.active.first.state.ID
	}
	return value
}

func (r *runtime) captureStateLocked() agent.RuntimeState {
	state := agent.RuntimeState{Revision: r.revision}
	for _, item := range r.submissions {
		state.Messages = append(state.Messages, item.state)
	}
	for _, queue := range r.queues {
		value := captureQueue(queue)
		value.Revision = r.revision
		state.Queues = append(state.Queues, value)
		if queue.active != nil {
			state.Turns = append(state.Turns, captureTurnState(queue.active.state))
		}
	}
	for _, id := range r.turnOrder {
		state.Turns = append(state.Turns, r.completedTurns[id])
	}
	return state
}

func cloneRuntimeState(state agent.RuntimeState) agent.RuntimeState {
	state.Messages = append([]agent.MessageState(nil), state.Messages...)
	for i := range state.Messages {
		item := &state.Messages[i]
		item.Request.Input = agent.CloneInput(item.Request.Input)
		if item.HistoryEnd != nil {
			end := *item.HistoryEnd
			item.HistoryEnd = &end
		}
	}
	sort.Slice(state.Messages, func(i, j int) bool { return state.Messages[i].Sequence < state.Messages[j].Sequence })
	state.Turns = append([]agent.TurnState(nil), state.Turns...)
	for i := range state.Turns {
		state.Turns[i] = cloneTurnState(state.Turns[i])
	}
	state.Queues = append([]agent.QueueState(nil), state.Queues...)
	sort.Slice(state.Queues, func(i, j int) bool { return state.Queues[i].SessionID < state.Queues[j].SessionID })
	return state
}

func cloneRuntimeUpdate(update agent.RuntimeUpdate) agent.RuntimeUpdate {
	update.RuntimeState = cloneRuntimeState(update.RuntimeState)
	if update.Progress != nil {
		progress := *update.Progress
		progress.Tools = append([]agent.ToolProgress(nil), progress.Tools...)
		for i := range progress.Tools {
			progress.Tools[i].Content = content.Clone(progress.Tools[i].Content)
		}
		update.Progress = &progress
	}
	return update
}

func (r *runtime) publishLocked(update agent.RuntimeUpdate) {
	r.revision++
	update.Revision = r.revision
	for i := range update.Queues {
		update.Queues[i].Revision = r.revision
	}
	if r.updateCount == len(r.updates) {
		r.updates[r.updateStart] = update
		r.updateStart = (r.updateStart + 1) % len(r.updates)
	} else {
		r.updates[(r.updateStart+r.updateCount)%len(r.updates)] = update
		r.updateCount++
	}
	close(r.changed)
	r.changed = make(chan struct{})
}

// Structural changes flush preceding progress, then capture only the affected
// lifecycle/receipts and queue. nil (or retention eviction) publishes a reset.
func (r *runtime) changedLocked(queue *messageQueue, extra ...*queuedMessage) {
	if queue == nil || r.resetPending {
		r.flushAllProgressLocked()
		r.resetPending = false
		r.publishLocked(agent.RuntimeUpdate{RuntimeState: r.captureStateLocked(), Reset: true})
		return
	}
	state := agent.RuntimeState{Queues: []agent.QueueState{captureQueue(queue)}}
	items := append([]*queuedMessage(nil), extra...)
	turns := make(map[string]bool)
	if run := queue.active; run != nil {
		r.flushProgressLocked(run)
		items = append(items, run.items...)
		state.Turns = append(state.Turns, captureTurnState(run.state))
		turns[run.state.TurnID] = true
	}
	seen := make(map[string]bool)
	for _, item := range items {
		if seen[item.state.ID] {
			continue
		}
		seen[item.state.ID] = true
		state.Messages = append(state.Messages, item.state)
		if id := item.state.TurnID; id != "" && !turns[id] {
			if turn, ok := r.completedTurns[id]; ok {
				state.Turns = append(state.Turns, turn)
				turns[id] = true
			}
		}
	}
	r.publishLocked(agent.RuntimeUpdate{RuntimeState: state})
}

func (r *runtime) queueProgressLocked(run *activeExecution, bytes int) {
	run.progressPending = true
	run.progressBytes += bytes
	if run.progressBytes >= progressBatchBytes || len(run.progressTools) >= 128 || r.closed {
		r.flushProgressLocked(run)
		return
	}
	if run.progressTimer == nil {
		var timer *time.Timer
		timer = time.AfterFunc(progressInterval, func() {
			r.queueMu.Lock()
			defer r.queueMu.Unlock()
			if run.progressTimer == timer {
				r.flushProgressLocked(run)
			}
		})
		run.progressTimer = timer
	}
}

func (r *runtime) flushProgressLocked(run *activeExecution) {
	if run.progressTimer != nil {
		run.progressTimer.Stop()
		run.progressTimer = nil
	}
	if !run.progressPending {
		return
	}
	run.progressPending, run.progressBytes = false, 0
	output, reasoning := run.output.String(), run.reasoning.String()
	progress := &agent.TurnProgress{
		SessionID: run.state.Turn.SessionID, TurnID: run.state.TurnID,
		MessageID: run.lastInput.state.ID, RoundIndex: run.roundIndex,
		OutputDelta: output[run.sentOutput:], ReasoningDelta: reasoning[run.sentReasoning:],
		Tools: run.progressTools,
	}
	run.sentOutput, run.sentReasoning = len(output), len(reasoning)
	run.progressTools = nil
	r.publishLocked(agent.RuntimeUpdate{Progress: progress})
}

func (r *runtime) flushAllProgressLocked() {
	for _, queue := range r.queues {
		if queue.active != nil {
			r.flushProgressLocked(queue.active)
		}
	}
}

func (r *runtime) State(ctx context.Context) (agent.RuntimeState, error) {
	if ctx == nil {
		return agent.RuntimeState{}, agent.ErrInvalidMessage
	}
	if err := ctx.Err(); err != nil {
		return agent.RuntimeState{}, err
	}
	r.queueMu.Lock()
	// Include pending text in the snapshot's revision so delayed SSE batches
	// cannot append text that bootstrap/reconciliation has already seen.
	r.flushAllProgressLocked()
	state := r.captureStateLocked()
	r.queueMu.Unlock()
	return cloneRuntimeState(state), nil
}

func (r *runtime) Watch(ctx context.Context, after uint64) (agent.RuntimeUpdate, error) {
	if ctx == nil {
		return agent.RuntimeUpdate{}, agent.ErrInvalidMessage
	}
	for {
		if err := ctx.Err(); err != nil {
			return agent.RuntimeUpdate{}, err
		}
		r.queueMu.Lock()
		if r.revision > after || after > r.revision {
			var update agent.RuntimeUpdate
			if after == 0 || after > r.revision || after < r.revision-uint64(r.updateCount) {
				r.flushAllProgressLocked()
				update = agent.RuntimeUpdate{RuntimeState: r.captureStateLocked(), Reset: true}
			} else {
				index := (r.updateStart + int(after-(r.revision-uint64(r.updateCount)))) % len(r.updates)
				update = r.updates[index]
			}
			r.queueMu.Unlock()
			return cloneRuntimeUpdate(update), nil
		}
		if r.closed {
			r.queueMu.Unlock()
			return agent.RuntimeUpdate{}, agent.ErrClosed
		}
		changed := r.changed
		r.queueMu.Unlock()
		select {
		case <-ctx.Done():
			return agent.RuntimeUpdate{}, ctx.Err()
		case <-changed:
		}
	}
}
