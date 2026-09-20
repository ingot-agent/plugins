package agentdefault

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

// invokeRoundModel builds the actual invocation, applies compaction, invokes
// the model, and materializes an interceptor-owned Round snapshot.
func (r *runtime) invokeRoundModel(
	ctx context.Context,
	sessionID session.ID,
	index int,
	messages []model.Message,
	definitions []tool.Definition,
	handler agent.StreamHandler,
) (agent.Round, error) {
	request := model.Request{
		Provider:    r.provider,
		Model:       r.modelName,
		Messages:    cloneMessages(messages),
		Tools:       cloneDefinitions(definitions),
		Temperature: copyFloat(r.temperature),
		MaxTokens:   copyInt(r.maxTokens),
	}
	request, err := r.compactRequest(ctx, sessionID, request)
	if err != nil {
		return agent.Round{}, err
	}
	invocation := cloneModelRequest(request)
	response, err := r.invokeModel(ctx, cloneModelRequest(request), handler)
	if err != nil {
		executionRecorderFrom(ctx).recordFailure(err, agent.FailureModel, &index, "")
		return agent.Round{}, err
	}
	return agent.Round{
		SessionID:  sessionID,
		Index:      index,
		Invocation: invocation,
		Response:   cloneModelResponse(response),
		Decision:   cloneMessage(response.Message),
	}, nil
}

// executeRound applies round policy and performs canonical durable execution.
// Interceptors that call next may inspect, but must not rewrite, its committed
// result after next returns.
func (r *runtime) executeRound(ctx context.Context, round agent.Round, lastAllowed bool, frame *turnFrame) (agent.RoundResult, error) {
	recorder := executionRecorderFrom(ctx)

	if err := ctx.Err(); err != nil {
		recorder.recordFailure(err, agent.FailureRoundControl, &round.Index, "")
		return agent.RoundResult{}, err
	}
	if round.Index < 0 || round.SessionID == "" || !reflect.DeepEqual(round.Decision, round.Response.Message) {
		recorder.recordFailure(ErrInvalidRound, agent.FailureRoundControl, &round.Index, "")
		return agent.RoundResult{}, ErrInvalidRound
	}
	original := cloneRound(round)
	var (
		terminalCalled bool
		terminalErr    error
		committed      *agent.RoundResult
	)

	terminal := func(callCtx context.Context, selected agent.Round) (agent.RoundResult, error) {
		if terminalCalled {
			terminalErr = ErrInvalidRoundResult
			recorder.recordFailure(terminalErr, agent.FailureRoundControl, &round.Index, "")
			return agent.RoundResult{}, terminalErr
		}
		terminalCalled = true
		if callCtx == nil {
			terminalErr = fmt.Errorf("nil round context: %w", ErrInvalidRound)
			recorder.recordFailure(terminalErr, agent.FailureRoundControl, &round.Index, "")
			return agent.RoundResult{}, terminalErr
		}
		callCtx = restoreExecutionContext(callCtx, ctx, recorder)
		if err := callCtx.Err(); err != nil {
			terminalErr = err
			recorder.recordFailure(err, agent.FailureRoundControl, &round.Index, "")
			return agent.RoundResult{}, err
		}
		if err := validateRoundIdentity(original, selected); err != nil {
			terminalErr = err
			recorder.recordFailure(err, agent.FailureRoundControl, &round.Index, "")
			return agent.RoundResult{}, err
		}
		if err := validateRoundDecisionMutation(original.Response.Message, selected.Decision); err != nil {
			terminalErr = err
			recorder.recordFailure(err, agent.FailureRoundControl, &round.Index, "")
			return agent.RoundResult{}, err
		}
		if err := validateFrameToolCalls(frame, selected.Decision.ToolCalls, lastAllowed); err != nil {
			terminalErr = err
			recorder.recordFailure(terminalErr, agent.FailureRoundControl, &round.Index, "")
			return agent.RoundResult{}, terminalErr
		}
		assistant, err := r.appendMessage(callCtx, selected.SessionID, selected.Decision)
		if err != nil {
			terminalErr = fmt.Errorf("append assistant message: %w", err)
			recorder.recordFailure(terminalErr, agent.FailureAssistantPersistence, &round.Index, "")
			return agent.RoundResult{}, terminalErr
		}
		toolMessages, err := r.executeToolCalls(callCtx, selected.SessionID, assistant.ToolCalls, frame)
		if err != nil {
			terminalErr = err
			return agent.RoundResult{}, err
		}
		result := agent.RoundResult{Decision: assistant, ToolMessages: toolMessages}
		snapshot := cloneRoundResult(result)
		committed = &snapshot
		return cloneRoundResult(snapshot), nil
	}

	next := pipeline.Compose[agent.Round, agent.RoundResult](terminal, r.roundInterceptors...)
	result, err := next(ctx, cloneRound(round))
	if err != nil {
		recorder.recordFailure(err, agent.FailureRoundControl, &round.Index, "")
		return agent.RoundResult{}, err
	}
	if terminalErr != nil {
		recorder.recordFailure(terminalErr, agent.FailureRoundControl, &round.Index, "")
		return agent.RoundResult{}, terminalErr
	}
	if terminalCalled {
		if committed == nil || !reflect.DeepEqual(result, *committed) {
			recorder.recordFailure(ErrInvalidRoundResult, agent.FailureRoundControl, &round.Index, "")
			return agent.RoundResult{}, ErrInvalidRoundResult
		}
		return cloneRoundResult(*committed), nil
	}

	if err := validateShortCircuitResult(result); err != nil {
		recorder.recordFailure(err, agent.FailureRoundControl, &round.Index, "")
		return agent.RoundResult{}, err
	}
	decision, err := r.appendMessage(ctx, round.SessionID, result.Decision)
	if err != nil {
		persistErr := fmt.Errorf("append short-circuit assistant message: %w", err)
		recorder.recordFailure(persistErr, agent.FailureAssistantPersistence, &round.Index, "")
		return agent.RoundResult{}, persistErr
	}
	return agent.RoundResult{Decision: decision}, nil
}

