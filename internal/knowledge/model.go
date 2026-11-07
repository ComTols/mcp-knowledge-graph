package knowledge

import (
	"time"

	"github.com/google/uuid"
)

// RootID is the fixed, well-known ID of the single root document.
var RootID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// EmbeddingDimensions is the vector dimension fixed by the schema. Every
// embedding model used with this database must produce vectors of exactly
// this length.
const EmbeddingDimensions = 1024

// SourceKind classifies an external source reference.
type SourceKind string

// Supported source kinds.
const (
	SourceKindConversation SourceKind = "conversation"
	SourceKindOther        SourceKind = "other"
)

// Valid reports whether k is a known source kind.
func (k SourceKind) Valid() bool {
	return k == SourceKindConversation || k == SourceKindOther
}

// TargetKind identifies what an embedding job indexes.
type TargetKind string

// Embedding targets.
const (
	TargetDocument TargetKind = "document"
	TargetBlock    TargetKind = "block"
)

// Valid reports whether k is a known target kind.
func (k TargetKind) Valid() bool {
	return k == TargetDocument || k == TargetBlock
}

// DocumentSummary is the compact representation of a document.
type DocumentSummary struct {
	ID      uuid.UUID `json:"id"`
	Title   string    `json:"title"`
	Summary string    `json:"summary"`
	Version int64     `json:"version"`
}

// ParentRef is a direct parent of a document.
type ParentRef struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	IsPrimary bool      `json:"isPrimary"`
}

// ChildRef is a direct child of a document including its current summary.
type ChildRef struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	IsPrimary bool      `json:"isPrimary"`
}

// Block is an ordered Markdown content block of a document. Position is an
// internal sparse ordering key: it is informational only and never accepted
// as input; blocks are addressed by their stable IDs.
type Block struct {
	ID       uuid.UUID `json:"id"`
	Position int64     `json:"position"`
	Content  string    `json:"content"`
	Version  int64     `json:"version"`
}

// Fulltext is the ordered block content of a document together with the
// document version to use as ExpectedVersion for EditDocument.
type Fulltext struct {
	DocumentID uuid.UUID `json:"documentId"`
	Version    int64     `json:"version"`
	Blocks     []Block   `json:"blocks"`
}

// Source is an external reference (e.g. the originating conversation).
type Source struct {
	ID        uuid.UUID  `json:"id"`
	Kind      SourceKind `json:"kind"`
	URI       string     `json:"uri"`
	Title     *string    `json:"title,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Document is the aggregated, structured view of a single document.
type Document struct {
	DocumentSummary
	Parents  []ParentRef `json:"parents"`
	Children []ChildRef  `json:"children"`
	Blocks   []Block     `json:"blocks"`
	Sources  []Source    `json:"sources"`
}

// PathElement is one node of a root path. It intentionally carries only the
// ID and the title.
type PathElement struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
}

// Path is a sequence of nodes starting at root and ending at a document.
type Path []PathElement

// PathSet is the result of a path resolution. The canonical (primary parent)
// path is always the first entry. Truncated is set when alternative paths
// were omitted because of the configured limits.
type PathSet struct {
	Paths     []Path `json:"paths"`
	Truncated bool   `json:"truncated,omitempty"`
}

// SearchParams configures a hybrid search.
type SearchParams struct {
	Query string
	// Limit is the maximum number of documents returned. Values <= 0 select
	// the adapter default; values above the adapter maximum are clamped.
	Limit int
	// AllPaths requests all (bounded) root paths instead of only the
	// canonical one.
	AllPaths bool
	// LexicalFallback allows a lexical-only search if no query embedding
	// can be produced. Without it, Search fails with
	// ErrEmbeddingUnavailable in that case.
	LexicalFallback bool
}

// SearchResult is a single, deduplicated document hit.
type SearchResult struct {
	ID             uuid.UUID `json:"id"`
	Paths          []Path    `json:"paths"`
	PathsTruncated bool      `json:"pathsTruncated,omitempty"`
}

// SearchResponse is the transport-neutral search result format. It contains
// IDs and titles only; details are fetched with the read methods.
type SearchResponse struct {
	Query   string         `json:"query"`
	Results []SearchResult `json:"results"`
	// SemanticSearchAvailable is false if the vector lists were skipped
	// because of a lexical fallback.
	SemanticSearchAvailable bool `json:"semanticSearchAvailable"`
	// SemanticError is the (internal, possibly sensitive) reason of a
	// lexical fallback. It is meant for server side logging only.
	SemanticError error `json:"-"`
}

// SourceInput describes a source reference to attach to a document.
type SourceInput struct {
	Kind  SourceKind // empty defaults to SourceKindConversation
	URI   string
	Title *string
}

// CreateDocumentInput describes a new, non-root document.
type CreateDocumentInput struct {
	Title   string
	Summary string
	// ParentIDs must contain at least one existing document. The first ID
	// becomes the primary parent; duplicates are ignored.
	ParentIDs []uuid.UUID
	// Blocks are optional Markdown contents in their final order.
	Blocks  []string
	Sources []SourceInput
}

// MetadataUpdate describes a change of title and/or summary. Nil fields are
// left unchanged.
type MetadataUpdate struct {
	Title   *string
	Summary *string
}
