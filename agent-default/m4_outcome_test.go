package agentdefault

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type outcomeModel struct {
	responses []model.Response
	errors    []error
	calls     int
}

func (m *outcomeModel) Complete(context.Context, session.ID, session.ID, model.Request) (model.Response, error) {
	index := m.calls
	m.calls++
	if index < len(m.errors) && m.errors[index] != nil {
		return model.Response{}, m.errors[index]
	}
	return m.responses[index], nil
}

type outcomeTools struct {
	err   error
	calls []tool.Call
}

func (*outcomeTools) Definitions() []tool.Definition {
	return []tool.Definition{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}

func (t *outcomeTools) Call(_ context.Context, invocation tool.Invocation) (tool.Result, error) {
	t.calls = append(t.calls, cloneCall(invocation.Call))
	if t.err != nil {
		return tool.Result{}, t.err
	}
	return tool.Result{Content: content.FromText("ok")}, nil
}

func outcomeResponse(provider, modelName string, input, output int, calls ...tool.Call) model.Response {
	return model.Response{
		Message:  model.Message{Role: model.RoleAssistant, Content: content.FromText("done"), ToolCalls: calls},
		Provider: provider, Model: modelName,
		Usage: model.Usage{InputTokens: input, OutputTokens: output, TotalTokens: input + output, Reported: true},
	}
}

func newOutcomeRuntime(t *testing.T, models model.Runtime, tools tool.Runtime, options ...func(*Dependencies)) Exports {
	t.Helper()
	deps := Dependencies{
		Model: models, Tools: tools, Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}},
		Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
	}
	for _, option := range options {
		option(&deps)
	}
	exports, _, err := New(context.Background(), withState(t, Config{}, deps))
	if err != nil {
		t.Fatal(err)
	}
	return exports
}

func TestM4SuccessfulTurnOutcome(t *testing.T) {
	models := &outcomeModel{responses: []model.Response{
		outcomeResponse("provider", "a", 10, 2, tool.Call{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)}),
		outcomeResponse("provider", "b", 20, 3, tool.Call{ID: "c2", Name: "echo", Arguments: json.RawMessage(`{}`)}),
		outcomeResponse("provider", "a", 30, 4),
	}}
	tools := &outcomeTools{}
	consumer := &recordingConsumer{}
	exports := newOutcomeRuntime(t, models, tools, func(deps *Dependencies) { deps.Observation = consumer })

	execution, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if err != nil || execution.Result == nil || textValue(execution.Result.Output) != "done" {
		t.Fatalf("execution=%#v err=%v", execution, err)
	}
	if execution.Outcome.Status != agent.OutcomeSucceeded || execution.Outcome.Failure != nil {
		t.Fatalf("outcome=%#v", execution.Outcome)
	}
	if models.calls != 3 || len(tools.calls) != 2 {
		t.Fatalf("model calls=%d tool calls=%d", models.calls, len(tools.calls))
	}
	events := consumer.snapshot()
	finished := events[len(events)-1].Detail.(observation.TurnFinished)
	if !reflect.DeepEqual(finished.Outcome, execution.Outcome) || !reflect.DeepEqual(finished.Result, execution.Result) {
		t.Fatalf("turn finished=%#v execution=%#v", finished, execution)
	}
}

