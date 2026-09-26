package contextcompact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/usage"
)

func TestCompactionRecoveryPartialFailureCannotAdvanceCheckpoint(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"call budget", "model failure"} {
		t.Run(failure, func(t *testing.T) {
			request := firstToolRound()
			original := cloneRequest(request)
			store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
			cause := errors.New("partial evidence model failed")
			calls := 0
			runtime := protocolModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
				calls++
				if messageText(request.Messages[0]) != evidenceSystemPrompt {
					t.Fatal("reached final summary before partial failure")
				}
				if len(store.entries["s"]) != 0 {
					t.Fatal("checkpoint saved after a partial summary")
				}
				if failure == "model failure" && calls == 2 {
					return model.Response{}, cause
				}
				return summaryResponse(`{"summary":"One partial fragment was processed."}`), nil
			})
			cfg := Config{TriggerInputTokens: 3000, TargetInputTokens: 1400, SummaryInputTokens: 3200, MaxSummaryPasses: 32}
			wantErr, wantCalls := cause, 2
			if failure == "call budget" {
				cfg.MaxSummaryPasses = 1
				wantErr, wantCalls = ErrContextUncompactable, 1
			}
			compactor := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, runtime, store)
			_, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
			if !errors.Is(err, wantErr) || calls != wantCalls {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if len(store.entries["s"]) != 0 || !reflect.DeepEqual(request, original) {
				t.Fatalf("failed partial processing changed history: checkpoints=%d", len(store.entries["s"]))
			}
		})
	}
}

