package openairesponses

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
)

type responseStreamEvent struct {
	Type         string          `json:"type"`
	Code         string          `json:"code"`
	Message      string          `json:"message"`
	Delta        string          `json:"delta"`
	Text         string          `json:"text"`
	Refusal      string          `json:"refusal"`
	Arguments    string          `json:"arguments"`
	OutputIndex  *int            `json:"output_index"`
	ContentIndex *int            `json:"content_index"`
	SummaryIndex *int            `json:"summary_index"`
	Item         json.RawMessage `json:"item"`
	Part         json.RawMessage `json:"part"`
	Response     *responseObject `json:"response"`
}

type responseStreamPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type streamPartKey struct {
	output int
	part   int
}

type accumulatedStreamPart struct {
	index int
	kind  string
	text  strings.Builder
	open  bool
}

type accumulatedFunctionCall struct {
	outputIndex int
	callID      string
	name        string
	arguments   strings.Builder
}

type streamAccumulator struct {
	provider string
	secret   string

	contentParts  map[streamPartKey]*accumulatedStreamPart
	contentOrder  []*accumulatedStreamPart
	activeContent *accumulatedStreamPart

	reasoningParts  map[streamPartKey]*accumulatedStreamPart
	reasoningOrder  []*accumulatedStreamPart
	activeReasoning *accumulatedStreamPart

	functionCalls map[int]*accumulatedFunctionCall
	functionOrder []*accumulatedFunctionCall

	terminal *model.Response
}

func (p *provider) Stream(ctx context.Context, request model.Request, handler model.StreamHandler) (model.Response, error) {
	if err := p.validateRequest(ctx, request); err != nil {
		return model.Response{}, err
	}
	if handler == nil {
		return model.Response{}, fmt.Errorf("nil stream handler: %w", ErrInvalidRequest)
	}
	body, err := p.encodeResponseRequest(ctx, request, true)
	if err != nil {
		return model.Response{}, err
	}
	response, err := p.do(ctx, body, "text/event-stream")
	if err != nil {
		return model.Response{}, err
	}
	stopBodyWatch := closeBodyOnCancel(ctx, response.Body)
	defer stopBodyWatch()
	if err := p.checkStatus(ctx, response); err != nil {
		return model.Response{}, err
	}
	return p.decodeStream(ctx, response, handler)
}