func TestM4StreamingFallbackReturnsCanonicalOutcome(t *testing.T) {
	models := &outcomeModel{responses: []model.Response{outcomeResponse("provider", "model", 5, 1)}}
	streamCalls := 0
	streaming := modelStreamFunc(func(context.Context, model.Request, model.StreamHandler) (model.Response, error) {
		streamCalls++
		return model.Response{}, model.ErrStreamingUnsupported
	})
	exports := newOutcomeRuntime(t, models, &outcomeTools{}, func(deps *Dependencies) {
		deps.Streaming = ingotabi.Some[model.StreamingRuntime](streaming)
	})
	execution, err := exports.Streaming.Stream(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}, func(agent.StreamEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if execution.Result == nil || execution.Outcome.Status != agent.OutcomeSucceeded || streamCalls != 1 || models.calls != 1 {
		t.Fatalf("execution=%#v stream calls=%d complete calls=%d", execution, streamCalls, models.calls)
	}
}

func TestM4ModelFailurePreservesFailureDetails(t *testing.T) {
	modelErr := errors.New("model failed")
	models := &outcomeModel{
		responses: []model.Response{
			outcomeResponse("provider", "model", 7, 2, tool.Call{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)}),
			{},
		},
		errors: []error{nil, modelErr},
	}
	execution, err := newOutcomeRuntime(t, models, &outcomeTools{}).Runtime.Run(
		context.Background(), agent.Turn{SessionID: "s", Input: "hello"},
	)
	if !errors.Is(err, modelErr) || execution.Result != nil || execution.Outcome.Status != agent.OutcomeFailed {
		t.Fatalf("execution=%#v err=%v", execution, err)
	}
	if models.calls != 2 {
		t.Fatalf("model calls=%d", models.calls)
	}
	if failure := execution.Outcome.Failure; failure == nil || failure.Stage != agent.FailureModel || failure.RoundIndex == nil || *failure.RoundIndex != 1 {
		t.Fatalf("failure=%#v", failure)
	}
}

func TestM4RecoveredToolRejectionReturnsSuccessfulOutcome(t *testing.T) {
	models := &outcomeModel{responses: []model.Response{
		outcomeResponse("provider", "model", 2, 1, tool.Call{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)}),
		outcomeResponse("provider", "model", 3, 1),
	}}
	execution, err := newOutcomeRuntime(t, models, &outcomeTools{err: tool.ErrInvalidArguments}).Runtime.Run(
		context.Background(), agent.Turn{SessionID: "s", Input: "hello"},
	)
	if err != nil || execution.Outcome.Status != agent.OutcomeSucceeded || execution.Outcome.Failure != nil ||
		models.calls != 2 {
		t.Fatalf("execution=%#v err=%v", execution, err)
	}
}

func TestM4ModelSelectionRejectionReturnsFailedOutcome(t *testing.T) {
	for _, rejection := range []error{model.ErrProviderNotFound, model.ErrModelNotFound} {
		t.Run(rejection.Error(), func(t *testing.T) {
			models := &outcomeModel{responses: []model.Response{{}}, errors: []error{rejection}}
			execution, err := newOutcomeRuntime(t, models, &outcomeTools{}).Runtime.Run(
				context.Background(), agent.Turn{SessionID: "s", Input: "hello"},
			)
			if !errors.Is(err, rejection) || execution.Result != nil || execution.Outcome.Status != agent.OutcomeFailed || models.calls != 1 {
				t.Fatalf("execution=%#v err=%v", execution, err)
			}
		})
	}
}

func TestM4InterceptorsMayReplaceContextWithoutLosingExecutionState(t *testing.T) {
	t.Run("turn", func(t *testing.T) {
		interceptor := agentInterceptorFunc(func(_ context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
			return next(context.Background(), turn)
		})
		consumer := &recordingConsumer{}
		execution, err := newOutcomeRuntime(t,
			&outcomeModel{responses: []model.Response{outcomeResponse("provider", "model", 1, 1)}},
			&outcomeTools{}, func(deps *Dependencies) {
				deps.Interceptors = []agent.Interceptor{interceptor}
				deps.Observation = consumer
			},
		).Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
		if err != nil || execution.Result == nil || execution.Outcome.Status != agent.OutcomeSucceeded {
			t.Fatalf("execution=%#v err=%v", execution, err)
		}
		for _, event := range consumer.snapshot() {
			if event.Correlation.TurnID == "" {
				t.Fatalf("event lost correlation: %#v", event)
			}
		}
	})

	t.Run("round", func(t *testing.T) {
		interceptor := roundInterceptorFunc(func(_ context.Context, round agent.Round, next pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
			return next(context.Background(), round)
		})
		execution, err := newOutcomeRuntime(t,
			&outcomeModel{responses: []model.Response{outcomeResponse("provider", "model", 1, 1)}},
			&outcomeTools{}, func(deps *Dependencies) {
				deps.RoundInterceptors = []agent.RoundInterceptor{interceptor}
			},
		).Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
		if err != nil || execution.Result == nil || execution.Outcome.Status != agent.OutcomeSucceeded {
			t.Fatalf("execution=%#v err=%v", execution, err)
		}
	})
}

