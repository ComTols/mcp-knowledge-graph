package mcpserver

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool names of the public contract.
const (
	ToolSearch           = "search"
	ToolGetRoot          = "get_root"
	ToolGetSummary       = "get_summary"
	ToolGetParents       = "get_parents"
	ToolGetChildren      = "get_children"
	ToolGetFulltext      = "get_fulltext"
	ToolGetPaths         = "get_paths"
	ToolGetSources       = "get_sources"
	ToolCreateDocument   = "create_document"
	ToolUpdateDocument   = "update_document"
	ToolEditDocument     = "edit_document"
	ToolAddParent        = "add_parent"
	ToolRemoveParent     = "remove_parent"
	ToolSetPrimaryParent = "set_primary_parent"
	ToolAddSource        = "add_source"
)

// ToolNames lists all registered tools in registration order.
var ToolNames = []string{
	ToolSearch, ToolGetRoot, ToolGetSummary, ToolGetParents, ToolGetChildren,
	ToolGetFulltext, ToolGetPaths, ToolGetSources, ToolCreateDocument,
	ToolUpdateDocument, ToolEditDocument, ToolAddParent, ToolRemoveParent,
	ToolSetPrimaryParent, ToolAddSource,
}

// Agent-facing tool descriptions (verbatim from the specification).
const (
	descSearch           = "Search the knowledge graph by meaning and keywords. Use this before creating a document, when you do not know a document ID, or when looking for related topics. Searches document titles/summaries and content blocks; returns ranked document IDs and root-to-document title paths only, not summaries or full text. Call get_summary, get_children, or get_fulltext on a result to inspect it. Search can find concepts even if the query words are absent from the text. Results may be incomplete while embeddings are pending."
	descGetRoot          = "Return the single root document of the knowledge graph. Use this to begin browsing categories when you do not have a search query. Then call get_children with the returned document ID. This does not return the entire knowledge graph."
	descGetSummary       = "Read the title, concise summary, and current version of one document without loading its content blocks or child documents. Use after search to decide whether the topic is relevant, or before updating document metadata. The returned version is required for safe subsequent write operations."
	descGetParents       = "List all direct parent documents of one document and identify its primary parent. Use this before adding, removing, or changing parent relationships, and to understand where the topic is classified. Parents can overlap because the knowledge graph is a DAG. This returns direct parents only, not all ancestors."
	descGetChildren      = "List the direct child topics under a parent, including each child's title and short summary. Use this for hierarchy exploration and to choose a parent for new knowledge. A child may also appear under other parents. This does not return child full texts or recursive descendants; call get_children again to navigate deeper."
	descGetFulltext      = "Retrieve the ordered Markdown content blocks of one document, including stable block IDs, individual block versions, and the current document version. Use this when detailed knowledge is needed or before edit_document. To modify content, refer to block IDs and expected document version; never send internal numeric positions. Large documents can be read in pages using after_block_id. This does not automatically include child summaries or generated navigation lists."
	descGetPaths         = "Resolve the hierarchical root-to-document location of an existing document. Returns the primary-parent path by default; optionally returns multiple valid paths when a document has several parents. Use this to understand the topic's context or after changing parent relationships. Paths contain document IDs and titles only."
	descGetSources       = "List the original external sources cited by a document, such as the archived conversation from which knowledge was extracted. Use this to verify provenance or inspect the original context. This tool returns references only and does not fetch, execute, or interpret external content."
	descCreateDocument   = "Create a new knowledge document under one or more existing parents. Before calling, search for an equivalent topic and inspect candidate parents; prefer updating an existing document when it already covers the subject. Provide a specific title, a concise summary of this document's own topic, and at least one parent ID. The first parent becomes the primary parent. Optional initial Markdown blocks and source references are stored atomically. The server does not create intermediate categories or infer parents automatically."
	descUpdateDocument   = "Update only a document's title and/or summary. Use get_summary to obtain its current version, then supply expected_version to avoid overwriting concurrent changes. Omit fields that should stay unchanged. This tool never changes content blocks or parent relationships; use edit_document for block content and the parent tools for hierarchy changes."
	descEditDocument     = "Apply an atomic, ordered batch of changes to one document's Markdown content blocks. Use get_fulltext first to obtain block IDs and the document's current version. Supported operations: replace a block's text, insert a new block immediately before or after an existing block, append, prepend, delete, and move. Refer to stable block IDs, never numeric positions. All operations run in the given order; if any operation fails, none are applied. A version conflict means you must re-read the document and recompute the edit."
	descAddParent        = "Add an existing document as another direct parent of the target document, creating an additional navigation path without duplicating its content. Use get_parents and get_paths first to understand the current hierarchy. Set make_primary=true only if this should become the canonical path. The operation fails if it would create a cycle or otherwise violate the root/reachability rules."
	descRemoveParent     = "Remove one direct parent relationship from an existing document without deleting the document or any of its content. Use get_parents first. The server refuses to remove the last parent or modify the root; when removing the primary parent it selects a deterministic remaining primary parent. This is a structural operation and must not rewrite document text or embeddings."
	descSetPrimaryParent = "Make one of a document's existing direct parents its primary parent. This changes only the canonical root-to-document path, not the actual graph edges or stored content. Use get_parents to identify valid existing parents. If the requested parent is already primary, the operation is a no-op."
	descAddSource        = "Attach a stable external source reference to an existing document, such as the archived conversation that supports its content. Use this after creating or updating knowledge from a source. The same source URI is stored only once per document; it may be referenced by different documents. Provide a permanent source identifier or URL, not an expiring presigned URL or a secret-bearing link."
)

