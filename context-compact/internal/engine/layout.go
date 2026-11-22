// Package engine implements the deterministic shrinking core of
// context.compact v2. It contains no model calls, no persistence and no
// I/O: given messages and a token target it returns a smaller message
// sequence, always succeeding for structurally valid input.
package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

var (
	// ErrInvalidHistory reports a message sequence that cannot be split into
	// rounds, such as an unmatched tool result. It is the only kind of error
	// this package returns for a call that was given a usable target.
	ErrInvalidHistory = errors.New("invalid message history")
	// ErrInvalidRequest reports malformed request metadata.
	ErrInvalidRequest = errors.New("invalid compaction request")
)

// EstimatorSource names this package's estimator. It keeps the historical
// identifier so published telemetry stays comparable across the rewrite.
const EstimatorSource = "unicode-estimate-v1"

// Fixed estimator framing costs, matching the historical
// unicode-estimate-v1 profile so watermarks keep their meaning.
const (
	requestFramingTokens = 3
	messageFramingTokens = 4
	callFramingTokens    = 8
	definitionTokens     = 12
)

// EstimateText approximates the token cost of one text field. ASCII bytes
// contribute one token per four bytes (rounded up) and each non-ASCII rune
// contributes one token. It is deliberately cheap and monotonic: a longer
// string never estimates fewer tokens than its prefix.
func EstimateText(value string) int64 {
	var ascii, wide int64
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r <= 0x7f {
			ascii++
		} else {
			wide++
		}
		value = value[size:]
	}
	return (ascii+3)/4 + wide
}

