package knowledge

import (
	"time"

	"github.com/google/uuid"
)

// ChildrenCursor is the keyset position of a child in the order
// lower(title), id. TitleKey is the lower-cased title as computed by the
// storage adapter.
type ChildrenCursor struct {
	TitleKey string
	ID       uuid.UUID
}

// ChildrenPageRequest requests a page of direct children.
type ChildrenPageRequest struct {
	// After continues after the given child; nil starts at the beginning.
	After *ChildrenCursor
	// Limit is the maximum number of children (must be positive).
	Limit int
}

// ChildrenPage is a page of direct children ordered by lower(title), id.
type ChildrenPage struct {
	Children []ChildRef
	// Next is set if more children follow.
	Next *ChildrenCursor
}

// SourcesCursor is the keyset position of a source in the order
// created_at, id.
type SourcesCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// SourcesPageRequest requests a page of source references.
type SourcesPageRequest struct {
	After *SourcesCursor
	Limit int
}

// SourcesPage is a page of sources ordered by created_at, id.
type SourcesPage struct {
	Sources []Source
	Next    *SourcesCursor
}

// BlockPageRequest requests a page of content blocks.
type BlockPageRequest struct {
	// AfterBlockID continues strictly after this block in the current
	// order. It must belong to the requested document.
	AfterBlockID *uuid.UUID
	// Limit is the maximum number of blocks (must be positive).
	Limit int
}

// FulltextPage is a page of the ordered content blocks of a document,
// read from one snapshot together with the document version.
type FulltextPage struct {
	DocumentID uuid.UUID
	Version    int64
	Blocks     []Block
	// HasMore reports whether blocks follow the last returned block.
	HasMore bool
}

// CreateDocumentResult reports a newly created document.
type CreateDocumentResult struct {
	DocumentSummary
	PrimaryParentID uuid.UUID
	// BlockIDs are the IDs of the initial blocks in input order.
	BlockIDs []uuid.UUID
	// SourceIDs are the IDs of the attached sources (after deduplication by
	// URI) in input order.
	SourceIDs []uuid.UUID
}

// ParentChange describes a version-checked change of a parent edge.
type ParentChange struct {
	DocumentID uuid.UUID
	ParentID   uuid.UUID
	// ExpectedVersion must equal the current document version.
	ExpectedVersion int64
	// MakePrimary is only used by AddParentChecked.
	MakePrimary bool
}

// ParentChangeResult reports the outcome of a parent edge change.
type ParentChangeResult struct {
	DocumentID uuid.UUID
	ParentID   uuid.UUID
	// Version is the document version after the change (unchanged for
	// no-ops).
	Version int64
	Changed bool
	// IsPrimary reports whether ParentID is (still) a primary parent
	// afterwards; false if the edge was removed.
	IsPrimary bool
	// PrimaryParentID is the primary parent after the change.
	PrimaryParentID uuid.UUID
}

// AddSourceResult reports the outcome of a version-checked AddSource.
type AddSourceResult struct {
	DocumentID uuid.UUID
	Source     Source
	Version    int64
	// Changed is false if the URI was already attached.
	Changed bool
}

// ParentList is the set of direct parents of a document together with the
// document version, read from one snapshot.
type ParentList struct {
	DocumentID uuid.UUID
	Version    int64
	// Parents are ordered primary first, then lower(title), id.
	Parents []ParentRef
}
