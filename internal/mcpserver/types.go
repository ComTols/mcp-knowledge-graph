package mcpserver

// This file defines the public tool contract. JSON field names are
// snake_case; the `jsonschema` tags become property descriptions in the
// schemas advertised via tools/list. Fields without omitempty are required.

// DocumentReference is a document ID with its title.
type DocumentReference struct {
	ID    string `json:"id" jsonschema:"Document UUID"`
	Title string `json:"title" jsonschema:"Document title"`
}

// DocumentPath is ordered from the root to the target document.
type DocumentPath []DocumentReference

// --- search ---

type SearchInput struct {
	Query    string `json:"query" jsonschema:"Natural-language or keyword query. Search before creating a document to avoid duplicates."`
	Limit    int    `json:"limit,omitempty" jsonschema:"Maximum number of matched documents; default 10, max 50"`
	AllPaths bool   `json:"all_paths,omitempty" jsonschema:"Include bounded alternative root paths instead of only the primary path"`
	MaxPaths int    `json:"max_paths,omitempty" jsonschema:"Maximum paths per matched document when all_paths is true; default 5, max 10"`
}

type SearchHit struct {
	ID             string         `json:"id" jsonschema:"UUID of the matched document"`
	Paths          []DocumentPath `json:"paths" jsonschema:"Root-to-document paths; the first one follows primary parents"`
	PathsTruncated bool           `json:"paths_truncated" jsonschema:"True if further paths exist that were omitted"`
}

type SearchOutput struct {
	Query                   string      `json:"query"`
	Results                 []SearchHit `json:"results"`
	SemanticSearchAvailable bool        `json:"semantic_search_available" jsonschema:"False if only lexical matching was used"`
	Warnings                []string    `json:"warnings" jsonschema:"Machine-readable warnings such as semantic_search_unavailable"`
}

// --- get_root / get_summary ---

type GetRootInput struct{}

type DocumentIDInput struct {
	DocumentID string `json:"document_id" jsonschema:"Document UUID"`
}

type SummaryOutput struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Version int64  `json:"version" jsonschema:"Current document version; pass it as expected_version to write tools"`
}

// --- get_parents ---

type ParentOutput struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	IsPrimary bool   `json:"is_primary"`
}

type GetParentsOutput struct {
	DocumentID string         `json:"document_id"`
	Version    int64          `json:"version"`
	Parents    []ParentOutput `json:"parents" jsonschema:"Direct parents, primary parent first"`
}

// --- get_children ---

type GetChildrenInput struct {
	DocumentID string `json:"document_id" jsonschema:"UUID of the parent document"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum number of children; default 30, max 100"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"Opaque next_cursor of a previous call"`
}

type ChildOutput struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	IsPrimary bool   `json:"is_primary" jsonschema:"True if the requested document is this child's primary parent"`
}

type GetChildrenOutput struct {
	DocumentID string        `json:"document_id"`
	Children   []ChildOutput `json:"children"`
	HasMore    bool          `json:"has_more"`
	NextCursor *string       `json:"next_cursor"`
}

// --- get_fulltext ---

type GetFulltextInput struct {
	DocumentID      string `json:"document_id" jsonschema:"Document UUID"`
	Limit           int    `json:"limit,omitempty" jsonschema:"Maximum number of blocks; default 30, max 100"`
	AfterBlockID    string `json:"after_block_id,omitempty" jsonschema:"Return blocks strictly after this block of the same document (use next_after_block_id)"`
	ExpectedVersion int64  `json:"expected_version,omitempty" jsonschema:"If set, fail with version_conflict when the document version differs"`
}

type BlockOutput struct {
	ID      string `json:"id" jsonschema:"Stable block UUID"`
	Version int64  `json:"version" jsonschema:"Content version of this block"`
	Content string `json:"content" jsonschema:"Markdown content, verbatim"`
}

type GetFulltextOutput struct {
	DocumentID       string        `json:"document_id"`
	Version          int64         `json:"version" jsonschema:"Document version; required as expected_version by edit_document"`
	Blocks           []BlockOutput `json:"blocks"`
	HasMore          bool          `json:"has_more"`
	NextAfterBlockID *string       `json:"next_after_block_id"`
}

// --- get_paths ---

type GetPathsInput struct {
	DocumentID string `json:"document_id" jsonschema:"Document UUID"`
	AllPaths   bool   `json:"all_paths,omitempty" jsonschema:"Return alternative paths in addition to the primary path"`
	MaxPaths   int    `json:"max_paths,omitempty" jsonschema:"Maximum number of paths when all_paths is true; default 5, max 10"`
}

type GetPathsOutput struct {
	DocumentID     string         `json:"document_id"`
	Paths          []DocumentPath `json:"paths"`
	PathsTruncated bool           `json:"paths_truncated"`
}

// --- get_sources ---

type GetSourcesInput struct {
	DocumentID string `json:"document_id" jsonschema:"Document UUID"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum number of sources; default 30, max 100"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"Opaque next_cursor of a previous call"`
}

type SourceOutput struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	SourceURI string  `json:"source_uri"`
	Title     *string `json:"title"`
}

type GetSourcesOutput struct {
	DocumentID string         `json:"document_id"`
	Sources    []SourceOutput `json:"sources"`
	HasMore    bool           `json:"has_more"`
	NextCursor *string        `json:"next_cursor"`
}

// --- create_document ---

type BlockContentInput struct {
	Content string `json:"content" jsonschema:"Markdown content of the block"`
}

