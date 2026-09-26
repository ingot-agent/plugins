// Package sessiontree implements persistent single-Turn child Session
// management for agent.default.
package sessiontree

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/plugins/agent-default/sessioncontrol"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/workspace"
)

const (
	childTaskTimeout  = 30 * time.Minute
	settlementTimeout = 10 * time.Second
)

var ErrInvalidConfig = errors.New("invalid agent.default session-tree config")

// Dependencies are optional until subagents.toml enables at least one child
// type. This preserves compositions whose Session provider implements only the
// base SDK contracts.
type Dependencies struct {
	State      state.Scope
	Repository ingotabi.Optional[agent.ChildSessionRepository]
	Workspace  ingotabi.Optional[workspace.Manager]
}

// Exports provides public child management, per-Session prompt contribution,
// and the private Runtime scheduling capability.
type Exports struct {
	Children    agent.Children
	Contributor prompt.Contributor
	Control     sessioncontrol.Control
}

type tree struct {
	repository agent.ChildSessionRepository
	workspace  workspace.Manager
	config     configuration
	startupCtx context.Context

	mu           sync.Mutex
	nextToken    uint64
	active       map[session.ID]*executionState
	ready        []session.ID
	notify       chan struct{}
	closed       bool
	childActive  int
	reserved     int
	rootGates    map[session.ID]*sync.Mutex
	blockedRoots map[session.ID]bool
}

type executionState struct {
	handle     sessioncontrol.Handle
	rootID     session.ID
	parentID   session.ID
	depth      uint32
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	definition agent.ChildDefinition

	closing   bool
	started   bool
	settled   bool
	candidate *sessioncontrol.FinishIntent
	final     agent.ChildSnapshot
	settleErr error
}

// New loads subagents.toml, validates optional storage capabilities when the
// feature is enabled, and settles stale process-local states before accepting
// new work.
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil || isNil(deps.State) {
		return Exports{}, nil, fmt.Errorf("construct session tree: state is required: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	config, err := loadConfiguration(deps.State.Dir())
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct session tree: %w: %w", err, ErrInvalidConfig)
	}
	var repository agent.ChildSessionRepository
	var workspaceManager workspace.Manager
	if deps.Repository.Valid {
		if isNil(deps.Repository.Value) {
			return Exports{}, nil, fmt.Errorf("child repository is typed nil: %w", ErrInvalidConfig)
		}
		repository = deps.Repository.Value
	}
	if deps.Workspace.Valid {
		if isNil(deps.Workspace.Value) {
			return Exports{}, nil, fmt.Errorf("workspace manager is typed nil: %w", ErrInvalidConfig)
		}
		workspaceManager = deps.Workspace.Value
	}
	if config.enabled && (repository == nil || workspaceManager == nil) {
		return Exports{}, nil, fmt.Errorf("enabled child agents require child Session storage and workspace management: %w", agent.ErrChildUnsupported)
	}
	created := &tree{
		repository:   repository,
		workspace:    workspaceManager,
		config:       config,
		startupCtx:   ctx,
		active:       make(map[session.ID]*executionState),
		notify:       make(chan struct{}, 1),
		rootGates:    make(map[session.ID]*sync.Mutex),
		blockedRoots: make(map[session.ID]bool),
	}
	if config.enabled {
		if err := repository.RecoverChildSessions(ctx, agent.ChildRecoveryRequest{Reason: "runtime_restart"}); err != nil {
			return Exports{}, nil, fmt.Errorf("recover child sessions: %w", err)
		}
	}
	cleanup := ingotabi.Cleanup(func(cleanupCtx context.Context) error {
		return created.Shutdown(cleanupCtx)
	})
	return Exports{Children: created, Contributor: created, Control: created}, cleanup, nil
}

func (t *tree) signal() {
	select {
	case t.notify <- struct{}{}:
	default:
	}
}

func (t *tree) gate(rootID session.ID) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	gate := t.rootGates[rootID]
	if gate == nil {
		gate = &sync.Mutex{}
		t.rootGates[rootID] = gate
	}
	return gate
}

func (t *tree) removeReadyLocked(id session.ID) {
	for index, queued := range t.ready {
		if queued != id {
			continue
		}
		copy(t.ready[index:], t.ready[index+1:])
		t.ready = t.ready[:len(t.ready)-1]
		return
	}
}

func (t *tree) closeExecutionLocked(exec *executionState, snapshot agent.ChildSnapshot, settleErr error) {
	if exec.settled {
		return
	}
	exec.settled = true
	exec.final = cloneSnapshot(snapshot)
	exec.settleErr = settleErr
	if current := t.active[exec.handle.SessionID]; current == exec {
		delete(t.active, exec.handle.SessionID)
	}
	if exec.handle.Child {
		t.removeReadyLocked(exec.handle.SessionID)
		if t.childActive > 0 {
			t.childActive--
		}
	}
	if exec.cancel != nil {
		exec.cancel()
	}
	close(exec.done)
}

func cloneSnapshot(value agent.ChildSnapshot) agent.ChildSnapshot {
	value.State = clonePointer(value.State)
	value.ExecutionStopped = clonePointer(value.ExecutionStopped)
	value.HasResult = clonePointer(value.HasResult)
	value.Result = clonePointer(value.Result)
	value.Error = clonePointer(value.Error)
	value.PreviousState = clonePointer(value.PreviousState)
	value.StartedAt = clonePointer(value.StartedAt)
	value.FinishedAt = clonePointer(value.FinishedAt)
	return value
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

var (
	_ agent.Children         = (*tree)(nil)
	_ prompt.Contributor     = (*tree)(nil)
	_ sessioncontrol.Control = (*tree)(nil)
)
