package skillruntime

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/prompt"
)

type catalogContributor struct{ registry *registry }

func (c *catalogContributor) Contribute(ctx context.Context, _ prompt.Request) ([]prompt.Block, error) {
	if ctx == nil {
		return nil, fmt.Errorf("skill catalog: nil context")
	}
	if err := c.registry.Refresh(ctx); err != nil {
		return nil, err
	}
	snapshot := c.registry.Snapshot()
	if len(snapshot.Ordered) == 0 {
		return nil, nil
	}
	var result strings.Builder
	result.WriteString("Available Skills are optional task-specific instructions. Call read_skill with a Skill name before using it.\n")
	for _, item := range snapshot.Ordered {
		fmt.Fprintf(&result, "- `%s`: %s\n", item.Name, strconv.Quote(item.Description))
	}
	return []prompt.Block{{Name: "Skills", Content: content.FromText(strings.TrimSuffix(result.String(), "\n"))}}, nil
}