type SourceReferenceInput struct {
	Kind      string  `json:"kind" jsonschema:"conversation or other"`
	SourceURI string  `json:"source_uri" jsonschema:"Permanent URI or identifier of the source; no presigned or secret-bearing links"`
	Title     *string `json:"title,omitempty" jsonschema:"Optional human-readable title of the source"`
}

type CreateDocumentInput struct {
	Title     string                 `json:"title" jsonschema:"Specific document title"`
	Summary   string                 `json:"summary" jsonschema:"Concise summary of this document's own topic"`
	ParentIDs []string               `json:"parent_ids" jsonschema:"Distinct existing parent document UUIDs; the first becomes the primary parent"`
	Blocks    []BlockContentInput    `json:"blocks,omitempty" jsonschema:"Optional initial Markdown blocks in order"`
	Sources   []SourceReferenceInput `json:"sources,omitempty" jsonschema:"Optional source references"`
}

type CreateDocumentOutput struct {
	DocumentID      string   `json:"document_id"`
	Version         int64    `json:"version"`
	PrimaryParentID string   `json:"primary_parent_id"`
	BlockIDs        []string `json:"block_ids" jsonschema:"IDs of the initial blocks in input order"`
	SourceIDs       []string `json:"source_ids" jsonschema:"IDs of the attached sources"`
}

// --- update_document ---

type UpdateDocumentInput struct {
	DocumentID      string  `json:"document_id" jsonschema:"Document UUID"`
	ExpectedVersion int64   `json:"expected_version" jsonschema:"Current document version from get_summary"`
	Title           *string `json:"title,omitempty" jsonschema:"New title; omit to keep the current title"`
	Summary         *string `json:"summary,omitempty" jsonschema:"New summary; omit to keep the current summary"`
}

type ChangeOutput struct {
	DocumentID string `json:"document_id"`
	Version    int64  `json:"version"`
	Changed    bool   `json:"changed"`
}

// --- edit_document ---

type EditOperationInput struct {
	Type          string  `json:"type" jsonschema:"One of replace, insert, append, prepend, delete, move"`
	BlockID       *string `json:"block_id,omitempty" jsonschema:"Target block (replace, delete, move)"`
	BeforeBlockID *string `json:"before_block_id,omitempty" jsonschema:"Anchor block: place directly before it (insert, move); exclusive with after_block_id"`
	AfterBlockID  *string `json:"after_block_id,omitempty" jsonschema:"Anchor block: place directly after it (insert, move); exclusive with before_block_id"`
	Content       *string `json:"content,omitempty" jsonschema:"Markdown content (replace, insert, append, prepend)"`
}

type EditDocumentInput struct {
	DocumentID      string               `json:"document_id" jsonschema:"Document UUID"`
	ExpectedVersion int64                `json:"expected_version" jsonschema:"Document version from get_fulltext"`
	Operations      []EditOperationInput `json:"operations" jsonschema:"Operations applied atomically in this order"`
}

type CreatedBlockOutput struct {
	OperationIndex int    `json:"operation_index" jsonschema:"Zero-based index of the creating operation"`
	BlockID        string `json:"block_id"`
}

type EditDocumentOutput struct {
	DocumentID      string               `json:"document_id"`
	Version         int64                `json:"version"`
	Changed         bool                 `json:"changed"`
	CreatedBlocks   []CreatedBlockOutput `json:"created_blocks"`
	DeletedBlockIDs []string             `json:"deleted_block_ids"`
	BlockIDsInOrder []string             `json:"block_ids_in_order"`
}

// --- add_parent / remove_parent / set_primary_parent ---

type AddParentInput struct {
	DocumentID      string `json:"document_id" jsonschema:"UUID of the child document"`
	ParentID        string `json:"parent_id" jsonschema:"UUID of the existing document to add as parent"`
	ExpectedVersion int64  `json:"expected_version" jsonschema:"Current version of the child document"`
	MakePrimary     bool   `json:"make_primary,omitempty" jsonschema:"Make this the primary (canonical) parent; default false"`
}

type AddParentOutput struct {
	DocumentID string `json:"document_id"`
	ParentID   string `json:"parent_id"`
	Version    int64  `json:"version"`
	Changed    bool   `json:"changed"`
	IsPrimary  bool   `json:"is_primary"`
}

type ParentEdgeInput struct {
	DocumentID      string `json:"document_id" jsonschema:"UUID of the child document"`
	ParentID        string `json:"parent_id" jsonschema:"UUID of an existing direct parent"`
	ExpectedVersion int64  `json:"expected_version" jsonschema:"Current version of the child document"`
}

type ParentChangeOutput struct {
	DocumentID      string `json:"document_id"`
	Version         int64  `json:"version"`
	Changed         bool   `json:"changed"`
	PrimaryParentID string `json:"primary_parent_id"`
}

// --- add_source ---

type AddSourceInput struct {
	DocumentID      string  `json:"document_id" jsonschema:"Document UUID"`
	ExpectedVersion int64   `json:"expected_version" jsonschema:"Current document version"`
	Kind            string  `json:"kind" jsonschema:"conversation or other"`
	SourceURI       string  `json:"source_uri" jsonschema:"Permanent URI or identifier of the source; no presigned or secret-bearing links"`
	Title           *string `json:"title,omitempty" jsonschema:"Optional human-readable title of the source"`
}

type AddSourceOutput struct {
	DocumentID string `json:"document_id"`
	SourceID   string `json:"source_id"`
	Version    int64  `json:"version"`
	Changed    bool   `json:"changed"`
}
