package agentdefault

import (
	"context"
	"errors"
	"fmt"

	"github.com/ingot-agent/plugins/agent-default/sessioncontrol"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/tool"
)

const childSubmitToolName = "submit_agent_result"

type turnFrame struct {
	handle     sessioncontrol.Handle
	toolNames  map[string]struct{}
	submitTool string
	confirmed  *sessioncontrol.FinishIntent
}

func newChildFrame(task sessioncontrol.Task) *turnFrame {
	names := make(map[string]struct{}, len(task.ToolNames))
	for _, name := range task.ToolNames {
		names[name] = struct{}{}
	}
	return &turnFrame{handle: task.Handle, toolNames: names, submitTool: task.SubmitTool}
}

func definitionsForFrame(definitions []tool.Definition, frame *turnFrame, lastAllowed bool) []tool.Definition {
	result := make([]tool.Definition, 0, len(definitions))
	for _, definition := range definitions {
		if frame == nil {
			if definition.Name == childSubmitToolName {
				continue
			}
		} else {
			if _, allowed := frame.toolNames[definition.Name]; !allowed {
				continue
			}
			if lastAllowed && definition.Name != frame.submitTool {
				continue
			}
		}
		result = append(result, tool.Definition{
			Name: definition.Name, Description: definition.Description,
			InputSchema: append([]byte(nil), definition.InputSchema...),
		})
	}
	return result
}

func (r *runtime) startDispatcher(ctx context.Context) {
	r.dispatchDone = make(chan struct{})
	go func() {
		defer close(r.dispatchDone)
		for {
			task, err := r.control.Next(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, sessioncontrol.ErrClosed) {
					return
				}
				if ctx.Err() != nil {
					return
				}
				continue
			}
			r.dispatchWG.Add(1)
			go func() {
				defer r.dispatchWG.Done()
				r.runChild(task)
			}()
		}
	}()
}

func (r *runtime) runChild(task sessioncontrol.Task) {
	frame := newChildFrame(task)
	var (
		execution agent.Execution
		runErr    error
	)
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("child execution panic: %v", recovered)
		}
		_ = r.control.Settle(task.Context, task.Handle, frame.confirmed, execution, runErr)
	}()
	execution, runErr = r.executeFrame(task.Context, agent.Turn{SessionID: task.Handle.SessionID, Input: task.Input}, nil, frame)
}

func (r *runtime) cleanup(ctx context.Context) error {
	var resultErr error
	if r.control != nil {
		resultErr = r.control.Shutdown(ctx)
	}
	if r.dispatchCancel != nil {
		r.dispatchCancel()
	}
	if r.dispatchDone != nil {
		select {
		case <-r.dispatchDone:
		case <-ctx.Done():
			return errors.Join(resultErr, ctx.Err())
		}
	}
	done := make(chan struct{})
	go func() {
		r.dispatchWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return resultErr
	case <-ctx.Done():
		return errors.Join(resultErr, ctx.Err())
	}
}