func TestM4FinalFailureStageIgnoresSwallowedInnerFailure(t *testing.T) {
	outerErr := errors.New("outer interceptor failed")
	outer := agentInterceptorFunc(func(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
		_, _ = next(ctx, turn)
		return agent.Result{}, outerErr
	})
	inner := agentInterceptorFunc(func(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
		_, _ = next(ctx, turn)
		return agent.Result{Output: content.FromText("recovered")}, nil
	})
	models := &outcomeModel{responses: []model.Response{{}}, errors: []error{errors.New("model failed")}}
	execution, err := newOutcomeRuntime(t, models, &outcomeTools{}, func(deps *Dependencies) {
		deps.Interceptors = []agent.Interceptor{outer, inner}
	}).Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, outerErr) || execution.Outcome.Failure == nil ||
		execution.Outcome.Failure.Stage != agent.FailureTurnControl {
		t.Fatalf("execution=%#v err=%v", execution, err)
	}
}

func TestM4CancellationReturnsSettledOutcome(t *testing.T) {
	started := make(chan struct{})
	models := completeModelFunc(func(ctx context.Context, _ model.Request) (model.Response, error) {
		close(started)
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	})
	exports := newOutcomeRuntime(t, models, &outcomeTools{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		execution agent.Execution
		err       error
	}, 1)
	go func() {
		execution, err := exports.Runtime.Run(ctx, agent.Turn{SessionID: "s", Input: "hello"})
		done <- struct {
			execution agent.Execution
			err       error
		}{execution: execution, err: err}
	}()
	<-started
	cancel()
	result := <-done
	failure := result.execution.Outcome.Failure
	if !errors.Is(result.err, context.Canceled) || result.execution.Result != nil ||
		result.execution.Outcome.Status != agent.OutcomeCanceled || failure == nil || failure.Stage != agent.FailureModel ||
		failure.RoundIndex == nil || *failure.RoundIndex != 0 {
		t.Fatalf("execution=%#v err=%v", result.execution, result.err)
	}
}

func TestM4InterceptorShortCircuitsRespectExecutionBoundaries(t *testing.T) {
	t.Run("turn short circuit", func(t *testing.T) {
		interceptor := agentInterceptorFunc(func(context.Context, agent.Turn, pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
			return agent.Result{Output: content.FromText("cached")}, nil
		})
		models := &outcomeModel{}
		tools := &outcomeTools{}
		execution, err := newOutcomeRuntime(t, models, tools, func(deps *Dependencies) {
			deps.Interceptors = []agent.Interceptor{interceptor}
		}).Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
		if err != nil || textValue(executionOutput(execution)) != "cached" ||
			execution.Outcome.Status != agent.OutcomeSucceeded || models.calls != 0 || len(tools.calls) != 0 {
			t.Fatalf("execution=%#v err=%v", execution, err)
		}
	})

	t.Run("round short circuit", func(t *testing.T) {
		interceptor := roundInterceptorFunc(func(context.Context, agent.Round, pipeline.Next[agent.Round, agent.RoundResult]) (agent.RoundResult, error) {
			return agent.RoundResult{Decision: model.Message{Role: model.RoleAssistant, Content: content.FromText("blocked")}}, nil
		})
		models := &outcomeModel{responses: []model.Response{outcomeResponse("provider", "model", 2, 1,
			tool.Call{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)})}}
		tools := &outcomeTools{}
		execution, err := newOutcomeRuntime(t, models, tools, func(deps *Dependencies) {
			deps.RoundInterceptors = []agent.RoundInterceptor{interceptor}
		}).Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
		if err != nil || textValue(executionOutput(execution)) != "blocked" || execution.Outcome.Status != agent.OutcomeSucceeded ||
			models.calls != 1 || len(tools.calls) != 0 {
			t.Fatalf("execution=%#v err=%v", execution, err)
		}
	})
}