func (p *provider) decodeStream(ctx context.Context, response *http.Response, handler model.StreamHandler) (model.Response, error) {
	consumer := handler
	handler = func(event model.StreamEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return consumer(event)
	}
	limited := &io.LimitedReader{R: response.Body, N: int64(p.maxResponseBytes) + 1}
	reader := bufio.NewReader(limited)
	dataLines := make([]string, 0, 1)
	eventName := ""
	accumulator := newStreamAccumulator(p.name, p.apiKey)

	dispatch := func() error {
		defer func() {
			dataLines = dataLines[:0]
			eventName = ""
		}()
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.Join(dataLines, "\n")
		if strings.TrimSpace(payload) == "[DONE]" {
			if len(dataLines) != 1 || accumulator.terminal == nil {
				return protocolError("[DONE] is only valid after a terminal response event")
			}
			return nil
		}
		if !utf8.ValidString(payload) {
			return protocolError("SSE data is not valid UTF-8")
		}
		var event responseStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return protocolError("decode SSE data: %v", err)
		}
		if event.Type == "" {
			event.Type = eventName
		}
		if event.Type == "" || (eventName != "" && eventName != event.Type) {
			return protocolError("SSE event name and payload type are missing or inconsistent")
		}
		if accumulator.terminal != nil {
			return protocolError("stream event %q arrived after terminal response", event.Type)
		}
		return accumulator.add(event, handler)
	}

	for {
		if err := ctx.Err(); err != nil {
			return model.Response{}, err
		}
		line, err := reader.ReadString('\n')
		if ctxErr := ctx.Err(); ctxErr != nil {
			return model.Response{}, ctxErr
		}
		if limited.N <= 0 {
			return model.Response{}, &ResponseLimitError{Limit: p.maxResponseBytes, Kind: "stream response"}
		}
		if err != nil && err != io.EOF {
			return model.Response{}, wrapReadError(fmt.Errorf("read model stream: %w", err))
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if !utf8.ValidString(line) {
			return model.Response{}, protocolError("SSE line is not valid UTF-8")
		}
		if line == "" {
			if dispatchErr := dispatch(); dispatchErr != nil {
				return model.Response{}, dispatchErr
			}
		} else if !strings.HasPrefix(line, ":") {
			field, value, found := strings.Cut(line, ":")
			if !found {
				field, value = line, ""
			}
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			switch field {
			case "event":
				eventName = value
			case "data":
				dataLines = append(dataLines, value)
			}
		}
		if err == io.EOF {
			if dispatchErr := dispatch(); dispatchErr != nil {
				return model.Response{}, dispatchErr
			}
			break
		}
	}
	if accumulator.terminal == nil {
		return model.Response{}, protocolError("stream ended before a terminal response event")
	}
	if err := accumulator.finish(handler); err != nil {
		return model.Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	return *accumulator.terminal, nil
}

func newStreamAccumulator(provider, secret string) *streamAccumulator {
	return &streamAccumulator{
		provider: provider, secret: secret,
		contentParts:   make(map[streamPartKey]*accumulatedStreamPart),
		reasoningParts: make(map[streamPartKey]*accumulatedStreamPart),
		functionCalls:  make(map[int]*accumulatedFunctionCall),
	}
}

func (a *streamAccumulator) add(event responseStreamEvent, handler model.StreamHandler) error {
	switch event.Type {
	case "response.created", "response.queued", "response.in_progress":
		return nil
	case "response.output_item.added", "response.output_item.done":
		return a.addOutputItem(event)
	case "response.content_part.added":
		part, err := decodeStreamPart(event.Part)
		if err != nil {
			return err
		}
		return a.startContent(event, part.Type, handler)
	case "response.content_part.done":
		return a.endContent(event, "", false, handler)
	case "response.output_text.delta":
		return a.addContentDelta(event, "output_text", event.Delta, handler)
	case "response.output_text.done":
		return a.endContent(event, event.Text, true, handler)
	case "response.refusal.delta":
		return a.addContentDelta(event, "refusal", event.Delta, handler)
	case "response.refusal.done":
		return a.endContent(event, event.Refusal, true, handler)
	case "response.reasoning_summary_part.added":
		if err := validateReasoningPart(event.Part); err != nil {
			return err
		}
		return a.startReasoning(event, event.SummaryIndex, handler)
	case "response.reasoning_summary_part.done":
		return a.endReasoning(event, event.SummaryIndex, "", false, handler)
	case "response.reasoning_summary_text.delta":
		return a.addReasoningDelta(event, event.SummaryIndex, event.Delta, handler)
	case "response.reasoning_summary_text.done":
		return a.endReasoning(event, event.SummaryIndex, event.Text, true, handler)
	case "response.reasoning_text.delta":
		return a.addReasoningDelta(event, event.ContentIndex, event.Delta, handler)
	case "response.reasoning_text.done":
		return a.endReasoning(event, event.ContentIndex, event.Text, true, handler)
	case "response.function_call_arguments.delta":
		call, err := a.functionCall(event)
		if err != nil {
			return err
		}
		call.arguments.WriteString(event.Delta)
		return nil
	case "response.function_call_arguments.done":
		call, err := a.functionCall(event)
		if err != nil {
			return err
		}
		if call.arguments.Len() == 0 {
			call.arguments.WriteString(event.Arguments)
		} else if call.arguments.String() != event.Arguments {
			return protocolError("function call arguments at output %d do not match accumulated deltas", call.outputIndex)
		}
		return nil
	case "response.completed", "response.incomplete":
		if event.Response == nil {
			return protocolError("terminal event %q is missing response", event.Type)
		}
		wantStatus := strings.TrimPrefix(event.Type, "response.")
		if event.Response.Status != wantStatus {
			return protocolError("terminal event %q carries response status %q", event.Type, event.Response.Status)
		}
		result, err := decodeResponseObject(*event.Response, a.provider, a.secret)
		if err != nil {
			return err
		}
		if err := a.validateFinal(result); err != nil {
			return err
		}
		a.terminal = &result
		return nil
	case "response.failed", "response.cancelled":
		if event.Response == nil {
			return protocolError("terminal event %q is missing response", event.Type)
		}
		_, err := decodeResponseObject(*event.Response, a.provider, a.secret)
		return err
	case "error":
		return providerResponseError("failed", &responseError{Code: event.Code, Message: event.Message}, a.secret)
	default:
		return protocolError("unsupported Responses API stream event %q", event.Type)
	}
}

func decodeStreamPart(raw json.RawMessage) (responseStreamPart, error) {
	if len(raw) == 0 {
		return responseStreamPart{}, protocolError("content part event is missing part")
	}
	var part responseStreamPart
	if err := json.Unmarshal(raw, &part); err != nil {
		return responseStreamPart{}, protocolError("decode stream content part: %v", err)
	}
	if part.Type != "output_text" && part.Type != "refusal" {
		return responseStreamPart{}, protocolError("unsupported stream content part type %q", part.Type)
	}
	return part, nil
}

func validateReasoningPart(raw json.RawMessage) error {
	if len(raw) == 0 {
		return protocolError("reasoning part event is missing part")
	}
	var part responseStreamPart
	if err := json.Unmarshal(raw, &part); err != nil {
		return protocolError("decode reasoning part: %v", err)
	}
	if part.Type != "summary_text" {
		return protocolError("unsupported reasoning part type %q", part.Type)
	}
	return nil
}

func streamKey(outputIndex, partIndex *int, eventType string) (streamPartKey, error) {
	if outputIndex == nil || partIndex == nil || *outputIndex < 0 || *partIndex < 0 {
		return streamPartKey{}, protocolError("event %q requires non-negative output and part indices", eventType)
	}
	return streamPartKey{output: *outputIndex, part: *partIndex}, nil
}

func (a *streamAccumulator) startContent(event responseStreamEvent, kind string, handler model.StreamHandler) error {
	key, err := streamKey(event.OutputIndex, event.ContentIndex, event.Type)
	if err != nil {
		return err
	}
	if part, exists := a.contentParts[key]; exists {
		if part.kind != kind {
			return protocolError("content part %d:%d changed type", key.output, key.part)
		}
		return nil
	}
	if a.activeContent != nil {
		return protocolError("content part started before the previous part ended")
	}
	part := &accumulatedStreamPart{index: len(a.contentOrder), kind: kind, open: true}
	a.contentParts[key] = part
	a.contentOrder = append(a.contentOrder, part)
	a.activeContent = part
	return handler(model.StreamEvent{
		Kind: model.StreamPartStart, PartIndex: part.index,
		Semantic: model.StreamSemanticContent, PartKind: content.KindText,
	})
}

func (a *streamAccumulator) addContentDelta(event responseStreamEvent, kind, delta string, handler model.StreamHandler) error {
	if !utf8.ValidString(delta) {
		return protocolError("event %q has invalid UTF-8 delta", event.Type)
	}
	key, err := streamKey(event.OutputIndex, event.ContentIndex, event.Type)
	if err != nil {
		return err
	}
	part := a.contentParts[key]
	if part == nil {
		if err := a.startContent(event, kind, handler); err != nil {
			return err
		}
		part = a.contentParts[key]
	}
	if !part.open || part.kind != kind {
		return protocolError("event %q targets a closed or mismatched content part", event.Type)
	}
	part.text.WriteString(delta)
	if delta == "" {
		return nil
	}
	return handler(model.StreamEvent{
		Kind: model.StreamPartDelta, PartIndex: part.index,
		Semantic: model.StreamSemanticContent, TextDelta: delta,
	})
}

func (a *streamAccumulator) endContent(event responseStreamEvent, final string, verify bool, handler model.StreamHandler) error {
	key, err := streamKey(event.OutputIndex, event.ContentIndex, event.Type)
	if err != nil {
		return err
	}
	part := a.contentParts[key]
	if part == nil {
		return protocolError("event %q ends an unknown content part", event.Type)
	}
	if verify && part.text.String() != final {
		return protocolError("content part %d:%d does not match accumulated deltas", key.output, key.part)
	}
	if !part.open {
		return nil
	}
	part.open = false
	if a.activeContent == part {
		a.activeContent = nil
	}
	return handler(model.StreamEvent{
		Kind: model.StreamPartEnd, PartIndex: part.index,
		Semantic: model.StreamSemanticContent,
	})
}

func (a *streamAccumulator) startReasoning(event responseStreamEvent, partIndex *int, handler model.StreamHandler) error {
	key, err := streamKey(event.OutputIndex, partIndex, event.Type)
	if err != nil {
		return err
	}
	if _, exists := a.reasoningParts[key]; exists {
		return nil
	}
	if a.activeReasoning != nil {
		return protocolError("reasoning part started before the previous part ended")
	}
	part := &accumulatedStreamPart{index: len(a.reasoningOrder), kind: "reasoning", open: true}
	a.reasoningParts[key] = part
	a.reasoningOrder = append(a.reasoningOrder, part)
	a.activeReasoning = part
	return handler(model.StreamEvent{
		Kind: model.StreamPartStart, PartIndex: part.index,
		Semantic: model.StreamSemanticReasoning, PartKind: content.KindText,
	})
}

func (a *streamAccumulator) addReasoningDelta(event responseStreamEvent, partIndex *int, delta string, handler model.StreamHandler) error {
	if !utf8.ValidString(delta) {
		return protocolError("event %q has invalid UTF-8 delta", event.Type)
	}
	key, err := streamKey(event.OutputIndex, partIndex, event.Type)
	if err != nil {
		return err
	}
	part := a.reasoningParts[key]
	if part == nil {
		if err := a.startReasoning(event, partIndex, handler); err != nil {
			return err
		}
		part = a.reasoningParts[key]
	}
	if !part.open {
		return protocolError("event %q targets a closed reasoning part", event.Type)
	}
	part.text.WriteString(delta)
	if delta == "" {
		return nil
	}
	return handler(model.StreamEvent{
		Kind: model.StreamPartDelta, PartIndex: part.index,
		Semantic: model.StreamSemanticReasoning, TextDelta: delta,
	})
}

func (a *streamAccumulator) endReasoning(event responseStreamEvent, partIndex *int, final string, verify bool, handler model.StreamHandler) error {
	key, err := streamKey(event.OutputIndex, partIndex, event.Type)
	if err != nil {
		return err
	}
	part := a.reasoningParts[key]
	if part == nil {
		return protocolError("event %q ends an unknown reasoning part", event.Type)
	}
	if verify && part.text.String() != final {
		return protocolError("reasoning part %d:%d does not match accumulated deltas", key.output, key.part)
	}
	if !part.open {
		return nil
	}
	part.open = false
	if a.activeReasoning == part {
		a.activeReasoning = nil
	}
	return handler(model.StreamEvent{
		Kind: model.StreamPartEnd, PartIndex: part.index,
		Semantic: model.StreamSemanticReasoning,
	})
}

func (a *streamAccumulator) addOutputItem(event responseStreamEvent) error {
	if event.OutputIndex == nil || *event.OutputIndex < 0 || len(event.Item) == 0 {
		return protocolError("event %q requires output_index and item", event.Type)
	}
	var itemType responseOutputItemType
	if err := json.Unmarshal(event.Item, &itemType); err != nil || itemType.Type == "" {
		return protocolError("event %q has an invalid item", event.Type)
	}
	switch itemType.Type {
	case "message", "reasoning":
		return nil
	case "function_call":
		var item responseFunctionCallOutput
		if err := json.Unmarshal(event.Item, &item); err != nil {
			return protocolError("decode streamed function call: %v", err)
		}
		call, exists := a.functionCalls[*event.OutputIndex]
		if !exists {
			call = &accumulatedFunctionCall{outputIndex: *event.OutputIndex, callID: item.CallID, name: item.Name}
			call.arguments.WriteString(item.Arguments)
			a.functionCalls[*event.OutputIndex] = call
			a.functionOrder = append(a.functionOrder, call)
			return nil
		}
		if item.CallID != "" && call.callID != "" && item.CallID != call.callID {
			return protocolError("function call id changed at output %d", *event.OutputIndex)
		}
		if item.Name != "" && call.name != "" && item.Name != call.name {
			return protocolError("function call name changed at output %d", *event.OutputIndex)
		}
		if call.callID == "" {
			call.callID = item.CallID
		}
		if call.name == "" {
			call.name = item.Name
		}
		if item.Arguments != "" {
			if call.arguments.Len() == 0 {
				call.arguments.WriteString(item.Arguments)
			} else if call.arguments.String() != item.Arguments {
				return protocolError("function call arguments changed at output %d", *event.OutputIndex)
			}
		}
		return nil
	default:
		return protocolError("unsupported streamed output item type %q", itemType.Type)
	}
}

func (a *streamAccumulator) functionCall(event responseStreamEvent) (*accumulatedFunctionCall, error) {
	if event.OutputIndex == nil || *event.OutputIndex < 0 {
		return nil, protocolError("event %q requires a non-negative output_index", event.Type)
	}
	call := a.functionCalls[*event.OutputIndex]
	if call == nil {
		return nil, protocolError("event %q targets an unknown function call", event.Type)
	}
	return call, nil
}

func (a *streamAccumulator) validateFinal(result model.Response) error {
	if len(result.Message.Content) != len(a.contentOrder) {
		return protocolError("terminal response content count does not match streamed parts")
	}
	for i, part := range result.Message.Content {
		if part.Kind != content.KindText || part.Text != a.contentOrder[i].text.String() {
			return protocolError("terminal response content part %d does not match streamed deltas", i)
		}
	}
	if len(result.Message.ToolCalls) != len(a.functionOrder) {
		return protocolError("terminal response tool call count does not match streamed calls")
	}
	for i, call := range result.Message.ToolCalls {
		streamed := a.functionOrder[i]
		if call.ID != streamed.callID || call.Name != streamed.name || string(call.Arguments) != streamed.arguments.String() {
			return protocolError("terminal response tool call %d does not match streamed call", i)
		}
	}
	return nil
}

func (a *streamAccumulator) finish(handler model.StreamHandler) error {
	if a.activeReasoning != nil && a.activeReasoning.open {
		part := a.activeReasoning
		part.open = false
		if err := handler(model.StreamEvent{
			Kind: model.StreamPartEnd, PartIndex: part.index,
			Semantic: model.StreamSemanticReasoning,
		}); err != nil {
			return err
		}
		a.activeReasoning = nil
	}
	if a.activeContent != nil && a.activeContent.open {
		part := a.activeContent
		part.open = false
		if err := handler(model.StreamEvent{
			Kind: model.StreamPartEnd, PartIndex: part.index,
			Semantic: model.StreamSemanticContent,
		}); err != nil {
			return err
		}
		a.activeContent = nil
	}
	return nil
}
