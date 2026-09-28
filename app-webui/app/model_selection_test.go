package appcomponent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ingot-agent/plugins/app-webui/modelselection"
)

type testModelSelection struct {
	mu       sync.Mutex
	snapshot modelselection.Snapshot
	sequence int
}

func (s *testModelSelection) Snapshot(context.Context) (modelselection.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot, nil
}

func (s *testModelSelection) Update(_ context.Context, choice modelselection.Selection, revision string) (modelselection.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision != s.snapshot.Revision {
		return modelselection.Snapshot{}, modelselection.ErrConflict
	}
	valid := false
	for _, provider := range s.snapshot.Providers {
		if provider.Name != choice.Provider {
			continue
		}
		for _, candidate := range provider.Models {
			if candidate.Name != choice.Model {
				continue
			}
			if choice.ReasoningEffort == "providerDefault" {
				valid = true
				break
			}
			for _, effort := range candidate.ReasoningEfforts {
				if effort == choice.ReasoningEffort {
					valid = true
				}
			}
		}
	}
	if !valid {
		return modelselection.Snapshot{}, modelselection.ErrInvalid
	}
	s.sequence++
	s.snapshot.Configured = true
	s.snapshot.Current = choice
	s.snapshot.Revision = fmt.Sprintf("revision-%d", s.sequence)
	return s.snapshot, nil
}

func TestModelSelectionHTTP(t *testing.T) {
	a := testApplication(t)
	read := httptest.NewRecorder()
	a.routes().ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/model-selection", nil))
	if read.Code != http.StatusNotImplemented {
		t.Fatalf("missing capability = %d", read.Code)
	}
	a.modelSelection = &testModelSelection{snapshot: modelselection.Snapshot{
		Revision: "initial", Providers: []modelselection.Provider{{Name: "provider", Models: []modelselection.Model{{Name: "model", ReasoningEfforts: []string{"low"}}}}},
	}}
	read = httptest.NewRecorder()
	a.routes().ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/api/model-selection", nil))
	if read.Code != http.StatusOK {
		t.Fatalf("read = %d %s", read.Code, read.Body.String())
	}
	var initial modelselection.Snapshot
	if err := json.Unmarshal(read.Body.Bytes(), &initial); err != nil || initial.Revision != "initial" {
		t.Fatalf("initial = %#v, err = %v", initial, err)
	}
	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		a.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/model-selection", strings.NewReader(body)))
		return response
	}
	if response := put(`{"revision":"initial","selection":{"provider":"provider","model":"other","reasoningEffort":"low"}}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid selection = %d %s", response.Code, response.Body.String())
	}
	response := put(`{"revision":"initial","selection":{"provider":"provider","model":"model","reasoningEffort":"low"}}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"revision":"revision-1"`) {
		t.Fatalf("saved = %d %s", response.Code, response.Body.String())
	}
	if a.backend.Events().Cursor() != 1 {
		t.Fatal("selection update did not publish an event")
	}
	if response := put(`{"revision":"initial","selection":{"provider":"provider","model":"model","reasoningEffort":"low"}}`); response.Code != http.StatusConflict {
		t.Fatalf("stale selection = %d %s", response.Code, response.Body.String())
	}
	if response := put(`{"revision":"revision-1","selection":{"provider":"provider","model":"model","reasoningEffort":"providerDefault"}}`); response.Code != http.StatusOK {
		t.Fatalf("provider default = %d %s", response.Code, response.Body.String())
	}
}
