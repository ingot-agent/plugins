package skillruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/sdk/operation"
)

const (
	statusOperationName  = "status"
	statusOperationGroup = "skill-runtime"
)

type statusOperation struct{ registry *registry }

type statusSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Digest      string `json:"digest"`
	Source      string `json:"source"`
	Stale       bool   `json:"stale"`
	StaleReason string `json:"stale_reason"`
}

type statusOutput struct {
	Root             string        `json:"root"`
	Generation       uint64        `json:"generation"`
	Skills           []statusSkill `json:"skills"`
	Rejected         []rejection   `json:"rejected"`
	LastRefreshError string        `json:"last_refresh_error"`
}

func (*statusOperation) Definition() operation.Definition {
	return operation.Definition{
		Name: statusOperationName, Group: statusOperationGroup,
		Description:  "Show the live Skill root, loaded Skills, stale entries, and rejected filesystem entries.",
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["root","generation","skills","rejected","last_refresh_error"],"properties":{"root":{"type":"string"},"generation":{"type":"integer","minimum":1},"skills":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["name","description","digest","source","stale","stale_reason"],"properties":{"name":{"type":"string"},"description":{"type":"string"},"digest":{"type":"string"},"source":{"type":"string","enum":["builtin","state"]},"stale":{"type":"boolean"},"stale_reason":{"type":"string"}}}},"rejected":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["path","error"],"properties":{"path":{"type":"string"},"error":{"type":"string"}}}},"last_refresh_error":{"type":"string"}}}`),
	}
}

func (o *statusOperation) Invoke(ctx context.Context, _ operation.Request) (operation.Result, error) {
	if ctx == nil {
		return operation.Result{}, fmt.Errorf("skill status: nil context")
	}
	if err := o.registry.Refresh(ctx); err != nil {
		return operation.Result{}, err
	}
	snapshot := o.registry.Snapshot()
	output := statusOutput{
		Root: o.registry.Root(), Generation: snapshot.Generation,
		Skills:   make([]statusSkill, 0, len(snapshot.Ordered)),
		Rejected: make([]rejection, len(snapshot.Rejected)), LastRefreshError: snapshot.LastRefreshError,
	}
	copy(output.Rejected, snapshot.Rejected)
	for _, item := range snapshot.Ordered {
		output.Skills = append(output.Skills, statusSkill{
			Name: item.Name, Description: item.Description, Digest: item.Digest,
			Source: item.Source, Stale: item.Stale, StaleReason: item.StaleReason,
		})
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: raw}, nil
}
