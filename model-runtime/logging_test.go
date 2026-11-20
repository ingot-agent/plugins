package modelruntime_test

import (
	"context"
	"errors"
	"testing"

	ingotabi "github.com/ingot-agent/ingot-abi"
	modelruntime "github.com/ingot-agent/plugins/model-runtime"
	"github.com/ingot-agent/sdk/logging"
)

// typedNilLogger is a non-nil interface holding a nil pointer. The graph can
// supply that shape, so construction must reject it rather than fail later.
type typedNilLogger struct{ logging.Logger }

func (*typedNilLogger) Enabled(logging.Level) bool { return false }

func TestNewRejectsTypedNilLogger(t *testing.T) {
	t.Parallel()
	deps := withState(t, modelruntime.Config{}, modelruntime.Dependencies{
		Logger: ingotabi.Some[logging.Logger]((*typedNilLogger)(nil)),
	})
	if _, _, err := modelruntime.New(context.Background(), deps); !errors.Is(err, modelruntime.ErrInvalidConfig) {
		t.Fatalf("New error = %v, want ErrInvalidConfig", err)
	}
}
