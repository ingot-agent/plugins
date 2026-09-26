package contextcompact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
)

type protocolModelFunc func(context.Context, model.Request) (model.Response, error)

func (f protocolModelFunc) Complete(ctx context.Context, request model.Request) (model.Response, error) {
	return f(ctx, request)
}

type protocolCounterFunc func(context.Context, usage.CountRequest) (usage.CountResult, error)

func (f protocolCounterFunc) CountInput(ctx context.Context, request usage.CountRequest) (usage.CountResult, error) {
	return f(ctx, request)
}

func protocolCountResult(request model.Request, tokens int64) usage.CountResult {
	return usage.CountResult{
		InputTokens: tokens, Accuracy: usage.AccuracyExact, Source: "protocol-test",
		Provider: request.Provider, Model: request.Model,
	}
}

func protocolInputTokens(request model.Request) int64 {
	tokens := int64(17)
	for _, message := range request.Messages {
		text, _ := content.TextOnly(message.Content)
		tokens += int64(len(text))
	}
	return tokens
}

func newProtocolCompactor(t *testing.T, runtime model.Runtime) *compactor {
	t.Helper()
	cfg, err := normalizeConfig(Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &compactor{
		model: runtime, cfg: cfg,
		counter: protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
			return protocolCountResult(request.Invocation, protocolInputTokens(request.Invocation)), nil
		}),
	}
}

func protocolInvocation() model.Request {
	return model.Request{Provider: "main-provider", Model: "main-model"}
}

func TestProtocolRollupOnlyDiscardsExistingState(t *testing.T) {
	t.Parallel()
	state := map[string]json.RawMessage{"/done": json.RawMessage(`true`), "/keep": json.RawMessage(` { "revision" : 3 } `)}
	before := cloneState(state)
	var request model.Request
	r := newProtocolCompactor(t, protocolModelFunc(func(_ context.Context, input model.Request) (model.Response, error) {
		request = cloneRequest(input)
		return summaryResponse(`{"summary":"Keep the active revision.","discard_paths":["/done"]}`), nil
	}))
	budget := &callBudget{limit: 2}
	output, _, _, err := r.summarizeRollup(context.Background(), protocolInvocation(), state, []string{"Old work is done."}, budget)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatal("protocol mutated state before checkpoint acceptance")
	}
	if !reflect.DeepEqual(output.Operations, []patchOperation{{Op: "delete", Path: "/done"}}) {
		t.Fatalf("operations = %#v", output.Operations)
	}
	if err := applyOperations(state, output.Operations); err != nil {
		t.Fatal(err)
	}
	if len(state) != 1 || string(state["/keep"]) != string(before["/keep"]) {
		t.Fatalf("retained state was rewritten: %#v", state)
	}
	if *request.MaxTokens != 4096 || budget.used != 1 {
		t.Fatalf("max tokens=%d calls=%d", *request.MaxTokens, budget.used)
	}
	var input rollupInput
	if err := json.Unmarshal([]byte(messageText(request.Messages[1])), &input); err != nil {
		t.Fatal(err)
	}
	if input.MemoryTargetTokens != r.cfg.memoryTargetTokens || input.StateTargetTokens != r.cfg.stateTargetTokens {
		t.Fatalf("rollup targets missing: %#v", input)
	}
}

func TestProtocolRollupRejectsUnauthorizedStateChanges(t *testing.T) {
	t.Parallel()
	for _, response := range []string{
		`{"summary":"merged","operations":[]}`,
		`{"summary":"merged","discard_paths":null}`,
		`{"summary":"merged","discard_paths":["/missing"]}`,
		`{"summary":"merged","discard_paths":["/keep","/keep"]}`,
		`{"summary":"merged","discard_paths":[],"operations":[{"op":"set","path":"/keep","value":false}]}`,
	} {
		t.Run(response, func(t *testing.T) {
			r := newProtocolCompactor(t, protocolModelFunc(func(context.Context, model.Request) (model.Response, error) {
				return summaryResponse(response), nil
			}))
			state := map[string]json.RawMessage{"/keep": json.RawMessage(`true`)}
			_, _, _, err := r.summarizeRollup(context.Background(), protocolInvocation(), state, []string{"old"}, &callBudget{limit: 1})
			if !errors.Is(err, ErrCompactionFailed) || string(state["/keep"]) != "true" {
				t.Fatalf("state=%#v err=%v", state, err)
			}
		})
	}
}

