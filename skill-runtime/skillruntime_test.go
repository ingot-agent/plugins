package skillruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/tool"
)

type testStateScope string

func (s testStateScope) Dir() string { return string(s) }

var _ state.Scope = testStateScope("")
var _ prompt.Contributor = (*catalogContributor)(nil)
var _ tool.Tool = (*readSkillTool)(nil)
var _ tool.Tool = (*addSkillTool)(nil)
var _ operation.Operation = (*statusOperation)(nil)

func newTestRuntime(t *testing.T) (string, Exports) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "state")
	exports, cleanup, err := New(context.Background(), Dependencies{State: testStateScope(stateDir)})
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("New returned unexpected cleanup")
	}
	return filepath.Join(stateDir, "skills"), exports
}

func writeSkill(t *testing.T, root, name, description, instructions string, references map[string]string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(directory, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf("---\nname: %s\ndescription: %q\n---\n%s", name, description, instructions)
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range references {
		path := filepath.Join(directory, "references", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func invokeTool(t *testing.T, candidate tool.Tool, name, arguments string) string {
	t.Helper()
	result, err := candidate.Invoke(context.Background(), tool.Invocation{Call: tool.Call{Name: name, Arguments: json.RawMessage(arguments)}})
	if err != nil {
		t.Fatal(err)
	}
	text, ok := content.TextOnly(result.Content)
	if !ok {
		t.Fatalf("result is not text: %#v", result.Content)
	}
	return text
}

func TestNewExportsStableInterfaces(t *testing.T) {
	root, exports := newTestRuntime(t)
	if !filepath.IsAbs(root) {
		t.Fatalf("root = %q", root)
	}
	if len(exports.Contributors) != 1 || len(exports.Tools) != 2 || len(exports.Operations) != 1 {
		t.Fatalf("exports = %#v", exports)
	}
	if exports.Tools[0].Definition().Name != readSkillToolName || exports.Tools[1].Definition().Name != addSkillToolName {
		t.Fatalf("tool definitions = %#v %#v", exports.Tools[0].Definition(), exports.Tools[1].Definition())
	}
	definition := exports.Operations[0].Definition()
	if definition.Name != statusOperationName || definition.Group != statusOperationGroup {
		t.Fatalf("operation definition = %#v", definition)
	}
	status := readStatus(t, exports.Operations[0])
	if status.Skills == nil || status.Rejected == nil {
		t.Fatalf("empty status must use arrays: %#v", status)
	}
	if len(status.Skills) != 1 || status.Skills[0].Name != "skill-create" || status.Skills[0].Source != skillSourceBuiltin {
		t.Fatalf("built-in status = %#v", status)
	}
	builtin := invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"skill-create"}`)
	if !strings.Contains(builtin, "# Create a Skill") || !strings.Contains(builtin, "Source: builtin") {
		t.Fatalf("built-in Skill = %q", builtin)
	}
}

func TestNewRejectsInvalidState(t *testing.T) {
	for _, deps := range []Dependencies{{}, {State: testStateScope("relative")}} {
		if _, _, err := New(context.Background(), deps); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("deps=%#v error=%v", deps, err)
		}
	}
}

func TestExternalChangesRefreshWithoutRestart(t *testing.T) {
	root, exports := newTestRuntime(t)
	writeSkill(t, root, "review-go", "Review Go changes", "Check errors first.\n", map[string]string{"checklist.md": "- tests\n- race\n"})

	blocks, err := exports.Contributors[0].Contribute(context.Background(), prompt.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v", blocks)
	}
	catalog, _ := content.TextOnly(blocks[0].Content)
	if !strings.Contains(catalog, "review-go") || !strings.Contains(catalog, "Review Go changes") {
		t.Fatalf("catalog = %q", catalog)
	}

	main := invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"review-go"}`)
	if !strings.Contains(main, "Check errors first.") || !strings.Contains(main, "checklist.md") {
		t.Fatalf("main = %q", main)
	}
	reference := invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"review-go","reference":"checklist.md"}`)
	if !strings.Contains(reference, "- race") {
		t.Fatalf("reference = %q", reference)
	}

	writeSkill(t, root, "review-go", "Updated description", "Use the updated instructions.\n", nil)
	blocks, err = exports.Contributors[0].Contribute(context.Background(), prompt.Request{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ = content.TextOnly(blocks[0].Content)
	if !strings.Contains(catalog, "Updated description") {
		t.Fatalf("updated catalog = %q", catalog)
	}
	main = invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"review-go"}`)
	if !strings.Contains(main, "Use the updated instructions.") {
		t.Fatalf("updated main = %q", main)
	}

	if err := os.RemoveAll(filepath.Join(root, "review-go")); err != nil {
		t.Fatal(err)
	}
	blocks, err = exports.Contributors[0].Contribute(context.Background(), prompt.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("catalog blocks after delete = %#v", blocks)
	}
	catalog, _ = content.TextOnly(blocks[0].Content)
	if strings.Contains(catalog, "review-go") || !strings.Contains(catalog, "skill-create") {
		t.Fatalf("deleted skill remained in catalog: %q", catalog)
	}
}

