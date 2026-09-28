package agentdefault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"unicode/utf8"

	"github.com/ingot-agent/plugins/app-webui/modelselection"
	"github.com/ingot-agent/sdk/model"
)

var _ modelselection.Controller = (*runtime)(nil)

func (r *runtime) Snapshot(ctx context.Context) (modelselection.Snapshot, error) {
	if ctx == nil {
		return modelselection.Snapshot{}, fmt.Errorf("model selection: nil context")
	}
	if err := ctx.Err(); err != nil {
		return modelselection.Snapshot{}, err
	}
	return r.selectionSnapshot(ctx, *r.config.Load())
}

func (r *runtime) selectionSnapshot(ctx context.Context, cfg Config) (modelselection.Snapshot, error) {
	result := modelselection.Snapshot{Providers: []modelselection.Provider{}}
	seen := make(map[string]bool)
	for _, source := range r.selectionSources {
		entries, err := source.Snapshot(ctx)
		if err != nil {
			return modelselection.Snapshot{}, err
		}
		for _, entry := range entries {
			if entry.Name == "" || !utf8.ValidString(entry.Name) || entry.Complete == nil || seen[entry.Name] {
				return modelselection.Snapshot{}, fmt.Errorf("invalid or duplicate provider %q: %w", entry.Name, modelselection.ErrInvalid)
			}
			seen[entry.Name] = true
			provider := modelselection.Provider{Name: entry.Name, Models: []modelselection.Model{}}
			models := make(map[string]bool)
			for _, candidate := range entry.Models {
				if candidate.Name == "" || !utf8.ValidString(candidate.Name) || models[candidate.Name] {
					return modelselection.Snapshot{}, fmt.Errorf("invalid or duplicate model %q: %w", candidate.Name, modelselection.ErrInvalid)
				}
				models[candidate.Name] = true
				item := modelselection.Model{Name: candidate.Name, ReasoningEfforts: []string{}}
				for _, effort := range candidate.ReasoningEfforts {
					if !effort.Valid() || slices.Contains(item.ReasoningEfforts, string(effort)) {
						return modelselection.Snapshot{}, fmt.Errorf("invalid reasoning effort %q: %w", effort, modelselection.ErrInvalid)
					}
					item.ReasoningEfforts = append(item.ReasoningEfforts, string(effort))
				}
				provider.Models = append(provider.Models, item)
			}
			result.Providers = append(result.Providers, provider)
		}
	}
	if r.resolver.Valid {
		request := model.Request{Provider: cfg.Provider, Model: cfg.Model, ReasoningEffort: cfg.ReasoningEffort}
		resolved, err := r.resolver.Value.ResolveRequest(ctx, request)
		if err == nil {
			effort := resolved.ReasoningEffort
			if effort == "" {
				effort = model.ReasoningEffortProviderDefault
			}
			result.Current = modelselection.Selection{Provider: resolved.Provider, Model: resolved.Model, ReasoningEffort: string(effort)}
			result.Configured = true
		} else if ctx.Err() != nil {
			return modelselection.Snapshot{}, ctx.Err()
		}
	} else if cfg.Provider != "" && cfg.Model != "" && cfg.ReasoningEffort != "" {
		result.Current = modelselection.Selection{Provider: cfg.Provider, Model: cfg.Model, ReasoningEffort: string(cfg.ReasoningEffort)}
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
	latest, err := loadConfig(r.selectionState.Dir())
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	latest, err = normalizeConfig(latest, nil)
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	if !reflect.DeepEqual(current, latest) {
		return modelselection.Snapshot{}, modelselection.ErrConflict
	}
	snapshot, err := r.selectionSnapshot(ctx, current)
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	if expectedRevision == "" || expectedRevision != snapshot.Revision {
		return modelselection.Snapshot{}, modelselection.ErrConflict
	}
	valid := false
	for _, provider := range snapshot.Providers {
		if provider.Name != choice.Provider {
			continue
		}
		for _, candidate := range provider.Models {
			if candidate.Name == choice.Model && (choice.ReasoningEffort == string(model.ReasoningEffortProviderDefault) || slices.Contains(candidate.ReasoningEfforts, choice.ReasoningEffort)) {
				valid = true
			}
		}
	}
	if !valid {
		return modelselection.Snapshot{}, modelselection.ErrInvalid
	}
	updated := current
	updated.Provider = choice.Provider
	updated.Model = choice.Model
	updated.ReasoningEffort = model.ReasoningEffort(choice.ReasoningEffort)
	if updated, err = normalizeConfig(updated, nil); err != nil {
		return modelselection.Snapshot{}, err
	}
	updatedSnapshot, err := r.selectionSnapshot(ctx, updated)
	if err != nil {
		return modelselection.Snapshot{}, err
	}
	if !updatedSnapshot.Configured || updatedSnapshot.Current != choice {
		return modelselection.Snapshot{}, modelselection.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return modelselection.Snapshot{}, err
	}
	if err := saveConfig(r.selectionState.Dir(), updated); err != nil {
		return modelselection.Snapshot{}, err
	}
	r.config.Store(&updated)
	return updatedSnapshot, nil
}
