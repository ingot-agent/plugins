package projection

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
)

func TestMultimodalHistoryPreservesOrderAndSources(t *testing.T) {
	messages := []model.Message{{Role: model.RoleAssistant, Content: content.Content{
		content.Text("caption"), content.AssetPart(content.KindImage, "image/png", "photo.png", asset.Reference{ID: "asset-one"}),
		content.Inline(content.KindAudio, "audio/wav", "audio.wav", []byte{1, 2}), content.URI(content.KindFile, "application/pdf", "report.pdf", "urn:report"),
	}}}
	projected := Messages(messages)
	messages[0].Content[2].Media.Source.Data[0] = 9
	if projected[0].Content[2].Source.Data[0] != 1 {
		t.Fatal("projection retained inline bytes")
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"kind":"text"`, `"kind":"image"`, `"kind":"audio"`, `"kind":"file"`, `"assetId":"asset-one"`, `"uri":"urn:report"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %s in %s", want, encoded)
		}
	}
	if projected[0].Content[0].Text != "caption" || projected[0].Content[1].Name != "photo.png" {
		t.Fatal("content order or metadata changed")
	}
}

func TestToolResultWhitespaceSurvivesJSONProjection(t *testing.T) {
	text := "\nstdout:\n  第一行  with spaces\n\n\tsecond line\r\n" + `literal \n` + "\n"
	messages := []model.Message{{Role: model.RoleTool, ToolCallID: "call", Content: content.FromText(text)}}
	encoded, err := json.Marshal(Messages(messages))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []Message
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 || decoded[0].Role != "tool" || decoded[0].ToolCallID != "call" || len(decoded[0].Content) != 1 {
		t.Fatalf("unexpected tool result: %#v", decoded)
	}
	if got := decoded[0].Content[0].Text; got != text {
		t.Fatalf("projected text=%q want=%q", got, text)
	}
}