func TestInvalidHotEditKeepsLastValidVersion(t *testing.T) {
	root, exports := newTestRuntime(t)
	writeSkill(t, root, "stable", "Stable skill", "Original instructions.\n", nil)
	if _, err := exports.Contributors[0].Contribute(context.Background(), prompt.Request{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stable", "SKILL.md"), []byte("not frontmatter"), 0o600); err != nil {
		t.Fatal(err)
	}

	text := invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"stable"}`)
	if !strings.Contains(text, "Original instructions.") || !strings.Contains(text, "State: stale") {
		t.Fatalf("stale read = %q", text)
	}
	status := readStatus(t, exports.Operations[0])
	stable := findStatusSkill(t, status, "stable")
	if len(status.Skills) != 2 || !stable.Stale || len(status.Rejected) != 1 {
		t.Fatalf("status = %#v", status)
	}

	writeSkill(t, root, "stable", "Recovered skill", "Recovered instructions.\n", nil)
	text = invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"stable"}`)
	if !strings.Contains(text, "Recovered instructions.") || strings.Contains(text, "State: stale") {
		t.Fatalf("recovered read = %q", text)
	}
}

func TestInvalidNewEntryIsQuarantined(t *testing.T) {
	root, exports := newTestRuntime(t)
	writeSkill(t, root, "valid", "Valid", "Use it.\n", nil)
	writeSkill(t, root, "wrong-dir", "Invalid", "Do not load.\n", nil)
	manifest := filepath.Join(root, "wrong-dir", "SKILL.md")
	if err := os.WriteFile(manifest, []byte("---\nname: other\ndescription: mismatch\n---\nNo.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	blocks, err := exports.Contributors[0].Contribute(context.Background(), prompt.Request{})
	if err != nil {
		t.Fatal(err)
	}
	catalog, _ := content.TextOnly(blocks[0].Content)
	if !strings.Contains(catalog, "valid") || strings.Contains(catalog, "wrong-dir") {
		t.Fatalf("catalog = %q", catalog)
	}
	status := readStatus(t, exports.Operations[0])
	if len(status.Skills) != 2 || len(status.Rejected) != 1 || status.Rejected[0].Path != "wrong-dir" {
		t.Fatalf("status = %#v", status)
	}
}

func TestReferenceSymlinkIsRejectedButScriptsAndAssetsAreIgnored(t *testing.T) {
	root, exports := newTestRuntime(t)
	writeSkill(t, root, "unsafe-ref", "Unsafe reference", "Do not follow links.\n", nil)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "unsafe-ref", "references", "outside.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	writeSkill(t, root, "ignored", "Ignored directories", "Only read the Skill.\n", nil)
	if err := os.MkdirAll(filepath.Join(root, "ignored", "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "scripts", "run.sh"), []byte{0xff}, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ignored", "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "assets", "binary"), []byte{0xff, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}

	blocks, err := exports.Contributors[0].Contribute(context.Background(), prompt.Request{})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := content.TextOnly(blocks[0].Content)
	if !strings.Contains(text, "ignored") || strings.Contains(text, "unsafe-ref") {
		t.Fatalf("catalog = %q", text)
	}
	status := readStatus(t, exports.Operations[0])
	ignored := findStatusSkill(t, status, "ignored")
	if len(status.Skills) != 2 || ignored.Source != skillSourceState || len(status.Rejected) != 1 {
		t.Fatalf("status = %#v", status)
	}
}

func TestRootScanFailureKeepsLastSnapshot(t *testing.T) {
	root, exports := newTestRuntime(t)
	writeSkill(t, root, "cached", "Cached", "Serve the cached version.\n", nil)
	if _, err := exports.Contributors[0].Contribute(context.Background(), prompt.Request{}); err != nil {
		t.Fatal(err)
	}
	moved := root + ".moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(moved, root)

	text := invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"cached"}`)
	if !strings.Contains(text, "Serve the cached version.") {
		t.Fatalf("cached read = %q", text)
	}
	status := readStatus(t, exports.Operations[0])
	if len(status.Skills) != 2 || status.LastRefreshError == "" {
		t.Fatalf("status = %#v", status)
	}

	if err := os.Rename(moved, root); err != nil {
		t.Fatal(err)
	}
	status = readStatus(t, exports.Operations[0])
	if status.LastRefreshError != "" {
		t.Fatalf("recovered status = %#v", status)
	}
}

func TestGenerationChangesOnlyWhenPublishedStateChanges(t *testing.T) {
	root, exports := newTestRuntime(t)
	first := readStatus(t, exports.Operations[0])
	second := readStatus(t, exports.Operations[0])
	if first.Generation != second.Generation {
		t.Fatalf("stable scans changed generation: %d -> %d", first.Generation, second.Generation)
	}
	writeSkill(t, root, "new-state", "New state", "Publish it.\n", nil)
	third := readStatus(t, exports.Operations[0])
	if third.Generation <= second.Generation {
		t.Fatalf("new state did not advance generation: %d -> %d", second.Generation, third.Generation)
	}
	fourth := readStatus(t, exports.Operations[0])
	if fourth.Generation != third.Generation {
		t.Fatalf("stable published state changed generation: %d -> %d", third.Generation, fourth.Generation)
	}
}

func TestAddSkillCreatesReferencesAndNeverOverwrites(t *testing.T) {
	root, exports := newTestRuntime(t)
	arguments := `{"name":"created","description":"Created by the model","instructions":"Follow this procedure.\n","references":[{"path":"guide/steps.md","content":"1. inspect\n2. test\n"}]}`
	result := invokeTool(t, exports.Tools[1], addSkillToolName, arguments)
	if !strings.Contains(result, "Created skill \"created\"") {
		t.Fatalf("add result = %q", result)
	}
	if _, err := os.Stat(filepath.Join(root, "created", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	reference := invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"created","reference":"guide/steps.md"}`)
	if !strings.Contains(reference, "2. test") {
		t.Fatalf("reference = %q", reference)
	}

	second := invokeTool(t, exports.Tools[1], addSkillToolName, `{"name":"created","description":"Replacement","instructions":"Replace it."}`)
	if !strings.Contains(second, ErrSkillExists.Error()) {
		t.Fatalf("second add = %q", second)
	}
	main := invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"created"}`)
	if !strings.Contains(main, "Follow this procedure.") || strings.Contains(main, "Replace it.") {
		t.Fatalf("main after duplicate = %q", main)
	}
}

func TestBuiltinSkillNameIsReserved(t *testing.T) {
	root, exports := newTestRuntime(t)
	result := invokeTool(t, exports.Tools[1], addSkillToolName, `{"name":"skill-create","description":"Replacement","instructions":"Replace the built-in."}`)
	if !strings.Contains(result, ErrSkillExists.Error()) {
		t.Fatalf("add built-in result = %q", result)
	}
	if _, err := os.Stat(filepath.Join(root, "skill-create")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reserved add created state directory: %v", err)
	}

	writeSkill(t, root, "skill-create", "Manual replacement", "Replace it manually.\n", nil)
	text := invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"skill-create"}`)
	if !strings.Contains(text, "# Create a Skill") || strings.Contains(text, "Replace it manually") {
		t.Fatalf("built-in was replaced: %q", text)
	}
	status := readStatus(t, exports.Operations[0])
	if len(status.Rejected) != 1 || status.Rejected[0].Path != "skill-create" {
		t.Fatalf("reserved directory status = %#v", status)
	}
}

