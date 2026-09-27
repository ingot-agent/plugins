package appcomponent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ingot-agent/sdk/content"
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
	serve(http.MethodDelete, "/api/followups/"+note.ID, "", http.StatusNoContent)
	serve(http.MethodPost, path+"/followups", `{"messageIndex":1,"partIndex":0,"start":2,"end":10,"quote":"detailed"}`, http.StatusCreated)
	serve(http.MethodDelete, path, "", http.StatusNoContent)
	remaining, err := reopened.ListFollowups(context.Background(), session.ID(source.ID))
	if err != nil || len(remaining) != 0 {
		t.Fatalf("deleting the parent retained its followups: %#v, %v", remaining, err)
	}
}
