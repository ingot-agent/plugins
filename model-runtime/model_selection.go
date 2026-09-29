package modelruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/plugins/app-webui/modelselection"
	"github.com/ingot-agent/sdk/model"
)

var _ modelselection.Controller = (*runtime)(nil)

// Snapshot projects the runtime's defaults and live directory through the
// public contract owned by app-webui. No Agent configuration is involved.
func (r *runtime) Snapshot(ctx context.Context) (modelselection.Snapshot, error) {
	if ctx == nil {
		return modelselection.Snapshot{}, fmt.Errorf("model selection: nil context")
	}
	if err := ctx.Err(); err != nil {
		return modelselection.Snapshot{}, err
	}
	cfg := *r.config.Load()
	directory, err := r.snapshotWithConfig(ctx, cfg)
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	return selectionSnapshot(cfg, directory)
}

func selectionSnapshot(cfg Config, directory providerSnapshot) (modelselection.Snapshot, error) {
	result := modelselection.Snapshot{Providers: []modelselection.Provider{}}
	for _, name := range directory.names {
		entry := directory.providers[name]
		provider := modelselection.Provider{Name: name, Models: []modelselection.Model{}}
		for _, candidate := range entry.Models {
			item := modelselection.Model{Name: candidate.Name, ReasoningEfforts: []string{}}
			for _, effort := range candidate.ReasoningEfforts {
				item.ReasoningEfforts = append(item.ReasoningEfforts, string(effort))
			}
			provider.Models = append(provider.Models, item)
		}
		result.Providers = append(result.Providers, provider)
	}
	request := model.Request{}
	directory.applyDefaults(&request)
	if _, err := directory.selectProvider(request); err == nil {
		effort := request.ReasoningEffort
		if effort == "" {
			effort = model.ReasoningEffortProviderDefault
		}
		result.Current = modelselection.Selection{Provider: request.Provider, Model: request.Model, ReasoningEffort: string(effort)}
		result.Configured = true
	}
	data, err := json.Marshal(struct {
		Config    Config
		Providers []modelselection.Provider
	}{cfg, result.Providers})
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	digest := sha256.Sum256(data)
	result.Revision = hex.EncodeToString(digest[:])
	return result, nil
}

// Update shares the config Operation's lock, state file and active defaults.
// A picker snapshot cannot overwrite a newer Operation or file edit.
func (r *runtime) Update(ctx context.Context, choice modelselection.Selection, expectedRevision string) (modelselection.Snapshot, error) {
	if ctx == nil {
		return modelselection.Snapshot{}, fmt.Errorf("model selection: nil context")
	}
	if err := ctx.Err(); err != nil {
		return modelselection.Snapshot{}, err
	}
	configCommitMu.Lock()
	defer configCommitMu.Unlock()
	current := *r.config.Load()
	latest, err := loadConfig(r.scope.Dir())
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	if current != latest {
		return modelselection.Snapshot{}, modelselection.ErrConflict
	}
	directory, err := r.snapshotWithConfig(ctx, current)
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	snapshot, err := selectionSnapshot(current, directory)
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	if expectedRevision == "" || expectedRevision != snapshot.Revision {
		return modelselection.Snapshot{}, modelselection.ErrConflict
	}
	provider, exists := directory.providers[choice.Provider]
	if _, declared := findProviderModel(provider.Models, choice.Model); !exists || !declared || choice.ReasoningEffort == "" {
		return modelselection.Snapshot{}, modelselection.ErrInvalid
	}
	updated := Config{
		DefaultProvider: choice.Provider, DefaultModel: choice.Model,
		DefaultReasoningEffort: model.ReasoningEffort(choice.ReasoningEffort),
	}
	// In runtime configuration, an empty effort already means provider default.
	// The sentinel is only needed when a request bypasses a runtime default.
	if updated.DefaultReasoningEffort == model.ReasoningEffortProviderDefault {
		updated.DefaultReasoningEffort = ""
	}
	directory.defaults = updated
	request := model.Request{}
	directory.applyDefaults(&request)
	if _, err := directory.selectProvider(request); err != nil {
		return modelselection.Snapshot{}, fmt.Errorf("%w: %w", modelselection.ErrInvalid, err)
	}
	updatedSnapshot, err := selectionSnapshot(updated, directory)
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return modelselection.Snapshot{}, err
	}
	if err := saveConfig(r.scope.Dir(), updated); err != nil {
		return modelselection.Snapshot{}, err
	}
	r.config.Store(&updated)
	return updatedSnapshot, nil
}
