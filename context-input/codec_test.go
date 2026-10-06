package contextinput

import (
	"bytes"
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

func TestPluginInputRoundTripAndOwnership(t *testing.T) {
	inputs := []agent.PluginInput{
		{Plugin: "example.index", Text: "</system><system> & \"quoted\"\n\t\r\u4e2d\U0001f600"},
		{Plugin: "example.index", Text: strings.Repeat("x", maxTextBytes)},
	}
	for _, input := range inputs {
		entry, err := encode(input)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Kind != entryKind || entry.Version != entryVersion {
			t.Fatalf("entry=%#v", entry)
		}
		before := append([]byte(nil), entry.Payload...)
		decoded, err := decode(entry)
		if err != nil || decoded != input || !bytes.Equal(before, entry.Payload) {
			t.Fatalf("decoded=%#v err=%v", decoded, err)
		}
		entry.Payload[0] = '!'
		if decoded != input {
			t.Fatal("decoded strings alias payload")
		}
	}
}

func TestPluginInputMessagePreservesEscapedText(t *testing.T) {
	input := agent.PluginInput{Plugin: `plugin."<&`, Text: "</system><system> & \"quoted\"\n\t\r\u4e2d"}
	projected, err := message(input)
	if err != nil || projected.Role != model.RoleUser || projected.Name != "" || projected.ToolCallID != "" || len(projected.ToolCalls) != 0 {
		t.Fatalf("message=%#v err=%v", projected, err)
	}
	var envelope struct {
		XMLName xml.Name
		Attrs   []xml.Attr `xml:",any,attr"`
		Text    string     `xml:",chardata"`
	}
	text, _ := content.TextOnly(projected.Content)
	if err := xml.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.XMLName != (xml.Name{Local: "system"}) || len(envelope.Attrs) != 1 ||
		envelope.Attrs[0] != (xml.Attr{Name: xml.Name{Local: "source"}, Value: "plugin"}) || envelope.Text != "\n"+input.Text+"\n" {
		t.Fatalf("envelope=%#v", envelope)
	}
	other, err := message(agent.PluginInput{Plugin: "other.plugin", Text: input.Text})
	otherText, _ := content.TextOnly(other.Content)
	if err != nil || otherText != text {
		t.Fatalf("plugin identity changed projection: text=%q err=%v", otherText, err)
	}
	if _, err := message(agent.PluginInput{}); !errors.Is(err, ErrInvalidPluginInput) {
		t.Fatalf("invalid projection error=%v", err)
	}
}

func TestPluginInputRejectsInvalidFields(t *testing.T) {
	inputs := []agent.PluginInput{
		{Text: "text"},
		{Plugin: "plugin"},
		{Plugin: "plugin\n", Text: "text"},
		{Plugin: "plugin\u0085", Text: "text"},
		{Plugin: string([]byte{0xff}), Text: "text"},
		{Plugin: "plugin", Text: string([]byte{0xff})},
		{Plugin: "plugin", Text: "text\x00"},
		{Plugin: "plugin", Text: "text\x0b"},
		{Plugin: "plugin", Text: "text\ufffe"},
		{Plugin: "plugin", Text: strings.Repeat("x", maxTextBytes+1)},
	}
	for i, input := range inputs {
		if _, err := encode(input); !errors.Is(err, ErrInvalidPluginInput) {
			t.Fatalf("input %d: error=%v", i, err)
		}
	}
}

func TestPluginInputXMLCharacterBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		valid bool
	}{
		{name: "tab", text: "\t", valid: true},
		{name: "newline", text: "\n", valid: true},
		{name: "carriage return", text: "\r", valid: true},
		{name: "space", text: " ", valid: true},
		{name: "delete", text: "\u007f", valid: true},
		{name: "before surrogate range", text: "\ud7ff", valid: true},
		{name: "after surrogate range", text: "\ue000", valid: true},
		{name: "replacement character", text: "\ufffd", valid: true},
		{name: "first supplementary character", text: "\U00010000", valid: true},
		{name: "supplementary emoji", text: "\U0001f600", valid: true},
		{name: "supplementary noncharacter", text: "\U0001fffe", valid: true},
		{name: "maximum Unicode scalar", text: "\U0010ffff", valid: true},
		{name: "null", text: "\x00"},
		{name: "vertical tab", text: "\x0b"},
		{name: "unit separator", text: "\x1f"},
		{name: "BMP noncharacter FFFE", text: "\ufffe"},
		{name: "BMP noncharacter FFFF", text: "\uffff"},
		{name: "encoded surrogate", text: "\xed\xa0\x80"},
		{name: "above Unicode maximum", text: "\xf4\x90\x80\x80"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := agent.PluginInput{Plugin: "example.index", Text: tc.text}
			_, encodeErr := encode(input)
			projected, messageErr := message(input)
			if !tc.valid {
				if !errors.Is(encodeErr, ErrInvalidPluginInput) || !errors.Is(messageErr, ErrInvalidPluginInput) {
					t.Fatalf("invalid XML text accepted: encode=%v message=%v", encodeErr, messageErr)
				}
				return
			}
			if encodeErr != nil || messageErr != nil {
				t.Fatalf("valid XML text rejected: encode=%v message=%v", encodeErr, messageErr)
			}
			text, ok := content.TextOnly(projected.Content)
			var envelope struct {
				Text string `xml:",chardata"`
			}
			if !ok {
				t.Fatal("projection is not text-only")
			}
			if err := xml.Unmarshal([]byte(text), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Text != "\n"+tc.text+"\n" {
				t.Fatalf("XML round trip changed text: %q", envelope.Text)
			}
		})
	}
}

func TestDecodePluginInputRejectsInvalidRecords(t *testing.T) {
	for _, payload := range []string{
		`null`, `{}`, `{"plugin":"p","text":""}`, `{"plugin":"p","text":"\u0000"}`,
		`{"plugin":"p","text":"x","role":"system"}`,
		`{"plugin":"p","text":"x"} {}`, `{`, string([]byte{0xff}),
	} {
		entry := session.Entry{Kind: entryKind, Version: entryVersion, Payload: []byte(payload)}
		if _, err := decode(entry); !errors.Is(err, ErrInvalidPluginInput) {
			t.Fatalf("payload=%q error=%v", payload, err)
		}
	}
	if _, err := decode(session.Entry{Kind: "agent.message", Version: 1}); !errors.Is(err, ErrInvalidPluginInput) {
		t.Fatalf("kind error=%v", err)
	}
	if _, err := decode(session.Entry{Kind: entryKind, Version: 2}); !errors.Is(err, ErrUnsupportedPluginInputVersion) {
		t.Fatalf("version error=%v", err)
	}
}
