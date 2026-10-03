package appcomponent

import (
	"context"
	"log/slog"
	"time"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/session"
)

func (a *application) clearSessionUsage(ctx context.Context, id session.ID) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	channel := a.backend.Interactions().Scoped(appbackend.Scope{Agent: &appbackend.AgentScope{SessionID: string(id)}})
	for _, prefix := range []string{"model-runtime.session-usage/", "context-compact.session-context/"} {
		if err := channel.Clear(cleanupCtx, prefix+string(id)); err != nil {
			slog.Warn("clear deleted session usage state", "session", id, "error", err)
		}
	}
}
