package mcpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"mcp-knowledge-graph/internal/knowledge"
)

// Cursor kinds bind a cursor to the list it was issued for.
const (
	cursorKindChildren = "children"
	cursorKindSources  = "sources"
	cursorVersion      = 1
)

// cursorPayload is the content of an opaque pagination cursor. It is not a
// credential: it only encodes the keyset position and the scope (list kind
// and document) it is valid for.
type cursorPayload struct {
	Version    int       `json:"v"`
	Kind       string    `json:"k"`
	DocumentID uuid.UUID `json:"d"`
	Key        string    `json:"t,omitempty"` // children: lower(title)
	Time       time.Time `json:"c,omitzero"`  // sources: created_at
	ID         uuid.UUID `json:"i"`
}

func encodeCursor(p cursorPayload) string {
	p.Version = cursorVersion
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(raw, kind string, documentID uuid.UUID) (cursorPayload, error) {
	bad := invalidInput("cursor is malformed or was issued for a different request; restart without cursor")
	if len(raw) > 4096 {
		return cursorPayload{}, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursorPayload{}, bad
	}
	var p cursorPayload
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || dec.More() {
		return cursorPayload{}, bad
	}
	if p.Version != cursorVersion || p.Kind != kind || p.DocumentID != documentID || p.ID == uuid.Nil {
		return cursorPayload{}, bad
	}
	if kind == cursorKindSources && p.Time.IsZero() {
		return cursorPayload{}, bad
	}
	return p, nil
}

func encodeChildrenCursor(documentID uuid.UUID, c *knowledge.ChildrenCursor) *string {
	if c == nil {
		return nil
	}
	s := encodeCursor(cursorPayload{Kind: cursorKindChildren, DocumentID: documentID, Key: c.TitleKey, ID: c.ID})
	return &s
}

func decodeChildrenCursor(raw string, documentID uuid.UUID) (*knowledge.ChildrenCursor, error) {
	if raw == "" {
		return nil, nil
	}
	p, err := decodeCursor(raw, cursorKindChildren, documentID)
	if err != nil {
		return nil, err
	}
	return &knowledge.ChildrenCursor{TitleKey: p.Key, ID: p.ID}, nil
}

func encodeSourcesCursor(documentID uuid.UUID, c *knowledge.SourcesCursor) *string {
	if c == nil {
		return nil
	}
	s := encodeCursor(cursorPayload{Kind: cursorKindSources, DocumentID: documentID, Time: c.CreatedAt, ID: c.ID})
	return &s
}

func decodeSourcesCursor(raw string, documentID uuid.UUID) (*knowledge.SourcesCursor, error) {
	if raw == "" {
		return nil, nil
	}
	p, err := decodeCursor(raw, cursorKindSources, documentID)
	if err != nil {
		return nil, err
	}
	return &knowledge.SourcesCursor{CreatedAt: p.Time, ID: p.ID}, nil
}