func TestProtocolSummaryCountsTheCompleteSelectedRequest(t *testing.T) {
	t.Parallel()
	var counted, invoked model.Request
	r := newProtocolCompactor(t, protocolModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		invoked = cloneRequest(request)
		return summaryResponse(`{"summary":"done","operations":[]}`), nil
	}))
	r.cfg.provider, r.cfg.model = "summary-provider", "summary-model"
	r.counter = protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
		counted = cloneRequest(request.Invocation)
		return protocolCountResult(request.Invocation, r.cfg.summaryInputTokens), nil
	})
	invocation := protocolInvocation()
	invocation.Tools = []tool.Definition{{Name: "do_not_call", InputSchema: json.RawMessage(`{}`)}}
	_, _, _, err := r.summarizeSegment(context.Background(), invocation, nil, []model.Message{{Role: model.RoleUser, Content: content.FromText("source")}}, &callBudget{limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(counted, invoked) || counted.Provider != "summary-provider" || counted.Model != "summary-model" {
		t.Fatalf("counted=%#v invoked=%#v", counted, invoked)
	}
	if len(invoked.Messages) != 2 || invoked.Messages[0].Role != model.RoleSystem || invoked.Tools == nil || len(invoked.Tools) != 0 || *invoked.MaxTokens != 1024 {
		t.Fatalf("invalid summary request: %#v", invoked)
	}
}

func TestProtocolOversizedRoundPreservesOrderedUTF8Evidence(t *testing.T) {
	t.Parallel()
	state := map[string]json.RawMessage{"/phase": json.RawMessage(`"old"`)}
	source := []model.Message{
		{Role: model.RoleUser, Content: content.FromText("Keep chronological corrections.")},
		{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call", Name: "read", Arguments: json.RawMessage(`{}`)}}},
		{Role: model.RoleTool, ToolCallID: "call", Content: content.FromText(strings.Repeat("\u4e2d\u6587-data-", 900))},
	}
	var fragments []evidenceInput
	var final segmentInput
	var calls int
	r := newProtocolCompactor(t, nil)
	r.cfg.summaryInputTokens = 3200
	r.model = protocolModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		calls++
		if protocolInputTokens(request) > r.cfg.summaryInputTokens {
			t.Fatalf("oversized model call: %d", protocolInputTokens(request))
		}
		if messageText(request.Messages[0]) == evidenceSystemPrompt {
			var input evidenceInput
			if err := json.Unmarshal([]byte(messageText(request.Messages[1])), &input); err != nil {
				t.Fatal(err)
			}
			if !utf8.ValidString(input.Fragment) || input.ByteEnd-input.ByteStart != len(input.Fragment) {
				t.Fatalf("invalid fragment: %#v", input)
			}
			fragments = append(fragments, input)
			return summaryResponse(fmt.Sprintf(`{"summary":"evidence %d"}`, len(fragments))), nil
		}
		if err := json.Unmarshal([]byte(messageText(request.Messages[1])), &final); err != nil {
			t.Fatal(err)
		}
		return summaryResponse(`{"summary":"Completed in order.","operations":[{"op":"set","path":"/phase","value":"new"}]}`), nil
	})
	budget := &callBudget{limit: 30}
	output, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), state, source, budget)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) < 2 || calls != len(fragments)+1 || budget.used != calls {
		t.Fatalf("fragments=%d calls=%d budget=%#v", len(fragments), calls, budget)
	}
	var combined strings.Builder
	for i, input := range fragments {
		if input.ByteStart != combined.Len() || input.SourceKind != "conversation_json_fragment" {
			t.Fatalf("fragment %d is out of order: %#v", i, input)
		}
		combined.WriteString(input.Fragment)
		if final.OrderedEvidence[i] != fmt.Sprintf("evidence %d", i+1) {
			t.Fatalf("evidence order=%#v", final.OrderedEvidence)
		}
	}
	wantSource, err := json.Marshal(projectMessages(source))
	if err != nil {
		t.Fatal(err)
	}
	if combined.String() != string(wantSource) || len(final.Source) != 0 || !reflect.DeepEqual(final.CurrentState, stateSnapshot(state)) {
		t.Fatal("source was lost, duplicated, or state changed during partial extraction")
	}
	if len(output.Operations) != 1 || string(state["/phase"]) != `"old"` {
		t.Fatalf("premature state change: %#v operations=%#v", state, output.Operations)
	}
}

