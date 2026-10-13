package openairesponses

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/model"
)

const responsesReplayFormat = "openai.responses.output.v1"

type replayScope struct {
	model  string
	origin string
}

type replayPayload struct {
	Origin string            `json:"origin"`
	Output []json.RawMessage `json:"output"`
}

func (p *provider) replayScope(modelName string) replayScope {
	// Endpoint changes must not send encrypted state to a different upstream.
	// Store only its digest, not a URL that may contain routing credentials.
	sum := sha256.Sum256([]byte(p.endpoint))
	return replayScope{model: modelName, origin: hex.EncodeToString(sum[:])}
}

func (p *provider) replayInput(modelName string, message model.Message) ([]json.RawMessage, bool, error) {
	state := message.Replay
	if state == nil || state.Format != responsesReplayFormat || state.Provider != p.name || state.Model != modelName || message.Role != model.RoleAssistant {
		return nil, false, nil
	}
	var payload replayPayload
	if !utf8.Valid(state.Data) || json.Unmarshal(state.Data, &payload) != nil || payload.Origin == "" {
		return nil, false, fmt.Errorf("invalid Responses replay state: %w", ErrInvalidRequest)
	}
	if payload.Origin != p.replayScope(modelName).origin {
		return nil, false, nil
	}
	projected, err := decodeOutput(payload.Output)
	if err != nil {
		// Do not include provider-owned data in diagnostics.
		return nil, false, fmt.Errorf("invalid Responses replay output: %w", ErrInvalidRequest)
	}
	if !sameReplayMessage(projected, message) {
		// An interceptor may have edited the text or filtered tool calls. Its
		// decision remains authoritative; stale provider state cannot undo it.
		return nil, false, nil
	}
	return payload.Output, true, nil
}

func sameReplayMessage(a, b model.Message) bool {
	if a.Role != b.Role || a.Name != b.Name || a.ToolCallID != b.ToolCallID || len(a.Content) != len(b.Content) || len(a.ToolCalls) != len(b.ToolCalls) {
		return false
	}
	for i := range a.Content {
		if !reflect.DeepEqual(a.Content[i], b.Content[i]) {
			return false
		}
	}
	for i, call := range a.ToolCalls {
		other := b.ToolCalls[i]
		if call.ID != other.ID || call.Name != other.Name || !bytes.Equal(call.Arguments, other.Arguments) {
			return false
		}
	}
	return true
}
