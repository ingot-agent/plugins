package engine

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

func text(s string) content.Content { return content.FromText(s) }

func user(s string) model.Message {
	return model.Message{Role: model.RoleUser, Content: text(s)}
}

func assistant(s string) model.Message {
	return model.Message{Role: model.RoleAssistant, Content: text(s)}
}

func call(id, name, args string) tool.Call {
	return tool.Call{ID: id, Name: name, Arguments: json.RawMessage(args)}
}

func toolResult(id, name, out string) model.Message {
	return model.Message{Role: model.RoleTool, ToolCallID: id, Name: name, Content: text(out)}
}

// simpleRound builds a round with no tool calls.
func simpleRound(prompt, answer string) []model.Message {
	return []model.Message{user(prompt), assistant(answer)}
}

// toolRound builds a round with one tool call and its result.
func toolRound(prompt, args, output string) []model.Message {
	return []model.Message{
		user(prompt),
		{Role: model.RoleAssistant, ToolCalls: []tool.Call{call("call_1", "shell", args)}},
		toolResult("call_1", "shell", output),
		assistant("done"),
	}
}

func request(messages ...[]model.Message) model.Request {
	var flat []model.Message
	flat = append(flat, model.Message{Role: model.RoleSystem, Content: text("system prompt")})
	for _, round := range messages {
		flat = append(flat, round...)
	}
	return model.Request{Messages: flat}
}

func mustLayout(t *testing.T, r model.Request) Layout {
	t.Helper()
	layout, err := Inspect(r)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return layout
}

func TestInspectSplitsSystemRoundsAndTail(t *testing.T) {
	r := request(
		simpleRound("first", "answer one"),
		toolRound("second", `{"cmd":"ls"}`, "file.txt"),
	)
	// in-progress turn: a user message with no assistant reply yet
	r.Messages = append(r.Messages, user("third"))

	layout := mustLayout(t, r)
	if len(layout.System) != 1 || layout.System[0].Role != model.RoleSystem {
		t.Fatalf("system = %#v", layout.System)
	}
	if len(layout.Rounds) != 2 {
		t.Fatalf("rounds = %d, want 2", len(layout.Rounds))
	}
	tail := layout.Tail()
	if len(tail) != 1 || TextOf(tail[0]) != "third" {
		t.Fatalf("tail = %#v", tail)
	}
}

