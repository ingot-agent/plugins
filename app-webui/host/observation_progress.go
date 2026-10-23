package hostcomponent

import (
	"bytes"
	"context"
	"reflect"
	"sync"
	"time"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
)

// Diagnostic progress also uses SSE. Leaving it unbatched can evict the same
// slow subscribers even when the authoritative runtime stream is coalesced.
type observer struct {
	events  appbackend.EventSink
	mu      sync.Mutex
	pending map[observation.Correlation]*observationBatch
	closed  bool
}

type observationBatch struct {
	events []observation.Event
	size   int
	timer  *time.Timer
}

func (o *observer) Observe(event observation.Event) {
	// SDK detail methods use value receivers, so pointers are valid too.
	if value := reflect.ValueOf(event.Detail); value.IsValid() && value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		event.Detail, _ = value.Elem().Interface().(observation.Detail)
	}
	progress, size := false, 0
	switch d := event.Detail.(type) {
	case observation.ModelProgress:
		if d.Progress.Kind == model.StreamPartDelta {
			d.Progress.DataDelta = bytes.Clone(d.Progress.DataDelta)
			event.Detail = d
			progress, size = true, len(d.Progress.TextDelta)+len(d.Progress.DataDelta)
		}
	case observation.ToolProgress:
		d.Progress.Content = content.Clone(d.Progress.Content)
		event.Detail, progress = d, true
		for _, part := range d.Progress.Content {
			size += len(part.Text) + len(part.Media.Source.Data)
		}
	}
	key := observation.Correlation{SessionID: event.Correlation.SessionID, TurnID: event.Correlation.TurnID}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	if !progress {
		o.flushLocked(key)
		o.project(event)
		return
	}
	batch := o.pending[key]
	if batch == nil {
		batch = &observationBatch{}
		o.pending[key] = batch
		batch.timer = time.AfterFunc(50*time.Millisecond, func() {
			o.mu.Lock()
			defer o.mu.Unlock()
			if o.pending[key] == batch {
				o.flushLocked(key)
			}
		})
	}
	if len(batch.events) == 0 || !mergeProgress(&batch.events[len(batch.events)-1], event) {
		batch.events = append(batch.events, event)
	}
	batch.size += size
	if batch.size >= 4096 || len(batch.events) >= 32 {
		o.flushLocked(key)
	}
}

// Merge only adjacent increments with identical scope/part/channel. This keeps
// diagnostic part and tool ordering and uses the last source sequence/time.
func mergeProgress(previous *observation.Event, next observation.Event) bool {
	if previous.Correlation != next.Correlation {
		return false
	}
	switch before := previous.Detail.(type) {
	case observation.ModelProgress:
		after, ok := next.Detail.(observation.ModelProgress)
		if !ok || before.Progress.Semantic != after.Progress.Semantic || before.Progress.PartIndex != after.Progress.PartIndex || before.Progress.PartKind != after.Progress.PartKind {
			return false
		}
		before.Progress.TextDelta += after.Progress.TextDelta
		before.Progress.DataDelta = append(before.Progress.DataDelta, after.Progress.DataDelta...)
		previous.Detail = before
	case observation.ToolProgress:
		after, ok := next.Detail.(observation.ToolProgress)
		if !ok || before.Progress.Channel != after.Progress.Channel {
			return false
		}
		before.Progress.Content = append(before.Progress.Content, after.Progress.Content...)
		previous.Detail = before
	default:
		return false
	}
	previous.Sequence, previous.Time = next.Sequence, next.Time
	return true
}

func (o *observer) flushLocked(key observation.Correlation) {
	batch := o.pending[key]
	if batch == nil {
		return
	}
	batch.timer.Stop()
	delete(o.pending, key)
	for _, event := range batch.events {
		o.project(event)
	}
}

func (o *observer) cleanup(context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	for key := range o.pending {
		o.flushLocked(key)
	}
	return nil
}
