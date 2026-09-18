package openairesponses

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

type responseRequest struct {
	Model           string              `json:"model"`
	Input           []responseInputItem `json:"input"`
	Tools           []responseTool      `json:"tools,omitempty"`
	Temperature     *float64            `json:"temperature,omitempty"`
	MaxOutputTokens *int                `json:"max_output_tokens,omitempty"`
	Stream          bool                `json:"stream"`
	Store           bool                `json:"store"`
}

type responseInputItem struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Content   any    `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    any    `json:"output,omitempty"`
}

type responseInputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responseTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responseObject struct {
	ID                string                     `json:"id"`
	Object            string                     `json:"object"`
	Status            string                     `json:"status"`
	Model             string                     `json:"model"`
	Output            []json.RawMessage          `json:"output"`
	Error             *responseError             `json:"error"`
	IncompleteDetails *responseIncompleteDetails `json:"incomplete_details"`
	Usage             *responseUsage             `json:"usage"`
}

type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type responseIncompleteDetails struct {
	Reason string `json:"reason"`
}

type responseUsage struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
	TotalTokens  *int `json:"total_tokens"`
}

type responseOutputItemType struct {
	Type string `json:"type"`
}

type responseMessageOutput struct {
	Type    string                  `json:"type"`
	Status  string                  `json:"status"`
	Role    string                  `json:"role"`
	Content []responseOutputContent `json:"content"`
}

type responseOutputContent struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type responseFunctionCallOutput struct {
	Type      string `json:"type"`
	Status    string `json:"status"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (p *provider) encodeResponseRequest(ctx context.Context, request model.Request, stream bool) ([]byte, error) {
	input := make([]responseInputItem, 0, len(request.Messages))
	for i, message := range request.Messages {
		mapped, err := p.encodeInputMessage(ctx, i, message)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		input = append(input, mapped...)
	}
	tools := make([]responseTool, len(request.Tools))
	for i, definition := range request.Tools {
		if definition.Name == "" || !utf8.ValidString(definition.Name) || !utf8.ValidString(definition.Description) || !validJSONObject(definition.InputSchema) {
			return nil, fmt.Errorf("tools[%d] is invalid: %w", i, ErrInvalidRequest)
		}
		tools[i] = responseTool{
			Type:        "function",
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  append(json.RawMessage(nil), definition.InputSchema...),
		}
	}
	payload := responseRequest{
		Model: request.Model, Input: input, Tools: tools,
		Temperature: request.Temperature, MaxOutputTokens: request.MaxTokens,
		Stream: stream, Store: false,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode model request: %w", err)
	}
	return raw, nil
}

func validateSDKRequest(request model.Request) error {
	if !utf8.ValidString(request.Model) || !utf8.ValidString(request.Provider) {
		return fmt.Errorf("provider or model is invalid UTF-8: %w", ErrInvalidRequest)
	}
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || math.IsInf(*request.Temperature, 0) || *request.Temperature < 0 || *request.Temperature > 2) {
		return fmt.Errorf("temperature is outside [0,2]: %w", ErrInvalidRequest)
	}
	if request.MaxTokens != nil && *request.MaxTokens < 1 {
		return fmt.Errorf("max_output_tokens must be positive: %w", ErrInvalidRequest)
	}
	if request.Stop != nil {
		return fmt.Errorf("stop sequences are not supported by the Responses API: %w", ErrInvalidRequest)
	}
	for i, message := range request.Messages {
		if !validRole(message.Role) || !utf8.ValidString(message.Name) || !utf8.ValidString(message.ToolCallID) {
			return fmt.Errorf("messages[%d] has invalid fields: %w", i, ErrInvalidRequest)
		}
		if message.Name != "" {
			return fmt.Errorf("messages[%d].name is not supported by the Responses API: %w", i, ErrInvalidRequest)
		}
		if err := content.Validate(message.Content); err != nil {
			return fmt.Errorf("messages[%d] content: %w: %w", i, ErrInvalidRequest, err)
		}
		if message.Role == model.RoleTool {
			if message.ToolCallID == "" {
				return fmt.Errorf("messages[%d] tool message requires tool_call_id: %w", i, ErrInvalidRequest)
			}
			if len(message.ToolCalls) != 0 {
				return fmt.Errorf("messages[%d] tool message cannot contain tool calls: %w", i, ErrInvalidRequest)
			}
		} else if message.ToolCallID != "" {
			return fmt.Errorf("messages[%d] non-tool message carries tool_call_id: %w", i, ErrInvalidRequest)
		}
		if message.Role != model.RoleAssistant && len(message.ToolCalls) != 0 {
			return fmt.Errorf("messages[%d] non-assistant message carries tool calls: %w", i, ErrInvalidRequest)
		}
		for j, call := range message.ToolCalls {
			if call.ID == "" || call.Name == "" || !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) || !json.Valid(call.Arguments) {
				return fmt.Errorf("messages[%d].tool_calls[%d]: %w", i, j, ErrInvalidRequest)
			}
		}
	}
	return nil
}

