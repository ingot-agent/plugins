// Package modelselection defines the optional model picker capability consumed
// by the Web application. Other plugins may implement it without sharing state.
package modelselection

import (
	"context"
	"errors"
)

var (
	ErrConflict = errors.New("model selection changed")
	ErrInvalid  = errors.New("invalid model selection")
)

// Selection is the effective choice for future turns. ReasoningEffort uses
// "providerDefault" to bypass model-runtime defaults; other nonempty values
// must be supported by the selected model.
type Selection struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
}

type Model struct {
	Name             string   `json:"name"`
	ReasoningEfforts []string `json:"reasoningEfforts"`
}

type Provider struct {
	Name   string  `json:"name"`
	Models []Model `json:"models"`
}

// Snapshot is a caller-owned, consistent directory and selection view.
// Configured is false when no effective provider and model can be resolved.
type Snapshot struct {
	Revision   string     `json:"revision"`
	Configured bool       `json:"configured"`
	Current    Selection  `json:"current"`
	Providers  []Provider `json:"providers"`
}

// Controller owns the selection applied to future turns. Update revalidates
// against the live directory and rejects a stale expected revision. The
// implementation is responsible for persistence and concurrent calls.
type Controller interface {
	Snapshot(context.Context) (Snapshot, error)
	Update(context.Context, Selection, string) (Snapshot, error)
}
