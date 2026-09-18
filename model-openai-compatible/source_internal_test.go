package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/operation"
)

type liveTestHTTP func(context.Context, *http.Request) (*http.Response, error)

func (f liveTestHTTP) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return f(ctx, request)
}

func liveEntries(t *testing.T, exports Exports) []model.ProviderEntry {
	t.Helper()
	if exports.Source == nil {
		t.Fatal("missing provider source")
	}
	entries, err := exports.Source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func liveAnswer(config Config) interaction.Response {
	value := providerListValue(config.Providers)
	for i := range value.Items {
		for j := range value.Items[i].Entries {
			entry := &value.Items[i].Entries[j]
			switch entry.Name {
			case "source":
				entry.Value = interaction.StringValue("")
			case "api_key_action":
				entry.Value = interaction.StringValue(apiKeyClear)
			}
		}
	}
	return interaction.Response{Values: []interaction.Answer{{Name: "providers", Value: value}}}
}

func invokeLiveConfig(ctx context.Context, exports Exports, config Config) (operation.Result, error) {
	channel := &setupTestChannel{respond: func(interaction.Request) (interaction.Response, error) {
		return liveAnswer(config), nil
	}}
	return exports.Operations[0].Invoke(ctx, operation.Request{Interaction: channel})
}

func TestProviderSourceStartsEmptyAndPublishesSetup(t *testing.T) {
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	exports, _, err := New(context.Background(), Dependencies{HTTP: setupTestHTTP{}, Assets: setupTestAssets{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	source := exports.Source
	if got := liveEntries(t, exports); len(got) != 0 {
		t.Fatalf("initial providers = %#v", got)
	}
	for count := 1; count <= 2; count++ {
		config := Config{Providers: []ProviderConfig{{Name: "primary", BaseURL: "https://primary.test"}}}
		if count == 2 {
			config.Providers = append(config.Providers, ProviderConfig{Name: "fallback", BaseURL: "https://fallback.test"})
		}
		result, err := invokeLiveConfig(context.Background(), exports, config)
		if err != nil {
			t.Fatal(err)
		}
		if string(result.Output) != fmt.Sprintf("{\"providers\":%d,\"restart_required\":false}", count) {
			t.Fatalf("output = %s", result.Output)
		}
		entries := liveEntries(t, exports)
		if exports.Source != source || len(entries) != count || entries[0].Name != "primary" {
			t.Fatalf("live providers = %#v", entries)
		}
		stored, err := loadConfig(scope.Dir())
		if err != nil || len(stored.Providers) != count || stored.Providers[0].Name != "primary" || stored.Providers[0].BaseURL != "https://primary.test" {
			t.Fatalf("persisted config = %#v, error = %v", stored, err)
		}
		entries[0] = model.ProviderEntry{Name: "caller-mutation"}
		if fresh := liveEntries(t, exports); fresh[0].Name != "primary" || fresh[0].Complete == nil || fresh[0].Stream == nil {
			t.Fatalf("caller mutated source entries: %#v", fresh)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Snapshot(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot error = %v", err)
	}
}

func TestProviderSourceUpdatePreservesInflightAndOldHandles(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	client := liveTestHTTP(func(ctx context.Context, request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "old.test":
			if request.Header.Get("Authorization") != "Bearer old-secret" {
				return nil, errors.New("old provider lost its API key")
			}
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case "new.test":
			if request.Header.Get("Authorization") != "Bearer new-secret" {
				return nil, errors.New("new provider did not adopt its API key")
			}
		default:
			return nil, fmt.Errorf("unexpected endpoint: %s", request.URL)
		}
		if request.URL.Path != "/v1/chat/completions" {
			return nil, fmt.Errorf("unexpected path: %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"completion","model":"m","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))}, nil
	})
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	initial := Config{Providers: []ProviderConfig{{Name: "old", BaseURL: "https://old.test/v1", APIKey: "old-secret", Models: []string{"old-model"}}}}
	if err := saveConfig(scope.Dir(), initial); err != nil {
		t.Fatal(err)
	}
	exports, _, err := New(ctx, Dependencies{HTTP: client, Assets: setupTestAssets{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	old := liveEntries(t, exports)[0]
	done := make(chan error, 1)
	go func() {
		result, err := old.Complete(ctx, model.Request{Model: "old-model"})
		if err == nil && result.Provider != "old" {
			err = fmt.Errorf("inflight result provider = %q", result.Provider)
		}
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	channel := &setupTestChannel{respond: func(request interaction.Request) (interaction.Response, error) {
		item := findSetupField(t, request.Fields, "providers").Default.Items[0]
		setObjectString(t, &item, "name", "new")
		setObjectString(t, &item, "base_url", "https://new.test/v1")
		setObjectString(t, &item, "api_key_action", apiKeyReplace)
		item.Entries = append(item.Entries, interaction.Entry{Name: "api_key", Value: interaction.StringValue("new-secret")})
		setObjectValue(t, &item, "models", interaction.ListValue([]interaction.Value{interaction.StringValue("new-model")}))
		return interaction.Response{Values: []interaction.Answer{{Name: "providers", Value: interaction.ListValue([]interaction.Value{item})}}}, nil
	}}
	if _, err := exports.Operations[0].Invoke(ctx, operation.Request{Interaction: channel}); err != nil {
		t.Fatal(err)
	}
	entries := liveEntries(t, exports)
	if len(entries) != 1 || entries[0].Name != "new" {
		t.Fatalf("updated providers = %#v", entries)
	}
	stored, err := loadConfig(scope.Dir())
	if err != nil || stored.Providers[0].Name != "new" || stored.Providers[0].APIKey != "new-secret" {
		t.Fatalf("persisted update = %#v, error = %v", stored, err)
	}
	if _, err := entries[0].Complete(ctx, model.Request{Model: "old-model"}); !errors.Is(err, model.ErrModelNotFound) {
		t.Fatalf("old model on replacement provider error = %v", err)
	}
	if result, err := entries[0].Complete(ctx, model.Request{Model: "new-model"}); err != nil || result.Provider != "new" {
		t.Fatalf("new provider result = %#v, error = %v", result, err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if result, err := old.Complete(ctx, model.Request{Model: "old-model"}); err != nil || result.Provider != "old" {
		t.Fatalf("old handle result = %#v, error = %v", result, err)
	}
}

func TestProviderSourceFailedSetupRetainsSnapshot(t *testing.T) {
	for _, failure := range []string{"invalid", "empty", "cancel", "conflict"} {
		t.Run(failure, func(t *testing.T) {
			scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
			initial := Config{Providers: []ProviderConfig{{Name: "original", BaseURL: "https://original.test"}}}
			if err := saveConfig(scope.Dir(), initial); err != nil {
				t.Fatal(err)
			}
			exports, _, err := New(context.Background(), Dependencies{HTTP: setupTestHTTP{}, Assets: setupTestAssets{}, State: scope})
			if err != nil {
				t.Fatal(err)
			}
			before := liveEntries(t, exports)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantErr := ErrInvalidConfig
			channel := &setupTestChannel{respond: func(interaction.Request) (interaction.Response, error) {
				candidate := Config{Providers: []ProviderConfig{{Name: "replacement", BaseURL: "https://replacement.test"}}}
				switch failure {
				case "invalid":
					candidate.Providers[0].BaseURL = "file:///not-http"
				case "empty":
					candidate.Providers = nil
				case "cancel":
					cancel()
					wantErr = context.Canceled
				case "conflict":
					external := Config{Providers: []ProviderConfig{{Name: "external", BaseURL: "https://external.test"}}}
					if err := saveConfig(scope.Dir(), external); err != nil {
						t.Fatal(err)
					}
					wantErr = ErrConfigConflict
				}
				return liveAnswer(candidate), nil
			}}
			if _, err := exports.Operations[0].Invoke(ctx, operation.Request{Interaction: channel}); !errors.Is(err, wantErr) {
				t.Fatalf("setup error = %v, want %v", err, wantErr)
			}
			if after := liveEntries(t, exports); len(after) != len(before) || after[0].Name != before[0].Name || after[0].Complete == nil || after[0].Stream == nil {
				t.Fatalf("failed update published %#v", after)
			}
			stored, err := loadConfig(scope.Dir())
			if err != nil {
				t.Fatal(err)
			}
			wantName := "original"
			if failure == "conflict" {
				wantName = "external"
			}
			if len(stored.Providers) != 1 || stored.Providers[0].Name != wantName {
				t.Fatalf("failed update changed disk config: %#v", stored)
			}
		})
	}
}

func TestProviderSourceWriteFailureDoesNotPublish(t *testing.T) {
	scope := setupTestScope{dir: t.TempDir()}
	exports, _, err := New(context.Background(), Dependencies{HTTP: setupTestHTTP{}, Assets: setupTestAssets{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(scope.Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(scope.Dir(), 0o700) }()
	if file, err := os.CreateTemp(scope.Dir(), "permission-probe-"); err == nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		t.Skip("filesystem permits writes to a read-only directory")
	}
	config := Config{Providers: []ProviderConfig{{Name: "new", BaseURL: "https://new.test"}}}
	if _, err := invokeLiveConfig(context.Background(), exports, config); err == nil {
		t.Fatal("setup succeeded in read-only state directory")
	}
	if got := liveEntries(t, exports); len(got) != 0 {
		t.Fatalf("failed save published %#v", got)
	}
}

func TestProviderSourceConcurrentSnapshotsAndUpdates(t *testing.T) {
	scope := setupTestScope{dir: filepath.Join(t.TempDir(), "state")}
	exports, _, err := New(context.Background(), Dependencies{HTTP: setupTestHTTP{}, Assets: setupTestAssets{}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	errorsSeen := make(chan error, 8)
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				entries, err := exports.Source.Snapshot(context.Background())
				if err != nil {
					errorsSeen <- err
					return
				}
				for i, entry := range entries {
					if entry.Name == "" || entry.Complete == nil || entry.Stream == nil {
						errorsSeen <- fmt.Errorf("partial snapshot: %#v", entries)
						return
					}
					_, _ = entry.Complete(context.Background(), model.Request{Model: "m"})
					entries[i] = model.ProviderEntry{}
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		config := Config{Providers: []ProviderConfig{{Name: fmt.Sprintf("provider-%d", i), BaseURL: "https://example.test"}}}
		if _, err := invokeLiveConfig(context.Background(), exports, config); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	readers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
}
