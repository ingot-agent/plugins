package sessionsqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ingot-agent/sdk/session"
)

func TestAppendBatchAtomicAndContiguous(t *testing.T) {
	ctx := context.Background()
	s, err := openStore(ctx, filepath.Join(t.TempDir(), "batch.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })
	m, err := s.Create(ctx, session.CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(ctx, m.ID)
	if !after.UpdatedAt.Equal(m.UpdatedAt) {
		t.Fatal("empty batch changed activity")
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprint(i)
			if err := s.Append(ctx, m.ID, session.Entry{Kind: key + "-notice"}, session.Entry{Kind: key + "-user"}); err != nil {
				t.Error(err)
			}
			entries, err := s.Load(ctx, m.ID)
			if err != nil {
				t.Error(err)
				return
			}
			if len(entries)%2 != 0 {
				t.Error("partial batch visible")
				return
			}
			for j := 0; j < len(entries); j += 2 {
				prefix := entries[j].Kind[:len(entries[j].Kind)-len("-notice")]
				if entries[j+1].Kind != prefix+"-user" {
					t.Errorf("interleaved pair: %v", entries[j:j+2])
				}
			}
		}(i)
	}
	wg.Wait()
	before, _ := s.Load(ctx, m.ID)
	if len(before) != 60 {
		t.Fatalf("entries=%d", len(before))
	}
	// Fail the second INSERT inside the transaction, after the notice INSERT.
	if _, err := s.db.ExecContext(ctx, "CREATE TRIGGER fail_user BEFORE INSERT ON entries WHEN NEW.kind = 'fail-user' BEGIN SELECT RAISE(ABORT, 'injected'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ctx, m.ID, session.Entry{Kind: "fail-notice"}, session.Entry{Kind: "fail-user"}); err == nil {
		t.Fatal("expected failure")
	}
	entries, err := s.Load(ctx, m.ID)
	if err != nil || len(entries) != len(before) {
		t.Fatalf("partial batch survived: %d, %v", len(entries), err)
	}
}
