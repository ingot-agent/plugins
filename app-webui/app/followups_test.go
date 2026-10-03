package appcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/workspace"
)

func TestFollowupLifecycleAndSessionVisibility(t *testing.T) {
	a := testApplication(t)
	controller := a.agent.(*defaultAgentController)
	controller.history.(*testAgent).messages = []model.Message{
		{Role: model.RoleUser, Content: content.FromText("Explain this")},
		{Role: model.RoleAssistant, Content: content.FromText("A detailed answer")},
	}
	source, err := a.sessions.Create(context.Background(), "main", workspace.Binding{Root: a.defaultWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	serve := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		a.routes().ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		if response.Code != status {
			t.Fatalf("%s %s = %d %s, want %d", method, path, response.Code, response.Body.String(), status)
		}
		return response
	}
	path := "/api/sessions/" + source.ID
	serve(http.MethodPost, path+"/followups", `{"messageIndex":0,"partIndex":0,"start":0,"end":7,"quote":"Explain"}`, http.StatusBadRequest)
	created := serve(http.MethodPost, path+"/followups", `{"messageIndex":1,"partIndex":0,"start":2,"end":10,"quote":"detailed"}`, http.StatusCreated)
	var note followup
	if err := json.Unmarshal(created.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	if note.SourceSessionID != source.ID || note.BaseMessageCount != 2 || note.ID == "" || note.ID == source.ID {
		t.Fatalf("created followup = %#v", note)
	}
	var visible []map[string]any
	if err := json.Unmarshal(serve(http.MethodGet, "/api/sessions", "", http.StatusOK).Body.Bytes(), &visible); err != nil {
		t.Fatal(err)
	}
	if len(visible) != 1 || visible[0]["id"] != source.ID {
		t.Fatalf("visible sessions = %#v", visible)
	}
	var state struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(serve(http.MethodGet, "/api/state", "", http.StatusOK).Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 1 || state.Sessions[0]["id"] != source.ID {
		t.Fatalf("state sessions = %#v", state.Sessions)
	}
	serve(http.MethodPost, "/api/sessions/"+note.ID+"/followups", `{"messageIndex":1,"partIndex":0,"start":2,"end":10,"quote":"detailed"}`, http.StatusBadRequest)
	var notes []followup
	if err := json.Unmarshal(serve(http.MethodGet, path+"/followups", "", http.StatusOK).Body.Bytes(), &notes); err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].ID != note.ID {
		t.Fatalf("followups = %#v", notes)
	}
	store := a.sessions.(*defaultSessionController).store.(*testStore)
	reopened, err := newSessionController(store, store, store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.ListFollowups(context.Background(), session.ID(source.ID))
	if err != nil || len(restored) != 1 || restored[0] != note {
		t.Fatalf("reopened followups = %#v, %v", restored, err)
	}
	forked, err := reopened.Fork(context.Background(), session.ID(note.ID), session.ForkRequest{Title: "ordinary fork"})
	if err != nil {
		t.Fatal(err)
	}
	if _, isFollowup, err := reopened.GetFollowup(context.Background(), session.ID(forked.ID)); err != nil || isFollowup {
		t.Fatalf("ordinary fork inherited followup metadata: %v, %v", isFollowup, err)
	}
	for _, test := range []struct{ current, root string }{
		{source.ID, source.ID}, {note.ID, source.ID}, {forked.ID, forked.ID},
	} {
		root, err := reopened.TokenRoot(context.Background(), session.ID(test.current))
		if err != nil || string(root) != test.root {
			t.Fatalf("TokenRoot(%q) = %q, %v", test.current, root, err)
		}
	}
	for _, id := range []string{source.ID, note.ID} {
		channel := a.backend.Interactions().Scoped(appbackend.Scope{Agent: &appbackend.AgentScope{SessionID: id}})
		if err := channel.Set(context.Background(), interaction.State{
			Name:   "model-runtime.session-usage/" + id,
			Values: []interaction.Entry{{Name: "totalToken", Value: interaction.IntegerValue(72)}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	serve(http.MethodDelete, "/api/followups/"+note.ID, "", http.StatusNoContent)
	if states := a.backend.Interactions().States(); len(states) != 1 || states[0].Name != "model-runtime.session-usage/"+source.ID {
		t.Fatalf("deleting followup cleared the wrong usage state: %#v", states)
	}
	serve(http.MethodPost, path+"/followups", `{"messageIndex":1,"partIndex":0,"start":2,"end":10,"quote":"detailed"}`, http.StatusCreated)
	serve(http.MethodDelete, path, "", http.StatusNoContent)
	remaining, err := reopened.ListFollowups(context.Background(), session.ID(source.ID))
	if err != nil || len(remaining) != 0 {
		t.Fatalf("deleting the parent retained its followups: %#v, %v", remaining, err)
	}
	if states := a.backend.Interactions().States(); len(states) != 0 {
		t.Fatalf("deleting the parent retained usage state: %#v", states)
	}
}

func TestSessionTokensAndServerResolvedTurnIdentity(t *testing.T) {
	a := testApplication(t)
	ctx := context.Background()
	store := a.sessions.(*defaultSessionController).store.(*testStore)
	main, err := a.sessions.Create(ctx, "main", workspace.Binding{Root: a.defaultWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := encodeFollowupMeta(followup{SourceSessionID: main.ID, Quote: "answer", End: 6, BaseMessageCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	note, err := store.Fork(ctx, session.ID(main.ID), session.ForkRequest{Meta: meta})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTotalTokens(ctx, []session.ID{session.ID(main.ID)}, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddTotalTokens(ctx, []session.ID{session.ID(main.ID), note.ID}, 11); err != nil {
		t.Fatal(err)
	}
	fork, err := a.sessions.Fork(ctx, note.ID, session.ForkRequest{})
	if err != nil {
		t.Fatal(err)
	}
	turns := make(chan agent.Turn, 3)
	a.agent.(*defaultAgentController).history.(*testAgent).run = func(_ context.Context, turn agent.Turn) (agent.Execution, error) {
		turns <- turn
		return agent.Execution{}, nil
	}
	for _, test := range []struct {
		id, root string
		total    int64
	}{
		{main.ID, main.ID, 111}, {string(note.ID), main.ID, 11}, {fork.ID, fork.ID, 0},
	} {
		response := httptest.NewRecorder()
		a.routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+test.id, nil))
		var projected appbackend.Session
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &projected) != nil || projected.TotalToken != test.total {
			t.Fatalf("session projection = %d %s", response.Code, response.Body.String())
		}
		response = httptest.NewRecorder()
		a.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/turns", strings.NewReader(`{"sessionId":"`+test.id+`","input":"hello"}`)))
		if response.Code != http.StatusAccepted {
			t.Fatalf("create turn = %d %s", response.Code, response.Body.String())
		}
		select {
		case turn := <-turns:
			if string(turn.RootSessionID) != test.root || string(turn.SessionID) != test.id {
				t.Fatalf("turn identity = %#v", turn)
			}
		case <-time.After(time.Second):
			t.Fatal("turn did not reach Agent")
		}
	}
	response := httptest.NewRecorder()
	a.routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var state struct {
		Sessions []appbackend.Session `json:"sessions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 2 || state.Sessions[0].TotalToken+state.Sessions[1].TotalToken != 111 {
		t.Fatalf("bootstrap totals = %#v", state.Sessions)
	}
}

func TestTokenRootRejectsInvalidFollowupSources(t *testing.T) {
	meta := func(source string) session.Meta {
		value, err := encodeFollowupMeta(followup{SourceSessionID: source, Quote: "answer", End: 6})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, test := range []struct {
		name    string
		items   []session.Metadata
		missing bool
	}{
		{"missing source", []session.Metadata{{ID: "note", Meta: meta("gone")}}, true},
		{"nested followup", []session.Metadata{{ID: "main"}, {ID: "parent", Meta: meta("main")}, {ID: "note", Meta: meta("parent")}}, false},
		{"self reference", []session.Metadata{{ID: "note", Meta: meta("note")}}, false},
		{"malformed metadata", []session.Metadata{{ID: "note", Meta: session.Meta{followupNamespace: json.RawMessage(`{`)}}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &testStore{items: test.items}
			controller, err := newSessionController(store, store, store, store, store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := controller.TokenRoot(context.Background(), "note"); err == nil || (test.missing && !errors.Is(err, session.ErrNotFound)) {
				t.Fatalf("TokenRoot = %v", err)
			}
		})
	}
}
