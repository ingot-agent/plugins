package agentdefault

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/session"
)

const messageRetention = 128

type queuedMessage struct {
	state       agent.MessageState
	fingerprint [32]byte
}

type messageQueue struct {
	id           session.ID
	root         session.ID
	pending      []*queuedMessage
	active       *activeExecution
	paused       bool
	pauseReason  string
	history      []model.Message
	historyValid bool
}

type activeExecution struct {
	queue    *messageQueue
	first    *queuedMessage
	items    []*queuedMessage
	state    agent.TurnState
	cancel   context.CancelFunc
	prepared []preparedInput // execution goroutine only
	// Builders append without recopying the full answer for each model delta.
	output, reasoning strings.Builder
	progressTimer     *time.Timer
	progressPending   bool
	sentOutput        int
	sentReasoning     int
	progressBytes     int
	progressTools     []agent.ToolProgress
	lastInput         *queuedMessage
	roundIndex        int
}

func (r *runtime) initMessages() {
	r.runCtx, r.runCancel = context.WithCancel(context.Background())
	r.queues = make(map[session.ID]*messageQueue)
	r.submissions = make(map[string]*queuedMessage)
	r.requests = make(map[string]*queuedMessage)
	r.completedTurns = make(map[string]agent.TurnState)
	r.changed = make(chan struct{})
	r.identity = rand.Text()
}

func (r *runtime) checkSession(ctx context.Context, id session.ID) error {
	metadata, err := r.sessions.Get(ctx, id)
	if err != nil {
		return err
	}
	if metadata.ArchivedAt != nil {
		return session.ErrArchived
	}
	if r.control != nil {
		return r.control.ValidateRoot(ctx, id)
	}
	return nil
}

func (r *runtime) SendMessage(ctx context.Context, request agent.MessageRequest) error {
	if ctx == nil || request.SessionID == "" || !utf8.ValidString(string(request.SessionID)) || !utf8.ValidString(string(request.RootSessionID)) || !utf8.ValidString(request.RequestID) {
		return agent.ErrInvalidMessage
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := agent.ValidateInput(request.Input); err != nil {
		return err
	}
	if request.RootSessionID == "" {
		request.RootSessionID = request.SessionID
	}
	request.Input = agent.CloneInput(request.Input)
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	fingerprint := sha256.Sum256(raw)
	if err := r.checkSession(ctx, request.SessionID); err != nil {
		return err
	}
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.closed {
		return agent.ErrClosed
	}
	if request.RequestID != "" {
		if previous := r.requests[request.RequestID]; previous != nil {
			if previous.fingerprint != fingerprint {
				return agent.ErrRequestConflict
			}
			return nil
		}
	}
	queue := r.queues[request.SessionID]
	if queue == nil {
		queue = &messageQueue{id: request.SessionID, root: request.RootSessionID}
		r.queues[request.SessionID] = queue
	}
	if queue.root != "" && queue.root != request.RootSessionID {
		return fmt.Errorf("session accounting owner changed: %w", agent.ErrInvalidMessage)
	}
	queue.root = request.RootSessionID
	r.sequence++
	id := fmt.Sprintf("turn-inv-%s-%d", r.identity, r.sequence)
	item := &queuedMessage{state: agent.MessageState{ID: id, Request: request, Sequence: r.sequence, Revision: 1, Status: "queued"}, fingerprint: fingerprint}
	r.submissions[id] = item
	if request.RequestID != "" {
		r.requests[request.RequestID] = item
	}
	queue.pending = append(queue.pending, item)
	r.changedLocked(queue, item)
	r.dispatchLocked(queue)
	return nil
}

func (r *runtime) dispatchLocked(queue *messageQueue) {
	if r.closed || queue.paused || queue.active != nil || len(queue.pending) == 0 {
		return
	}
	first := queue.pending[0]
	queue.pending = queue.pending[1:]
	ctx, cancel := context.WithCancel(r.runCtx)
	run := &activeExecution{queue: queue, first: first, items: []*queuedMessage{first}, cancel: cancel}
	run.state.Turn = agent.Turn{SessionID: first.state.Request.SessionID, RootSessionID: queue.root}
	run.state.Status = "running"
	queue.active = run
	first.state.Status = "running"
	first.state.Started = true
	first.state.Revision++
	r.changedLocked(queue)
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		r.runMessage(ctx, run)
	}()
}