func TestM4FailureStagesAndCanonicalResultBoundary(t *testing.T) {
	t.Run("assistant persistence", func(t *testing.T) {
		persistErr := errors.New("persist failed")
		store := &failingAppendStore{
			memoryStore: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}, failAt: 2, err: persistErr,
		}
		models := &outcomeModel{responses: []model.Response{outcomeResponse("provider", "model", 1, 1)}}
		execution, err := newOutcomeRuntime(t, models, &outcomeTools{}, func(deps *Dependencies) { deps.Store = store }).Runtime.Run(
			context.Background(), agent.Turn{SessionID: "s", Input: "hello"},
		)
		failure := execution.Outcome.Failure
		if !errors.Is(err, persistErr) || execution.Result != nil || failure == nil || failure.Stage != agent.FailureAssistantPersistence ||
			failure.RoundIndex == nil || *failure.RoundIndex != 0 {
			t.Fatalf("execution=%#v err=%v", execution, err)
		}
	})

	t.Run("tool", func(t *testing.T) {
		toolErr := errors.New("unknown tool outcome")
		models := &outcomeModel{responses: []model.Response{outcomeResponse("provider", "model", 1, 1,
			tool.Call{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)})}}
		execution, err := newOutcomeRuntime(t, models, &outcomeTools{err: toolErr}).Runtime.Run(
			context.Background(), agent.Turn{SessionID: "s", Input: "hello"},
		)
		failure := execution.Outcome.Failure
		if !errors.Is(err, toolErr) || failure == nil || failure.Stage != agent.FailureTool || failure.ToolCallID != "c1" ||
			execution.Result != nil || execution.Outcome.Status != agent.OutcomeFailed {
			t.Fatalf("execution=%#v err=%v", execution, err)
		}
	})

	t.Run("stream consumer", func(t *testing.T) {
		consumerErr := errors.New("consumer stopped")
		response := outcomeResponse("provider", "model", 1, 1)
		streaming := modelStreamFunc(func(_ context.Context, _ model.Request, handler model.StreamHandler) (model.Response, error) {
			if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "x"}); err != nil {
				return model.Response{}, err
			}
			return response, nil
		})
		execution, err := newOutcomeRuntime(t, &outcomeModel{}, &outcomeTools{}, func(deps *Dependencies) {
			deps.Streaming = ingotabi.Some[model.StreamingRuntime](streaming)
		}).Streaming.Stream(context.Background(), agent.Turn{SessionID: "s", Input: "hello"}, func(agent.StreamEvent) error { return consumerErr })
		failure := execution.Outcome.Failure
		if !errors.Is(err, consumerErr) || failure == nil || failure.Stage != agent.FailureStreamConsumer ||
			execution.Result != nil || execution.Outcome.Status != agent.OutcomeFailed {
			t.Fatalf("execution=%#v err=%v", execution, err)
		}
	})

	t.Run("pre turn validation has no outcome", func(t *testing.T) {
		execution, err := newOutcomeRuntime(t, &outcomeModel{}, &outcomeTools{}).Runtime.Run(
			context.Background(), agent.Turn{Input: "hello"},
		)
		if !errors.Is(err, ErrInvalidTurn) || !reflect.DeepEqual(execution, agent.Execution{}) {
			t.Fatalf("execution=%#v err=%v", execution, err)
		}
	})
}