func TestProtocolPartialCallsShareTheSummaryBudget(t *testing.T) {
	t.Parallel()
	calls := 0
	r := newProtocolCompactor(t, protocolModelFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		return summaryResponse(`{"summary":"partial"}`), nil
	}))
	r.cfg.summaryInputTokens = 2200
	budget := &callBudget{limit: 2}
	source := []model.Message{{Role: model.RoleUser, Content: content.FromText(strings.Repeat("huge", 6000))}}
	output, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), nil, source, budget)
	if !errors.Is(err, ErrContextUncompactable) || calls != 2 || budget.used != 2 || output.Summary != "" {
		t.Fatalf("output=%#v calls=%d budget=%#v err=%v", output, calls, budget, err)
	}
	_, _, _, err = r.summarizeRollup(context.Background(), protocolInvocation(), nil, []string{"old"}, budget)
	if !errors.Is(err, ErrContextUncompactable) || calls != 2 {
		t.Fatalf("rollup escaped exhausted budget: calls=%d err=%v", calls, err)
	}
}

func TestProtocolOversizedEvidenceIsMergedBeforeFinalOperations(t *testing.T) {
	t.Parallel()
	var initialEvidence, mergedEvidence []string
	var mergedSource strings.Builder
	var final segmentInput
	r := newProtocolCompactor(t, nil)
	r.cfg.summaryInputTokens = 3200
	r.model = protocolModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		if protocolInputTokens(request) > r.cfg.summaryInputTokens {
			t.Fatalf("oversized model request: %d", protocolInputTokens(request))
		}
		if messageText(request.Messages[0]) == evidenceSystemPrompt {
			var input evidenceInput
			if err := json.Unmarshal([]byte(messageText(request.Messages[1])), &input); err != nil {
				t.Fatal(err)
			}
			var summary string
			if input.SourceKind == "conversation_json_fragment" {
				summary = fmt.Sprintf("fragment %d: %s", len(initialEvidence), strings.Repeat("e", 500))
				initialEvidence = append(initialEvidence, summary)
			} else {
				if input.SourceKind != "ordered_evidence_json_fragment" || input.ByteStart != mergedSource.Len() {
					t.Fatalf("unordered merging: %#v", input)
				}
				mergedSource.WriteString(input.Fragment)
				summary = fmt.Sprintf("merged %d", len(mergedEvidence))
				mergedEvidence = append(mergedEvidence, summary)
			}
			encoded, err := json.Marshal(struct {
				Summary string `json:"summary"`
			}{Summary: summary})
			if err != nil {
				t.Fatal(err)
			}
			return summaryResponse(string(encoded)), nil
		}
		if err := json.Unmarshal([]byte(messageText(request.Messages[1])), &final); err != nil {
			t.Fatal(err)
		}
		return summaryResponse(`{"summary":"final result","operations":[]}`), nil
	})
	budget := &callBudget{limit: 30}
	_, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), nil,
		[]model.Message{{Role: model.RoleUser, Content: content.FromText(strings.Repeat("source ", 2000))}}, budget)
	if err != nil {
		t.Fatal(err)
	}
	if len(mergedEvidence) == 0 || !reflect.DeepEqual(final.OrderedEvidence, mergedEvidence) {
		t.Fatalf("initial=%d merged=%#v final=%#v", len(initialEvidence), mergedEvidence, final)
	}
	wantSource, err := json.Marshal(initialEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if mergedSource.String() != string(wantSource) || budget.used != len(initialEvidence)+len(mergedEvidence)+1 {
		t.Fatalf("evidence lost or call budget incorrect: %#v", budget)
	}
}

func TestProtocolRejectsPartialOperations(t *testing.T) {
	t.Parallel()
	r := newProtocolCompactor(t, protocolModelFunc(func(context.Context, model.Request) (model.Response, error) {
		return summaryResponse(`{"summary":"partial","operations":[]}`), nil
	}))
	r.cfg.summaryInputTokens = 2200
	budget := &callBudget{limit: 8}
	_, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), nil,
		[]model.Message{{Role: model.RoleUser, Content: content.FromText(strings.Repeat("large", 2000))}}, budget)
	if !errors.Is(err, ErrCompactionFailed) || budget.used != 1 {
		t.Fatalf("budget=%#v err=%v", budget, err)
	}
}