func (r *runtime) runMessage(ctx context.Context, run *activeExecution) {
	var execution agent.Execution
	var runErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("agent execution panic: %v", recovered)
			// executeFrame settles its authoritative outcome before rethrowing.
			r.queueMu.Lock()
			execution = cloneExecution(run.state.Execution)
			r.queueMu.Unlock()
		}
		if run.state.TurnID == "" {
			r.settleMessageTurn(run, execution, runErr)
		}
		r.finishMessage(run, execution, runErr)
	}()
	var prepared preparedInput
	prepared, runErr = r.prepareInput(ctx, run.first.state.Request.Input)
	if runErr != nil {
		return
	}
	prepared.submission = run.first
	run.prepared = []preparedInput{prepared}
	turn := agent.Turn{RootSessionID: run.first.state.Request.RootSessionID, SessionID: run.first.state.Request.SessionID, Inputs: []agent.Input{prepared.input}}
	execution, runErr = r.executeFrame(ctx, turn, func(event agent.StreamEvent) error {
		if err := r.streamProgress(run, event); err != nil {
			return err
		}
		return ctx.Err()
	}, nil, run)
}

func (r *runtime) takePending(ctx context.Context, run *activeExecution) ([]preparedInput, error) {
	r.queueMu.Lock()
	queue := run.queue
	var snapshot []*queuedMessage
	if !queue.paused && queue.active == run {
		snapshot = append(snapshot, queue.pending...)
	}
	r.queueMu.Unlock()
	var prepared []preparedInput
	for _, item := range snapshot {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.queueMu.Lock()
		if queue.paused || r.closed || queue.active != run {
			r.queueMu.Unlock()
			break
		}
		if item.state.Status != "queued" {
			r.queueMu.Unlock()
			continue
		}
		for i, candidate := range queue.pending {
			if candidate == item {
				queue.pending = append(queue.pending[:i], queue.pending[i+1:]...)
				break
			}
		}
		item.state.Status = "running"
		item.state.Started = true
		item.state.TurnID = run.state.TurnID
		item.state.Revision++
		run.items = append(run.items, item)
		r.changedLocked(queue)
		r.queueMu.Unlock()
		value, err := r.prepareInput(ctx, item.state.Request.Input)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var attachment *attachmentError
			if !errors.As(err, &attachment) {
				return nil, err
			}
			r.queueMu.Lock()
			item.state.Status, item.state.Error = "failed", err.Error()
			item.state.Revision++
			queue.paused, queue.pauseReason = true, "attachment"
			r.changedLocked(queue)
			r.queueMu.Unlock()
			break
		}
		value.submission = item
		prepared = append(prepared, value)
	}
	return prepared, ctx.Err()
}

func (r *runtime) beginMessageTurn(run *activeExecution, turnID string, turn agent.Turn) {
	turn = agent.CloneTurn(turn)
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	run.state.TurnID, run.state.Turn = turnID, turn
	run.first.state.TurnID = turnID
	run.first.state.Revision++
	r.changedLocked(run.queue)
}

func (r *runtime) inputPersisted(run *activeExecution, input preparedInput) {
	messages := cloneMessages(input.messages)
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	r.flushProgressLocked(run)
	if input.submission != nil {
		run.lastInput = input.submission
		input.submission.state.Persisted = true
		input.submission.state.Revision++
	}
	run.state.Blocks = append(run.state.Blocks, agent.ExecutionBlock{Kind: "input", InputIndex: input.inputIndex, Messages: messages})
	r.changedLocked(run.queue)
}

func (r *runtime) roundStarted(run *activeExecution, index int) {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	r.flushProgressLocked(run)
	run.output.Reset()
	run.reasoning.Reset()
	run.sentOutput, run.sentReasoning, run.roundIndex = 0, 0, index
	run.state.Blocks = append(run.state.Blocks, agent.ExecutionBlock{Kind: "round", RoundIndex: index})
	r.changedLocked(run.queue)
}

