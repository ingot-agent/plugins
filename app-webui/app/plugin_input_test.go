package appcomponent

import (
	"context"
	"encoding/json"
	"encoding/xml"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

const testPluginInputKind = "test.plugin_input"

type pluginInputWriterFunc func(context.Context, session.ID, agent.PluginInput) error

func (f pluginInputWriterFunc) Append(ctx context.Context, id session.ID, input agent.PluginInput) error {
	return f(ctx, id, input)
}

type testPluginInputs struct {
	store session.Store
}

func (p testPluginInputs) Append(ctx context.Context, id session.ID, input agent.PluginInput) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return p.store.Append(ctx, id, session.Entry{Kind: testPluginInputKind, Version: 1, Payload: payload})
}

func (p testPluginInputs) Project(entry session.Entry) (model.Message, bool, error) {
	if entry.Kind != testPluginInputKind {
		return model.Message{}, false, nil
	}
	var input agent.PluginInput
	if err := json.Unmarshal(entry.Payload, &input); err != nil {
		return model.Message{}, true, err
	}
	text, err := xml.Marshal(struct {
		XMLName xml.Name `xml:"system"`
		Source  string   `xml:"source,attr"`
		Text    string   `xml:",chardata"`
	}{Source: "plugin", Text: "\n" + input.Text + "\n"})
	if err != nil {
		return model.Message{}, true, err
	}
	return model.Message{Role: model.RoleUser, Content: content.FromText(string(text))}, true, nil
}
