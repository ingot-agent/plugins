package contextinput

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

const (
	entryKind    = "agent.plugin_input"
	entryVersion = 1
	maxTextBytes = 64 * 1024
)

func message(input agent.PluginInput) (model.Message, error) {
	if err := validate(input); err != nil {
		return model.Message{}, err
	}
	envelope := struct {
		XMLName xml.Name `xml:"system"`
		Source  string   `xml:"source,attr"`
		Text    string   `xml:",chardata"`
	}{Source: "plugin", Text: "\n" + input.Text + "\n"}
	text, err := xml.Marshal(envelope)
	if err != nil {
		return model.Message{}, fmt.Errorf("format plugin input: %w", err)
	}
	return model.Message{Role: model.RoleUser, Content: content.FromText(string(text))}, nil
}

func encode(input agent.PluginInput) (session.Entry, error) {
	if err := validate(input); err != nil {
		return session.Entry{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return session.Entry{}, fmt.Errorf("encode plugin input: %w", err)
	}
	return session.Entry{Kind: entryKind, Version: entryVersion, Payload: payload}, nil
}

func decode(entry session.Entry) (agent.PluginInput, error) {
	if entry.Kind != entryKind {
		return agent.PluginInput{}, fmt.Errorf("entry kind %q: %w", entry.Kind, ErrInvalidPluginInput)
	}
	if entry.Version != entryVersion {
		return agent.PluginInput{}, fmt.Errorf("entry version %d: %w", entry.Version, ErrUnsupportedPluginInputVersion)
	}
	if !utf8.Valid(entry.Payload) {
		return agent.PluginInput{}, fmt.Errorf("payload is not valid UTF-8: %w", ErrInvalidPluginInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(entry.Payload))
	decoder.DisallowUnknownFields()
	var input agent.PluginInput
	if err := decoder.Decode(&input); err != nil {
		return agent.PluginInput{}, fmt.Errorf("decode plugin input: %w: %w", ErrInvalidPluginInput, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return agent.PluginInput{}, fmt.Errorf("payload must contain one JSON value: %w", ErrInvalidPluginInput)
	}
	if err := validate(input); err != nil {
		return agent.PluginInput{}, err
	}
	return input, nil
}

func validate(input agent.PluginInput) error {
	if input.Plugin == "" || !utf8.ValidString(input.Plugin) || !validXML(input.Plugin) {
		return fmt.Errorf("plugin name: %w", ErrInvalidPluginInput)
	}
	for _, r := range input.Plugin {
		if unicode.IsControl(r) {
			return fmt.Errorf("plugin name contains control characters: %w", ErrInvalidPluginInput)
		}
	}
	if input.Text == "" || len(input.Text) > maxTextBytes || !utf8.ValidString(input.Text) || !validXML(input.Text) {
		return fmt.Errorf("text must be nonempty XML-compatible UTF-8 and at most %d bytes: %w", maxTextBytes, ErrInvalidPluginInput)
	}
	return nil
}

func validXML(text string) bool {
	// range yields Unicode scalar values; the caller checks UTF-8 validity.
	for _, r := range text {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0xFFFE || r == 0xFFFF {
			return false
		}
	}
	return true
}