func (r *runtime) streamProgress(run *activeExecution, event agent.StreamEvent) error {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if len(run.state.Blocks) == 0 {
		return nil
	}
	block := &run.state.Blocks[len(run.state.Blocks)-1]
	if block.Kind != "round" {
		return nil
	}
	if event.Kind == agent.StreamRetry {
		output, outputOK := retractStreamText(run.output.String(), event.DiscardOutputBytes)
		reasoning, reasoningOK := retractStreamText(run.reasoning.String(), event.DiscardReasoningBytes)
		if !outputOK || !reasoningOK || event.Attempt < 2 || event.RetryDelay < 0 || event.TextDelta != "" {
			return errors.New("invalid agent stream retry")
		}
		// Publish preceding increments before the authoritative replacement. Reset
		// the sent offsets too: the next batch must start after the retained prefix.
		r.flushProgressLocked(run)
		run.output.Reset()
		run.output.WriteString(output)
		run.reasoning.Reset()
		run.reasoning.WriteString(reasoning)
		run.sentOutput, run.sentReasoning = len(output), len(reasoning)
		block.Output, block.Reasoning = output, reasoning
		block.Retry = &agent.RetryState{Attempt: event.Attempt, DelayMS: event.RetryDelay.Milliseconds()}
		r.changedLocked(run.queue)
		return nil
	}
	if event.Kind == agent.StreamRecovered {
		block.Retry = nil
		r.changedLocked(run.queue)
		return nil
	}
	wasRetrying := block.Retry != nil
	if event.Kind == agent.StreamReasoningDelta {
		run.reasoning.WriteString(event.TextDelta)
		block.Reasoning = run.reasoning.String()
	} else if event.Kind == agent.StreamOutputDelta {
		run.output.WriteString(event.TextDelta)
		block.Output = run.output.String()
	} else {
		return fmt.Errorf("unknown agent stream event kind %d", event.Kind)
	}
	if event.TextDelta != "" {
		r.queueProgressLocked(run, len(event.TextDelta))
		if wasRetrying {
			block.Retry = nil
			r.changedLocked(run.queue)
		}
	}
	return nil
}

func retractStreamText(value string, count int) (string, bool) {
	if count < 0 || count > len(value) {
		return "", false
	}
	prefix := value[:len(value)-count]
	return prefix, utf8.ValidString(prefix)
}

func (r *runtime) roundFinished(run *activeExecution, result agent.RoundResult) {
	messages := append([]model.Message{cloneMessage(result.Decision)}, cloneMessages(result.ToolMessages)...)
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	block := &run.state.Blocks[len(run.state.Blocks)-1]
	block.Messages = messages
	r.changedLocked(run.queue)
}

// Called before TurnFinished is emitted, independently of observer delivery.
func (r *runtime) settleMessageTurn(run *activeExecution, execution agent.Execution, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	history, historyErr := r.loadHistory(ctx, run.state.Turn.SessionID)
	execution = cloneExecution(execution)
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	run.state.Execution = execution
	run.state.Status, run.state.Error = messageStatus(err), errorText(err)
	if historyErr == nil {
		end := len(history)
		run.state.HistoryEnd = &end
		run.queue.history, run.queue.historyValid = history, true
	} else {
		run.queue.paused, run.queue.pauseReason = true, "history"
	}
	r.changedLocked(run.queue)
}

