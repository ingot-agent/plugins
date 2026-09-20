package skillruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/tool"
)

const (
	readSkillToolName = "read_skill"
	addSkillToolName  = "add_skill"
)

type readSkillTool struct{ registry *registry }
type addSkillTool struct{ registry *registry }

type readArguments struct {
	Name      *string `json:"name"`
	Reference string  `json:"reference"`
}

type addArguments struct {
	Name         *string                `json:"name"`
	Description  *string                `json:"description"`
	Instructions *string                `json:"instructions"`
	References   []addReferenceArgument `json:"references"`
}

type addReferenceArgument struct {
	Path    *string `json:"path"`
	Content *string `json:"content"`
}

func (*readSkillTool) Definition() tool.Definition {
	return tool.Definition{
		Name: readSkillToolName,
		Description: "Read one installed Skill. Omit reference to read its metadata, instructions, and reference list; " +
			"set reference to read one UTF-8 file relative to the Skill's references directory.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name"],"properties":{"name":{"type":"string","minLength":1,"maxLength":64,"pattern":"^[a-z0-9]+(?:-[a-z0-9]+)*$"},"reference":{"type":"string","minLength":1}}}`),
	}
}

func (t *readSkillTool) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, errors.New("read_skill: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if invocation.Call.Name != "" && invocation.Call.Name != readSkillToolName {
		return tool.Result{}, fmt.Errorf("call name %q: %w", invocation.Call.Name, ErrInvalidArguments)
	}
	var args readArguments
	if err := decodeObject(invocation.Call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Name == nil || !skillNamePattern.MatchString(*args.Name) || len(*args.Name) > 64 {
		return tool.Result{}, fmt.Errorf("name is invalid: %w", ErrInvalidArguments)
	}
	if err := t.registry.Refresh(ctx); err != nil {
		return tool.Result{}, err
	}
	item, ok := t.registry.Snapshot().Skills[*args.Name]
	if !ok {
		return businessResult(ctx, fmt.Sprintf("read_skill error: skill %q not found", *args.Name))
	}
	if args.Reference == "" {
		return tool.Result{Content: content.FromText(renderSkill(item))}, nil
	}
	if !utf8.ValidString(args.Reference) {
		return tool.Result{}, fmt.Errorf("reference is not valid UTF-8: %w", ErrInvalidArguments)
	}
	normalized, err := validateReferencePath(args.Reference)
	if err != nil || normalized != args.Reference {
		return businessResult(ctx, fmt.Sprintf("read_skill error: invalid reference %q", args.Reference))
	}
	value, ok := item.References[normalized]
	if !ok {
		return businessResult(ctx, fmt.Sprintf("read_skill error: reference %q not found in skill %q", normalized, item.Name))
	}
	text := fmt.Sprintf("Skill: %s\nReference: %s\nDigest: %s\n\n%s", item.Name, normalized, item.Digest, value)
	return tool.Result{Content: content.FromText(text)}, nil
}

func (*addSkillTool) Definition() tool.Definition {
	return tool.Definition{
		Name: addSkillToolName,
		Description: "Create a new persistent Skill. This tool is create-only and never overwrites an existing Skill. " +
			"The Skill becomes readable immediately and appears in the catalog on the next turn.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["name","description","instructions"],"properties":{"name":{"type":"string","minLength":1,"maxLength":64,"pattern":"^[a-z0-9]+(?:-[a-z0-9]+)*$"},"description":{"type":"string","minLength":1,"maxLength":1024},"instructions":{"type":"string","minLength":1},"references":{"type":"array","maxItems":32,"items":{"type":"object","additionalProperties":false,"required":["path","content"],"properties":{"path":{"type":"string","minLength":1},"content":{"type":"string"}}}}}}`),
	}
}

func (t *addSkillTool) Invoke(ctx context.Context, invocation tool.Invocation) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, errors.New("add_skill: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if invocation.Call.Name != "" && invocation.Call.Name != addSkillToolName {
		return tool.Result{}, fmt.Errorf("call name %q: %w", invocation.Call.Name, ErrInvalidArguments)
	}
	var args addArguments
	if err := decodeObject(invocation.Call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Name == nil || args.Description == nil || args.Instructions == nil {
		return tool.Result{}, fmt.Errorf("name, description, and instructions are required: %w", ErrInvalidArguments)
	}
	references := make(map[string]string, len(args.References))
	for i, reference := range args.References {
		if reference.Path == nil || reference.Content == nil {
			return tool.Result{}, fmt.Errorf("references[%d] requires path and content: %w", i, ErrInvalidArguments)
		}
		if _, duplicate := references[*reference.Path]; duplicate {
			return businessResult(ctx, fmt.Sprintf("add_skill error: duplicate reference %q", *reference.Path))
		}
		references[*reference.Path] = *reference.Content
	}
	created, err := t.registry.Add(ctx, addRequest{
		Name: *args.Name, Description: *args.Description, Instructions: *args.Instructions, References: references,
	})
	if err != nil {
		if errors.Is(err, ErrInvalidSkill) || errors.Is(err, ErrSkillExists) || errors.Is(err, ErrSkillLimit) {
			return businessResult(ctx, fmt.Sprintf("add_skill error: %v", err))
		}
		return tool.Result{}, err
	}
	return tool.Result{Content: content.FromText(fmt.Sprintf(
		"Created skill %q (%s). It is readable immediately and will appear in the catalog on the next turn.",
		created.Name, created.Digest,
	))}, nil
}

func renderSkill(item *skill) string {
	var result strings.Builder
	fmt.Fprintf(&result, "Skill: %s\nDescription: %s\nDigest: %s\n", item.Name, strconv.Quote(item.Description), item.Digest)
	fmt.Fprintf(&result, "Source: %s\n", item.Source)
	if item.Stale {
		fmt.Fprintf(&result, "State: stale; serving the last valid version (%s)\n", item.StaleReason)
	}
	if item.License != "" {
		fmt.Fprintf(&result, "License: %s\n", strconv.Quote(item.License))
	}
	if item.Compatibility != "" {
		fmt.Fprintf(&result, "Compatibility: %s\n", strconv.Quote(item.Compatibility))
	}
	if item.AllowedTools != "" {
		fmt.Fprintf(&result, "Allowed tools hint: %s (advisory only; grants no authorization)\n", strconv.Quote(item.AllowedTools))
	}
	names := sortedReferenceNames(item.References)
	if len(names) == 0 {
		result.WriteString("References: none\n")
	} else {
		result.WriteString("References:\n")
		for _, name := range names {
			fmt.Fprintf(&result, "- %s\n", name)
		}
	}
	result.WriteString("\n--- Instructions ---\n")
	result.WriteString(item.Instructions)
	return result.String()
}

func businessResult(ctx context.Context, message string) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Content: content.FromText(message)}, nil
}

func decodeObject(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode arguments: %w: %w", err, ErrInvalidArguments)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("arguments contain trailing JSON: %w", ErrInvalidArguments)
		}
		return fmt.Errorf("decode trailing arguments: %w: %w", err, ErrInvalidArguments)
	}
	return nil
}