func (p *provider) encodeInputMessage(ctx context.Context, messageIndex int, message model.Message) ([]responseInputItem, error) {
	if message.Role == model.RoleTool {
		output, ok := content.TextOnly(message.Content)
		if !ok {
			for partIndex, part := range message.Content {
				if part.Kind != content.KindText {
					return nil, unsupported(messageIndex, partIndex, part, "tool output only supports text content")
				}
			}
			return nil, fmt.Errorf("tool output content is invalid: %w", ErrInvalidRequest)
		}
		return []responseInputItem{{Type: "function_call_output", CallID: message.ToolCallID, Output: output}}, nil
	}

	wireContent, err := p.encodeInputContent(ctx, messageIndex, message)
	if err != nil {
		return nil, err
	}
	items := make([]responseInputItem, 0, 1+len(message.ToolCalls))
	if len(message.Content) != 0 || len(message.ToolCalls) == 0 {
		items = append(items, responseInputItem{Type: "message", Role: string(message.Role), Content: wireContent})
	}
	for _, call := range message.ToolCalls {
		items = append(items, responseInputItem{
			Type: "function_call", CallID: call.ID, Name: call.Name, Arguments: string(call.Arguments),
		})
	}
	return items, nil
}

func (p *provider) encodeInputContent(ctx context.Context, messageIndex int, message model.Message) (any, error) {
	if text, ok := content.TextOnly(message.Content); ok {
		return text, nil
	}
	parts := make([]responseInputContent, 0, len(message.Content))
	for partIndex, part := range message.Content {
		switch part.Kind {
		case content.KindText:
			parts = append(parts, responseInputContent{Type: "input_text", Text: part.Text})
		case content.KindImage:
			if message.Role != model.RoleUser {
				return nil, unsupported(messageIndex, partIndex, part, "image content is only supported for user messages")
			}
			imageURL, err := p.imageURL(ctx, part)
			if err != nil {
				var unsupportedError *content.UnsupportedError
				if errors.As(err, &unsupportedError) {
					unsupportedError.MessageIndex = messageIndex
					unsupportedError.PartIndex = partIndex
					unsupportedError.Kind = part.Kind
					unsupportedError.MIMEType = part.Media.MIMEType
				}
				return nil, err
			}
			parts = append(parts, responseInputContent{Type: "input_image", ImageURL: imageURL})
		default:
			return nil, unsupported(messageIndex, partIndex, part, "modality is not supported by this provider")
		}
	}
	return parts, nil
}

func (p *provider) imageURL(ctx context.Context, part content.Part) (string, error) {
	switch part.Media.Source.Kind {
	case content.SourceURI:
		if !utf8.ValidString(part.Media.Source.URI) {
			return "", &content.UnsupportedError{Reason: "image URI must be valid UTF-8"}
		}
		parsed, err := url.Parse(part.Media.Source.URI)
		if err != nil || !parsed.IsAbs() {
			return "", &content.UnsupportedError{Reason: "image URI must be an absolute remote URI"}
		}
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https":
			return part.Media.Source.URI, nil
		default:
			return "", &content.UnsupportedError{Reason: "image URI scheme is not supported"}
		}
	case content.SourceInline, content.SourceAsset:
		mediaType, _, err := mime.ParseMediaType(part.Media.MIMEType)
		if err != nil || !strings.HasPrefix(strings.ToLower(mediaType), "image/") {
			return "", &content.UnsupportedError{Reason: "inline and asset images require an image MIME type"}
		}
		data := part.Media.Source.Data
		if part.Media.Source.Kind == content.SourceAsset {
			data, err = p.readAsset(ctx, part.Media.Source.Asset)
			if err != nil {
				return "", err
			}
		}
		if len(data) > p.maxAssetBytes {
			return "", &content.UnsupportedError{Reason: "image exceeds provider input limit"}
		}
		return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	default:
		return "", &content.UnsupportedError{Reason: "image source is not supported"}
	}
}

func unsupported(messageIndex, partIndex int, part content.Part, reason string) error {
	return &content.UnsupportedError{MessageIndex: messageIndex, PartIndex: partIndex, Kind: part.Kind, MIMEType: part.Media.MIMEType, Reason: reason}
}

func validRole(role model.Role) bool {
	return role == model.RoleSystem || role == model.RoleUser || role == model.RoleAssistant || role == model.RoleTool
}