func boolPtr(b bool) *bool { return &b }

func readOnly(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)}
}

func write(title string, destructive bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: boolPtr(destructive), OpenWorldHint: boolPtr(false)}
}

func (h *Handler) registerTools(s *mcp.Server) {
	addTool(s, h, &mcp.Tool{
		Name: ToolSearch, Title: "Search Knowledge", Description: descSearch,
		Annotations: readOnly("Search Knowledge"),
		InputSchema: schemaFor[SearchInput](func(s *jsonschema.Schema) {
			minLength(s, "query", 1)
			maxLength(s, "query", maxQueryRunes)
			intRange(s, "limit", 1, maxSearchLimit, defaultSearchLimit)
			intRange(s, "max_paths", 1, maxMaxPaths, defaultMaxPaths)
		}),
	}, h.search)

	addTool(s, h, &mcp.Tool{
		Name: ToolGetRoot, Title: "Get Root Document", Description: descGetRoot,
		Annotations: readOnly("Get Root Document"),
		InputSchema: schemaFor[GetRootInput](nil),
	}, h.getRoot)

	addTool(s, h, &mcp.Tool{
		Name: ToolGetSummary, Title: "Get Document Summary", Description: descGetSummary,
		Annotations: readOnly("Get Document Summary"),
		InputSchema: schemaFor[DocumentIDInput](func(s *jsonschema.Schema) { uuidProps(s, "document_id") }),
	}, h.getSummary)

	addTool(s, h, &mcp.Tool{
		Name: ToolGetParents, Title: "Get Parent Documents", Description: descGetParents,
		Annotations: readOnly("Get Parent Documents"),
		InputSchema: schemaFor[DocumentIDInput](func(s *jsonschema.Schema) { uuidProps(s, "document_id") }),
	}, h.getParents)

	addTool(s, h, &mcp.Tool{
		Name: ToolGetChildren, Title: "Get Child Documents", Description: descGetChildren,
		Annotations: readOnly("Get Child Documents"),
		InputSchema: schemaFor[GetChildrenInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id")
			intRange(s, "limit", 1, maxPageLimit, defaultPageLimit)
		}),
	}, h.getChildren)

	addTool(s, h, &mcp.Tool{
		Name: ToolGetFulltext, Title: "Get Document Blocks", Description: descGetFulltext,
		Annotations: readOnly("Get Document Blocks"),
		InputSchema: schemaFor[GetFulltextInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id", "after_block_id")
			intRange(s, "limit", 1, maxPageLimit, defaultPageLimit)
			minimum(s, "expected_version", 1)
		}),
	}, h.getFulltext)

	addTool(s, h, &mcp.Tool{
		Name: ToolGetPaths, Title: "Get Document Paths", Description: descGetPaths,
		Annotations: readOnly("Get Document Paths"),
		InputSchema: schemaFor[GetPathsInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id")
			intRange(s, "max_paths", 1, maxMaxPaths, defaultMaxPaths)
		}),
	}, h.getPaths)

	addTool(s, h, &mcp.Tool{
		Name: ToolGetSources, Title: "Get Document Sources", Description: descGetSources,
		Annotations: readOnly("Get Document Sources"),
		InputSchema: schemaFor[GetSourcesInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id")
			intRange(s, "limit", 1, maxPageLimit, defaultPageLimit)
		}),
	}, h.getSources)

	addTool(s, h, &mcp.Tool{
		Name: ToolCreateDocument, Title: "Create Document", Description: descCreateDocument,
		Annotations: write("Create Document", false),
		InputSchema: schemaFor[CreateDocumentInput](func(s *jsonschema.Schema) {
			minLength(s, "title", 1)
			minLength(s, "summary", 1)
			array(s, "parent_ids", 1, maxCreateParents)
			s.Properties["parent_ids"].UniqueItems = true
			s.Properties["parent_ids"].Items.Format = "uuid"
			array(s, "blocks", 0, maxCreateBlocks)
			minLength(s.Properties["blocks"].Items, "content", 1)
			array(s, "sources", 0, maxCreateSources)
			sourceRefSchema(s.Properties["sources"].Items)
		}),
	}, h.createDocument)

	addTool(s, h, &mcp.Tool{
		Name: ToolUpdateDocument, Title: "Update Document Metadata", Description: descUpdateDocument,
		Annotations: write("Update Document Metadata", true),
		InputSchema: schemaFor[UpdateDocumentInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id")
			minimum(s, "expected_version", 1)
			nonNullString(s, "title", "summary")
			minLength(s, "title", 1)
			minLength(s, "summary", 1)
		}),
	}, h.updateDocument)

	addTool(s, h, &mcp.Tool{
		Name: ToolEditDocument, Title: "Edit Document Blocks", Description: descEditDocument,
		Annotations: write("Edit Document Blocks", true),
		InputSchema: schemaFor[EditDocumentInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id")
			minimum(s, "expected_version", 1)
			array(s, "operations", 1, h.limits.MaxEditOperations)
			op := s.Properties["operations"].Items
			enum(op, "type", "replace", "insert", "append", "prepend", "delete", "move")
			nonNullString(op, "block_id", "before_block_id", "after_block_id", "content")
			uuidProps(op, "block_id", "before_block_id", "after_block_id")
		}),
	}, h.editDocument)

	addTool(s, h, &mcp.Tool{
		Name: ToolAddParent, Title: "Add Parent", Description: descAddParent,
		Annotations: write("Add Parent", false),
		InputSchema: schemaFor[AddParentInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id", "parent_id")
			minimum(s, "expected_version", 1)
			s.Properties["make_primary"].Default = []byte("false")
		}),
	}, h.addParent)

	addTool(s, h, &mcp.Tool{
		Name: ToolRemoveParent, Title: "Remove Parent", Description: descRemoveParent,
		Annotations: write("Remove Parent", true),
		InputSchema: schemaFor[ParentEdgeInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id", "parent_id")
			minimum(s, "expected_version", 1)
		}),
	}, h.removeParent)

	addTool(s, h, &mcp.Tool{
		Name: ToolSetPrimaryParent, Title: "Set Primary Parent", Description: descSetPrimaryParent,
		Annotations: write("Set Primary Parent", true),
		InputSchema: schemaFor[ParentEdgeInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id", "parent_id")
			minimum(s, "expected_version", 1)
		}),
	}, h.setPrimaryParent)

	addTool(s, h, &mcp.Tool{
		Name: ToolAddSource, Title: "Add Source", Description: descAddSource,
		Annotations: write("Add Source", false),
		InputSchema: schemaFor[AddSourceInput](func(s *jsonschema.Schema) {
			uuidProps(s, "document_id")
			minimum(s, "expected_version", 1)
			sourceRefSchema(s)
		}),
	}, h.addSource)
}

