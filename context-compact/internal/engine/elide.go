package engine

import (
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

// Level selects how much of a round is elided. Levels are ordered: a higher
// level elides strictly more than a lower one, and every level below
// LevelDropped preserves all narrative text.
type Level uint8

const (
	// LevelFull keeps the round byte-for-byte.
	LevelFull Level = iota
	// LevelNoToolOutput replaces tool result text with a short marker. Tool
	// calls, their arguments and every user or assistant message text are
	// preserved, so the command that produced the output remains visible.
	LevelNoToolOutput
	// LevelNoArguments additionally replaces tool call arguments. The call
	// name and id remain, so the round still shows which tool ran.
	LevelNoArguments
	// LevelNarrative removes tool results and tool calls entirely, keeping only
	// user messages and assistant text.
	LevelNarrative
	// LevelDropped removes the round from the request. It is the only level
	// that discards narrative, so summaries are generated for it.
	LevelDropped
)

// String renders a level for logs and test failures.
func (l Level) String() string {
	switch l {
	case LevelFull:
		return "full"
	case LevelNoToolOutput:
		return "no_tool_output"
	case LevelNoArguments:
		return "no_arguments"
	case LevelNarrative:
		return "narrative"
	case LevelDropped:
		return "dropped"
	default:
		return fmt.Sprintf("level(%d)", uint8(l))
	}
}

// elidedArguments is valid JSON because request validation rejects a tool call
// whose arguments are not a JSON value.
var elidedArguments = json.RawMessage(`{"elided":true}`)

// outputMarker and argumentMarker are intentionally explicit: a model reading
// them learns that content existed and can re-run the command instead of
// assuming the output was empty.
func outputMarker(message model.Message) string {
	return fmt.Sprintf("[tool output elided: %d bytes; re-run the command if needed]", TextBytes(message))
}

// cheaper reports whether replacing original with replacement lowers the
// estimate. Elision must never make a round bigger: a short tool output can be
// smaller than any explanatory marker, and estimators must stay monotone so the
// shrink search never mis-predicts its own effect.
func cheaper(original, replacement string) bool {
	return EstimateText(replacement) < EstimateText(original)
}

// argumentMarker is the literal replacement for tool call arguments.
const argumentMarker = "[arguments elided]"

// TextBytes returns the size of the concatenated text payload.
func TextBytes(message model.Message) int {
	total := 0
	for _, part := range message.Content {
		if part.Kind == content.KindText {
			total += len(part.Text)
		}
	}
	return total
}

// Transform returns a copy of message reduced to the given level. LevelFull
// returns the message unchanged, including its Replay state. A message whose
// content actually changes loses Replay: provider continuation data describes
// the original text and must not be replayed against a rewritten payload.
func Transform(message model.Message, level Level) (model.Message, bool) {
	switch level {
	case LevelFull:
		return message, true
	case LevelNoToolOutput:
		if message.Role != model.RoleTool {
			return message, true
		}
		marker := outputMarker(message)
		if !cheaper(TextOf(message), marker) {
			return message, true
		}
		return replaceText(message, marker)
	case LevelNoArguments:
		switch message.Role {
		case model.RoleTool:
			marker := outputMarker(message)
			if !cheaper(TextOf(message), marker) {
				return message, true
			}
			return replaceText(message, marker)
		case model.RoleAssistant:
			if len(message.ToolCalls) == 0 {
				return message, true
			}
			result := message
			result.ToolCalls = make([]tool.Call, len(message.ToolCalls))
			changed := false
			for i, call := range message.ToolCalls {
				result.ToolCalls[i] = call
				if !cheaper(string(call.Arguments), string(elidedArguments)) {
					continue
				}
				result.ToolCalls[i].Arguments = append(json.RawMessage(nil), elidedArguments...)
				changed = true
			}
			if !changed {
				return message, true
			}
			result.Replay = nil
			return result, true
		}
		return message, true
	case LevelNarrative:
		switch message.Role {
		case model.RoleTool:
			return model.Message{}, false
		case model.RoleAssistant:
			if len(message.ToolCalls) == 0 {
				return message, true
			}
			result := message
			result.ToolCalls = nil
			result.Replay = nil
			return result, true
		}
		return message, true
	case LevelDropped:
		return model.Message{}, false
	default:
		return message, true
	}
}

// replaceText rewrites the text parts of a message while keeping media parts.
// Media is left in place because the estimator does not count it: removing it
// would lose information without freeing budget.
func replaceText(message model.Message, text string) (model.Message, bool) {
	hasText := false
	for _, part := range message.Content {
		if part.Kind == content.KindText {
			hasText = true
			break
		}
	}
	if !hasText {
		return message, true
	}
	rewritten := make(content.Content, 0, len(message.Content))
	wrote := false
	for _, part := range message.Content {
		if part.Kind != content.KindText {
			rewritten = append(rewritten, part)
			continue
		}
		if wrote {
			continue
		}
		wrote = true
		rewritten = append(rewritten, content.Part{Kind: content.KindText, Text: text})
	}
	result := message
	result.Content = rewritten
	result.Replay = nil
	return result, true
}

// Project applies one level to every message of a round. It returns nil when
// the round has no content left, which is how a dropped round is represented.
// Untouched messages are returned as-is: their Content, ToolCalls and Replay
// values are shared with the caller's history, never re-encoded.
func (l Layout) Project(round Round, level Level) []model.Message {
	if level == LevelDropped {
		return nil
	}
	source := l.Messages(round)
	if level == LevelFull {
		return source
	}
	result := make([]model.Message, 0, len(source))
	for _, message := range source {
		projected, keep := Transform(message, level)
		if !keep {
			continue
		}
		result = append(result, projected)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