func TestInspectRejectsBrokenPairing(t *testing.T) {
	cases := map[string]model.Request{
		"tool result without call": {Messages: []model.Message{
			{Role: model.RoleSystem, Content: text("s")}, user("hi"), toolResult("call_9", "shell", "out"),
		}},
		"user interrupts tool exchange": {Messages: []model.Message{
			{Role: model.RoleSystem, Content: text("s")}, user("hi"),
			{Role: model.RoleAssistant, ToolCalls: []tool.Call{call("call_1", "shell", `{}`)}},
			user("wait"),
		}},
		"conversation must start with user": {Messages: []model.Message{
			{Role: model.RoleSystem, Content: text("s")}, assistant("hi"),
		}},
	}
	for name, r := range cases {
		if _, err := Inspect(r); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestShrinkKeepsEverythingUnderTarget(t *testing.T) {
	layout := mustLayout(t, request(simpleRound("a", "b"), toolRound("c", `{"x":1}`, "output")))
	result := Shrink(layout, Config{TargetTokens: 1_000_000})
	if result.Tokens > 1_000_000 {
		t.Fatalf("tokens = %d", result.Tokens)
	}
	for i, level := range result.Levels {
		if level != LevelFull {
			t.Fatalf("round %d elided to %v under a generous target", i, level)
		}
	}
	if len(result.Dropped) != 0 || result.Elided != 0 {
		t.Fatalf("unexpected degradation: %#v", result)
	}
}

func TestShrinkPrefersElidingToolOutputBeforeNarrative(t *testing.T) {
	big := strings.Repeat("log line\n", 4000)
	r := request(toolRound("run tests", `{"cmd":"go test"}`, big), simpleRound("next", "answer"))
	layout := mustLayout(t, r)
	full := Shrink(layout, Config{TargetTokens: 1 << 40})
	if full.Tokens < 5000 {
		t.Fatalf("fixture too small: %d", full.Tokens)
	}

	result := Shrink(layout, Config{TargetTokens: full.Tokens - 3000})
	if result.Tokens > full.Tokens-3000 {
		t.Fatalf("target missed: %d", result.Tokens)
	}
	if len(result.Dropped) != 0 {
		t.Fatalf("dropped rounds for a shrink that elision alone could cover: %#v", result.Dropped)
	}
	if result.Levels[0] == LevelFull {
		t.Fatal("oldest round was not elided")
	}
	for _, want := range []string{"run tests", "next", "answer"} {
		if !containsText(result.Messages, want) {
			t.Fatalf("narrative %q lost", want)
		}
	}
}

// containsText reports whether any message carries text equal to want.
func containsText(messages []model.Message, want string) bool {
	for _, message := range messages {
		if TextOf(message) == want {
			return true
		}
	}
	return false
}

func TestShrinkElidesOldestFirst(t *testing.T) {
	big := strings.Repeat("x\n", 20000)
	r := request(
		toolRound("oldest", `{"cmd":"a"}`, big),
		toolRound("middle", `{"cmd":"b"}`, big),
		toolRound("newest", `{"cmd":"c"}`, big),
	)
	layout := mustLayout(t, r)
	full := Shrink(layout, Config{TargetTokens: 1 << 40})

	// Ask for a shrink that one round's tool output can cover.
	one := full.Tokens / 4
	result := Shrink(layout, Config{TargetTokens: full.Tokens - one})
	for i, level := range result.Levels {
		t.Logf("round %d -> %v", i, level)
	}
	if result.Levels[0] == LevelFull {
		t.Fatal("oldest round should be elided first")
	}
	if result.Levels[2] != LevelFull {
		t.Fatalf("newest round degraded before older ones: %v", result.Levels)
	}
}

func TestShrinkProtectsRecentTokensNotRoundCount(t *testing.T) {
	output := strings.Repeat("y\n", 20000)
	r := request(
		toolRound("a", `{"c":1}`, output),
		toolRound("b", `{"c":2}`, output),
		toolRound("c", `{"c":3}`, output),
		toolRound("d", `{"c":4}`, output),
		simpleRound("ok", "ok"),
	)
	layout := mustLayout(t, r)
	full := Shrink(layout, Config{TargetTokens: 1 << 40})

	// Four heavy rounds followed by one tiny round. A 100-token protection
	// budget reaches back past the tiny round and pins it plus one neighbour,
	// which a round *count* of 1 could not express.
	result := Shrink(layout, Config{
		TargetTokens:        full.Tokens - 5000,
		ProtectRecentTokens: 100,
	})
	if result.Levels[4] != LevelFull || result.Levels[3] != LevelFull {
		t.Fatalf("protected rounds were elided: %v", result.Levels)
	}
	if result.Levels[0] == LevelFull {
		t.Fatalf("no older round was elided: %v", result.Levels)
	}
	if result.Tokens > full.Tokens-5000 {
		t.Fatalf("target missed: %d", result.Tokens)
	}
}

func TestShrinkDropsOnlyWhenElisionIsExhausted(t *testing.T) {
	// Narrative-only rounds cannot be elided at all, so reaching a small target
	// requires dropping them.
	prose := strings.Repeat("narrative text\n", 5000)
	r := request(
		simpleRound("one", prose),
		simpleRound("two", prose),
		simpleRound("three", prose),
	)
	layout := mustLayout(t, r)
	full := Shrink(layout, Config{TargetTokens: 1 << 40})

	result := Shrink(layout, Config{TargetTokens: full.Tokens / 8})
	if len(result.Dropped) == 0 {
		t.Fatal("expected narrative loss when elision cannot reach the target")
	}
	if result.Tokens > full.Tokens/8 {
		t.Fatalf("target missed: %d > %d", result.Tokens, full.Tokens/8)
	}
	if result.Levels[0] != LevelDropped {
		t.Fatalf("dropped set is not oldest-first: %v", result.Levels)
	}
}

func TestShrinkReportsUncompactableInsteadOfFailing(t *testing.T) {
	layout := mustLayout(t, request(simpleRound("hello", strings.Repeat("a", 1000))))
	result := Shrink(layout, Config{TargetTokens: 1})
	if !result.Uncompactable {
		t.Fatal("expected Uncompactable")
	}
	if len(result.Messages) == 0 {
		t.Fatal("Uncompactable must still return a usable message list")
	}
}

func TestShrinkAlwaysFitsOrReportsUncompactable(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	for trial := 0; trial < 200; trial++ {
		rounds := make([][]model.Message, 1+random.Intn(12))
		for i := range rounds {
			switch random.Intn(3) {
			case 0:
				rounds[i] = simpleRound(fmt.Sprintf("q%d", i), strings.Repeat("a", random.Intn(400)))
			default:
				rounds[i] = toolRound(
					fmt.Sprintf("q%d", i),
					fmt.Sprintf(`{"n":%d}`, i),
					strings.Repeat("o", random.Intn(3000)),
				)
			}
		}
		layout := mustLayout(t, request(rounds...))
		full := Shrink(layout, Config{TargetTokens: 1 << 40})
		target := int64(random.Intn(int(full.Tokens) + 1))
		result := Shrink(layout, Config{TargetTokens: target, ProtectRecentTokens: int64(random.Intn(2000))})

		if !result.Uncompactable && result.Tokens > target {
			t.Fatalf("trial %d: tokens %d exceed target %d", trial, result.Tokens, target)
		}
		if err := validateSequence(result.Messages); err != nil {
			t.Fatalf("trial %d: %v", trial, err)
		}
	}
}

// validateSequence asserts the structural invariants the provider enforces.
func validateSequence(messages []model.Message) error {
	var pending []string
	matched := 0
	for i, message := range messages {
		switch message.Role {
		case model.RoleSystem:
			if i != 0 {
				return fmt.Errorf("system message at %d", i)
			}
		case model.RoleUser:
			if len(pending) != matched {
				return fmt.Errorf("user at %d interrupts tool exchange", i)
			}
		case model.RoleAssistant:
			if len(pending) != matched {
				return fmt.Errorf("assistant at %d interrupts tool exchange", i)
			}
			pending = pending[:0]
			matched = 0
			for _, c := range message.ToolCalls {
				if !json.Valid(c.Arguments) {
					return fmt.Errorf("assistant at %d has invalid arguments", i)
				}
				pending = append(pending, c.ID)
			}
		case model.RoleTool:
			if matched >= len(pending) || message.ToolCallID != pending[matched] {
				return fmt.Errorf("tool at %d has no matching call", i)
			}
			matched++
			if matched == len(pending) {
				pending = pending[:0]
				matched = 0
			}
		default:
			return fmt.Errorf("unexpected role %q at %d", message.Role, i)
		}
	}
	return nil
}

func TestShrinkPreservesNarrativeVerbatim(t *testing.T) {
	random := rand.New(rand.NewSource(11))
	for trial := 0; trial < 100; trial++ {
		rounds := make([][]model.Message, 1+random.Intn(10))
		var wantUsers, wantAssistants []string
		for i := range rounds {
			prompt := fmt.Sprintf("prompt-%d-%s", i, strings.Repeat("p", random.Intn(80)))
			answer := fmt.Sprintf("answer-%d-%s", i, strings.Repeat("a", random.Intn(80)))
			wantUsers = append(wantUsers, prompt)
			wantAssistants = append(wantAssistants, answer)
			rounds[i] = []model.Message{
				user(prompt),
				{Role: model.RoleAssistant, Content: text("working"), ToolCalls: []tool.Call{call("c", "shell", `{"a":1}`)}},
				toolResult("c", "shell", strings.Repeat("out\n", 400)),
				assistant(answer),
			}
		}
		layout := mustLayout(t, request(rounds...))
		full := Shrink(layout, Config{TargetTokens: 1 << 40})

		// Halving a tool-heavy history is reachable by eliding tool output alone,
		// so no round may be dropped and no narrative may change.
		result := Shrink(layout, Config{TargetTokens: full.Tokens / 2})
		if len(result.Dropped) != 0 {
			t.Fatalf("trial %d: dropped %d rounds although elision suffices", trial, len(result.Dropped))
		}
		var gotUsers, gotAssistants []string
		for _, message := range result.Messages {
			switch message.Role {
			case model.RoleUser:
				gotUsers = append(gotUsers, TextOf(message))
			case model.RoleAssistant:
				if text := TextOf(message); text != "" && text != "working" {
					gotAssistants = append(gotAssistants, text)
				}
			}
		}
		if strings.Join(gotUsers, "\n") != strings.Join(wantUsers, "\n") {
			t.Fatalf("trial %d: user narrative changed\n got %v\nwant %v", trial, gotUsers, wantUsers)
		}
		if strings.Join(gotAssistants, "\n") != strings.Join(wantAssistants, "\n") {
			t.Fatalf("trial %d: assistant narrative changed\n got %v\nwant %v", trial, gotAssistants, wantAssistants)
		}
	}
}

func TestLevelTokensAreMonotone(t *testing.T) {
	random := rand.New(rand.NewSource(3))
	for trial := 0; trial < 100; trial++ {
		layout := mustLayout(t, request(toolRound("q", `{"n":1}`, strings.Repeat("o", random.Intn(2000)))))
		previous := int64(1 << 62)
		for level := LevelFull; level <= LevelDropped; level++ {
			var total int64
			for _, message := range layout.Project(layout.Rounds[0], level) {
				total += MessageTokens(message)
			}
			if total > previous {
				t.Fatalf("level %v costs %d, more than the shallower level's %d", level, total, previous)
			}
			previous = total
		}
	}
}

func TestTransformDropsReplayOnlyWhenContentChanges(t *testing.T) {
	replay := &model.ReplayState{Format: "f", Provider: "p", Model: "m", Data: []byte("opaque")}
	message := model.Message{Role: model.RoleTool, ToolCallID: "c", Content: text(strings.Repeat("output line\n", 200)), Replay: replay}

	unchanged, keep := Transform(message, LevelFull)
	if !keep || unchanged.Replay == nil {
		t.Fatal("full level must preserve Replay")
	}
	changed, keep := Transform(message, LevelNoToolOutput)
	if !keep {
		t.Fatal("tool message must be kept when eliding output")
	}
	if changed.Replay != nil {
		t.Fatal("rewritten message must clear Replay")
	}
	if !strings.Contains(TextOf(changed), "elided") {
		t.Fatalf("marker missing: %q", TextOf(changed))
	}
}

func TestTransformKeepsMediaWhenElidingText(t *testing.T) {
	message := model.Message{Role: model.RoleTool, ToolCallID: "c", Content: content.Content{
		{Kind: content.KindText, Text: strings.Repeat("screenshot bytes\n", 200)},
		{Kind: content.KindImage, Media: content.Media{MIMEType: "image/png", Name: "shot.png"}},
	}}
	elided, _ := Transform(message, LevelNoToolOutput)
	if len(elided.Content) != 2 || elided.Content[1].Kind != content.KindImage {
		t.Fatalf("media part lost: %#v", elided.Content)
	}
	if strings.Contains(elided.Content[0].Text, "screenshot bytes") {
		t.Fatal("text part was not elided")
	}
}

func TestTransformArgumentsStayValidJSON(t *testing.T) {
	message := model.Message{Role: model.RoleAssistant, Content: text("thinking"), ToolCalls: []tool.Call{
		call("c1", "shell", `{"cmd":"go test ./..."}`),
	}}
	elided, _ := Transform(message, LevelNoArguments)
	if len(elided.ToolCalls) != 1 {
		t.Fatalf("call count changed: %#v", elided.ToolCalls)
	}
	if !json.Valid(elided.ToolCalls[0].Arguments) {
		t.Fatalf("arguments are not valid JSON: %s", elided.ToolCalls[0].Arguments)
	}
	if elided.ToolCalls[0].Name != "shell" || elided.ToolCalls[0].ID != "c1" {
		t.Fatal("call identity must survive argument elision")
	}
	if TextOf(elided) != "thinking" {
		t.Fatal("assistant narrative must survive argument elision")
	}
}

// TestShrinkNeverDropsWhileElisionRemains guards the ordering rule the design
// relies on: a round may only be dropped once every round is already elided to
// the deepest narrative-preserving level. Dropping earlier would discard
// narrative that elision could have kept.
func TestShrinkNeverDropsWhileElisionRemains(t *testing.T) {
	output := strings.Repeat("y\n", 20000)
	rounds := make([][]model.Message, 0, 8)
	for i := 0; i < 8; i++ {
		rounds = append(rounds, toolRound(fmt.Sprintf("step-%d", i), `{"c":1}`, output))
	}
	layout := mustLayout(t, request(rounds...))
	full := Shrink(layout, Config{TargetTokens: 1 << 40})

	// A protection budget above the whole conversation must not cause drops
	// while deep elision alone can still reach the target.
	for _, protect := range []int64{0, full.Tokens / 4, full.Tokens / 2, full.Tokens * 2} {
		result := Shrink(layout, Config{TargetTokens: 250, ProtectRecentTokens: protect})
		if len(result.Dropped) == 0 {
			continue
		}
		for i, level := range result.Levels {
			if level == LevelDropped {
				continue
			}
			if level != LevelNarrative {
				t.Fatalf("protect=%d: round %d is %v while round(s) were dropped", protect, i, level)
			}
		}
	}
}

// TestInspectCoversEveryMessage locks the invariant that no message can fall
// outside both the rounds and the tail. An earlier version grouped rounds by
// "the assistant stopped requesting tools", which silently excluded interrupted
// turns: 265 messages and 41k tokens of a real session disappeared from the
// compacted request without any error.
func TestInspectCoversEveryMessage(t *testing.T) {
	interrupted := func(prompt, output string) []model.Message {
		return []model.Message{
			user(prompt),
			{Role: model.RoleAssistant, ToolCalls: []tool.Call{call("call_1", "shell", `{"cmd":"ls"}`)}},
			toolResult("call_1", "shell", output),
			// no final assistant message: the turn was interrupted
		}
	}
	conversations := map[string][]model.Message{
		"interrupted turn followed by another": append(
			append(simpleRound("start", "ok"), interrupted("interrupted", "partial output")...),
			simpleRound("next", "fine")...,
		),
		"only an interrupted turn": append(simpleRound("start", "ok"), interrupted("interrupted", "partial")...),
		"trailing user message":    append(simpleRound("start", "ok"), user("awaiting reply")),
		"chained tool exchanges": {user("go"), {Role: model.RoleAssistant, ToolCalls: []tool.Call{
			call("a", "shell", `{"n":1}`), call("b", "shell", `{"n":2}`),
		}}, toolResult("a", "shell", "one"), toolResult("b", "shell", "two"),
			{Role: model.RoleAssistant, ToolCalls: []tool.Call{call("c", "shell", `{"n":3}`)}},
			toolResult("c", "shell", "three"), assistant("done")},
	}
	for name, conversation := range conversations {
		t.Run(name, func(t *testing.T) {
			messages := append([]model.Message{{Role: model.RoleSystem, Content: text("s")}}, conversation...)
			layout := mustLayout(t, model.Request{Messages: messages})

			var total int64
			for _, message := range messages {
				total += MessageTokens(message)
			}
			var covered int64
			for _, message := range layout.System {
				covered += MessageTokens(message)
			}
			for _, round := range layout.Rounds {
				for _, message := range layout.Messages(round) {
					covered += MessageTokens(message)
				}
			}
			for _, message := range layout.Tail() {
				covered += MessageTokens(message)
			}
			if covered != total {
				t.Fatalf("coverage %d != total %d: %d tokens are unaccounted for",
					covered, total, total-covered)
			}

			// A projection at full fidelity must reproduce the input exactly.
			result := Shrink(layout, Config{TargetTokens: 1 << 40})
			if len(result.Messages) != len(messages) {
				t.Fatalf("full-fidelity projection has %d messages, want %d",
					len(result.Messages), len(messages))
			}
		})
	}
}

// TestInspectKeepsOpenTurnAsTail: the turn being prepared must never be elided
// or dropped, otherwise the model would receive a request without its own work.
func TestInspectKeepsOpenTurnAsTail(t *testing.T) {
	messages := []model.Message{{Role: model.RoleSystem, Content: text("s")}}
	messages = append(messages, simpleRound("start", "ok")...)
	messages = append(messages, user("the current request"))
	layout := mustLayout(t, model.Request{Messages: messages})
	tail := layout.Tail()
	if len(tail) != 1 || TextOf(tail[0]) != "the current request" {
		t.Fatalf("tail = %#v", tail)
	}

	// Even an impossible target must not remove it.
	result := Shrink(layout, Config{TargetTokens: 1})
	if !containsText(result.Messages, "the current request") {
		t.Fatal("the open turn was dropped")
	}
}