func messageStatus(err error) string {
	if err == nil {
		return "succeeded"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "failed"
}

func (r *runtime) finishMessage(run *activeExecution, execution agent.Execution, err error) {
	run.cancel()
	execution = cloneExecution(execution)
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	r.flushProgressLocked(run)
	queue := run.queue
	if err != nil {
		queue.paused = true
		if queue.pauseReason == "" {
			queue.pauseReason = "execution"
		}
		var attachment *attachmentError
		if errors.As(err, &attachment) {
			queue.pauseReason = "attachment"
		}
	}
	for _, item := range run.items {
		item.state.HistoryEnd = run.state.HistoryEnd
		if item.state.Status == "running" {
			item.state.Status, item.state.Error = messageStatus(err), errorText(err)
			item.state.Revision++
		}
		r.settled = append(r.settled, item.state.ID)
	}
	if run.state.TurnID != "" {
		run.state.Execution, run.state.Status, run.state.Error = execution, messageStatus(err), errorText(err)
		r.completedTurns[run.state.TurnID] = captureTurnState(run.state)
		r.turnOrder = append(r.turnOrder, run.state.TurnID)
	}
	r.pruneMessagesLocked()
	queue.active = nil
	r.changedLocked(queue, run.items...)
	r.dispatchLocked(queue)
}

func cloneExecution(value agent.Execution) agent.Execution {
	return agent.Execution{Result: cloneResult(value.Result), Outcome: cloneOutcome(value.Outcome)}
}
func cloneTurnState(value agent.TurnState) agent.TurnState {
	value.Turn = agent.CloneTurn(value.Turn)
	value.Execution = cloneExecution(value.Execution)
	value.Blocks = append([]agent.ExecutionBlock(nil), value.Blocks...)
	for i := range value.Blocks {
		if retry := value.Blocks[i].Retry; retry != nil {
			owned := *retry
			value.Blocks[i].Retry = &owned
		}
		value.Blocks[i].Messages = cloneMessages(value.Blocks[i].Messages)
		value.Blocks[i].Tools = append([]agent.ToolState(nil), value.Blocks[i].Tools...)
		for j := range value.Blocks[i].Tools {
			item := &value.Blocks[i].Tools[j]
			item.Call = cloneCall(item.Call)
			item.Content = content.Clone(item.Content)
		}
	}
	if value.HistoryEnd != nil {
		end := *value.HistoryEnd
		value.HistoryEnd = &end
	}
	return value
}

func (r *runtime) SetPaused(ctx context.Context, id session.ID, paused bool) error {
	if ctx == nil {
		return agent.ErrInvalidMessage
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if r.closed {
		return agent.ErrClosed
	}
	queue := r.queues[id]
	if queue == nil {
		queue = &messageQueue{id: id}
		r.queues[id] = queue
	}
	queue.paused, queue.pauseReason = paused, ""
	if paused {
		queue.pauseReason = "user"
	}
	r.changedLocked(queue)
	r.dispatchLocked(queue)
	return nil
}

func (r *runtime) CancelMessage(ctx context.Context, id string, onlyQueued bool) error {
	if ctx == nil {
		return agent.ErrInvalidMessage
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	item := r.submissions[id]
	if item == nil {
		return agent.ErrMessageNotFound
	}
	queue := r.queues[item.state.Request.SessionID]
	if item.state.Status == "queued" {
		for i, candidate := range queue.pending {
			if candidate == item {
				queue.pending = append(queue.pending[:i], queue.pending[i+1:]...)
				break
			}
		}
		item.state.Status = "canceled"
		item.state.Revision++
		r.settled = append(r.settled, id)
		r.pruneMessagesLocked()
	} else if item.state.Status == "running" {
		if onlyQueued {
			return agent.ErrMessageStarted
		}
		queue.paused, queue.pauseReason = true, "stopped"
		queue.active.cancel()
	} else {
		// Several receipts can refer to the same lifecycle. A second stop after
		// settlement is a no-op, not a new single-receipt lifecycle snapshot.
		return nil
	}
	r.changedLocked(queue, item)
	return nil
}

func (r *runtime) Stop(ctx context.Context, id session.ID) error {
	if ctx == nil {
		return agent.ErrInvalidMessage
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if queue := r.queues[id]; queue != nil {
		queue.paused, queue.pauseReason = true, "stopped"
		if queue.active != nil {
			queue.active.cancel()
		}
		r.changedLocked(queue)
	}
	return nil
}

func (r *runtime) ForgetSession(ctx context.Context, id session.ID) error {
	if ctx == nil {
		return agent.ErrInvalidMessage
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if queue := r.queues[id]; queue != nil && queue.active != nil {
		return agent.ErrSessionRunning
	}
	delete(r.queues, id)
	for key, item := range r.submissions {
		if item.state.Request.SessionID == id {
			delete(r.requests, item.state.Request.RequestID)
			delete(r.submissions, key)
		}
	}
	for key, turn := range r.completedTurns {
		if turn.Turn.SessionID == id {
			delete(r.completedTurns, key)
		}
	}
	remaining := r.turnOrder[:0]
	for _, key := range r.turnOrder {
		if _, ok := r.completedTurns[key]; ok {
			remaining = append(remaining, key)
		}
	}
	r.turnOrder = remaining
	r.changedLocked(nil)
	return nil
}

var _ agent.Control = (*runtime)(nil)

func (r *runtime) cacheHistory(queue *messageQueue, history []model.Message) {
	history = cloneMessages(history)
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	queue.history, queue.historyValid = history, true
	r.changedLocked(queue)
}

func (r *runtime) CachedHistory(ctx context.Context, id session.ID) ([]model.Message, bool, error) {
	if ctx == nil {
		return nil, false, agent.ErrInvalidMessage
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		r.queueMu.Lock()
		queue := r.queues[id]
		if queue == nil || queue.active == nil && len(queue.pending) == 0 {
			r.queueMu.Unlock()
			return nil, false, nil
		}
		if queue.historyValid {
			history := queue.history // Immutable once published; detach outside queueMu.
			r.queueMu.Unlock()
			return cloneMessages(history), true, nil
		}
		if queue.active == nil {
			r.queueMu.Unlock()
			return nil, false, nil
		}
		changed := r.changed
		r.queueMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-changed:
		}
	}
}

func (r *runtime) toolProgress(run *activeExecution, ctx context.Context, detail observation.Detail) {
	switch detail.(type) {
	case observation.ToolStarted, observation.ToolProgress, observation.ToolFinished:
	default:
		return
	}
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	if len(run.state.Blocks) == 0 {
		return
	}
	block := &run.state.Blocks[len(run.state.Blocks)-1]
	if block.Kind != "round" {
		return
	}
	correlation, _ := observation.CorrelationFromContext(ctx)
	if started, ok := detail.(observation.ToolStarted); ok {
		block.Tools = append(block.Tools, agent.ToolState{Call: cloneCall(started.Call), Status: "running"})
		r.changedLocked(run.queue)
		return
	}
	for i := range block.Tools {
		item := &block.Tools[i]
		if item.Call.ID != correlation.ToolCallID {
			continue
		}
		switch value := detail.(type) {
		case observation.ToolProgress:
			delta := content.Clone(value.Progress.Content)
			item.Content = append(item.Content, delta...)
			run.progressTools = append(run.progressTools, agent.ToolProgress{ToolCallID: item.Call.ID, Content: delta})
			size := 0
			for _, part := range delta {
				size += len(part.Text) + len(part.Media.Source.Data)
			}
			r.queueProgressLocked(run, size)
			return
		case observation.ToolFinished:
			item.Status = "succeeded"
			if value.Status == observation.StatusFailed {
				item.Status = "failed"
			} else if value.Status == observation.StatusCanceled {
				item.Status = "canceled"
			}
			item.Error = value.Error
			if value.Result != nil {
				item.Content = content.Clone(value.Result.Content)
			}
		}
		r.changedLocked(run.queue)
		break
	}
}

func (r *runtime) pruneMessagesLocked() {
	for len(r.settled) > messageRetention {
		id := r.settled[0]
		r.settled = r.settled[1:]
		if item := r.submissions[id]; item != nil {
			// Inputs of a retained lifecycle stay together; block associations
			// use input positions even when a Turn contains many submissions.
			if _, retained := r.completedTurns[item.state.TurnID]; retained {
				continue
			}
			delete(r.requests, item.state.Request.RequestID)
			delete(r.submissions, id)
			r.resetPending = true
		}
	}
	for len(r.turnOrder) > messageRetention {
		for id, item := range r.submissions {
			if item.state.TurnID == r.turnOrder[0] {
				delete(r.requests, item.state.Request.RequestID)
				delete(r.submissions, id)
			}
		}
		delete(r.completedTurns, r.turnOrder[0])
		r.resetPending = true
		r.turnOrder = r.turnOrder[1:]
	}
}

func (r *runtime) admitInputState(run *activeExecution, inputs []preparedInput) {
	r.queueMu.Lock()
	defer r.queueMu.Unlock()
	for i := range inputs {
		if inputs[i].submission == run.first {
			run.state.Turn.Inputs[0] = agent.CloneInput(inputs[i].input)
			inputs[i].inputIndex = 0
		} else {
			inputs[i].inputIndex = len(run.state.Turn.Inputs)
			run.state.Turn.Inputs = append(run.state.Turn.Inputs, agent.CloneInput(inputs[i].input))
		}
	}
	r.changedLocked(run.queue)
}
