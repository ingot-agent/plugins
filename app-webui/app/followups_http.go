package appcomponent

import (
	"context"
	"errors"
	"net/http"
	"strings"

	appbackend "github.com/ingot-agent/plugins/app-webui"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/session"
)

func (a *application) handleListFollowups(w http.ResponseWriter, r *http.Request) {
	if _, err := a.sessions.Get(r.Context(), session.ID(r.PathValue("id"))); err != nil {
		writeError(w, err)
		return
	}
	items, err := a.sessions.ListFollowups(r.Context(), session.ID(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *application) handleCreateFollowup(w http.ResponseWriter, r *http.Request) {
	var request struct {
		MessageIndex int    `json:"messageIndex"`
		PartIndex    int    `json:"partIndex"`
		Start        int    `json:"start"`
		End          int    `json:"end"`
		Quote        string `json:"quote"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	quote := strings.TrimSpace(request.Quote)
	if request.MessageIndex < 0 || request.PartIndex < 0 || request.Start < 0 || request.End <= request.Start || quote == "" || len([]rune(quote)) > 2000 {
		writeAPIError(w, http.StatusBadRequest, "invalid_followup_anchor", "select text in a completed assistant message")
		return
	}
	source := session.ID(r.PathValue("id"))
	if _, err := a.ensureWorkspace(r.Context(), source); err != nil {
		writeError(w, err)
		return
	}
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	for _, turn := range a.turns.Snapshots() {
		if turn.SessionID == string(source) {
			writeAPIError(w, http.StatusConflict, "source_running", "wait for the current answer to finish")
			return
		}
	}
	if _, isFollowup, err := a.sessions.GetFollowup(r.Context(), source); err != nil {
		writeError(w, err)
		return
	} else if isFollowup {
		writeAPIError(w, http.StatusBadRequest, "invalid_followup_source", "cannot follow up on a followup session")
		return
	}
	history, err := a.agent.History(r.Context(), source)
	if err != nil {
		writeError(w, err)
		return
	}
	if request.MessageIndex >= len(history) || history[request.MessageIndex].Role != "assistant" || request.PartIndex >= len(history[request.MessageIndex].Content) || history[request.MessageIndex].Content[request.PartIndex].Kind != content.KindText {
		writeAPIError(w, http.StatusBadRequest, "invalid_followup_anchor", "selected assistant text no longer exists")
		return
	}
	title := quote
	if runes := []rune(title); len(runes) > 60 {
		title = string(runes[:60])
	}
	note := followup{SourceSessionID: string(source), MessageIndex: request.MessageIndex,
		PartIndex: request.PartIndex, Start: request.Start, End: request.End, Quote: quote,
		BaseMessageCount: len(history)}
	meta, err := encodeFollowupMeta(note)
	if err != nil {
		writeError(w, err)
		return
	}
	child, err := a.sessions.Fork(r.Context(), source, session.ForkRequest{Title: title, Meta: meta})
	if err != nil {
		writeError(w, err)
		return
	}
	note.ID, note.CreatedAt = child.ID, child.CreatedAt
	_ = a.backend.Events().Publish(appbackend.Event{Type: "followup.created", Data: note})
	writeJSON(w, http.StatusCreated, note)
}

func (a *application) deleteFollowup(ctx context.Context, note followup) error {
	for _, turn := range a.turns.Snapshots() {
		if turn.SessionID == note.ID {
			return errors.New("followup is still running")
		}
	}
	if err := a.sessions.Delete(ctx, session.ID(note.ID)); err != nil && !errors.Is(err, session.ErrNotFound) {
		return err
	}
	_ = a.backend.Events().Publish(appbackend.Event{Type: "session.deleted", Data: map[string]string{"id": note.ID}})
	_ = a.backend.Events().Publish(appbackend.Event{Type: "followup.deleted", Data: note})
	return nil
}

func (a *application) handleDeleteFollowup(w http.ResponseWriter, r *http.Request) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	note, ok, err := a.sessions.GetFollowup(r.Context(), session.ID(r.PathValue("id")))
	if err != nil {
		writeError(w, err)
		return
	}
	if !ok {
		writeAPIError(w, http.StatusNotFound, "followup_not_found", "followup not found")
		return
	}
	err = a.deleteFollowup(r.Context(), note)
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