func (r *runtime) executeToolCalls(ctx context.Context, sessionID session.ID, calls []tool.Call, frame *turnFrame) ([]model.Message, error) {
	messages := make([]model.Message, 0, len(calls))
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			executionRecorderFrom(ctx).recordFailure(err, agent.FailureRoundControl, roundIndexFrom(ctx), "")
			return nil, err
		}
		// Every tool invocation produced by this turn inherits the turn's
		// Session identity as its explicit dynamic execution scope.
		invocation := tool.Invocation{
			Scope: execution.Scope{SessionID: sessionID},
			Call:  cloneCall(call),
		}
		result, callErr := r.executeTool(ctx, invocation)
		if callErr != nil {
			if !errors.Is(callErr, tool.ErrNotFound) && !errors.Is(callErr, tool.ErrInvalidArguments) {
				return nil, fmt.Errorf("tool %q call %q: %w", call.Name, call.ID, callErr)
			}
			if err := ctx.Err(); err != nil {
				executionRecorderFrom(ctx).recordFailure(err, agent.FailureTool, roundIndexFrom(ctx), call.ID)
				return nil, err
			}
			result = tool.Result{Content: content.FromText(preDispatchToolResult(callErr))}
		} else if err := ctx.Err(); err != nil {
			executionRecorderFrom(ctx).recordFailure(err, agent.FailureTool, roundIndexFrom(ctx), call.ID)
			return nil, err
		}
		message := model.Message{Role: model.RoleTool, Content: result.Content, ToolCallID: call.ID}
		message, err := r.appendMessage(ctx, sessionID, message)
		if err != nil {
			persistErr := fmt.Errorf("append tool result for %q: %w", call.ID, err)
			executionRecorderFrom(ctx).recordFailure(persistErr, agent.FailureToolResultPersistence, roundIndexFrom(ctx), call.ID)
			return nil, persistErr
		}
		messages = append(messages, message)
		if frame != nil && call.Name == frame.submitTool {
			intent, ok, err := r.control.FinishIntent(frame.handle, call.ID)
			if err != nil {
				controlErr := fmt.Errorf("confirm child result submission %q: %w", call.ID, err)
				executionRecorderFrom(ctx).recordFailure(controlErr, agent.FailureRoundControl, roundIndexFrom(ctx), call.ID)
				return nil, controlErr
			}
			if !ok {
				controlErr := fmt.Errorf("submit tool %q did not register a matching result: %w", call.ID, agent.ErrChildSubmission)
				executionRecorderFrom(ctx).recordFailure(controlErr, agent.FailureRoundControl, roundIndexFrom(ctx), call.ID)
				return nil, controlErr
			}
			frame.confirmed = &intent
		}
	}
	return messages, nil
}

