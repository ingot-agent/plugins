// Package sessioncontrol defines the private capability exchanged between the
// agent.default session-tree and runtime Components. It is intentionally not a
// public SDK contract.
package sessioncontrol

import (
	"context"
	"errors"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

// Handle identifies one in-process turn registration. Token prevents a late
// callback from a previous root Turn from attaching work to a later Turn of
// the same Session.
type Handle struct {
	SessionID session.ID
	Token     uint64
	Child     bool
}

// Task is one accepted child Session ready for the existing Agent Runtime.
// Context is owned by the session tree and remains independent of the parent
// Turn's normal completion.
type Task struct {
	Handle     Handle
	Context    context.Context
	Input      string
	ToolNames  []string
	SubmitTool string
}

// FinishIntent is a candidate final report accepted from the submit tool. It
// becomes formal only after the Runtime confirms the matching tool boundary
// and settlement is persisted.
type FinishIntent struct {
	ToolCallID string
	Result     string
}

// Control coordinates root Turn registration, child dispatch, result
// submission confirmation, and final settlement.
type Control interface {
	BeginRoot(context.Context, session.ID) (Handle, error)
	EndRoot(context.Context, Handle, bool) error
	Next(context.Context) (Task, error)
	FinishIntent(Handle, string) (FinishIntent, bool, error)
	Settle(context.Context, Handle, *FinishIntent, agent.Execution, error) error
	ValidateTools([]tool.Definition) error
	Shutdown(context.Context) error
}

var (
	// ErrClosed indicates that the Runtime is shutting down and no further work
	// can be accepted or dispatched.
	ErrClosed = errors.New("child session control closed")
	// ErrInvalidHandle indicates a stale or mismatched in-process Turn handle.
	ErrInvalidHandle = errors.New("invalid child session control handle")
)
