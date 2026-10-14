package appcomponent

import (
	"context"
	"errors"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
)

func TestAgentControllerRequiresSubmissionControlAndHistory(t *testing.T) {
	runtime := &testAgent{}
	initTestAgent(t, runtime, nil)
	controller, err := newAgentController(runtime, runtime, runtime)
	if err != nil || !controller.Capabilities().SendMessage {
		t.Fatalf("controller=%v err=%v", controller, err)
	}
	for _, construct := range []func() error{
		func() error { _, err := newAgentController(nil, runtime, runtime); return err },
		func() error { _, err := newAgentController(runtime, nil, runtime); return err },
		func() error { _, err := newAgentController(runtime, runtime, nil); return err },
	} {
		if err := construct(); !errors.Is(err, appbackend.ErrInvalidConfig) {
			t.Fatal(err)
		}
	}
}
func TestWebWatcherDisconnectDoesNotCancelAcceptedExecution(t *testing.T) {
	app := testApplication(t)
	entered, release := make(chan context.Context, 1), make(chan struct{})
	runtime := &testAgent{run: func(ctx context.Context, _ agent.Turn) (agent.Execution, error) {
		entered <- ctx
		select {
		case <-release:
		case <-ctx.Done():
			return agent.Execution{}, ctx.Err()
		}
		return agent.Execution{Result: &agent.Result{Output: content.FromText("done")}}, nil
	}}
	replaceTestController(app, testController(t, runtime))
	requestCtx, cancel := context.WithCancel(context.Background())
	snapshot, err := app.turns.Submit(requestCtx, agent.MessageRequest{SessionID: "s", Input: agent.Input{Text: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	executionCtx := <-entered
	cancel()
	app.turns.stop()
	<-app.turns.done
	select {
	case <-executionCtx.Done():
		t.Fatal("Web disconnect canceled execution")
	default:
	}
	close(release)
	ctx, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	state, _ := runtime.State(ctx)
	for {
		finished := false
		for _, m := range state.Messages {
			if m.ID == snapshot.ID && m.Status == "succeeded" {
				finished = true
			}
		}
		if finished {
			break
		}
		_, err = runtime.Watch(ctx, state.Revision)
		if err == nil {
			state, err = runtime.State(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}
