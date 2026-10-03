package sessionsqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/session"
)

func TestTotalTokensAttributionConcurrencyAndLifecycle(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "sessions.sqlite3")
	s, err := openStore(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })
	ids := []session.ID{"R", "A", "B", "Q", "C", "F"}
	s.generateID = func(uint32) (session.ID, error) { id := ids[0]; ids = ids[1:]; return id, nil }
	for i := 0; i < 5; i++ {
		if _, err := s.Create(ctx, session.CreateRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.Get(ctx, "R")
	if _, err := s.AddTotalTokens(ctx, []session.ID{"R", "R"}, 100); err != nil {
		t.Fatal(err)
	}
	fork, err := s.Fork(ctx, "R", session.ForkRequest{})
	if err != nil || fork.TotalToken != 0 {
		t.Fatalf("fork=%+v err=%v", fork, err)
	}
	for _, call := range []struct {
		root, current session.ID
		delta         int64
	}{
		{"R", "A", 20}, {"R", "B", 30}, {"R", "R", 10}, {"R", "B", 5}, {"F", "F", 7}, {"R", "Q", 11}, {"R", "C", 13},
	} {
		if _, err := s.AddTotalTokens(ctx, []session.ID{call.root, call.current}, call.delta); err != nil {
			t.Fatal(err)
		}
	}
	want := map[session.ID]int64{"R": 189, "A": 20, "B": 35, "F": 7, "Q": 11, "C": 13}
	items, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.TotalToken != want[item.ID] {
			t.Fatalf("%s=%d want=%d", item.ID, item.TotalToken, want[item.ID])
		}
	}
	after, _ := s.Get(ctx, "R")
	if !after.UpdatedAt.Equal(before.UpdatedAt) || !reflect.DeepEqual(before.Meta, after.Meta) {
		t.Fatal("token settlement changed timestamps or meta")
	}

	const concurrent = 32
	var wg sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AddTotalTokens(ctx, []session.ID{"R", "B"}, 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for id, total := range map[session.ID]int64{"R": 189 + concurrent, "B": 35 + concurrent} {
		metadata, err := s.Get(ctx, id)
		if err != nil || metadata.TotalToken != total {
			t.Fatalf("metadata=%+v err=%v", metadata, err)
		}
	}
	archived, err := s.Archive(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := s.AddTotalTokens(ctx, []session.ID{"B", "B", "R"}, 0)
	if err != nil || len(snapshots) != 2 || snapshots[0].ID != "B" || snapshots[0].TotalToken != archived.TotalToken {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	if _, err := s.AddTotalTokens(ctx, []session.ID{"R", "B"}, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "B"); err != nil {
		t.Fatal(err)
	}
	root, _ := s.Get(ctx, "R")
	if root.TotalToken != 189+concurrent+2 {
		t.Fatal("delete deducted root usage")
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	root, err = s.Get(ctx, "R")
	if err != nil || root.TotalToken != 189+concurrent+2 {
		t.Fatalf("reopened=%+v err=%v", root, err)
	}
}

func TestTotalTokensRollbackAndValidation(t *testing.T) {
	ctx := context.Background()
	s, err := openStore(ctx, filepath.Join(t.TempDir(), "sessions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })
	r, _ := s.Create(ctx, session.CreateRequest{})
	a, _ := s.Create(ctx, session.CreateRequest{})
	if _, err := s.AddTotalTokens(ctx, []session.ID{a.ID}, math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct {
		ids   []session.ID
		delta int64
	}{
		{[]session.ID{r.ID, a.ID}, 1}, {[]session.ID{r.ID, "missing"}, 1}, {nil, 1}, {[]session.ID{""}, 1}, {[]session.ID{r.ID}, -1}, {[]session.ID{session.ID(string([]byte{0xff}))}, 1},
	} {
		if _, err := s.AddTotalTokens(ctx, call.ids, call.delta); err == nil {
			t.Fatalf("accepted %+v", call)
		}
	}
	root, _ := s.Get(ctx, r.ID)
	if root.TotalToken != 0 {
		t.Fatalf("partial transaction committed: %d", root.TotalToken)
	}
	if _, err := s.AddTotalTokens(ctx, []session.ID{r.ID, "missing"}, 1); !errors.Is(err, session.ErrNotFound) {
		t.Fatal(err)
	}
	ctxCanceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.AddTotalTokens(ctxCanceled, []session.ID{r.ID}, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE sessions SET totaltoken = 1.5 WHERE id = ?", string(r.ID)); err == nil {
		t.Fatal("column accepted a non-integer")
	}
}

func TestTotalTokensChildQueries(t *testing.T) {
	ctx := context.Background()
	s, err := openStore(ctx, filepath.Join(t.TempDir(), "sessions.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })
	r, _ := s.Create(ctx, session.CreateRequest{})
	if _, err := s.AddTotalTokens(ctx, []session.ID{r.ID}, 10); err != nil {
		t.Fatal(err)
	}
	stopped := true
	child, err := s.CreateChild(ctx, agent.ChildSessionCreateRequest{ParentSessionID: r.ID, Depth: 1, Agent: agent.ChildSessionMeta{RootSessionID: r.ID, AgentType: "coder", DefinitionDigest: "digest", Task: "task", State: agent.ChildQueued, ExecutionStopped: &stopped}})
	if err != nil || child.Session.TotalToken != 0 {
		t.Fatalf("child=%+v err=%v", child, err)
	}
	if _, err := s.AddTotalTokens(ctx, []session.ID{r.ID, child.Session.ID}, 3); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetChildSession(ctx, child.Session.ID)
	if err != nil || got.Session.TotalToken != 3 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	page, err := s.ListChildSessions(ctx, r.ID, agent.ChildSessionPageRequest{})
	if err != nil || len(page.Records) != 1 || page.Records[0].Session.TotalToken != 3 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestRejectUnsupportedSchemasWithoutChangingDatabase(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3, schemaVersion + 1} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			ctx := context.Background()
			databasePath := filepath.Join(t.TempDir(), "sessions.sqlite3")
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
			if err != nil {
				t.Fatal(err)
			}
			meta := ""
			if version == 3 {
				meta = ", meta TEXT NOT NULL DEFAULT '{}'"
			}
			_, err = db.ExecContext(ctx, fmt.Sprintf("CREATE TABLE sessions (id TEXT PRIMARY KEY, title TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, archived_at INTEGER%s); INSERT INTO sessions (id,title,created_at,updated_at) VALUES ('legacy','Legacy',1,1); PRAGMA user_version = %d;", meta, version))
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if s, err := openStore(ctx, databasePath); !errors.Is(err, ErrUnsupportedSchema) {
				if s != nil {
					_ = s.close()
				}
				t.Fatalf("unsupported schema %d: %v", version, err)
			}
			db, err = sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var actualVersion, tableCount int
			if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&actualVersion); err != nil || actualVersion != version {
				t.Fatalf("version=%d want=%d err=%v", actualVersion, version, err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name NOT GLOB 'sqlite_*'").Scan(&tableCount); err != nil || tableCount != 1 {
				t.Fatalf("tables=%d err=%v", tableCount, err)
			}
			var title string
			if err := db.QueryRowContext(ctx, "SELECT title FROM sessions WHERE id = 'legacy'").Scan(&title); err != nil || title != "Legacy" {
				t.Fatalf("title=%q err=%v", title, err)
			}
			if _, err := db.ExecContext(ctx, "SELECT totaltoken FROM sessions"); err == nil {
				t.Fatal("unsupported schema was altered")
			}
		})
	}
}

func TestEmptyDatabaseInitializesCurrentSchema(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "sessions.sqlite3")
	for i := 0; i < 2; i++ {
		s, err := openStore(ctx, databasePath)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		err = s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
		closeErr := s.close()
		if err != nil || closeErr != nil || version != schemaVersion {
			t.Fatalf("schema version=%d err=%v close=%v", version, err, closeErr)
		}
	}
}