// TextOf returns the concatenated text parts of a message. Media parts are
// skipped because the estimator does not model them.
func TextOf(message model.Message) string {
	var builder strings.Builder
	for _, part := range message.Content {
		if part.Kind == content.KindText {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

// HasMedia reports whether any content part carries a non-text modality.
func HasMedia(message model.Message) bool {
	for _, part := range message.Content {
		if part.Kind != content.KindText {
			return true
		}
	}
	return false
}

// MessageTokens estimates one message at full fidelity.
func MessageTokens(message model.Message) int64 {
	count := int64(messageFramingTokens)
	count += EstimateText(string(message.Role))
	count += EstimateText(TextOf(message))
	count += EstimateText(message.Name)
	count += EstimateText(message.ToolCallID)
	for _, call := range message.ToolCalls {
		count += callFramingTokens
		count += EstimateText(call.ID)
		count += EstimateText(call.Name)
		count += EstimateText(string(call.Arguments))
	}
	return count
}

// TokensOf estimates a message sequence including the request framing cost.
func TokensOf(messages []model.Message) int64 {
	count := int64(requestFramingTokens)
	for _, message := range messages {
		count += MessageTokens(message)
	}
	return count
}

// ToolsTokens estimates the tool definition block of a request.
func ToolsTokens(definitions []tool.Definition) int64 {
	count := int64(0)
	for _, definition := range definitions {
		count += definitionTokens
		count += EstimateText(definition.Name)
		count += EstimateText(definition.Description)
		count += EstimateText(string(definition.InputSchema))
	}
	return count
}

// Round is one complete user-originated exchange: a user message, the
// assistant turns it produced, and every matching tool result.
type Round struct {
	// Start and End are indices into Layout.Conversation with End exclusive.
	Start int
	End   int
}

// Layout is a request split into the parts that compaction treats
// differently. Conversation[:lastRoundEnd] is fully covered by Rounds;
// Conversation[lastRoundEnd:] is an in-progress exchange that compaction
// never rewrites.
type Layout struct {
	System       []model.Message
	Conversation []model.Message
	Rounds       []Round
	ToolsTokens  int64
	tailStart    int
}

// RoundsEnd returns the index in Conversation at which the last complete
// round ends. Conversation at and beyond this index is the in-progress tail.
func (l Layout) RoundsEnd() int {
	if len(l.Rounds) == 0 {
		return l.tailStart
	}
	return l.Rounds[len(l.Rounds)-1].End
}

// Tail returns the in-progress messages that compaction leaves untouched.
func (l Layout) Tail() []model.Message {
	return l.Conversation[l.RoundsEnd():]
}

// FromRound returns the same layout with the oldest index complete rounds
// removed from consideration. Round indices remain absolute into Conversation,
// so a projection of the result still slices the original messages.
func (l Layout) FromRound(index int) Layout {
	if index <= 0 {
		return l
	}
	if index > len(l.Rounds) {
		index = len(l.Rounds)
	}
	trimmed := l
	trimmed.Rounds = l.Rounds[index:]
	return trimmed
}

// RoundEndingAt returns the number of leading rounds that end at or before the
// given conversation index. It reports false when no round ends exactly there,
// which is the boundary rule for persisted coverage.
func (l Layout) RoundEndingAt(index int) (int, bool) {
	for i, round := range l.Rounds {
		if round.End == index {
			return i + 1, true
		}
	}
	return 0, index == 0
}

// Messages returns the round messages of one round.
func (l Layout) Messages(round Round) []model.Message {
	return l.Conversation[round.Start:round.End]
}

// Inspect validates a request and splits it into system messages, complete
// rounds and an in-progress tail. Validation is strict: a tool result must
// follow the call it answers, and a user or assistant message must not
// interrupt an unfinished tool exchange. Compaction must never emit a
// sequence that a provider would reject, so an invalid input is reported
// rather than repaired.
func Inspect(request model.Request) (Layout, error) {
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || math.IsInf(*request.Temperature, 0)) {
		return Layout{}, fmt.Errorf("temperature is not finite: %w", ErrInvalidRequest)
	}
	if !utf8.ValidString(string(request.ReasoningEffort)) {
		return Layout{}, fmt.Errorf("reasoning effort is not valid UTF-8: %w", ErrInvalidRequest)
	}
	for i, definition := range request.Tools {
		if !utf8.ValidString(definition.Name) || !utf8.ValidString(definition.Description) || !validJSON(definition.InputSchema) {
			return Layout{}, fmt.Errorf("tools[%d] is invalid: %w", i, ErrInvalidRequest)
		}
	}

	systemEnd := 0
	for systemEnd < len(request.Messages) && request.Messages[systemEnd].Role == model.RoleSystem {
		if err := validateMessage(request.Messages[systemEnd]); err != nil {
			return Layout{}, fmt.Errorf("messages[%d]: %w", systemEnd, err)
		}
		systemEnd++
	}
	conversation := request.Messages[systemEnd:]
	rounds, tailStart, err := splitRounds(conversation)
	if err != nil {
		return Layout{}, err
	}
	return Layout{
		System:       request.Messages[:systemEnd],
		Conversation: conversation,
		Rounds:       rounds,
		ToolsTokens:  ToolsTokens(request.Tools),
		tailStart:    tailStart,
	}, nil
}

func validateMessage(message model.Message) error {
	if !utf8.ValidString(message.Name) || !utf8.ValidString(message.ToolCallID) {
		return fmt.Errorf("message carries invalid UTF-8 metadata: %w", ErrInvalidHistory)
	}
	for i, part := range message.Content {
		if part.Kind == content.KindText && !utf8.ValidString(part.Text) {
			return fmt.Errorf("content[%d] text is not valid UTF-8: %w", i, ErrInvalidHistory)
		}
		if part.Kind != content.KindText && part.Kind != content.KindImage &&
			part.Kind != content.KindAudio && part.Kind != content.KindVideo && part.Kind != content.KindFile {
			return fmt.Errorf("content[%d] has unknown kind: %w", i, ErrInvalidHistory)
		}
	}
	seen := make(map[string]struct{}, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		if call.ID == "" || call.Name == "" || !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) || !validJSON(call.Arguments) {
			return fmt.Errorf("tool_calls[%d] is invalid: %w", i, ErrInvalidHistory)
		}
		if _, duplicate := seen[call.ID]; duplicate {
			return fmt.Errorf("tool_calls[%d] repeats id %q: %w", i, call.ID, ErrInvalidHistory)
		}
		seen[call.ID] = struct{}{}
	}
	switch message.Role {
	case model.RoleSystem:
		// Only text system messages are accepted; they are validated here for
		// the leading block and rejected inside the conversation by splitRounds.
		if message.ToolCallID != "" || len(message.ToolCalls) != 0 {
			return fmt.Errorf("system message carries tool linkage: %w", ErrInvalidHistory)
		}
	case model.RoleUser:
		if message.ToolCallID != "" || len(message.ToolCalls) != 0 {
			return fmt.Errorf("user message carries tool linkage: %w", ErrInvalidHistory)
		}
	case model.RoleAssistant:
		if message.ToolCallID != "" {
			return fmt.Errorf("assistant message carries tool_call_id: %w", ErrInvalidHistory)
		}
	case model.RoleTool:
		if message.ToolCallID == "" || len(message.ToolCalls) != 0 {
			return fmt.Errorf("tool message linkage is invalid: %w", ErrInvalidHistory)
		}
	default:
		return fmt.Errorf("unsupported role %q: %w", message.Role, ErrInvalidHistory)
	}
	return nil
}

// splitRounds groups a conversation into complete rounds and reports where the
// in-progress tail begins.
//
// A round runs from one user message to the message before the next user
// message. Grouping by user turn rather than by "the assistant stopped asking
// for tools" is what keeps coverage total: an interrupted turn ends without a
// final assistant message, and a rule that waited for one would leave those
// messages outside every round, which means silently dropping them from the
// compacted request.
//
// The final turn is excluded when it still has unmatched tool calls, because
// that is a conversation the runtime is in the middle of. It is returned as the
// tail and never rewritten.
func splitRounds(conversation []model.Message) ([]Round, int, error) {
	if len(conversation) == 0 {
		return nil, 0, nil
	}
	if conversation[0].Role != model.RoleUser {
		return nil, 0, fmt.Errorf("conversation must start with a user message: %w", ErrInvalidHistory)
	}
	rounds := make([]Round, 0, 8)
	var pending []string
	matched := 0
	start := 0
	for i, message := range conversation {
		if err := validateMessage(message); err != nil {
			return nil, 0, fmt.Errorf("conversation message %d: %w", i, err)
		}
		switch message.Role {
		case model.RoleSystem:
			return nil, 0, fmt.Errorf("system message after conversation start at %d: %w", i, ErrInvalidHistory)
		case model.RoleUser:
			if len(pending) != matched {
				return nil, 0, fmt.Errorf("user message follows incomplete tool exchange at %d: %w", i, ErrInvalidHistory)
			}
			if i > start {
				rounds = append(rounds, Round{Start: start, End: i})
			}
			start = i
			pending = pending[:0]
			matched = 0
		case model.RoleAssistant:
			if len(pending) != matched {
				return nil, 0, fmt.Errorf("assistant message follows incomplete tool exchange at %d: %w", i, ErrInvalidHistory)
			}
			pending = pending[:0]
			matched = 0
			for _, call := range message.ToolCalls {
				pending = append(pending, call.ID)
			}
		case model.RoleTool:
			if matched >= len(pending) || message.ToolCallID != pending[matched] {
				return nil, 0, fmt.Errorf("tool result has no matching call at %d: %w", i, ErrInvalidHistory)
			}
			matched++
		}
	}
	// The last turn is open unless it already ended with an assistant message
	// that asks for no tools. An open turn is the one the runtime is about to
	// continue, so it is returned as the tail and never rewritten: dropping the
	// current user request would be unrecoverable.
	last := conversation[len(conversation)-1]
	closed := last.Role == model.RoleAssistant && len(last.ToolCalls) == 0
	if closed && len(pending) == matched {
		rounds = append(rounds, Round{Start: start, End: len(conversation)})
		return rounds, len(conversation), nil
	}
	return rounds, start, nil
}

func validJSON(raw []byte) bool {
	return len(raw) > 0 && utf8.Valid(raw) && json.Valid(raw)
}

func cloneMessages(messages []model.Message) []model.Message {
	if messages == nil {
		return nil
	}
	result := make([]model.Message, len(messages))
	for i, message := range messages {
		result[i] = message
		result[i].Content = cloneContent(message.Content)
		if len(message.ToolCalls) != 0 {
			calls := make([]tool.Call, len(message.ToolCalls))
			for j, call := range message.ToolCalls {
				calls[j] = call
				calls[j].Arguments = append([]byte(nil), call.Arguments...)
			}
			result[i].ToolCalls = calls
		}
		result[i].Replay = message.Replay.Clone()
	}
	return result
}

func cloneContent(value content.Content) content.Content {
	if value == nil {
		return nil
	}
	return content.Clone(value)
}