// --- schema helpers ---

// schemaFor infers the JSON Schema of T and lets customize add constraints
// that cannot be expressed with struct tags. Go validation in the handlers
// remains authoritative; the schema documents and pre-validates.
func schemaFor[T any](customize func(*jsonschema.Schema)) *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("infer schema: %v", err))
	}
	if customize != nil {
		customize(s)
	}
	return s
}

func prop(s *jsonschema.Schema, name string) *jsonschema.Schema {
	p, ok := s.Properties[name]
	if !ok {
		panic(fmt.Sprintf("schema has no property %q", name))
	}
	return p
}

func f64(v float64) *float64 { return &v }
func intp(v int) *int        { return &v }

func intRange(s *jsonschema.Schema, name string, lo, hi, def int) {
	p := prop(s, name)
	p.Minimum, p.Maximum = f64(float64(lo)), f64(float64(hi))
	p.Default = []byte(fmt.Sprint(def))
}

func minimum(s *jsonschema.Schema, name string, lo float64) {
	prop(s, name).Minimum = f64(lo)
}

func minLength(s *jsonschema.Schema, name string, n int) {
	prop(s, name).MinLength = intp(n)
}

func maxLength(s *jsonschema.Schema, name string, n int) {
	prop(s, name).MaxLength = intp(n)
}

func enum(s *jsonschema.Schema, name string, values ...string) {
	p := prop(s, name)
	p.Enum = make([]any, len(values))
	for i, v := range values {
		p.Enum[i] = v
	}
}

func uuidProps(s *jsonschema.Schema, names ...string) {
	for _, n := range names {
		prop(s, n).Format = "uuid"
	}
}

// nonNullString turns optional pointer fields (inferred as ["null",
// "string"]) into plain optional strings: omit the field instead of
// sending null.
func nonNullString(s *jsonschema.Schema, names ...string) {
	for _, n := range names {
		p := prop(s, n)
		p.Types, p.Type = nil, "string"
	}
}

// array makes a slice property a non-nullable array with bounds.
func array(s *jsonschema.Schema, name string, minItems, maxItems int) {
	p := prop(s, name)
	p.Types, p.Type = nil, "array"
	p.MinItems, p.MaxItems = intp(minItems), intp(maxItems)
}

func sourceRefSchema(s *jsonschema.Schema) {
	enum(s, "kind", "conversation", "other")
	minLength(s, "source_uri", 1)
	nonNullString(s, "title")
}
