// Package skillruntime discovers, reads, and creates Agent Skills in
// plugin-scoped persistent state.
package skillruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/tool"
)

const (
	maxSkills          = 64
	maxCatalogBytes    = 48 * 1024
	maxSkillBytes      = 64 * 1024
	maxReferenceFiles  = 32
	maxReferenceBytes  = 64 * 1024
	maxSkillTotalBytes = 512 * 1024
	maxReferenceDepth  = 4
)

var (
	// ErrInvalidConfig indicates an invalid host state dependency.
	ErrInvalidConfig = errors.New("invalid skill.runtime config")
	// ErrInvalidArguments indicates malformed tool arguments.
	ErrInvalidArguments = errors.New("invalid skill tool arguments")
	// ErrInvalidSkill indicates malformed or unsupported Skill content.
	ErrInvalidSkill = errors.New("invalid skill")
	// ErrSkillNotFound indicates that a requested Skill is unavailable.
	ErrSkillNotFound = errors.New("skill not found")
	// ErrSkillExists indicates that create-only add_skill found an existing entry.
	ErrSkillExists = errors.New("skill already exists")
	// ErrSkillLimit indicates that a fixed Skill capacity limit was exceeded.
	ErrSkillLimit = errors.New("skill limit exceeded")
)

// Dependencies contains this Plugin's persistent state scope.
type Dependencies struct {
	State state.Scope
}

// Exports contains the catalog contributor, Skill tools, and status Operation.
type Exports struct {
	Contributors []prompt.Contributor
	Tools        []tool.Tool
	Operations   []operation.Operation
}

// New creates one live Skill registry. Skills are refreshed on every catalog,
// read, add, or status access, so external edits do not require a restart.
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil || isNil(deps.State) {
		return Exports{}, nil, fmt.Errorf("construct skill.runtime: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	stateDir := deps.State.Dir()
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return Exports{}, nil, fmt.Errorf("state directory must be absolute and non-empty: %w", ErrInvalidConfig)
	}
	root := filepath.Join(filepath.Clean(stateDir), "skills")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Exports{}, nil, fmt.Errorf("create skill root: %w", err)
	}
	builtins, err := loadBuiltinSkills()
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct skill.runtime: %w", err)
	}
	manager := newRegistry(root, builtins)
	if err := manager.Refresh(ctx); err != nil {
		return Exports{}, nil, fmt.Errorf("initialize skill registry: %w", err)
	}
	return Exports{
		Contributors: []prompt.Contributor{&catalogContributor{registry: manager}},
		Tools:        []tool.Tool{&readSkillTool{registry: manager}, &addSkillTool{registry: manager}},
		Operations:   []operation.Operation{&statusOperation{registry: manager}},
	}, nil, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
