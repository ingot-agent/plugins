package hostcomponent

import (
	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/plugins/app-webui/internal/projection"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
)

func (o *observer) project(event observation.Event) {
	detail := event.Detail
	data := map[string]any{"sequence": event.Sequence, "time": event.Time}
	agentScope := &appbackend.AgentScope{SessionID: string(event.Correlation.SessionID), TurnID: string(event.Correlation.TurnID)}
	eventType := ""
	round, tool := false, false
	terminal := func(status observation.Status, err string) {
		data["status"] = map[observation.Status]string{observation.StatusSucceeded: "succeeded", observation.StatusFailed: "failed", observation.StatusCanceled: "canceled"}[status]
		if err != "" {
			data["error"] = err
		}
	}
	switch d := detail.(type) {
	case observation.TurnStarted:
		eventType = "agent.turn.started"
		inputs := make([]any, 0, len(d.Turn.Inputs))
		for _, input := range d.Turn.Inputs {
			attachments := make([]any, 0, len(input.Attachments))
			for _, a := range input.Attachments {
				value := map[string]any{"name": a.Name, "mimeType": a.MIMEType, "size": a.Size, "path": a.Path}
				if a.Media != nil {
					value["media"] = projection.Content(content.Content{content.AttachmentPart(*a.Media)})
				}
				attachments = append(attachments, value)
			}
			inputs = append(inputs, map[string]any{"text": input.Text, "attachments": attachments})
		}
		data["turn"] = map[string]any{"sessionId": string(d.Turn.SessionID), "inputs": inputs}
	case observation.TurnFinished:
		eventType = "agent.turn.finished"
		terminal(d.Status, d.Error)
		data["outcome"] = projection.Outcome(d.Outcome)
		if d.Result != nil {
			data["result"] = map[string]any{"output": projection.Content(d.Result.Output)}
		}
	case observation.RoundStarted:
		eventType, round = "agent.round.started", true
	case observation.RoundFinished:
		eventType, round = "agent.round.finished", true
		terminal(d.Status, d.Error)
		if d.Result != nil {
			data["result"] = map[string]any{"decision": projection.Messages([]model.Message{d.Result.Decision})[0], "toolMessages": projection.Messages(d.Result.ToolMessages)}
		}
	case observation.ModelStarted:
		eventType, round = "agent.model.started", true
		data["request"] = projection.ModelRequest(d.Request)
	case observation.ModelProgress:
		eventType, round = "agent.model.progress", true
		data["progress"] = projection.ModelProgress(d.Progress)
	case observation.ModelFinished:
		eventType, round = "agent.model.finished", true
		terminal(d.Status, d.Error)
		if d.Response != nil {
			data["response"] = projection.ModelResponse(*d.Response)
		}
	case observation.ToolStarted:
		eventType, round, tool = "agent.tool.started", true, true
		data["call"] = projection.ToolCall(d.Call)
	case observation.ToolProgress:
		eventType, round, tool = "agent.tool.progress", true, true
		data["progress"] = map[string]any{"channel": d.Progress.Channel, "content": projection.Content(d.Progress.Content)}
	case observation.ToolFinished:
		eventType, round, tool = "agent.tool.finished", true, true
		terminal(d.Status, d.Error)
		if d.Result != nil {
			data["result"] = map[string]any{"content": projection.Content(d.Result.Content)}
		}
	default:
		return
	}
	if round {
		index := event.Correlation.RoundIndex
		agentScope.RoundIndex = &index
	}
	if tool {
		agentScope.ToolCallID = event.Correlation.ToolCallID
	}
	_ = o.events.Publish(appbackend.Event{Type: eventType, Scope: &appbackend.Scope{Agent: agentScope}, Data: data})
}

var _ observation.Observer = (*observer)(nil)
