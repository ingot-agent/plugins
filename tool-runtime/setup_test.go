package toolruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

type setupChannel struct {
	request  interaction.Request
	response interaction.Response
}

func (c *setupChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	return c.response, nil
}

func (*setupChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*setupChannel) Set(context.Context, interaction.State) error  { return nil }
func (*setupChannel) Clear(context.Context, string) error           { return nil }

func TestTextLimitConfiguration(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   int
		want    int
		invalid bool
	}{
		{name: "default", want: 64 * 1024},
		{name: "minimum", value: minimumMaxTextBytes, want: minimumMaxTextBytes},
		{name: "below minimum", value: minimumMaxTextBytes - 1, invalid: true},
		{name: "negative", value: -1, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			exports, _, err := New(context.Background(), withState(t, Config{MaxTextBytes: test.value}, Dependencies{}))
			if test.invalid {
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("error=%v want ErrInvalidConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := exports.Runtime.(*runtime).maxText; got != test.want {
				t.Fatalf("max text=%d want=%d", got, test.want)
			}
		})
	}
}

func TestSetupTextLimitDefaultAndMinimum(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "accept minimum", true: "reject below minimum"}[invalid], func(t *testing.T) {
			scope := testStateScope{dir: writeTestConfig(t, Config{})}
			active, err := normalizeConfig(Config{})
			if err != nil {
				t.Fatal(err)
			}
			selected := minimumMaxTextBytes
			if invalid {
				selected--
			}
			channel := &setupChannel{response: interaction.Response{Values: []interaction.Answer{
				{Name: "max_text_bytes", Value: interaction.Value{Kind: interaction.ValueInteger, Integer: int64(selected)}},
			}}}
			op := &setupOperation{scope: scope, active: active}
			result, err := op.Invoke(context.Background(), operation.Request{Interaction: channel})
			found := false
			for _, field := range channel.request.Fields {
				if field.Name == "max_text_bytes" {
					found = true
					if field.Default == nil || field.Default.Integer != 64*1024 {
						t.Fatalf("text limit default=%#v", field.Default)
					}
				}
			}
			if !found {
				t.Fatal("text limit field is missing")
			}
			stored, loadErr := loadConfig(scope.Dir())
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if invalid {
				if !errors.Is(err, ErrInvalidConfig) || stored != (Config{}) {
					t.Fatalf("error=%v stored=%#v", err, stored)
				}
				return
			}
			if err != nil || stored.MaxTextBytes != minimumMaxTextBytes {
				t.Fatalf("error=%v stored=%#v", err, stored)
			}
			var output struct {
				RestartRequired bool `json:"restart_required"`
			}
			if err := json.Unmarshal(result.Output, &output); err != nil || !output.RestartRequired {
				t.Fatalf("output=%s error=%v", result.Output, err)
			}
		})
	}
}