func TestProtocolRejectsFixedStateThatExceedsInputBudget(t *testing.T) {
	t.Parallel()
	calls := 0
	r := newProtocolCompactor(t, protocolModelFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		return summaryResponse(`{"summary":"unexpected","operations":[]}`), nil
	}))
	r.cfg.summaryInputTokens = 1500
	state := map[string]json.RawMessage{"/large": json.RawMessage(`"` + strings.Repeat("x", 3000) + `"`)}
	_, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), state,
		[]model.Message{{Role: model.RoleUser, Content: content.FromText("input")}}, &callBudget{limit: 8})
	if !errors.Is(err, ErrContextUncompactable) || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestProtocolEvidenceMustReduceInputBeforeAnotherLevel(t *testing.T) {
	t.Parallel()
	r := newProtocolCompactor(t, protocolModelFunc(func(context.Context, model.Request) (model.Response, error) {
		return summaryResponse(`{"summary":"nonshrinking evidence"}`), nil
	}))
	r.cfg.summaryInputTokens = 100
	r.counter = protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
		tokens := int64(1)
		if messageText(request.Invocation.Messages[0]) == summarySystemPrompt {
			var input segmentInput
			if err := json.Unmarshal([]byte(messageText(request.Invocation.Messages[1])), &input); err != nil {
				t.Fatal(err)
			}
			if len(input.Source) > 0 || len(input.OrderedEvidence) > 0 {
				tokens = 1000
			}
		}
		return protocolCountResult(request.Invocation, tokens), nil
	})
	budget := &callBudget{limit: 8}
	_, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), nil,
		[]model.Message{{Role: model.RoleUser, Content: content.FromText("source")}}, budget)
	if !errors.Is(err, ErrContextUncompactable) || budget.used != 1 || !strings.Contains(err.Error(), "did not reduce") {
		t.Fatalf("budget=%#v err=%v", budget, err)
	}
}

func TestProtocolCountFailurePreservesCauseWithoutModelCalls(t *testing.T) {
	t.Parallel()
	failure := errors.New("tokenizer unavailable")
	r := newProtocolCompactor(t, protocolModelFunc(func(context.Context, model.Request) (model.Response, error) {
		t.Fatal("called model after count failure")
		return model.Response{}, nil
	}))
	r.counter = protocolCounterFunc(func(context.Context, usage.CountRequest) (usage.CountResult, error) {
		return usage.CountResult{}, failure
	})
	budget := &callBudget{limit: 8}
	_, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), nil,
		[]model.Message{{Role: model.RoleUser, Content: content.FromText("source")}}, budget)
	if !errors.Is(err, failure) || budget.used != 0 {
		t.Fatalf("budget=%#v err=%v", budget, err)
	}
}

func TestProtocolSummaryHonorsAllowedCountingAccuracy(t *testing.T) {
	t.Parallel()
	r := newProtocolCompactor(t, protocolModelFunc(func(context.Context, model.Request) (model.Response, error) {
		t.Fatal("called model using disallowed count accuracy")
		return model.Response{}, nil
	}))
	var err error
	r.cfg, err = normalizeConfig(Config{AllowedAccuracies: []usage.Accuracy{usage.AccuracyExact}})
	if err != nil {
		t.Fatal(err)
	}
	r.counter = protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
		count := protocolCountResult(request.Invocation, 1)
		count.Accuracy = usage.AccuracyEstimate
		return count, nil
	})
	_, _, _, err = r.summarizeSegment(context.Background(), protocolInvocation(), nil,
		[]model.Message{{Role: model.RoleUser, Content: content.FromText("source")}}, &callBudget{limit: 8})
	if !errors.Is(err, ErrUnsupportedAccuracy) {
		t.Fatalf("err=%v", err)
	}
}

