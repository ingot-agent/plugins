package appcomponent

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/ingot-agent/sdk/session"
)

const followupNamespace = "app-webui"
const followupKind = "inline-followup"

type followup struct {
	ID               string    `json:"id"`
	SourceSessionID  string    `json:"sourceSessionId"`
	MessageIndex     int       `json:"messageIndex"`
	PartIndex        int       `json:"partIndex"`
	Start            int       `json:"start"`
	End              int       `json:"end"`
	Quote            string    `json:"quote"`
	BaseMessageCount int       `json:"baseMessageCount"`
	CreatedAt        time.Time `json:"createdAt"`
}

type followupAnchor struct {
	MessageIndex int    `json:"messageIndex"`
	PartIndex    int    `json:"partIndex"`
	Start        int    `json:"start"`
	End          int    `json:"end"`
	Quote        string `json:"quote"`
}

type followupMetadata struct {
	Kind             string         `json:"kind"`
	SourceSessionID  string         `json:"sourceSessionId"`
	Anchor           followupAnchor `json:"anchor"`
	BaseMessageCount int            `json:"baseMessageCount"`
}

func encodeFollowupMeta(note followup) (session.Meta, error) {
	encoded, err := json.Marshal(followupMetadata{
		Kind: followupKind, SourceSessionID: note.SourceSessionID,
		Anchor: followupAnchor{MessageIndex: note.MessageIndex, PartIndex: note.PartIndex,
			Start: note.Start, End: note.End, Quote: note.Quote},
		BaseMessageCount: note.BaseMessageCount,
	})
	if err != nil {
		return nil, fmt.Errorf("encode followup metadata: %w", err)
	}
	return session.Meta{followupNamespace: encoded}, nil
}

func followupFromMetadata(metadata session.Metadata) (followup, bool, error) {
	raw, ok := metadata.Meta[followupNamespace]
	if !ok {
		return followup{}, false, nil
	}
	var value followupMetadata
	if err := json.Unmarshal(raw, &value); err != nil {
		return followup{}, false, fmt.Errorf("decode followup metadata of session %q: %w", metadata.ID, err)
	}
	if value.Kind != followupKind {
		return followup{}, false, nil
	}
	if value.SourceSessionID == "" || value.SourceSessionID == string(metadata.ID) || value.Anchor.Quote == "" ||
		value.Anchor.MessageIndex < 0 || value.Anchor.PartIndex < 0 || value.Anchor.Start < 0 ||
		value.Anchor.End <= value.Anchor.Start || value.BaseMessageCount < 0 {
		return followup{}, false, fmt.Errorf("invalid followup metadata of session %q", metadata.ID)
	}
	return followup{ID: string(metadata.ID), SourceSessionID: value.SourceSessionID,
		MessageIndex: value.Anchor.MessageIndex, PartIndex: value.Anchor.PartIndex,
		Start: value.Anchor.Start, End: value.Anchor.End, Quote: value.Anchor.Quote,
		BaseMessageCount: value.BaseMessageCount, CreatedAt: metadata.CreatedAt}, true, nil
}

func sortFollowups(items []followup) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
}