func validJSONObject(raw json.RawMessage) bool {
	if !json.Valid(raw) {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func decodeResponse(raw []byte, providerName, secret string) (model.Response, error) {
	if !utf8.Valid(raw) {
		return model.Response{}, protocolError("response is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var response responseObject
	if err := decoder.Decode(&response); err != nil {
		return model.Response{}, protocolError("decode response: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return model.Response{}, protocolError("multiple JSON values in response")
	}
	return decodeResponseObject(response, providerName, secret)
}

func decodeResponseObject(response responseObject, providerName, secret string) (model.Response, error) {
	if response.Object != "response" || response.Model == "" || !utf8.ValidString(response.Model) {
		return model.Response{}, protocolError("response requires object=response and a non-empty model")
	}
	if response.Error != nil {
		if response.Status != "failed" && response.Status != "cancelled" {
			return model.Response{}, protocolError("response status %q carries an error", response.Status)
		}
		return model.Response{}, providerResponseError(response.Status, response.Error, secret)
	}
	if response.Status == "failed" || response.Status == "cancelled" {
		return model.Response{}, providerResponseError(response.Status, nil, secret)
	}
	if response.Status != "completed" && response.Status != "incomplete" {
		return model.Response{}, protocolError("response has non-terminal status %q", response.Status)
	}

	message := model.Message{Role: model.RoleAssistant}
	for i, rawItem := range response.Output {
		var itemType responseOutputItemType
		if err := json.Unmarshal(rawItem, &itemType); err != nil || itemType.Type == "" {
			return model.Response{}, protocolError("invalid output item at index %d", i)
		}
		switch itemType.Type {
		case "message":
			var item responseMessageOutput
			if err := json.Unmarshal(rawItem, &item); err != nil {
				return model.Response{}, protocolError("decode message output at index %d: %v", i, err)
			}
			if item.Role != "assistant" || (item.Status != "" && item.Status != "completed" && item.Status != "incomplete") {
				return model.Response{}, protocolError("invalid message output at index %d", i)
			}
			for j, part := range item.Content {
				var text string
				switch part.Type {
				case "output_text":
					text = part.Text
				case "refusal":
					text = part.Refusal
				default:
					return model.Response{}, protocolError("unsupported message content type %q at output %d part %d", part.Type, i, j)
				}
				if !utf8.ValidString(text) {
					return model.Response{}, protocolError("message content at output %d part %d is not valid UTF-8", i, j)
				}
				message.Content = append(message.Content, content.Text(text))
			}
		case "function_call":
			var item responseFunctionCallOutput
			if err := json.Unmarshal(rawItem, &item); err != nil {
				return model.Response{}, protocolError("decode function call at index %d: %v", i, err)
			}
			if item.CallID == "" || item.Name == "" || !utf8.ValidString(item.CallID) || !utf8.ValidString(item.Name) || !json.Valid([]byte(item.Arguments)) ||
				(item.Status != "" && item.Status != "completed" && item.Status != "incomplete") {
				return model.Response{}, protocolError("invalid function call at output index %d", i)
			}
			message.ToolCalls = append(message.ToolCalls, tool.Call{
				ID: item.CallID, Name: item.Name, Arguments: json.RawMessage(item.Arguments),
			})
		case "reasoning":
			// Reasoning output items are transient provider metadata. Streaming
			// summary deltas are exposed separately through the reasoning semantic.
		default:
			return model.Response{}, protocolError("unsupported output item type %q at index %d", itemType.Type, i)
		}
	}

	usage, err := decodeUsage(response.Usage)
	if err != nil {
		return model.Response{}, err
	}
	finishReason := "stop"
	if len(message.ToolCalls) != 0 {
		finishReason = "tool_calls"
	}
	if response.Status == "incomplete" {
		if response.IncompleteDetails == nil || response.IncompleteDetails.Reason == "" {
			return model.Response{}, protocolError("incomplete response requires incomplete_details.reason")
		}
		switch response.IncompleteDetails.Reason {
		case "max_output_tokens":
			finishReason = "length"
		case "content_filter":
			finishReason = "content_filter"
		default:
			finishReason = response.IncompleteDetails.Reason
		}
	}
	return model.Response{
		Message: message, FinishReason: finishReason, Usage: usage,
		Provider: providerName, Model: response.Model,
	}, nil
}

func providerResponseError(status string, response *responseError, secret string) error {
	code := "unknown"
	message := "provider returned no error detail"
	if response != nil {
		if response.Code != "" {
			code = response.Code
		}
		if response.Message != "" {
			message = response.Message
		}
	}
	return &ProviderResponseError{
		Status: status, Code: redactSecret(code, secret), Message: redactSecret(message, secret),
	}
}

func decodeUsage(value *responseUsage) (model.Usage, error) {
	if value == nil {
		return model.Usage{}, nil
	}
	if value.InputTokens == nil || value.OutputTokens == nil || value.TotalTokens == nil {
		return model.Usage{}, protocolError("usage requires input_tokens, output_tokens, and total_tokens")
	}
	if *value.InputTokens < 0 || *value.OutputTokens < 0 || *value.TotalTokens < 0 || *value.TotalTokens != *value.InputTokens+*value.OutputTokens {
		return model.Usage{}, protocolError("invalid usage counts")
	}
	return model.Usage{
		InputTokens: *value.InputTokens, OutputTokens: *value.OutputTokens,
		TotalTokens: *value.TotalTokens, Reported: true,
	}, nil
}
