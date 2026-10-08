// Package contextinput implements durable plugin inputs for session context.
package contextinput

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"

	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
)

const sourcePrompt = `User-role messages enclosed in <system source="plugin">...</system>
are inputs inserted into the conversation by runtime plugins.`

var (
	ErrInvalidConfig                 = errors.New("invalid context.input dependencies")
	ErrInvalidPluginInput            = errors.New("invalid plugin input")
	ErrUnsupportedPluginInputVersion = errors.New("unsupported plugin input version")
)

type Dependencies struct {
	Store session.Store
}

type Exports struct {
	Inputs      agent.PluginInputWriter
	Projector   agent.PluginInputProjector
	Contributor prompt.Contributor
}

type inputs struct {
	store session.Store
}

func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Store) {
		return Exports{}, nil, fmt.Errorf("session store is required: %w", ErrInvalidConfig)
	}
	instance := &inputs{store: deps.Store}
	return Exports{Inputs: instance, Projector: instance, Contributor: instance}, nil, nil
}

func (p *inputs) Append(ctx context.Context, id session.ID, input agent.PluginInput) error {
	if ctx == nil || id == "" || !utf8.ValidString(string(id)) {
		return fmt.Errorf("context and valid session ID are required: %w", ErrInvalidPluginInput)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entry, err := encode(input)
	if err != nil {
		return err
	}
	if err := p.store.Append(ctx, id, entry); err != nil {
		return fmt.Errorf("append plugin input: %w", err)
	}
	return nil
}

func (*inputs) Project(entry session.Entry) (model.Message, bool, error) {
	if entry.Kind != entryKind {
		return model.Message{}, false, nil
	}
	input, err := decode(entry)
	if err != nil {
		return model.Message{}, true, err
	}
	value, err := message(input)
	return value, true, err
}

func (*inputs) Contribute(ctx context.Context, _ prompt.Request) ([]prompt.Block, error) {
	if ctx == nil {
		return nil, fmt.Errorf("plugin input source prompt: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []prompt.Block{{Name: "Plugin inputs", Content: content.FromText(sourcePrompt)}}, nil
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

var _ agent.PluginInputWriter = (*inputs)(nil)
var _ agent.PluginInputProjector = (*inputs)(nil)
var _ prompt.Contributor = (*inputs)(nil)