func TestCompactionRecoverySplitRoundPersistsOneCompleteCheckpoint(t *testing.T) {
	t.Parallel()
	request := firstToolRound()
	request.Messages = append(request.Messages, model.Message{Role: model.RoleUser, Content: textContent("Continue from the findings.")})
	original := cloneRequest(request)
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	partialCalls, finalCalls := 0, 0
	counter := &canonicalTokenCounter{}
	cfg := Config{TriggerInputTokens: 3000, TargetInputTokens: 1400, SummaryInputTokens: 3200, MaxSummaryPasses: 32}
	runtime := protocolModelFunc(func(ctx context.Context, input model.Request) (model.Response, error) {
		count, err := counter.CountInput(ctx, usage.CountRequest{Invocation: input})
		if err != nil || count.InputTokens > cfg.SummaryInputTokens {
			t.Fatalf("summary input count=%d err=%v", count.InputTokens, err)
		}
		if len(store.entries["s"]) != 0 {
			t.Fatal("round checkpoint persisted before final summary completed")
		}
		if messageText(input.Messages[0]) == evidenceSystemPrompt {
			partialCalls++
			return summaryResponse(`{"summary":"Log evidence locates the failure in /tmp/build.log."}`), nil
		}
		if messageText(input.Messages[0]) != summarySystemPrompt {
			t.Fatal("unexpected rollup during a single round")
		}
		finalCalls++
		return summaryResponse(`{"summary":"Investigation found the failure in /tmp/build.log.","operations":[{"op":"set","path":"/file","value":"/tmp/build.log"}]}`), nil
	})
	compactor := newTokenTestCompactor(t, cfg, counter, runtime, store)
	result, err := compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if partialCalls < 2 || finalCalls != 1 || len(store.entries["s"]) != 1 || !result.Changed {
		t.Fatalf("partial=%d final=%d checkpoints=%d changed=%v", partialCalls, finalCalls, len(store.entries["s"]), result.Changed)
	}
	checkpoint, err := decodeCheckpoint(store.entries["s"][0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := messageDigest(request.Messages[1:4])
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Mode != checkpointModeSegment || checkpoint.CoveredMessages != 3 || checkpoint.SourceDigest != wantDigest || checkpoint.Sequence != 1 || checkpoint.ParentSequence != 0 {
		t.Fatalf("checkpoint does not cover exactly the complete tool round: %+v", checkpoint)
	}
	if len(checkpoint.Operations) != 1 || checkpoint.Operations[0].Path != "/file" {
		t.Fatalf("final state operations were not committed once: %+v", checkpoint.Operations)
	}
	if !reflect.DeepEqual(request, original) || !reflect.DeepEqual(result.Messages[len(result.Messages)-1], request.Messages[4]) {
		t.Fatal("raw history or incomplete user tail changed")
	}
	restartedModels := &fakeModel{err: errors.New("complete round must reuse its only checkpoint")}
	restarted := newTokenTestCompactor(t, cfg, counter, restartedModels, store)
	replay, err := restarted.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || !reflect.DeepEqual(replay, result) || len(restartedModels.requests) != 0 || len(store.entries["s"]) != 1 {
		t.Fatalf("replay=%+v calls=%d checkpoints=%d err=%v", replay, len(restartedModels.requests), len(store.entries["s"]), err)
	}
}

func TestCompactionRecoveryFailedRollupResumesAfterPersistedSegment(t *testing.T) {
	t.Parallel()
	request := firstToolRound()
	original := cloneRequest(request)
	obsolete, err := json.Marshal(strings.Repeat("obsolete", 230))
	if err != nil {
		t.Fatal(err)
	}
	segmentResponse := `{"summary":"Investigated the build.","operations":[{"op":"set","path":"/keep","value":{"file":"/tmp/build.log","line":7}},{"op":"set","path":"/obsolete","value":` + string(obsolete) + `}]}`
	models := &fakeModel{responses: []model.Response{summaryResponse(segmentResponse)}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	rollupErr := errors.New("rollup model temporarily unavailable")
	failedRollups := 0
	runtime := protocolModelFunc(func(ctx context.Context, input model.Request) (model.Response, error) {
		if messageText(input.Messages[0]) == rollupSystemPrompt {
			failedRollups++
			if len(store.entries["s"]) != 1 {
				t.Fatal("segment was not durably committed before rollup")
			}
			return model.Response{}, rollupErr
		}
		return models.Complete(ctx, input)
	})
	cfg := Config{TriggerInputTokens: 7000, TargetInputTokens: 5000, StateTriggerTokens: 1400, StateTargetTokens: 700}
	compactor := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, runtime, store)
	_, err = compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if !errors.Is(err, rollupErr) || failedRollups != 1 || len(models.requests) != 1 || len(store.entries["s"]) != 1 {
		t.Fatalf("segment calls=%d failed rollups=%d checkpoints=%d err=%v", len(models.requests), failedRollups, len(store.entries["s"]), err)
	}
	segmentPayload := cloneRaw(store.entries["s"][0].Payload)
	segment, err := decodeCheckpoint(segmentPayload)
	if err != nil || segment.Mode != checkpointModeSegment || segment.CoveredMessages != 3 {
		t.Fatalf("persisted segment=%+v err=%v", segment, err)
	}
	restartedModels := &fakeModel{responses: []model.Response{
		summaryResponse(`{"summary":"Build investigation remains open.","discard_paths":["/obsolete"]}`),
	}}
	restarted := newTokenTestCompactor(t, cfg, &canonicalTokenCounter{}, restartedModels, store)
	result, err := restarted.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if len(restartedModels.requests) != 1 || messageText(restartedModels.requests[0].Messages[0]) != rollupSystemPrompt {
		t.Fatalf("restart repeated the segment instead of only retrying rollup: %+v", restartedModels.requests)
	}
	if len(store.entries["s"]) != 2 || !bytes.Equal(store.entries["s"][0].Payload, segmentPayload) {
		t.Fatal("restart rewrote or duplicated the original segment checkpoint")
	}
	rollup, err := decodeCheckpoint(store.entries["s"][1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if rollup.Mode != checkpointModeRollup || rollup.Sequence != segment.Sequence+1 || rollup.ParentSequence != segment.Sequence || rollup.CoveredMessages != segment.CoveredMessages {
		t.Fatalf("resumed rollup is not linked to the persisted segment: %+v", rollup)
	}
	if len(rollup.StateSnapshot) != 1 || rollup.StateSnapshot[0].Path != "/keep" || !rawEqual(rollup.StateSnapshot[0].Value, json.RawMessage(`{"file":"/tmp/build.log","line":7}`)) {
		t.Fatalf("resumed rollup lost retained state: %+v", rollup.StateSnapshot)
	}
	if !result.Changed || strings.Contains(messagesText(result.Messages), "obsolete") || !reflect.DeepEqual(request, original) {
		t.Fatal("resumed result contains stale state or changed raw history")
	}
	replay, err := restarted.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil || !reflect.DeepEqual(replay, result) || len(restartedModels.requests) != 1 || len(store.entries["s"]) != 2 {
		t.Fatalf("completed rollup was not reused: calls=%d checkpoints=%d err=%v", len(restartedModels.requests), len(store.entries["s"]), err)
	}
}
