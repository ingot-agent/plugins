package sessionsqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/session"
)

func encodeMeta(meta session.Meta) (string, error) {
	if meta == nil {
		return "{}", nil
	}
	owned := make(session.Meta, len(meta))
	for namespace, raw := range meta {
		if namespace == "" || !utf8.ValidString(namespace) {
			return "", errors.New("metadata namespace must be non-empty UTF-8")
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || !json.Valid(trimmed) || trimmed[0] != '{' {
			return "", fmt.Errorf("metadata namespace %q must contain one JSON object", namespace)
		}
		var object map[string]any
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&object); err != nil || object == nil {
			return "", fmt.Errorf("metadata namespace %q must contain one JSON object", namespace)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return "", fmt.Errorf("metadata namespace %q contains multiple values", namespace)
		}
		owned[namespace] = append(json.RawMessage(nil), trimmed...)
	}
	encoded, err := json.Marshal(owned)
	if err != nil {
		return "", fmt.Errorf("encode metadata: %w", err)
	}
	return string(encoded), nil
}

func decodeMeta(raw []byte) (session.Meta, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("metadata is empty")
	}
	var meta session.Meta
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&meta); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("metadata contains multiple JSON values")
	}
	if meta == nil {
		return nil, errors.New("metadata must be a JSON object")
	}
	if _, err := encodeMeta(meta); err != nil {
		return nil, err
	}
	return cloneMeta(meta), nil
}

func cloneMeta(meta session.Meta) session.Meta {
	if meta == nil {
		return session.Meta{}
	}
	result := make(session.Meta, len(meta))
	for namespace, raw := range meta {
		result[namespace] = append(json.RawMessage(nil), raw...)
	}
	return result
}

func tableHasColumn(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			position     int
			name         string
			dataType     string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		if err := rows.Scan(&position, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}
