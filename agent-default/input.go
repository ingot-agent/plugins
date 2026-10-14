package agentdefault

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
)

// attachmentError is safe to isolate only before this input's message batch
// has been written. Storage, cancellation and shared runtime failures escape.
type attachmentError struct{ err error }

func (e *attachmentError) Error() string { return e.err.Error() }
func (e *attachmentError) Unwrap() error { return e.err }

type preparedInput struct {
	inputIndex int
	input      agent.Input
	messages   []model.Message
	entries    []session.Entry
	submission *queuedMessage
}

type fileNotice struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	MIMEType string `json:"mimeType"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
}

func (r *runtime) prepareInput(ctx context.Context, input agent.Input) (preparedInput, error) {
	if err := agent.ValidateInput(input); err != nil {
		return preparedInput{}, &attachmentError{err}
	}
	owned := agent.CloneInput(input)
	var files []fileNotice
	var media []content.Attachment
	for i := range owned.Attachments {
		if err := ctx.Err(); err != nil {
			return preparedInput{}, err
		}
		a := &owned.Attachments[i]
		if a.Path != "" {
			path, err := filepath.Abs(a.Path)
			if err != nil {
				return preparedInput{}, &attachmentError{err}
			}
			info, err := os.Stat(path)
			if err != nil {
				return preparedInput{}, &attachmentError{fmt.Errorf("inspect attachment %q: %w", path, err)}
			}
			if !info.Mode().IsRegular() {
				return preparedInput{}, &attachmentError{fmt.Errorf("attachment %q is not a regular file", path)}
			}
			a.Path, a.Size = path, info.Size()
			if a.Name == "" {
				a.Name = info.Name()
			}
			kind := "file"
			if a.Media != nil && a.Media.Kind == content.KindImage {
				kind = "image"
			}
			files = append(files, fileNotice{Kind: kind, Name: a.Name, MIMEType: a.MIMEType, Path: a.Path, Size: a.Size})
		}
		if a.Media != nil {
			if a.Media.Media.Source.Kind == content.SourceAsset {
				if _, err := r.assets.Stat(ctx, a.Media.Media.Source.Asset); err != nil {
					if ctx.Err() != nil {
						return preparedInput{}, ctx.Err()
					}
					if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
						return preparedInput{}, &attachmentError{fmt.Errorf("resolve attachment %q: %w", a.Name, err)}
					}
					return preparedInput{}, fmt.Errorf("resolve attachment %q: %w", a.Name, err)
				}
			}
			media = append(media, *a.Media)
		}
	}
	prepared := preparedInput{input: owned}
	if len(files) > 0 {
		raw, err := json.MarshalIndent(files, "", "  ")
		if err != nil {
			return preparedInput{}, err
		}
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, append(raw, []byte("\n\n以上是紧随其后的用户输入所上传的文件。")...)); err != nil {
			return preparedInput{}, err
		}
		prepared.messages = append(prepared.messages, model.Message{Role: model.RoleUser, Content: content.FromText("<system source=\"plugin\">\n" + escaped.String() + "\n</system>")})
	}
	value, err := r.materializeContent(ctx, content.FromInput(owned.Text, media))
	if err != nil {
		return preparedInput{}, err
	}
	prepared.messages = append(prepared.messages, model.Message{Role: model.RoleUser, Content: value})
	for _, message := range prepared.messages {
		payload, err := encodePersistedMessage(message)
		if err != nil {
			return preparedInput{}, err
		}
		prepared.entries = append(prepared.entries, session.Entry{Kind: agentMessageKind, Version: agentMessageVersion, Payload: payload})
	}
	return prepared, nil
}

func (r *runtime) admitInputs(ctx context.Context, id session.ID, history []model.Message, inputs []preparedInput, run *activeExecution) ([]model.Message, error) {
	if run != nil {
		r.admitInputState(run, inputs)
	}
	for _, input := range inputs {
		if err := r.store.Append(ctx, id, input.entries...); err != nil {
			executionRecorderFrom(ctx).recordFailure(err, agent.FailureUserPersistence, nil, "")
			return nil, fmt.Errorf("append user input: %w", err)
		}
		history = append(history, cloneMessages(input.messages)...)
		if run != nil {
			r.inputPersisted(run, input)
		}
	}
	last := len(history) - 1
	messages, err := r.prompt.Render(ctx, prompt.Request{SessionID: id, History: cloneMessages(history[:last]), Input: content.Clone(history[last].Content)})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		executionRecorderFrom(ctx).recordFailure(err, agent.FailurePrompt, nil, "")
		return nil, fmt.Errorf("render prompt: %w", err)
	}
	return cloneMessages(messages), nil
}
