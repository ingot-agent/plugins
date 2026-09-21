package toolruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/tool"
)

func TestRuntimeTextTruncationBoundaries(t *testing.T) {
	limit := minimumMaxTextBytes + 8
	for _, test := range []struct {
		name  string
		limit int
		input content.Content
		want  string
	}{
		{name: "empty", limit: limit},
		{name: "exact limit", limit: limit, input: content.FromText(strings.Repeat("a", limit)), want: strings.Repeat("a", limit)},
		{name: "one byte over", limit: limit, input: content.FromText("head" + strings.Repeat("x", limit-7) + "tail"), want: "head" + textTruncationMarker + "tail"},
		{name: "multiple parts", limit: limit, input: content.Content{content.Text("he"), content.Text("ad" + strings.Repeat("x", limit)), content.Text("ta"), content.Text("il")}, want: "head" + textTruncationMarker + "tail"},
		{name: "UTF-8 boundaries", limit: limit, input: content.FromText("\u4e16\u754c" + strings.Repeat("x", limit) + "\u4e16\u754c"), want: "\u4e16" + textTruncationMarker + "\u754c"},
		{name: "UTF-8 part boundaries", limit: limit, input: content.Content{content.Text("\u4e16"), content.Text("\u754c" + strings.Repeat("x", limit) + "\u4e16"), content.Text("\u754c")}, want: "\u4e16" + textTruncationMarker + "\u754c"},
		{name: "minimum limit", limit: minimumMaxTextBytes, input: content.FromText(strings.Repeat("x", minimumMaxTextBytes+1)), want: textTruncationMarker},
		{name: "odd retained budget", limit: limit + 1, input: content.FromText("start" + strings.Repeat("x", limit) + "tail"), want: "start" + textTruncationMarker + "tail"},
		{name: "default exact limit", input: content.FromText(strings.Repeat("x", 64*1024)), want: strings.Repeat("x", 64*1024)},
		{name: "default exceeded", input: content.FromText(strings.Repeat("x", 64*1024+1)), want: strings.Repeat("x", (64*1024-minimumMaxTextBytes+1)/2) + textTruncationMarker + strings.Repeat("x", (64*1024-minimumMaxTextBytes)/2)},
	} {
		t.Run(test.name, func(t *testing.T) {
			implementation := &resultTool{result: tool.Result{Content: test.input}}
			original := content.Clone(test.input)
			exports, _, err := New(context.Background(), withState(t, Config{MaxTextBytes: test.limit}, Dependencies{Tools: []tool.Tool{implementation}}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := exports.Runtime.Call(context.Background(), testInvocation("media", `{"x":"value"}`, execution.Scope{}))
			if err != nil {
				t.Fatal(err)
			}
			text, ok := content.TextOnly(result.Content)
			if !ok || text != test.want || !utf8.ValidString(text) {
				t.Fatalf("text=%q want=%q", text, test.want)
			}
			maximum := test.limit
			if maximum == 0 {
				maximum = 64 * 1024
			}
			if len(text) > maximum {
				t.Fatalf("text bytes=%d exceed limit=%d", len(text), maximum)
			}
			if !reflect.DeepEqual(implementation.result.Content, original) {
				t.Fatal("tool-owned result was changed")
			}
			if len(result.Content) > 0 {
				result.Content[0] = content.Text("changed")
				if !reflect.DeepEqual(implementation.result.Content, original) {
					t.Fatal("returned content aliases the tool result")
				}
			}
		})
	}
}

func TestRuntimeTruncatesAfterInterceptors(t *testing.T) {
	for _, shortCircuit := range []bool{false, true} {
		t.Run(map[bool]string{false: "appended output", true: "short circuit"}[shortCircuit], func(t *testing.T) {
			implementation := &fakeTool{definition: validDefinition("echo"), content: "head"}
			interceptor := interceptorFunc(func(ctx context.Context, invocation tool.Invocation, next pipeline.Next[tool.Invocation, tool.Result]) (tool.Result, error) {
				result := tool.Result{Content: content.FromText("head")}
				if !shortCircuit {
					var err error
					result, err = next(ctx, invocation)
					if err != nil {
						return tool.Result{}, err
					}
				}
				result.Content = append(result.Content, content.Text(strings.Repeat("x", minimumMaxTextBytes+1)), content.Text("tail"))
				return result, nil
			})
			exports, _, err := New(context.Background(), withState(t, Config{MaxTextBytes: minimumMaxTextBytes + 8}, Dependencies{
				Tools: []tool.Tool{implementation}, Interceptors: []tool.Interceptor{interceptor},
			}))
			if err != nil {
				t.Fatal(err)
			}
			result, err := exports.Runtime.Call(context.Background(), testInvocation("echo", `{"x":"value"}`, execution.Scope{}))
			text, ok := content.TextOnly(result.Content)
			if err != nil || !ok || text != "head"+textTruncationMarker+"tail" {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if (implementation.calls == 0) != shortCircuit {
				t.Fatalf("tool calls=%d short circuit=%v", implementation.calls, shortCircuit)
			}
		})
	}
}

func TestRuntimeTruncationPreservesMediaOrderAndOwnership(t *testing.T) {
	first := content.Inline(content.KindImage, "image/png", "first.png", []byte{1, 2})
	second := content.Inline(content.KindImage, "image/png", "second.png", []byte{3, 4})
	implementation := &resultTool{result: tool.Result{Content: content.Content{
		content.Text("head"), first, content.Text(strings.Repeat("x", minimumMaxTextBytes+1)), second, content.Text("tail"),
	}}}
	exports, _, err := New(context.Background(), withState(t, Config{MaxTextBytes: minimumMaxTextBytes + 8}, Dependencies{Tools: []tool.Tool{implementation}}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Call(context.Background(), testInvocation("media", `{"x":"value"}`, execution.Scope{}))
	if err != nil {
		t.Fatal(err)
	}
	want := content.Content{content.Text("head"), first, content.Text(textTruncationMarker), second, content.Text("tail")}
	if !reflect.DeepEqual(result.Content, want) {
		t.Fatalf("content=%#v want=%#v", result.Content, want)
	}
	result.Content[1].Media.Source.Data[0] = 9
	implementation.result.Content[3].Media.Source.Data[0] = 8
	if implementation.result.Content[1].Media.Source.Data[0] != 1 || result.Content[3].Media.Source.Data[0] != 3 {
		t.Fatal("truncated result aliases media data")
	}
}

func TestRuntimeTruncationDoesNotHideInvalidContentOrMedia(t *testing.T) {
	for _, test := range []struct {
		name    string
		content content.Content
		want    error
	}{
		{name: "invalid UTF-8 in discarded text", content: content.Content{content.Text(string([]byte{0xff}))}, want: content.ErrInvalidContent},
		{name: "oversized media part", content: content.Content{content.Inline(content.KindImage, "image/png", "", []byte{1, 2, 3, 4})}, want: ErrInvalidResult},
		{name: "oversized total media", content: content.Content{content.Inline(content.KindImage, "image/png", "", []byte{1, 2}), content.Inline(content.KindImage, "image/png", "", []byte{3, 4})}, want: ErrInvalidResult},
	} {
		t.Run(test.name, func(t *testing.T) {
			parts := append(content.Content{content.Text(strings.Repeat("x", minimumMaxTextBytes+1))}, test.content...)
			implementation := &resultTool{result: tool.Result{Content: parts}}
			exports, _, err := New(context.Background(), withState(t, Config{MaxTextBytes: minimumMaxTextBytes, MaxInlinePartBytes: 3, MaxInlineBytes: 3}, Dependencies{Tools: []tool.Tool{implementation}}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := exports.Runtime.Call(context.Background(), testInvocation("media", `{"x":"value"}`, execution.Scope{})); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}