func validateFrameToolCalls(frame *turnFrame, calls []tool.Call, lastAllowed bool) error {
	if frame == nil {
		for _, call := range calls {
			if call.Name == childSubmitToolName {
				return fmt.Errorf("%s is unavailable to root Sessions: %w", childSubmitToolName, ErrToolNotAllowed)
			}
		}
		if lastAllowed && len(calls) != 0 {
			return ErrMaxRounds
		}
		return nil
	}
	submissions := 0
	for _, call := range calls {
		if _, allowed := frame.toolNames[call.Name]; !allowed {
			return fmt.Errorf("tool %q: %w", call.Name, ErrToolNotAllowed)
		}
		if call.Name == frame.submitTool {
			submissions++
		}
	}
	if submissions != 0 && (submissions != 1 || len(calls) != 1) {
		return ErrInvalidSubmissionRound
	}
	if lastAllowed && len(calls) != 0 && submissions != 1 {
		return ErrMaxRounds
	}
	return nil
}

func (r *runtime) executeTool(ctx context.Context, invocation tool.Invocation) (result tool.Result, resultErr error) {
	call := invocation.Call
	correlation, _ := observation.CorrelationFromContext(ctx)
	correlation.ToolCallID = call.ID
	ctx = observation.WithCorrelation(ctx, correlation)
	recorder := executionRecorderFrom(ctx)
	recorder.accounting.toolStarted()
	recorder.emit(ctx, observation.ToolStarted{Call: call})
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr := fmt.Errorf("%v", recovered)
			recorder.recordFailure(panicErr, agent.FailureTool, &correlation.RoundIndex, call.ID)
			recorder.emit(ctx, observation.ToolFinished{Status: observation.StatusFailed, Error: fmt.Sprint(recovered)})
			panic(recovered)
		}
		finished := observation.ToolFinished{Status: terminalStatus(resultErr), Error: errorText(resultErr)}
		if resultErr == nil {
			finished.Result = &result
		}
		recorder.emit(ctx, finished)
	}()
	result, resultErr = r.tools.Call(ctx, cloneInvocation(invocation))
	if resultErr != nil {
		if !errors.Is(resultErr, tool.ErrNotFound) && !errors.Is(resultErr, tool.ErrInvalidArguments) {
			recorder.recordFailure(resultErr, agent.FailureTool, &correlation.RoundIndex, call.ID)
		}
		return tool.Result{}, resultErr
	}
	if err := content.Validate(result.Content); err != nil {
		validationErr := fmt.Errorf("tool %q returned invalid content: %w", call.Name, err)
		recorder.recordFailure(validationErr, agent.FailureTool, &correlation.RoundIndex, call.ID)
		return tool.Result{}, validationErr
	}
	result.Content = content.Clone(result.Content)
	return result, nil
}

func validateRoundIdentity(original, selected agent.Round) error {
	if selected.SessionID != original.SessionID || selected.Index != original.Index ||
		!reflect.DeepEqual(selected.Invocation, original.Invocation) ||
		!reflect.DeepEqual(selected.Response, original.Response) {
		return ErrInvalidRound
	}
	return nil
}

func validateRoundDecisionMutation(original, decision model.Message) error {
	if decision.Role != original.Role || decision.Name != original.Name || decision.ToolCallID != original.ToolCallID {
		return ErrInvalidRoundDecision
	}
	originalIndex := 0
	for _, call := range decision.ToolCalls {
		for originalIndex < len(original.ToolCalls) && original.ToolCalls[originalIndex].ID != call.ID {
			originalIndex++
		}
		if originalIndex == len(original.ToolCalls) {
			return ErrInvalidRoundDecision
		}
		originalIndex++
	}
	if err := validateAssistant(decision); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRoundDecision, err)
	}
	return nil
}

func validateShortCircuitResult(result agent.RoundResult) error {
	if len(result.ToolMessages) != 0 || len(result.Decision.ToolCalls) != 0 {
		return ErrInvalidRoundResult
	}
	if err := validateAssistant(result.Decision); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRoundResult, err)
	}
	return nil
}

func cloneModelResponse(response model.Response) model.Response {
	response.Message = cloneMessage(response.Message)
	return response
}

func cloneRound(round agent.Round) agent.Round {
	round.Invocation = cloneModelRequest(round.Invocation)
	round.Response = cloneModelResponse(round.Response)
	round.Decision = cloneMessage(round.Decision)
	return round
}

func cloneRoundResult(result agent.RoundResult) agent.RoundResult {
	result.Decision = cloneMessage(result.Decision)
	result.ToolMessages = cloneMessages(result.ToolMessages)
	return result
}
