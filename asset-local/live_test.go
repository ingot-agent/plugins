package assetlocal

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

type configTestChannel struct{ values []interaction.Answer }

func (c *configTestChannel) Request(context.Context, interaction.Request) (interaction.Response, error) {
	return interaction.Response{Values: c.values}, nil
}
func (*configTestChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*configTestChannel) Set(context.Context, interaction.State) error  { return nil }
func (*configTestChannel) Clear(context.Context, string) error           { return nil }

func TestSetupAppliesToRunningAssetStore(t *testing.T) {
	scope := testScope(filepath.Join(t.TempDir(), "state"))
	exports, _, err := New(context.Background(), Dependencies{State: scope})
	if err != nil {
		t.Fatal(err)
	}
	channel := &configTestChannel{values: []interaction.Answer{
		{Name: "max_object_bytes", Value: interaction.IntegerValue(1)},
		{Name: "max_total_bytes", Value: interaction.IntegerValue(1024)},
		{Name: "io_concurrency", Value: interaction.IntegerValue(2)},
	}}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(result.Output), `"restart_required":false`) {
		t.Fatalf("output = %s", result.Output)
	}
	_, _, err = exports.Store.Put(context.Background(), asset.PutRequest{Body: bytes.NewReader([]byte("xx")), Size: 2})
	if !errors.Is(err, ErrObjectLimit) {
		t.Fatalf("error = %v, want ErrObjectLimit", err)
	}
}