func TestAddAndReadRejectUnsafeReferencesAsBusinessResults(t *testing.T) {
	_, exports := newTestRuntime(t)
	for _, path := range []string{"../outside.md", "/absolute.md", `.hidden`} {
		arguments := fmt.Sprintf(`{"name":"unsafe-%d","description":"Unsafe","instructions":"No.","references":[{"path":%q,"content":"x"}]}`, len(path), path)
		text := invokeTool(t, exports.Tools[1], addSkillToolName, arguments)
		if !strings.Contains(text, "add_skill error") {
			t.Fatalf("path=%q result=%q", path, text)
		}
	}
	missing := invokeTool(t, exports.Tools[0], readSkillToolName, `{"name":"absent"}`)
	if !strings.Contains(missing, "not found") {
		t.Fatalf("missing = %q", missing)
	}
}

func TestConcurrentCreateOnlyAddHasOneWinner(t *testing.T) {
	root, exports := newTestRuntime(t)
	add := exports.Tools[1]
	const calls = 12
	results := make(chan string, calls)
	errorsChannel := make(chan error, calls)
	var wait sync.WaitGroup
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := add.Invoke(context.Background(), tool.Invocation{Call: tool.Call{
				Name: addSkillToolName, Arguments: json.RawMessage(`{"name":"shared","description":"Shared","instructions":"One winner."}`),
			}})
			if err != nil {
				errorsChannel <- err
				return
			}
			text, _ := content.TextOnly(result.Content)
			results <- text
		}()
	}
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("concurrent add error: %v", err)
	}
	created := 0
	exists := 0
	for result := range results {
		if strings.HasPrefix(result, "Created skill") {
			created++
		} else if strings.Contains(result, ErrSkillExists.Error()) {
			exists++
		}
	}
	if created != 1 || exists != calls-1 {
		t.Fatalf("created=%d exists=%d", created, exists)
	}
	if _, err := os.Stat(filepath.Join(root, "shared", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogEscapesDescriptionLines(t *testing.T) {
	root, exports := newTestRuntime(t)
	writeSkill(t, root, "escaped", "first\n## forged", "Use it.\n", nil)
	blocks, err := exports.Contributors[0].Contribute(context.Background(), prompt.Request{})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := content.TextOnly(blocks[0].Content)
	if strings.Contains(text, "\n## forged") || !strings.Contains(text, `first\n## forged`) {
		t.Fatalf("catalog description was not escaped: %q", text)
	}
}

func TestCanceledCallsStop(t *testing.T) {
	_, exports := newTestRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := exports.Contributors[0].Contribute(ctx, prompt.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("contributor error = %v", err)
	}
	if _, err := exports.Tools[0].Invoke(ctx, tool.Invocation{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("read error = %v", err)
	}
	if _, err := exports.Operations[0].Invoke(ctx, operation.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("status error = %v", err)
	}
}

func readStatus(t *testing.T, candidate operation.Operation) statusOutput {
	t.Helper()
	result, err := candidate.Invoke(context.Background(), operation.Request{Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var output statusOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func findStatusSkill(t *testing.T, status statusOutput, name string) statusSkill {
	t.Helper()
	for _, item := range status.Skills {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("skill %q not found in status %#v", name, status)
	return statusSkill{}
}
