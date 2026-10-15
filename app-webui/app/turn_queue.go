package appcomponent

import "github.com/ingot-agent/sdk/agent"

var (
	errTurnRequestConflict = agent.ErrRequestConflict
	errSessionRunning      = agent.ErrSessionRunning
	errQueuedTurnStarted   = agent.ErrMessageStarted
)
