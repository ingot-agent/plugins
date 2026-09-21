package modelruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/pipeline"
)

type liveSource struct {
	entries atomic.Pointer[[]model.ProviderEntry]
	calls   atomic.Int32
}

func (s *liveSource) set(entries ...model.ProviderEntry) {
	owned := slices.Clone(entries)
	s.entries.Store(&owned)
}

func (s *liveSource) Snapshot(ctx context.Context) ([]model.ProviderEntry, error) {
	s.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if entries := s.entries.Load(); entries != nil {
		return slices.Clone(*entries), nil
	}
	return nil, nil
}

type liveProvider string

func (p liveProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText(string(p))}}, nil
}

func (p liveProvider) Stream(ctx context.Context, request model.Request, handler model.StreamHandler) (model.Response, error) {
	for _, event := range []model.StreamEvent{
		{Kind: model.StreamPartStart, PartKind: content.KindText},
		{Kind: model.StreamPartDelta, TextDelta: string(p)},
		{Kind: model.StreamPartEnd},
	} {
		if err := handler(event); err != nil {
			return model.Response{}, err
		}
	}
	return p.Complete(ctx, request)
}

func liveEntry(name, label string) model.ProviderEntry {
	provider := liveProvider(label)
	return model.ProviderEntry{Name: name, Complete: provider.Complete, Stream: provider.Stream}
}

func liveAnswer(provider, modelName string) interaction.Response {
	return interaction.Response{Values: []interaction.Answer{
		{Name: "default_provider", Value: interaction.StringValue(provider)},
		{Name: "default_model", Value: interaction.StringValue(modelName)},
	}}
}

func setLiveDefaults(t *testing.T, exports Exports, provider, modelName string) {
	t.Helper()
	channel := &setupTestChannel{respond: func(interaction.Request) (interaction.Response, error) {
		return liveAnswer(provider, modelName), nil
	}}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Output) != `{"restart_required":false}` {
		t.Fatalf("config result = %s", result.Output)
	}
}

