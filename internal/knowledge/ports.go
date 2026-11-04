package knowledge

import (
	"context"

	"github.com/google/uuid"
)

// Reader provides read access to the knowledge graph.
type Reader interface {
	Search(ctx context.Context, params SearchParams) (SearchResponse, error)
	GetSummary(ctx context.Context, documentID uuid.UUID) (DocumentSummary, error)
	GetParents(ctx context.Context, documentID uuid.UUID) ([]ParentRef, error)
	GetChildren(ctx context.Context, documentID uuid.UUID) ([]ChildRef, error)
	GetFulltext(ctx context.Context, documentID uuid.UUID) (Fulltext, error)
	GetSources(ctx context.Context, documentID uuid.UUID) ([]Source, error)
	GetDocument(ctx context.Context, documentID uuid.UUID) (Document, error)
	GetPaths(ctx context.Context, documentID uuid.UUID, allPaths bool) (PathSet, error)

	GetParentList(ctx context.Context, documentID uuid.UUID) (ParentList, error)

	// Paged variants with deterministic keyset ordering.
	GetChildrenPage(ctx context.Context, documentID uuid.UUID, req ChildrenPageRequest) (ChildrenPage, error)
	GetSourcesPage(ctx context.Context, documentID uuid.UUID, req SourcesPageRequest) (SourcesPage, error)
	GetFulltextPage(ctx context.Context, documentID uuid.UUID, req BlockPageRequest) (FulltextPage, error)
}

// Writer provides the invariant-preserving write operations. Every method is
// executed in a single database transaction.
type Writer interface {
	CreateDocument(ctx context.Context, in CreateDocumentInput) (CreateDocumentResult, error)
	UpdateDocumentMetadata(ctx context.Context, id uuid.UUID, expectedVersion int64, upd MetadataUpdate) (DocumentSummary, error)
	AddParent(ctx context.Context, childID, parentID uuid.UUID, makePrimary bool) error
	RemoveParent(ctx context.Context, childID, parentID uuid.UUID) error
	SetPrimaryParent(ctx context.Context, childID, parentID uuid.UUID) error
	// EditDocument applies a batch of block operations atomically, guarded
	// by the document version. It is the only way to change blocks.
	EditDocument(ctx context.Context, in EditDocumentInput) (EditDocumentResult, error)
	AddSource(ctx context.Context, documentID uuid.UUID, in SourceInput) (Source, error)

	// Version-checked variants of the hierarchy and source writes. The
	// expected version is compared after the hierarchy lock and the
	// document row lock were acquired; a mismatch returns a
	// *VersionConflictError. No-ops keep the version.
	AddParentChecked(ctx context.Context, in ParentChange) (ParentChangeResult, error)
	RemoveParentChecked(ctx context.Context, in ParentChange) (ParentChangeResult, error)
	SetPrimaryParentChecked(ctx context.Context, in ParentChange) (ParentChangeResult, error)
	AddSourceChecked(ctx context.Context, documentID uuid.UUID, expectedVersion int64, in SourceInput) (AddSourceResult, error)
	RetryEmbedding(ctx context.Context, kind TargetKind, targetID uuid.UUID) error
}

// Repository combines read and write access.
type Repository interface {
	Reader
	Writer
}

// Embedder turns texts into embedding vectors. Implementations must return
// exactly one vector of EmbeddingDimensions values per input, in input order.
type Embedder interface {
	// Model returns a stable identifier of the model (and its
	// configuration) that produced the vectors. Vectors of different models
	// are never compared with each other.
	Model() string
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}
