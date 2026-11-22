package engine

import (
	"github.com/ingot-agent/sdk/model"
)

// Config controls deterministic shrinking.
type Config struct {
	// TargetTokens is the input budget the result must fit into.
	TargetTokens int64
	// ProtectRecentTokens keeps the newest complete rounds at full fidelity.
	// It is a token budget rather than a round count: a conversation whose last
	// turns are short would be left almost entirely unprotected by a count.
	// A zero value protects nothing.
	ProtectRecentTokens int64
	// Preamble is inserted after the system messages and counted in the budget.
	// It carries the running summary of rounds that earlier compactions removed.
	Preamble []model.Message
}

// Result is one deterministic projection of a request.
type Result struct {
	// Messages is the projected request: system messages, every round at its
	// assigned level, then the in-progress tail. Untouched messages share
	// Content, ToolCalls and Replay with the input.
	Messages []model.Message
	// Levels is aligned with Layout.Rounds.
	Levels []Level
	// Tokens is the estimated input size of Messages.
	Tokens int64
	// Dropped lists the rounds removed entirely; these are the rounds the
	// summary layer must cover.
	Dropped []Round
	// Elided counts rounds whose content changed without being dropped.
	Elided int
	// Uncompactable reports that even dropping every round left the request
	// above TargetTokens, which only happens when the system prompt and the
	// in-progress tail alone exceed the budget.
	Uncompactable bool
}

// Shrink reduces a layout to fit cfg.TargetTokens. It never fails: the caller
// receives a usable projection in every case, and Uncompactable marks the one
// situation it cannot fix.
//
// Degradation is monotone by age: rounds are elided oldest first, so a newer
// round is always at least as intact as an older one. The search is also
// monotone in depth: each elision level is exhausted across the unprotected
// prefix before any round goes deeper. Dropping a round is the last resort
// because it is the only level that removes narrative.
func Shrink(layout Layout, cfg Config) Result {
	tail := layout.Tail()
	base := int64(requestFramingTokens) + layout.ToolsTokens
	for _, message := range layout.System {
		base += MessageTokens(message)
	}
	for _, message := range cfg.Preamble {
		base += MessageTokens(message)
	}
	for _, message := range tail {
		base += MessageTokens(message)
	}

	costs := make([]roundCosts, len(layout.Rounds))
	full := base
	for i, round := range layout.Rounds {
		costs[i] = measureRound(layout, round)
		full += costs[i].perLevel[LevelFull]
	}

	levels := make([]Level, len(layout.Rounds))
	total := full
	if total <= cfg.TargetTokens {
		return build(layout, cfg.Preamble, levels, base, costs, tail)
	}

	protectedFrom := protectBand(len(layout.Rounds), costs, cfg.ProtectRecentTokens)

	// Shallow elision: every level below LevelDropped keeps all narrative.
	for _, level := range []Level{LevelNoToolOutput, LevelNoArguments, LevelNarrative} {
		if tryLevel(levels, costs, protectedFrom, level, &total, cfg.TargetTokens) {
			return build(layout, cfg.Preamble, levels, base, costs, tail)
		}
	}

	// Deep elision: raise rounds to LevelNarrative one at a time, oldest first.
	// This keeps every round's narrative, so it must be exhausted before any
	// round is dropped. The unprotected prefix arrives here already elided, so
	// the loop normally spends its effort releasing protected rounds.
	for i := 0; i < len(levels); i++ {
		saving := costs[i].perLevel[levels[i]] - costs[i].perLevel[LevelNarrative]
		if saving <= 0 {
			continue
		}
		total -= saving
		levels[i] = LevelNarrative
		if total <= cfg.TargetTokens {
			return build(layout, cfg.Preamble, levels, base, costs, tail)
		}
	}

	// Narrative loss: drop rounds, oldest first. Reaching this point means every
	// round is already at LevelNarrative, so no further elision is available.
	for i := range levels {
		saving := costs[i].perLevel[levels[i]]
		if saving <= 0 {
			continue
		}
		total -= saving
		levels[i] = LevelDropped
		if total <= cfg.TargetTokens {
			return build(layout, cfg.Preamble, levels, base, costs, tail)
		}
	}

	// The system prompt and the in-progress tail alone exceed the target.
	uncompactable := make([]Level, len(levels))
	for i := range uncompactable {
		uncompactable[i] = LevelDropped
	}
	result := build(layout, cfg.Preamble, uncompactable, base, costs, tail)
	result.Uncompactable = true
	return result
}

// roundCosts memoizes the estimated size of one round at each level.
type roundCosts struct {
	perLevel [5]int64
}

func measureRound(layout Layout, round Round) roundCosts {
	var costs roundCosts
	for level := LevelFull; level <= LevelNarrative; level++ {
		var total int64
		for _, message := range layout.Project(round, level) {
			total += MessageTokens(message)
		}
		costs.perLevel[level] = total
	}
	return costs
}

// protectBand returns the index of the first protected round: rounds in
// [result, len(costs)) stay at full fidelity. A zero budget protects nothing,
// and a budget larger than the conversation protects all of it.
func protectBand(count int, costs []roundCosts, budget int64) int {
	if budget <= 0 || count == 0 {
		return count
	}
	var accumulated int64
	for i := count - 1; i >= 0; i-- {
		accumulated += costs[i].perLevel[LevelFull]
		if accumulated >= budget {
			return i
		}
	}
	return 0
}

// tryLevel raises the oldest unprotected rounds that actually shrink to level,
// stopping as soon as total fits target. Reports whether the target was reached.
func tryLevel(levels []Level, costs []roundCosts, protectedFrom int, level Level, total *int64, target int64) bool {
	if *total <= target {
		return true
	}
	for i := 0; i < protectedFrom; i++ {
		saving := costs[i].perLevel[levels[i]] - costs[i].perLevel[level]
		if saving <= 0 {
			continue
		}
		*total -= saving
		levels[i] = level
		if *total <= target {
			return true
		}
	}
	return false
}

func build(layout Layout, preamble []model.Message, levels []Level, base int64, costs []roundCosts, tail []model.Message) Result {
	result := Result{
		Levels: append([]Level(nil), levels...),
		Tokens: base,
	}
	result.Messages = make([]model.Message, 0, len(layout.System)+len(preamble)+len(layout.Conversation))
	result.Messages = append(result.Messages, layout.System...)
	result.Messages = append(result.Messages, preamble...)
	for i, round := range layout.Rounds {
		level := levels[i]
		result.Tokens += costs[i].perLevel[level]
		switch level {
		case LevelDropped:
			result.Dropped = append(result.Dropped, round)
		case LevelFull:
		default:
			result.Elided++
		}
		if level == LevelDropped {
			continue
		}
		result.Messages = append(result.Messages, layout.Project(round, level)...)
	}
	result.Messages = append(result.Messages, tail...)
	return result
}