func assertLiveSelection(t *testing.T, exports Exports, provider, modelName, label string) {
	t.Helper()
	resolved, err := exports.Resolver.ResolveRequest(context.Background(), model.Request{})
	if err != nil || resolved.Provider != provider || resolved.Model != modelName {
		t.Fatalf("resolved = %#v, err = %v", resolved, err)
	}
	complete, err := exports.Runtime.Complete(context.Background(), model.Request{})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := exports.Streaming.Stream(context.Background(), model.Request{}, func(model.StreamEvent) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []model.Response{complete, stream} {
		if response.Provider != provider || response.Model != modelName || response.Message.Content[0].Text != label {
			t.Fatalf("response = %#v", response)
		}
	}
}

func TestLiveInitialConfigurationAndDefaultSwitch(t *testing.T) {
	source := &liveSource{}
	scope := setupTestScope{dir: t.TempDir()}
	deps := Dependencies{State: scope, ProviderSources: []model.ProviderSource{source}}
	exports, _, err := New(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Resolver.ResolveRequest(context.Background(), model.Request{Model: "m"}); !errors.Is(err, model.ErrProviderNotFound) {
		t.Fatalf("unconfigured resolver error = %v", err)
	}
	source.set(liveEntry("a", "first"))
	setLiveDefaults(t, exports, "", "model-a")
	assertLiveSelection(t, exports, "a", "model-a", "first")

	source.set(liveEntry("a", "first"), liveEntry("b", "second"))
	if _, err := exports.Resolver.ResolveRequest(context.Background(), model.Request{}); !errors.Is(err, model.ErrProviderNotFound) {
		t.Fatalf("automatic selection with two providers = %v", err)
	}
	for _, name := range []string{"b", "a", "b"} {
		setLiveDefaults(t, exports, name, "model-"+name)
		label := "first"
		if name == "b" {
			label = "second"
		}
		assertLiveSelection(t, exports, name, "model-"+name, label)
	}
	restarted, _, err := New(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	assertLiveSelection(t, restarted, "b", "model-b", "second")
	explicit, err := exports.Resolver.ResolveRequest(context.Background(), model.Request{Provider: "a", Model: "explicit"})
	if err != nil || explicit.Provider != "a" || explicit.Model != "explicit" {
		t.Fatalf("explicit request = %#v, %v", explicit, err)
	}
}

func TestLiveMissingDefaultCanBeRepairedAfterRestart(t *testing.T) {
	scope := setupTestScope{dir: t.TempDir()}
	if err := saveConfig(scope.Dir(), Config{DefaultProvider: "removed", DefaultModel: "old-model"}); err != nil {
		t.Fatal(err)
	}
	source := &liveSource{}
	source.set(liveEntry("new", "new"))
	exports, _, err := New(context.Background(), Dependencies{State: scope, ProviderSources: []model.ProviderSource{source}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Complete(context.Background(), model.Request{}); !errors.Is(err, model.ErrProviderNotFound) {
		t.Fatalf("missing default error = %v", err)
	}
	if _, err := exports.Runtime.Complete(context.Background(), model.Request{Provider: "new"}); err != nil {
		t.Fatalf("explicit provider was blocked by unused default: %v", err)
	}
	channel := &setupTestChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		for _, field := range request.Fields {
			if field.Name == "default_provider" && (field.Default != nil || len(field.Options) != 2 || field.Options[1].Value != "new") {
				t.Fatalf("repair field = %#v", field)
			}
		}
		return liveAnswer("new", "new-model"), nil
	}}
	if _, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	assertLiveSelection(t, exports, "new", "new-model", "new")
}

func TestLiveDirectoryInvalidEntriesAreRecoverable(t *testing.T) {
	for _, entries := range [][]model.ProviderEntry{
		{liveEntry("duplicate", "a"), liveEntry("duplicate", "b")},
		{liveEntry("", "empty")},
		{liveEntry("\xff", "invalid")},
		{{Name: "missing-complete"}},
	} {
		source := &liveSource{}
		source.set(entries...)
		exports, _, err := New(context.Background(), Dependencies{State: setupTestScope{dir: t.TempDir()}, ProviderSources: []model.ProviderSource{source}})
		if err != nil {
			t.Fatalf("directory validation prevented startup: %v", err)
		}
		if _, err := exports.Resolver.ResolveRequest(context.Background(), model.Request{Model: "m"}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid directory error = %v", err)
		}
		source.set(liveEntry("fixed", "fixed"))
		setLiveDefaults(t, exports, "fixed", "m")
		assertLiveSelection(t, exports, "fixed", "m", "fixed")
	}
}

type liveSourceFunc func(context.Context) ([]model.ProviderEntry, error)

func (f liveSourceFunc) Snapshot(ctx context.Context) ([]model.ProviderEntry, error) {
	return f(ctx)
}

func TestLiveSourceConflictsAndErrorsCanRecover(t *testing.T) {
	unavailable := errors.New("source unavailable")
	for _, sourceError := range []error{nil, unavailable} {
		t.Run(fmt.Sprint(sourceError), func(t *testing.T) {
			first := &liveSource{}
			first.set(liveEntry("p", "first"))
			broken := true
			second := liveSourceFunc(func(context.Context) ([]model.ProviderEntry, error) {
				if !broken {
					return nil, nil
				}
				return []model.ProviderEntry{liveEntry("p", "second")}, sourceError
			})
			exports, _, err := New(context.Background(), Dependencies{
				State: setupTestScope{dir: t.TempDir()}, ProviderSources: []model.ProviderSource{first, second},
			})
			if err != nil {
				t.Fatal(err)
			}
			wantErr := sourceError
			if wantErr == nil {
				wantErr = ErrInvalidConfig
			}
			_, resolveErr := exports.Resolver.ResolveRequest(context.Background(), model.Request{Provider: "p", Model: "m"})
			_, completeErr := exports.Runtime.Complete(context.Background(), model.Request{Provider: "p", Model: "m"})
			_, streamErr := exports.Streaming.Stream(context.Background(), model.Request{Provider: "p", Model: "m"}, func(model.StreamEvent) error { return nil })
			for _, err := range []error{resolveErr, completeErr, streamErr} {
				if !errors.Is(err, wantErr) {
					t.Fatalf("directory error = %v, want %v", err, wantErr)
				}
			}
			broken = false
			setLiveDefaults(t, exports, "p", "m")
			assertLiveSelection(t, exports, "p", "m", "first")
		})
	}
}

type liveInterceptor func(context.Context, model.Request, pipeline.Next[model.Request, model.Response]) (model.Response, error)

func (f liveInterceptor) Invoke(ctx context.Context, req model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
	return f(ctx, req, next)
}

type liveStreamInterceptor func(context.Context, model.Request, model.StreamHandler, model.StreamNext) (model.Response, error)

func (f liveStreamInterceptor) InvokeStream(ctx context.Context, req model.Request, handler model.StreamHandler, next model.StreamNext) (model.Response, error) {
	return f(ctx, req, handler, next)
}

func TestLiveInvocationKeepsSnapshotThroughInterceptorRewrite(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			source := &liveSource{}
			source.set(liveEntry("a", "original-a"), liveEntry("b", "original-b"))
			started, resume := make(chan struct{}), make(chan struct{})
			var once sync.Once
			pause := func() {
				once.Do(func() {
					close(started)
					select {
					case <-resume:
					case <-ctx.Done():
					}
				})
			}
			deps := Dependencies{State: setupTestScope{dir: t.TempDir()}, ProviderSources: []model.ProviderSource{source}}
			deps.Interceptors = []model.Interceptor{liveInterceptor(func(ctx context.Context, req model.Request, next pipeline.Next[model.Request, model.Response]) (model.Response, error) {
				pause()
				req.Provider = "b"
				return next(ctx, req)
			})}
			deps.StreamInterceptors = []model.StreamInterceptor{liveStreamInterceptor(func(ctx context.Context, req model.Request, handler model.StreamHandler, next model.StreamNext) (model.Response, error) {
				pause()
				req.Provider = "b"
				return next(ctx, req, handler)
			})}
			exports, _, err := New(context.Background(), deps)
			if err != nil {
				t.Fatal(err)
			}
			invoke := func() (model.Response, error) {
				req := model.Request{Provider: "a", Model: "m"}
				if streaming {
					return exports.Streaming.Stream(ctx, req, func(model.StreamEvent) error { return nil })
				}
				return exports.Runtime.Complete(ctx, req)
			}
			type outcome struct {
				response model.Response
				err      error
			}
			done := make(chan outcome, 1)
			go func() { response, err := invoke(); done <- outcome{response, err} }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("model invocation did not reach interceptor")
			}
			source.set(liveEntry("a", "updated-a"), liveEntry("b", "updated-b"))
			close(resume)
			first := <-done
			if first.err != nil || first.response.Message.Content[0].Text != "original-b" || source.calls.Load() != 1 {
				t.Fatalf("first = %#v, snapshots = %d", first, source.calls.Load())
			}
			second, err := invoke()
			if err != nil || second.Message.Content[0].Text != "updated-b" || source.calls.Load() != 2 {
				t.Fatalf("second = %#v, err = %v, snapshots = %d", second, err, source.calls.Load())
			}
		})
	}
}