func TestProtocolRejectsSummaryCountingIdentityChanges(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*usage.CountResult)
	}{
		{name: "source", mutate: func(count *usage.CountResult) { count.Source = "new-profile" }},
		{name: "accuracy", mutate: func(count *usage.CountResult) { count.Accuracy = usage.AccuracyEstimate }},
		{name: "provider", mutate: func(count *usage.CountResult) { count.Provider = "new-provider" }},
		{name: "model", mutate: func(count *usage.CountResult) { count.Model = "new-model" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			counts := 0
			r := newProtocolCompactor(t, protocolModelFunc(func(context.Context, model.Request) (model.Response, error) {
				t.Fatal("model invoked after summary counting identity changed")
				return model.Response{}, nil
			}))
			r.counter = protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
				counts++
				count := protocolCountResult(request.Invocation, 10)
				if counts > 1 {
					test.mutate(&count)
				}
				return count, nil
			})
			budget := &callBudget{limit: 8}
			_, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), nil,
				[]model.Message{{Role: model.RoleUser, Content: content.FromText("source")}}, budget)
			if !errors.Is(err, ErrInvalidCount) || budget.used != 0 {
				t.Fatalf("budget=%#v err=%v", budget, err)
			}
		})
	}
}

func TestProtocolRejectsProfileChangeAfterPartialEvidence(t *testing.T) {
	t.Parallel()
	calls := 0
	r := newProtocolCompactor(t, protocolModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		calls++
		if calls != 1 || messageText(request.Messages[0]) != evidenceSystemPrompt {
			t.Fatalf("unexpected model invocation after partial evidence: calls=%d", calls)
		}
		return summaryResponse(`{"summary":"partial evidence"}`), nil
	}))
	r.cfg.summaryInputTokens = 2200
	r.counter = protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
		count := protocolCountResult(request.Invocation, protocolInputTokens(request.Invocation))
		if calls > 0 {
			count.Source = "changed-after-first-fragment"
		}
		return count, nil
	})
	budget := &callBudget{limit: 8}
	output, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), nil,
		[]model.Message{{Role: model.RoleUser, Content: content.FromText(strings.Repeat("large", 2000))}}, budget)
	if !errors.Is(err, ErrInvalidCount) || calls != 1 || budget.used != 1 || output.Summary != "" {
		t.Fatalf("calls=%d budget=%#v output=%#v err=%v", calls, budget, output, err)
	}
}

func TestProtocolSharesCountingIdentityAcrossSegmentAndRollup(t *testing.T) {
	t.Parallel()
	calls := 0
	r := newProtocolCompactor(t, protocolModelFunc(func(context.Context, model.Request) (model.Response, error) {
		calls++
		return summaryResponse(`{"summary":"first segment","operations":[]}`), nil
	}))
	r.counter = protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
		count := protocolCountResult(request.Invocation, 10)
		if calls > 0 {
			count.Source = "changed-before-rollup"
		}
		return count, nil
	})
	budget := &callBudget{limit: 8}
	_, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), nil,
		[]model.Message{{Role: model.RoleUser, Content: content.FromText("source")}}, budget)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = r.summarizeRollup(context.Background(), protocolInvocation(), nil, []string{"first segment"}, budget)
	if !errors.Is(err, ErrInvalidCount) || calls != 1 || budget.used != 1 {
		t.Fatalf("calls=%d budget=%#v err=%v", calls, budget, err)
	}
}

func TestProtocolPinsResolvedSummaryModelBeforeInvocation(t *testing.T) {
	t.Parallel()
	var invoked model.Request
	r := newProtocolCompactor(t, protocolModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		invoked = cloneRequest(request)
		return summaryResponse(`{"summary":"done","operations":[]}`), nil
	}))
	r.counter = protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
		count := protocolCountResult(request.Invocation, 10)
		count.Provider, count.Model = "resolved-provider", "resolved-model"
		return count, nil
	})
	budget := &callBudget{limit: 1}
	_, _, _, err := r.summarizeSegment(context.Background(), model.Request{}, nil,
		[]model.Message{{Role: model.RoleUser, Content: content.FromText("source")}}, budget)
	if err != nil {
		t.Fatal(err)
	}
	if invoked.Provider != "resolved-provider" || invoked.Model != "resolved-model" || budget.used != 1 {
		t.Fatalf("invoked=%#v budget=%#v", invoked, budget)
	}
}

