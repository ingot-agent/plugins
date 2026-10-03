package contextcompact

import (
	"context"
	"log/slog"

	"github.com/ingot-agent/sdk/execution"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/usage"
)

func (r *compactor) publishContext(ctx context.Context, id session.ID, tokens int64, count usage.CountResult) {
	if r.interactions == nil {
		return
	}
	channel, err := r.interactions.Bind(execution.Scope{SessionID: id})
	if err != nil {
		slog.Warn("bind context estimate state", "session", id, "error", err)
		return
	}
	if isNil(channel) {
		slog.Warn("context estimate binder returned nil channel", "session", id)
		return
	}
	values := []interaction.Entry{
		{Name: "sessionId", Value: interaction.StringValue(string(id))},
		{Name: "inputTokens", Value: interaction.IntegerValue(tokens)},
		{Name: "accuracy", Value: interaction.StringValue(string(count.Accuracy))},
		{Name: "source", Value: interaction.StringValue(count.Source)},
		{Name: "provider", Value: interaction.StringValue(count.Provider)},
		{Name: "model", Value: interaction.StringValue(count.Model)},
	}
	if correlation, ok := observation.CorrelationFromContext(ctx); ok && correlation.SessionID == id && correlation.TurnID != "" {
		values = append(values,
			interaction.Entry{Name: "turnId", Value: interaction.StringValue(string(correlation.TurnID))},
			interaction.Entry{Name: "roundIndex", Value: interaction.IntegerValue(int64(correlation.RoundIndex))},
		)
	}
	// This is the final input request, after checkpoint materialization and any
	// compaction. Publishing telemetry must not fail the model invocation.
	if err := channel.Set(ctx, interaction.State{
		Name: "context-compact.session-context/" + string(id), Level: interaction.LevelInfo, Values: values,
	}); err != nil {
		slog.Warn("publish context estimate state", "session", id, "error", err)
	}
}