func TestLiveDefaultsRejectStaleDirectoryAndFailedCommit(t *testing.T) {
	for _, failure := range []string{"directory", "cancellation", "conflict", "write"} {
		t.Run(failure, func(t *testing.T) {
			source := &liveSource{}
			source.set(liveEntry("a", "a"), liveEntry("b", "b"))
			scope := setupTestScope{dir: t.TempDir()}
			exports, _, err := New(context.Background(), Dependencies{State: scope, ProviderSources: []model.ProviderSource{source}})
			if err != nil {
				t.Fatal(err)
			}
			setLiveDefaults(t, exports, "a", "model-a")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			channel := &setupTestChannel{respond: func(interaction.Request) (interaction.Response, error) {
				switch failure {
				case "directory":
					source.set(liveEntry("a", "a"))
				case "cancellation":
					cancel()
				case "conflict":
					setLiveDefaults(t, exports, "a", "concurrent")
				case "write":
					if err := os.Chmod(scope.Dir(), 0o500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(scope.Dir(), 0o700) })
					if probe, err := os.CreateTemp(scope.Dir(), ".write-probe-"); err == nil {
						_ = probe.Close()
						_ = os.Remove(probe.Name())
						t.Skip("filesystem permissions do not prevent writing")
					}
				}
				return liveAnswer("b", "model-b"), nil
			}}
			_, err = exports.Operations[0].Invoke(ctx, operation.Request{Interaction: channel})
			if err == nil {
				t.Fatal("expected failed update")
			}
			if failure == "conflict" && !errors.Is(err, ErrConfigConflict) {
				t.Fatalf("conflict = %v", err)
			}
			if failure == "cancellation" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation = %v", err)
			}
			modelName := "model-a"
			if failure == "conflict" {
				modelName = "concurrent"
			}
			assertLiveSelection(t, exports, "a", modelName, "a")
			stored, err := loadConfig(scope.Dir())
			if err != nil || stored != (Config{DefaultProvider: "a", DefaultModel: modelName}) {
				t.Fatalf("stored = %#v, err = %v", stored, err)
			}
		})
	}
}

func TestLiveConcurrentDefaultAndProviderUpdates(t *testing.T) {
	source := &liveSource{}
	source.set(liveEntry("a", "a"), liveEntry("b", "b"))
	exports, _, err := New(context.Background(), Dependencies{State: setupTestScope{dir: filepath.Join(t.TempDir(), "state")}, ProviderSources: []model.ProviderSource{source}})
	if err != nil {
		t.Fatal(err)
	}
	setLiveDefaults(t, exports, "a", "model-a")
	var readers sync.WaitGroup
	for reader := 0; reader < 6; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 150; i++ {
				var provider, modelName string
				var callErr error
				switch i % 3 {
				case 0:
					resolved, err := exports.Resolver.ResolveRequest(context.Background(), model.Request{})
					provider, modelName, callErr = resolved.Provider, resolved.Model, err
				case 1:
					response, err := exports.Runtime.Complete(context.Background(), model.Request{})
					provider, modelName, callErr = response.Provider, response.Model, err
				case 2:
					response, err := exports.Streaming.Stream(context.Background(), model.Request{}, func(model.StreamEvent) error { return nil })
					provider, modelName, callErr = response.Provider, response.Model, err
				}
				if callErr != nil || (provider != "a" && provider != "b") || modelName != "model-"+provider {
					t.Errorf("inconsistent selection: %q %q, err = %v", provider, modelName, callErr)
					return
				}
			}
		}()
	}
	for i := 0; i < 24; i++ {
		source.set(liveEntry("a", fmt.Sprint(i)), liveEntry("b", fmt.Sprint(i)))
		name := "a"
		if i%2 != 0 {
			name = "b"
		}
		setLiveDefaults(t, exports, name, "model-"+name)
	}
	readers.Wait()
}