func TestProtocolLargeSourceFitsDefaultCallBudget(t *testing.T) {
	t.Parallel()
	source := []model.Message{{Role: model.RoleUser, Content: content.FromText(strings.Repeat("x", 180000))}}
	var recovered strings.Builder
	var final segmentInput
	calls, fragments := 0, 0
	r := newProtocolCompactor(t, nil)
	r.cfg.summaryInputTokens = 32000 // Exercise the fragment path independently of the new 256k default.
	r.counter = protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
		tokens := protocolInputTokens(request.Invocation)
		if messageText(request.Invocation.Messages[0]) == evidenceSystemPrompt {
			var input evidenceInput
			if err := json.Unmarshal([]byte(messageText(request.Invocation.Messages[1])), &input); err != nil {
				t.Fatal(err)
			}
			tokens = int64(len(input.Fragment) + 100)
		}
		return protocolCountResult(request.Invocation, tokens), nil
	})
	r.model = protocolModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		calls++
		if messageText(request.Messages[0]) == evidenceSystemPrompt {
			var input evidenceInput
			if err := json.Unmarshal([]byte(messageText(request.Messages[1])), &input); err != nil {
				t.Fatal(err)
			}
			if int64(len(input.Fragment)+100) > r.cfg.summaryInputTokens || input.ByteStart != recovered.Len() {
				t.Fatalf("oversized or unordered fragment: start=%d bytes=%d", input.ByteStart, len(input.Fragment))
			}
			recovered.WriteString(input.Fragment)
			fragments++
			return summaryResponse(fmt.Sprintf(`{"summary":"evidence %d"}`, fragments)), nil
		}
		if err := json.Unmarshal([]byte(messageText(request.Messages[1])), &final); err != nil {
			t.Fatal(err)
		}
		return summaryResponse(`{"summary":"all source was processed","operations":[]}`), nil
	})
	budget := &callBudget{limit: r.cfg.maxSummaryPasses}
	output, _, _, err := r.summarizeSegment(context.Background(), protocolInvocation(), nil, source, budget)
	if err != nil {
		t.Fatal(err)
	}
	wantSource, err := json.Marshal(projectMessages(source))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.String() != string(wantSource) || fragments != 6 || calls != 7 || budget.used != calls || budget.limit != 8 {
		t.Fatalf("fragments=%d calls=%d budget=%#v recovered=%d want=%d", fragments, calls, budget, recovered.Len(), len(wantSource))
	}
	if output.Summary == "" || len(final.OrderedEvidence) != fragments || len(final.Source) != 0 {
		t.Fatalf("output=%#v final=%#v", output, final)
	}
}

func TestProtocolFragmentGrowthChecksNonmonotonicCounts(t *testing.T) {
	t.Parallel()
	source := strings.Repeat("\u4e16", 200)
	var recovered strings.Builder
	countedInputs := make(map[string]bool)
	grewToFit := false
	fragmentTokens := func(size int) int64 {
		switch size {
		case 300:
			return 1
		case 375:
			return 10
		default:
			return int64(size + 50)
		}
	}
	r := newProtocolCompactor(t, nil)
	r.cfg.summaryInputTokens = 180
	r.counter = protocolCounterFunc(func(_ context.Context, request usage.CountRequest) (usage.CountResult, error) {
		encoded := messageText(request.Invocation.Messages[1])
		var input evidenceInput
		if err := json.Unmarshal([]byte(encoded), &input); err != nil {
			t.Fatal(err)
		}
		countedInputs[encoded] = true
		return protocolCountResult(request.Invocation, fragmentTokens(len(input.Fragment))), nil
	})
	r.model = protocolModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		encoded := messageText(request.Messages[1])
		var input evidenceInput
		if err := json.Unmarshal([]byte(encoded), &input); err != nil {
			t.Fatal(err)
		}
		if !countedInputs[encoded] || fragmentTokens(len(input.Fragment)) > r.cfg.summaryInputTokens {
			t.Fatalf("unverified or oversized fragment: bytes=%d", len(input.Fragment))
		}
		if input.ByteStart != recovered.Len() || !utf8.ValidString(input.Fragment) {
			t.Fatalf("unordered or invalid UTF-8 fragment: start=%d", input.ByteStart)
		}
		if len(input.Fragment) == 375 {
			grewToFit = true
		}
		recovered.WriteString(input.Fragment)
		return summaryResponse(`{"summary":"evidence"}`), nil
	})
	budget := &callBudget{limit: 8}
	_, err := r.summarizeFragments(context.Background(), protocolInvocation(), source, "conversation_json_fragment", budget)
	if err != nil || recovered.String() != source || !grewToFit {
		t.Fatalf("recovered=%d wanted=%d grew=%v err=%v", recovered.Len(), len(source), grewToFit, err)
	}
}
