# Implementation Specification: Knowledge Graph MCP Server (Go)

**Status:** Implementation-ready specification for Claude  
**Language:** All public tool names, field names, tool descriptions, Go identifiers, error codes, comments, logs, tests, and documentation produced by the implementation **must be English**.  
**Scope:** MCP server, tool contracts, handlers, validation, authentication, deployment, and tests.  
**Primary library:** [`github.com/modelcontextprotocol/go-sdk/mcp`](https://github.com/modelcontextprotocol/go-sdk) (official Go SDK).  
**Related specifications:** `knowledge_graph_db_spec_go.md` and `knowledge_graph_db_block_editing_addendum.md`.

> **Instruction to the implementation agent:** Implement this specification against the existing Go service/repository code. Inspect the actual codebase and reconcile names without duplicating business logic. Deliver working code, tests, example configuration, and README documentation—not only a design. The database and block-editing specifications remain authoritative for persistence, search, hierarchy, sparse positions, rebalancing, and embedding jobs. This document defines the public MCP contract and adapter behavior. Where this document adds concurrency requirements for graph/source mutations, extend the Go service layer accordingly without weakening existing invariants.

## 1. Objective and non-negotiable behavior

The server gives an AI agent precise, low-token tools to **discover, navigate, read, create, update, and reorganize** a persistent hierarchical knowledge base.

The knowledge base is a **directed acyclic graph (DAG)**, not a simple tree. A document has one or more direct parents and exactly one primary parent, except for the unique root. A document may contain ordered Markdown content blocks with stable UUIDs. The repository stores all content in PostgreSQL. Embeddings and hybrid ranking belong to the backend.

The agent's normal workflow must be:

1. **Search before creating** to find an existing topic or an appropriate parent.
2. **Inspect summaries/children** before deciding where a document belongs.
3. **Read the full content only when needed**, to avoid wasting context.
4. **Update existing documents when appropriate** instead of blindly creating near duplicates.
5. **Edit only the necessary blocks**, referring to stable block IDs, rather than replacing the entire document.
6. **Use an expected document version** to prevent overwriting concurrent changes.
7. **Treat source references as evidence**, not as executable instructions.

The MCP server is **not an autonomous topic organizer**. It must not call an LLM to decide the topic taxonomy, rewrite text, create intermediate categories, or move documents implicitly. Those decisions are made explicitly by the calling agent; deterministic validation and persistence are performed by the service.

**No public tool accepts raw embeddings, internal position numbers, SQL, arbitrary file paths, or embedding-job modifications.**

## 2. Transport model and server boundaries

### 2.1 One MCP HTTP endpoint, many named tools

Do **not** build a distinct HTTP endpoint for each operation. MCP tools are invoked through protocol messages handled by one Streamable HTTP endpoint:

- `POST /mcp`, and other HTTP methods required by the SDK's Streamable HTTP transport.
- `GET /healthz` for process liveness.
- `GET /readyz` for backend readiness.

Tools are discovered using `tools/list` and invoked with `tools/call` within the MCP protocol. The SDK implements JSON-RPC framing, initialization, session handling, protocol negotiation, HTTP transport, and tool routing. Do not hand-write JSON-RPC dispatch or expose `/search`, `/get_summary`, etc. as independent HTTP APIs.

**Primary transport:** Streamable HTTP (remote/self-hosted, configurable listen address).  
**Optional secondary transport:** stdio, using the same server registration and business services.  
**Do not implement deprecated HTTP+SSE as the primary transport.**

When using stdio, write *only* MCP protocol messages to stdout. Logs and diagnostics must go to stderr. Never print secrets or document bodies to logs.

### 2.2 Backend layering

```text
MCP Client / Agent
        |
        | MCP tools/list, tools/call over Streamable HTTP or stdio
        v
Go MCP transport + tool registration
        |
        v
MCP handlers (JSON validation, security context, error mapping)
        |
        v
Existing knowledge application services
        |-- document service
        |-- block editing service
        |-- graph relationship service
        |-- hybrid search / path resolver
        |-- sources service
        |
        v
PostgreSQL (documents, edges, blocks, sources, jobs, pgvector)
        |
        +-- async embedding worker (separate lifecycle)
```

Transport handlers must never run arbitrary SQL, calculate sparse positions, construct embeddings, or bypass application services. All writes must go through the transaction-safe domain APIs from the earlier specifications.

### 2.3 Suggested package layout (adapt to actual repository)

```text
cmd/kg-mcp/main.go
internal/mcpserver/server.go
internal/mcpserver/tools.go
internal/mcpserver/types.go
internal/mcpserver/handlers_read.go
internal/mcpserver/handlers_write.go
internal/mcpserver/errors.go
internal/mcpserver/auth.go
internal/mcpserver/server_test.go
internal/service/...                 # existing services; do not duplicate
internal/repository/...              # existing PostgreSQL layer
```

The `mcpserver` package depends on a **narrow Go service interface**, not on concrete `pgxpool` queries. Support dependency injection for integration tests.

## 3. Tool catalog and agent-facing conventions

Register the following **15 MVP tools** with their exact snake_case names. Descriptions below are intended to be used as the actual `mcp.Tool.Description` strings, not merely as comments.

| Tool | Read/write | Core purpose |
|---|---|---|
| `search` | Read | Hybrid semantic + lexical search; return IDs and root-to-document title paths only |
| `get_root` | Read | Find the single stable starting document |
| `get_summary` | Read | Lightweight topic summary and document version |
| `get_parents` | Read | Inspect direct parents and primary-parent relationship |
| `get_children` | Read | Discover direct child topics, with summaries |
| `get_fulltext` | Read | Retrieve editable content blocks and document version |
| `get_paths` | Read | Resolve a canonical path or bounded alternative paths |
| `get_sources` | Read | Retrieve external provenance references |
| `create_document` | Write | Create a document, parents, optional blocks and sources atomically |
| `update_document` | Write | Update title and/or summary, never replace blocks |
| `edit_document` | Write | Atomic block operations with relative placement and version checking |
| `add_parent` | Write | Add another parent or promote it to primary |
| `remove_parent` | Write | Remove an edge without orphaning the document |
| `set_primary_parent` | Write | Choose the canonical parent among existing parent edges |
| `add_source` | Write | Attach an external source reference idempotently |

**Not exposed in the MVP:** `delete_document`, raw block-ID deletion outside `edit_document`, direct embedding controls, SQL execution, arbitrary graph rebalancing, retrying embedding jobs, taxonomy-wide autonomous restructuring, or whole-database dump. These remain internal/admin responsibilities. An optional aggregated `get_document` or deterministic Markdown rendering tool may be introduced later, but is not required here: avoid duplicating retrieval APIs in the initial public tool set.

### 3.1 Shared public type conventions

- Document and block IDs are lowercase, canonical UUID strings. The fixed root ID is `00000000-0000-0000-0000-000000000001`.
- `document_id`, `parent_id`, `block_id`, and source IDs must be parsed and validated by the server. Never trust an arbitrary string as a database identifier.
- Versions are positive JSON integers (`int64` in Go) and must not be silently defaulted for mutations.
- Strings must be valid UTF-8. Trim only *for validation* unless the underlying domain rules explicitly normalize them; do not rewrite Markdown content.
- Required fields must actually be required by the generated/declared JSON Schema. Distinguish **field missing** from an explicitly empty string.
- All arrays should be non-null in responses (`[]`, not `null`) where possible.
- For read methods, an unknown document ID is an error, not an empty result.
- Query and navigation responses are intentionally concise. They must not sneak in embeddings, internal positions, scores, paragraphs, or entire child subtrees.
- Error responses use the convention in §7; a failed operation must not return an apparent success object.

### 3.2 Shared `DocumentReference`

```json
{
  "id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "title": "Go HTTP Server Hardening"
}
```

### 3.3 Shared `DocumentPath`

A path is a JSON array of `DocumentReference` objects, **ordered from root to target**. Do not use parent-title strings, cached paths, graph depth, or summaries in path elements.

```json
[
  {"id":"00000000-0000-0000-0000-000000000001","title":"root"},
  {"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","title":"Minkter"},
  {"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","title":"Go HTTP Server Hardening"}
]
```

### 3.4 Pagination convention

- `limit` is optional, with a documented tool-specific default and hard upper bound.
- `cursor` is an optional **opaque** token when a list might be large.
- Response uses `has_more` and `next_cursor` (`null` if no next page).
- Use stable, deterministic keyset ordering rather than an unbounded `OFFSET` for large lists.
- Cursor decoding must validate its structure and the relevant scope (document ID, list kind, query parameters); malformed/mismatched cursors return `invalid_input`.
- A cursor is not a security credential. It must not provide access outside the authenticated scope.
- For `get_fulltext` use an explicit stable block anchor `after_block_id` instead of an opaque cursor, paired with the document version to detect concurrent reordering.

## 4. Read tools — exact contracts

### 4.1 `search`

**MCP tool description (use verbatim):**

> Search the knowledge graph by meaning and keywords. Use this before creating a document, when you do not know a document ID, or when looking for related topics. Searches document titles/summaries and content blocks; returns ranked document IDs and root-to-document title paths only, not summaries or full text. Call get_summary, get_children, or get_fulltext on a result to inspect it. Search can find concepts even if the query words are absent from the text. Results may be incomplete while embeddings are pending.

**Input:**

| Field | Type | Required | Rules |
|---|---|---|---|
| `query` | string | Yes | Nonempty after trim, max 1,000 Unicode characters |
| `limit` | integer | No | Default 10; valid 1–50 |
| `all_paths` | boolean | No | Default `false`; if true include alternative root paths |
| `max_paths` | integer | No | Default 5; valid 1–10; relevant only if `all_paths=true` |

**Output:**

```json
{
  "query": "backend security in Go",
  "results": [
    {
      "id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      "paths": [[
        {"id":"00000000-0000-0000-0000-000000000001","title":"root"},
        {"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","title":"Minkter"},
        {"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","title":"Go HTTP Server Hardening"}
      ]],
      "paths_truncated": false
    }
  ],
  "semantic_search_available": true,
  "warnings": []
}
```

**Required behavior:**

- Delegate to the existing hybrid search implementation (document vectors, block vectors, lexical signals, document deduplication, RRF, and path resolution).
- Query embedding uses the **same compatible embedding model/configuration** as the stored vectors, with 1,024 dimensions in the current schema. Never generate embeddings in the MCP handler itself.
- Return **one entry per document**, even if several blocks matched.
- Default to exactly **one canonical path**, following primary parents. If `all_paths=true`, return up to `max_paths` bounded alternative paths and set `paths_truncated=true` when additional paths exist.
- A matched document must be the last node in each returned path.
- No score fields or content blocks in the response.
- Empty results are successful: `results: []`.
- If the embedding provider is unavailable, use **lexical-only fallback** whenever repository capabilities permit; set `semantic_search_available=false` and add a machine-readable warning such as `"semantic_search_unavailable"`. Do not invent semantic matches or claim they were found.
- Root should not appear as a normal search result unless a future explicit option enables it.

### 4.2 `get_root`

**MCP tool description (use verbatim):**

> Return the single root document of the knowledge graph. Use this to begin browsing categories when you do not have a search query. Then call get_children with the returned document ID. This does not return the entire knowledge graph.

**Input:** `{}` (empty object; no required fields).

**Output:**

```json
{
  "id": "00000000-0000-0000-0000-000000000001",
  "title": "root",
  "summary": "Top-level knowledge categories.",
  "version": 1
}
```

**Required behavior:** Read the existing root record; do not create or silently repair it on a normal tool invocation. A missing/malformed root is `integrity_violation`. Root seeding/migration occurs at startup as specified by the database contract.

### 4.3 `get_summary`

**MCP tool description (use verbatim):**

> Read the title, concise summary, and current version of one document without loading its content blocks or child documents. Use after search to decide whether the topic is relevant, or before updating document metadata. The returned version is required for safe subsequent write operations.

**Input:** `{"document_id":"<uuid>"}`.

**Output:**

```json
{
  "id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "title": "Go HTTP Server Hardening",
  "summary": "Timeouts, middleware, and request validation for Go HTTP servers.",
  "version": 7
}
```

**Required behavior:** Summary is the document's *own* summary, not a generated recursive summary of descendants. No hidden block loads.

### 4.4 `get_parents`

**MCP tool description (use verbatim):**

> List all direct parent documents of one document and identify its primary parent. Use this before adding, removing, or changing parent relationships, and to understand where the topic is classified. Parents can overlap because the knowledge graph is a DAG. This returns direct parents only, not all ancestors.

**Input:** `{"document_id":"<uuid>"}`.

**Output:**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "version": 7,
  "parents": [
    {"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","title":"Minkter","is_primary":true},
    {"id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc","title":"Go","is_primary":false}
  ]
}
```

**Required behavior:** A non-root document always has at least one parent, exactly one marked primary. For root return `parents: []`. Sort primary first, then `lower(title), id`. Treat invalid graph state as `integrity_violation`, not as a valid response.

### 4.5 `get_children`

**MCP tool description (use verbatim):**

> List the direct child topics under a parent, including each child's title and short summary. Use this for hierarchy exploration and to choose a parent for new knowledge. A child may also appear under other parents. This does not return child full texts or recursive descendants; call get_children again to navigate deeper.

**Input:**

```json
{"document_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","limit":30}
```

`document_id` required. `limit` default 30, maximum 100. `cursor` optional opaque string.

**Output:**

```json
{
  "document_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  "children": [
    {
      "id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
      "title":"Go HTTP Server Hardening",
      "summary":"Timeouts, middleware, and request validation for Go HTTP servers.",
      "is_primary":true
    }
  ],
  "has_more": false,
  "next_cursor": null
}
```

**Required behavior:** Sort by `lower(title), id`, use stable keyset pagination. `is_primary` is true when the requested parent is the child's primary parent. Summary changes are visible immediately; parent records must not contain redundant child summaries. Empty child list is valid.

### 4.6 `get_fulltext`

**MCP tool description (use verbatim):**

> Retrieve the ordered Markdown content blocks of one document, including stable block IDs, individual block versions, and the current document version. Use this when detailed knowledge is needed or before edit_document. To modify content, refer to block IDs and expected document version; never send internal numeric positions. Large documents can be read in pages using after_block_id. This does not automatically include child summaries or generated navigation lists.

**Input:**

| Field | Type | Required | Rules |
|---|---|---|---|
| `document_id` | UUID string | Yes | Existing document |
| `limit` | integer | No | Default 30, max 100 blocks |
| `after_block_id` | UUID string | No | Return blocks strictly after this block in current order |
| `expected_version` | integer | No | If provided, reject a changed document with `version_conflict` |

**Output:**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "version": 7,
  "blocks": [
    {"id":"11111111-1111-4111-8111-111111111111","version":2,"content":"## Timeouts\nUse ReadHeaderTimeout..."},
    {"id":"22222222-2222-4222-8222-222222222222","version":1,"content":"## Middleware\nValidate requests..."}
  ],
  "has_more": false,
  "next_after_block_id": null
}
```

**Required behavior:**

- Return the immutable block UUID and content in `ORDER BY position ASC, id ASC`; do **not** expose actual numeric position values by default.
- The returned `version` is **`documents.version`**, required by `edit_document`.
- `blocks[].version` is the individual block's content version, useful for inspection; document-level version controls edit batches.
- An `after_block_id` not belonging to the specified document is `invalid_input`, not a silent empty page.
- With `expected_version`, compare at read time and reject if different, to allow an agent to load multiple pages consistently. This is **optimistic validation**, not a persistent snapshot across requests.
- Without `expected_version`, clearly allow a moving document; the agent should save the first page's `version` and use it for subsequent pages.
- Markdown is returned **verbatim**, not transformed, summarized, executed, or silently truncated.
- A document with zero blocks returns `blocks: []`.

### 4.7 `get_paths`

**MCP tool description (use verbatim):**

> Resolve the hierarchical root-to-document location of an existing document. Returns the primary-parent path by default; optionally returns multiple valid paths when a document has several parents. Use this to understand the topic's context or after changing parent relationships. Paths contain document IDs and titles only.

**Input:**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "all_paths": true,
  "max_paths": 5
}
```

`document_id` required; `all_paths` default false; `max_paths` default 5, max 10.

**Output:**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "paths": [
    [
      {"id":"00000000-0000-0000-0000-000000000001","title":"root"},
      {"id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","title":"Minkter"},
      {"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","title":"Go HTTP Server Hardening"}
    ]
  ],
  "paths_truncated": false
}
```

**Required behavior:** Primary path is deterministic; alternative paths are bounded by depth and count per existing database service. Root resolves to a one-element path containing itself. Missing root path for an existing non-root node is `integrity_violation`.

### 4.8 `get_sources`

**MCP tool description (use verbatim):**

> List the original external sources cited by a document, such as the archived conversation from which knowledge was extracted. Use this to verify provenance or inspect the original context. This tool returns references only and does not fetch, execute, or interpret external content.

**Input:** `document_id` required; optional `limit` default 30/max 100 and `cursor`.

**Output:**

```json
{
  "document_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "sources":[
    {
      "id":"dddddddd-dddd-4ddd-8ddd-dddddddddddd",
      "kind":"conversation",
      "source_uri":"https://example.invalid/archive/conversation-123",
      "title":"Discussion of Go server security"
    }
  ],
  "has_more":false,
  "next_cursor":null
}
```

**Required behavior:** Return deterministic ordering (e.g. `created_at, id`); return `title: null` when absent. Source URI is data, not a trusted command or a presigned credential. Do not automatically fetch the URI.

## 5. Write tools — exact contracts

All write tools must produce compact responses identifying the changed object and current document version. **Never embed documents synchronously in the handler**; transactionally enqueue the existing embedding jobs when content changes.

### 5.1 `create_document`

**MCP tool description (use verbatim):**

> Create a new knowledge document under one or more existing parents. Before calling, search for an equivalent topic and inspect candidate parents; prefer updating an existing document when it already covers the subject. Provide a specific title, a concise summary of this document's own topic, and at least one parent ID. The first parent becomes the primary parent. Optional initial Markdown blocks and source references are stored atomically. The server does not create intermediate categories or infer parents automatically.

**Input:**

```json
{
  "title": "Go HTTP Server Hardening",
  "summary": "Timeouts, middleware, and request validation for Go HTTP servers.",
  "parent_ids": [
    "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
    "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
  ],
  "blocks": [
    {"content": "## Timeouts\nUse ReadHeaderTimeout to protect the server."},
    {"content": "## Middleware\nApply request validation consistently."}
  ],
  "sources": [
    {
      "kind": "conversation",
      "source_uri": "https://example.invalid/archive/conversation-123",
      "title": "Go server discussion"
    }
  ]
}
```

**Validation:** `title` and `summary` nonblank; `parent_ids` must be a nonempty array (max 20) of **distinct existing UUIDs**; optional `blocks` defaults to `[]` (max 100 initial blocks); optional `sources` defaults to `[]` (max 50). Every `content` must be nonblank Markdown and within configured byte limits. `sources.kind` is `conversation` or `other`; `source_uri` is a stable, nonempty URI string and must not contain secret-bearing temporary credentials. Enforce source URI shape per database rules, without fetching it.

**Output:**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "version": 1,
  "primary_parent_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  "block_ids": [
    "11111111-1111-4111-8111-111111111111",
    "22222222-2222-4222-8222-222222222222"
  ],
  "source_ids": ["dddddddd-dddd-4ddd-8ddd-dddddddddddd"]
}
```

**Required behavior:** Create the document, edges, blocks, sources, and embedding jobs **in one transaction**; use the global graph mutation advisory lock from the database spec. Block positions use the existing sparse-position logic, not client-provided numbers. Deduplicate source references per document. Root cannot be created via this tool. Client-supplied IDs, embedding values, position values, and a `root` flag are not accepted.

### 5.2 `update_document`

**MCP tool description (use verbatim):**

> Update only a document's title and/or summary. Use get_summary to obtain its current version, then supply expected_version to avoid overwriting concurrent changes. Omit fields that should stay unchanged. This tool never changes content blocks or parent relationships; use edit_document for block content and the parent tools for hierarchy changes.

**Input:**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "expected_version": 7,
  "title": "Secure Go HTTP Servers",
  "summary": "Security considerations for production Go HTTP servers."
}
```

`document_id` and `expected_version` required. At least one of `title`, `summary` must be **present**. Both optional values must use pointer or optional-field semantics, not default to empty strings. Any provided value must be nonblank after trim.

**Output:**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "version": 8,
  "changed": true
}
```

**Required behavior:** Check version atomically. If both provided values equal the stored values, return `changed=false` and the unchanged version, and do not enqueue embedding jobs. Otherwise increment document version **once**, invalidate/requeue **only the document title+summary embedding**, and return the new version. Updating summary must be reflected automatically in `get_children` on every parent. Root metadata changes may be rejected as `root_violation` in MVP to preserve root identity and predictable navigation.

### 5.3 `edit_document`

**MCP tool description (use verbatim):**

> Apply an atomic, ordered batch of changes to one document's Markdown content blocks. Use get_fulltext first to obtain block IDs and the document's current version. Supported operations: replace a block's text, insert a new block immediately before or after an existing block, append, prepend, delete, and move. Refer to stable block IDs, never numeric positions. All operations run in the given order; if any operation fails, none are applied. A version conflict means you must re-read the document and recompute the edit.

**Input:**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "expected_version": 7,
  "operations": [
    {
      "type": "replace",
      "block_id": "11111111-1111-4111-8111-111111111111",
      "content": "## Timeouts\nSet ReadHeaderTimeout and other appropriate limits."
    },
    {
      "type": "insert",
      "after_block_id": "11111111-1111-4111-8111-111111111111",
      "content": "## Request size\nLimit large request bodies."
    },
    {
      "type": "move",
      "block_id": "22222222-2222-4222-8222-222222222222",
      "before_block_id": "11111111-1111-4111-8111-111111111111"
    }
  ]
}
```

`document_id`, positive `expected_version`, and `operations` (1–100, configurable hard maximum) are required.

#### Operation-specific schema and validation

| `type` | Required fields | Forbidden fields | Meaning |
|---|---|---|---|
| `replace` | `block_id`, `content` | `before_block_id`, `after_block_id` | Replace only that block's Markdown content |
| `insert` | `content`, exactly one of `before_block_id` / `after_block_id` | `block_id` | Create a block immediately next to the referenced block |
| `append` | `content` | `block_id`, anchors | Create a block at the end |
| `prepend` | `content` | `block_id`, anchors | Create a block at the beginning |
| `delete` | `block_id` | `content`, anchors | Delete an existing block |
| `move` | `block_id`, exactly one anchor | `content` | Move an existing block immediately before/after anchor |

- Additional unexpected operation properties must be rejected, not silently ignored.
- Referenced block IDs must belong to the target document **at that point in the ordered batch**. An operation may refer to a newly created block only if the API supports its reference mechanism; this MVP does **not** support referring to newly created blocks in the same request (their IDs are server-created and returned afterwards). Use successive requests when necessary.
- Moving a block relative to itself is invalid. Moving to its existing position is a no-op.
- Deleting a block makes it unavailable to later operations in the same batch. Replacing or moving a deleted block must cause a rollback.
- Empty/blank `content` is rejected for insert/append/prepend/replace.
- No operation accepts a `position`, `index`, or full-document Markdown string.
- Use a strict `oneOf` schema for operation variants **if supported cleanly by the installed SDK/schema tooling**; always add deterministic Go validation because cross-field constraints must not depend on a loosely inferred schema.

**Output:**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "version": 8,
  "changed": true,
  "created_blocks": [
    {
      "operation_index": 1,
      "block_id": "33333333-3333-4333-8333-333333333333"
    }
  ],
  "deleted_block_ids": [],
  "block_ids_in_order": [
    "22222222-2222-4222-8222-222222222222",
    "11111111-1111-4111-8111-111111111111",
    "33333333-3333-4333-8333-333333333333"
  ]
}
```

`operation_index` is **zero-based**. `block_ids_in_order` includes only IDs, **not** content or internal numeric positions. Return `changed=false` and unchanged document version if the entire batch is a semantic no-op.

**Required transaction implementation (reuse existing `EditDocument`):**

1. Open a DB transaction.
2. Lock the document row using `SELECT ... FOR UPDATE`.
3. Compare `expected_version` with the locked value; fail with `version_conflict` if different.
4. Validate and execute operations in their input order against the current block order.
5. Compute relative sparse positions internally using `BIGINT` and `PositionGap = 1_000_000`; use midpoint insertion when free space exists.
6. When there is no gap or an integer boundary would overflow, rebalance **only the target document** under the same lock. Preserve the **intended new logical order** for moves; use the deferrable uniqueness constraint.
7. Invalidate and enqueue embeddings only for inserted or actually replaced text. Deletion removes its job. Moves/rebalances do not trigger embeddings.
8. Increment the document version **once per changed batch**. Keep block IDs stable. Do not increment a block's content version for a pure move/rebalance.
9. Commit all data and embedding-job changes together; on any error roll back everything, including changes from earlier operations.

**Examples that must work:** Insert between A and B; move B before A; append to empty document; prepend to a one-block document; batch `replace + insert + move`; force a no-gap rebalance; reject a stale version; reject two mutually exclusive anchors; reject an anchor from a different document.

### 5.4 `add_parent`

**MCP tool description (use verbatim):**

> Add an existing document as another direct parent of the target document, creating an additional navigation path without duplicating its content. Use get_parents and get_paths first to understand the current hierarchy. Set make_primary=true only if this should become the canonical path. The operation fails if it would create a cycle or otherwise violate the root/reachability rules.

**Input:**

```json
{
  "document_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "parent_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  "expected_version":8,
  "make_primary":false
}
```

`make_primary` optional, default false; all other fields required.

**Output:**

```json
{
  "document_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "parent_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  "version":9,
  "changed":true,
  "is_primary":false
}
```

**Required behavior:** Use the graph advisory transaction lock; acquire locks in the existing agreed ordering; validate endpoints and cycle prevention. Duplicate edge is an idempotent no-op **unless** `make_primary=true` changes the primary parent. In the no-op case, preserve the version and return `changed=false`. An existing secondary edge may be promoted without inserting a duplicate. Any actual hierarchy change increments the target document version exactly once. No embeddings change.

### 5.5 `remove_parent`

**MCP tool description (use verbatim):**

> Remove one direct parent relationship from an existing document without deleting the document or any of its content. Use get_parents first. The server refuses to remove the last parent or modify the root; when removing the primary parent it selects a deterministic remaining primary parent. This is a structural operation and must not rewrite document text or embeddings.

**Input:**

```json
{
  "document_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "parent_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  "expected_version":9
}
```

**Output:**

```json
{
  "document_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "version":10,
  "changed":true,
  "primary_parent_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
}
```

**Required behavior:** Reject root modifications, removal of last parent, missing target, and missing edge. Do not silently report success for a nonexistent edge: return `relationship_not_found`. If primary is removed, choose the remaining edge with earliest `created_at`, then lowest `parent_id` as tie-breaker; do this atomically. Check version under the global graph lock and document row lock. Graph stays connected; no embeddings change.

### 5.6 `set_primary_parent`

**MCP tool description (use verbatim):**

> Make one of a document's existing direct parents its primary parent. This changes only the canonical root-to-document path, not the actual graph edges or stored content. Use get_parents to identify valid existing parents. If the requested parent is already primary, the operation is a no-op.

**Input:**

```json
{
  "document_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "parent_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc",
  "expected_version":10
}
```

**Output:**

```json
{
  "document_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "version":11,
  "changed":true,
  "primary_parent_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc"
}
```

**Required behavior:** Parent edge must exist, reject otherwise. Keep exactly one primary edge per non-root document. Use graph advisory lock and transaction, check expected version, update only necessary flags, and increment document version only on change. Embeddings do not change. No new parent edge is created by this tool.

### 5.7 `add_source`

**MCP tool description (use verbatim):**

> Attach a stable external source reference to an existing document, such as the archived conversation that supports its content. Use this after creating or updating knowledge from a source. The same source URI is stored only once per document; it may be referenced by different documents. Provide a permanent source identifier or URL, not an expiring presigned URL or a secret-bearing link.

**Input:**

```json
{
  "document_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "expected_version":11,
  "kind":"conversation",
  "source_uri":"https://example.invalid/archive/conversation-456",
  "title":"Follow-up on middleware"
}
```

`document_id`, `expected_version`, `kind`, and `source_uri` required; `title` optional and may be omitted. Allowed `kind` values initially `conversation`, `other`.

**Output:**

```json
{
  "document_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "source_id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
  "version":12,
  "changed":true
}
```

**Required behavior:** Enforce `(document_id, source_uri)` uniqueness. If the exact URI is already attached, return the existing source ID, `changed=false`, and unchanged version; do not overwrite its existing title implicitly. For new references, lock/check document version and insert under transaction, increment document version once. Do not trigger embeddings because source references are not embedded by the MVP. Reject secret-bearing or unsupported URI schemes according to the configured URI policy; never fetch the URI.

## 6. MCP SDK implementation requirements

### 6.1 SDK and versioning

Use the maintained official library:

```bash
go get github.com/modelcontextprotocol/go-sdk/mcp
```

Pin a compatible released version in `go.mod` / `go.sum`, not `@main`. Ensure the repository's Go toolchain meets that version's requirements. **Read the APIs for the actually resolved SDK version** rather than relying on older community `mcp-go` examples. Do not mix these different packages.

The generic `mcp.AddTool` API automatically infers JSON Schemas from typed Go input/output structs, validates inputs/outputs, and populates structured tool results. This is preferred to raw `Server.AddTool` unless a particular schema cannot be represented safely through the generic helper.

### 6.2 Server registration example

The following is **illustrative compile-oriented code** for the official SDK; adapt imports and service method signatures to the actual codebase. No placeholder business logic should remain in the delivered implementation.

```go
package mcpserver

import (
    "context"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Service interface {
    Search(ctx context.Context, input SearchInput) (SearchOutput, error)
    GetSummary(ctx context.Context, documentID string) (SummaryOutput, error)
    EditDocument(ctx context.Context, input EditDocumentInput) (EditDocumentOutput, error)
    // Include the other domain operations used by registered tools.
}

type Handler struct {
    service Service
}

func NewServer(service Service, version string) *mcp.Server {
    h := &Handler{service: service}
    server := mcp.NewServer(
        &mcp.Implementation{Name: "knowledge-graph", Version: version},
        nil,
    )

    mcp.AddTool(server, &mcp.Tool{
        Name: "search",
        Description: "Search the knowledge graph by meaning and keywords. " +
            "Returns ranked document IDs and root-to-document title paths only; " +
            "use get_summary or get_fulltext for details. Search before creating knowledge.",
        Annotations: &mcp.ToolAnnotations{
            ReadOnlyHint: true,
        },
    }, h.search)

    mcp.AddTool(server, &mcp.Tool{
        Name: "edit_document",
        Description: "Apply an atomic ordered batch of block edits to one document. " +
            "First call get_fulltext to obtain block IDs and document version. " +
            "Use relative anchors, never numeric positions; stale versions are rejected.",
        Annotations: &mcp.ToolAnnotations{
            ReadOnlyHint: false,
        },
    }, h.editDocument)

    // Register the remaining 13 tools with the exact descriptions in this spec.
    return server
}

type SearchInput struct {
    Query    string `json:"query" jsonschema:"Natural-language or keyword query; search before creating duplicate knowledge"`
    Limit    int    `json:"limit,omitempty" jsonschema:"Maximum number of matched documents; default 10, max 50"`
    AllPaths bool   `json:"all_paths,omitempty" jsonschema:"Include bounded alternative root paths instead of only the primary path"`
    MaxPaths int    `json:"max_paths,omitempty" jsonschema:"Maximum paths per matched document when all_paths is true; max 10"`
}

type PathNode struct {
    ID    string `json:"id"`
    Title string `json:"title"`
}

type SearchHit struct {
    ID             string       `json:"id"`
    Paths          [][]PathNode `json:"paths"`
    PathsTruncated bool         `json:"paths_truncated"`
}

type SearchOutput struct {
    Query                   string      `json:"query"`
    Results                 []SearchHit `json:"results"`
    SemanticSearchAvailable bool        `json:"semantic_search_available"`
    Warnings                []string    `json:"warnings"`
}

func (h *Handler) search(
    ctx context.Context,
    _ *mcp.CallToolRequest,
    input SearchInput,
) (*mcp.CallToolResult, SearchOutput, error) {
    if err := validateSearch(input); err != nil {
        return nil, SearchOutput{}, publicError("invalid_input", err.Error(), false)
    }
    output, err := h.service.Search(ctx, input)
    if err != nil {
        return nil, SearchOutput{}, mapServiceError(err)
    }
    return nil, output, nil
}
```

The registration descriptions in this example are abbreviated for readability. **Actual registration must use the full agent-facing descriptions in §4–§5.** Each tool needs its own input/output structs or carefully composed shared types. Do not implement only the two example tools.

**Schema caveat:** The SDK's schema inference does not replace domain validation. Set explicit schema limits/enum values/`oneOf` where needed (using `github.com/google/jsonschema-go/jsonschema`, which is used by the SDK), and validate again in Go. For the typed `edit_document` handler, ensure exclusive anchor requirements and per-operation forbidden fields are checked deterministically. Do not rely on a generic `map[string]any` as the primary API for every tool.

### 6.3 Handler shape and structured responses

Prefer handlers with the exact generic `AddTool` signature:

```go
func (
    h *Handler,
) getSummary(
    ctx context.Context,
    req *mcp.CallToolRequest,
    input GetSummaryInput,
) (*mcp.CallToolResult, GetSummaryOutput, error)
```

The Go SDK returns successful typed outputs through `CallToolResult.StructuredContent` **and** text content for compatibility. Return `(nil, output, nil)` on success and let `mcp.AddTool` handle marshaling and schema validation. Do not manually construct JSON-RPC messages or double-encode structured output.

When a domain operation fails, return an error; the generic helper maps an ordinary handler error to a tool result with `isError=true` and an error message in its content. See §7 for the format of that message. Do not use HTTP 200 success contents that merely say “failed” without `isError`.

### 6.4 Streamable HTTP bootstrap example

```go
package main

import (
    "context"
    "log"
    "net/http"
    "os"
    "time"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

func serveHTTP(ctx context.Context, appService KnowledgeService) error {
    server := newConfiguredMCPServer(appService) // Implements all registered tools.
    mcpHandler := mcp.NewStreamableHTTPHandler(
        func(*http.Request) *mcp.Server { return server },
        &mcp.StreamableHTTPOptions{
            JSONResponse: true,
            MaxRequestBodyBytes: 2 << 20,
        },
    )

    mux := http.NewServeMux()
    mux.Handle("/mcp", authMiddleware(mcpHandler))
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("ok"))
    })
    mux.HandleFunc("/readyz", readinessHandler(appService))

    httpServer := &http.Server{
        Addr:              "127.0.0.1:8080",
        Handler:           mux,
        ReadHeaderTimeout: 10 * time.Second,
    }

    // Replace placeholder wrappers with the concrete authenticated implementation.
    // Run HTTP in a goroutine, shut down gracefully when ctx is canceled.
    _ = ctx
    _ = os.Stderr
    log.Print("MCP server starting")
    return httpServer.ListenAndServe()
}
```

This example highlights SDK transport wiring; **the delivered code must implement graceful shutdown, actual authentication, readiness checks, and configurable listen address**. Do not copy the placeholder function names into working code without defining them. `JSONResponse: true` is an optional compatibility optimization for ordinary tool requests; preserve protocol support via the SDK and do not invent REST routing.

The example's `ListenAndServe` returns `http.ErrServerClosed` on graceful shutdown; treat it as a normal termination, not a fatal crash. Do not assume all clients use only POST; the SDK handler owns the HTTP methods at `/mcp`.

### 6.5 Optional stdio server

```go
server := newConfiguredMCPServer(appService)
if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
    return err
}
```

Log to stderr only. Reuse identical tool descriptions, input/output contracts and services across both transports.

### 6.6 Annotations and discoverability

Set `mcp.Tool.Annotations` truthfully. The current Go SDK exposes `mcp.ToolAnnotations` with `ReadOnlyHint`, `DestructiveHint`, `IdempotentHint`, and `OpenWorldHint`. These are **hints**, not access control.

- All eight read tools: `ReadOnlyHint=true`, `OpenWorldHint=false` (all reads stay in the local knowledge graph; sources are returned but not fetched).
- `create_document`, `add_parent`, and `add_source`: writing tools; `DestructiveHint=false` when operations are additive in the ordinary case.
- `update_document`, `edit_document`, `remove_parent`, and `set_primary_parent`: writing tools; mark potentially destructive or modifying appropriately. For instance, `edit_document` may delete/replace content, so `DestructiveHint=true`.
- Only advertise `IdempotentHint=true` when repeated calls with the **same complete parameters** have no additional effect; expected-version semantics may cause a retry to fail, so be conservative. In particular, `create_document` is **not idempotent** without a separately designed idempotency key.
- Tool titles should be short English names, e.g. `Search Knowledge`, `Edit Document Blocks`.

Use `mcp.AddTool` so that clients can discover accurate schemas and descriptions via `tools/list`. Never hide important required arguments only in README text.

## 7. Errors, concurrency, and recoverability

### 7.1 Structured, agent-readable failure messages

**Desired error content** is a small JSON object serialized as **one text content block** on a tool result with `isError=true`:

```json
{
  "code": "version_conflict",
  "message": "Document changed since it was read. Retrieve the current content and retry.",
  "retryable": true,
  "details": {
    "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
    "expected_version": 7,
    "current_version": 8
  }
}
```

For typed `mcp.AddTool` handlers, returning a sanitized Go `error` containing the serialized JSON text is sufficient: the SDK sets `isError=true` and places the error text in `content`. **Do not assume the SDK will produce `structuredContent` for failures.** Implement a shared `PublicToolError` with custom `Error()` JSON serialization, and test that MCP clients observe `isError=true` and can parse the text. If explicit structured error results are later needed, implement them deliberately using the appropriate lower-level SDK API and test schema behavior.

**Error code vocabulary:**

| Code | Meaning / agent action | Retryable? |
|---|---|---|
| `invalid_input` | Bad UUID, blank field, invalid op/anchor, impossible limit; correct arguments | No |
| `not_found` | Requested document/block does not exist; search or inspect parents | No |
| `relationship_not_found` | Requested parent edge does not exist; call `get_parents` | No |
| `version_conflict` | Expected version is stale; re-read and replan | Yes (after read) |
| `cycle_detected` | Proposed parent would create a cycle; choose another parent | No |
| `root_violation` | Attempt to create/change root incompatibly | No |
| `last_parent_violation` | Attempt to remove last parent | No |
| `integrity_violation` | Database graph/state invariant broken; administrator action | No |
| `semantic_search_unavailable` | Embedding request failed; lexical fallback may still succeed | Yes |
| `dependency_unavailable` | PostgreSQL or required service unavailable | Yes |
| `permission_denied` | Authenticated caller not allowed to perform operation | No |
| `internal_error` | Unexpected server failure, without leaked internals | Maybe |

Do **not** return raw SQL statements, database connection strings, stack traces, API keys, embedding provider errors containing credentials, or sensitive source bodies to the agent. Log internal errors with request IDs server-side and return a safe error message externally.

`semantic_search_unavailable` is preferably a **warning on a successful lexical-fallback search**, not an error, unless no search path is available.

### 7.2 Optimistic locking

- `update_document`, `edit_document`, `add_parent`, `remove_parent`, `set_primary_parent`, and `add_source` require positive `expected_version`.
- Validate inside the transaction **after acquiring the appropriate locks**, not with an earlier unlocked read.
- Every actual logical document change increases `documents.version` **exactly once per successful call**, except operations within `edit_document` (which increase once per entire batch).
- A no-op leaves version unchanged.
- A caller must not silently retry a failed *write* against a new version without first re-reading/re-evaluating the intention.
- For graph writes, acquire the database spec's global graph advisory lock **before** document row locks; keep consistent ordering to prevent deadlocks.
- The underlying repository currently may expose graph/source methods without `expectedVersion`; add version-aware service operations for the MCP adapter instead of implementing separate unprotected write logic.

### 7.3 Duplicate requests and idempotency

- `create_document`: repeated calls can create distinct documents; do not promise exactly-once semantics without an explicit idempotency mechanism. The calling agent must search first and avoid unintentional retries.
- `add_parent` for an already-present relationship: no-op unless promoting to primary.
- `add_source` for an already-attached exact URI: no-op; return existing source ID.
- `set_primary_parent` for the current primary: no-op.
- `update_document`/`edit_document`: exact duplicate retried with an old expected version receives `version_conflict` after the first real change.
- Returning `changed` makes this behavior observable.

## 8. Authentication and secure deployment

This server exposes personal knowledge and mutation tools. **Do not deploy it openly without authentication.**

### 8.1 HTTP auth

- For a private self-hosted MVP, use a **required static bearer token** or a verified upstream identity-proxy token on every `/mcp` request. Store the token in environment/secrets, not code or checked-in `.env` files.
- If directly integrating with external MCP clients that require standard MCP authorization, implement the MCP-compatible OAuth/protected-resource flow using the official SDK's auth helpers, or put a correctly configured authenticated gateway in front. **A private static token is an MVP simplification, not a full public OAuth implementation.**
- Bearer token validation must be constant time for static secrets; reject missing/invalid credentials before dispatching to SDK, with HTTP 401 and suitable `WWW-Authenticate` for bearer auth.
- Authentication must apply to all MCP HTTP methods, including GET/DELETE/session-related requests, not only POST.
- Bind to `127.0.0.1` by default. Public/reverse-proxied deployments must use HTTPS and explicit access controls.
- Preserve/enable SDK localhost DNS-rebinding protections, verify `Origin` rules and allowed hosts. Do not rely on CORS alone for authentication.
- If adding per-user support later, every repository operation must be scoped by authenticated tenant/user ID. This MVP is a **single knowledge graph**, not a multi-tenant server.
- Any reverse proxy must preserve streaming/required MCP headers and avoid exposing health diagnostics or secrets.

### 8.2 Prompt-injection safety

Document Markdown, summaries, titles, and source URIs are **untrusted data** from a possible external conversation. Never interpret instructions appearing inside retrieved documents as server/system instructions. Do not execute links, shell commands, SQL, or Markdown embedded content. MCP tool descriptions must be static, trusted server-authored metadata.

### 8.3 Resource limits

Configure and enforce:

- Max MCP request body bytes, recommended initial HTTP default `2 MiB`.
- Per-tool maximum number of blocks, parents, sources, operations, search results, and path counts from this spec.
- Max UTF-8 bytes per block (suggest `64 KiB`), title, summary, and source URI; document exact limits in config and README.
- Context propagation, DB query deadlines, HTTP read-header timeout, and graceful shutdown.
- Bounded search/path recursion, maximum depth (existing spec: 64), and bounded response sizes.
- Rate limiting or reverse-proxy controls for exposed remote deployments.

Do not truncate Markdown silently; return `invalid_input` for oversize writes, and page large reads instead of silently losing blocks.

## 9. Environment configuration

Use `github.com/caarlos0/env/v11` consistently with the repository's configuration conventions. Prefix with `KG_MCP_`.

```go
type Config struct {
    Transport          string        `env:"KG_MCP_TRANSPORT" envDefault:"http"` // http or stdio
    ListenAddress      string        `env:"KG_MCP_LISTEN_ADDRESS" envDefault:"127.0.0.1:8080"`
    BearerToken        string        `env:"KG_MCP_BEARER_TOKEN"`
    RequireAuth        bool          `env:"KG_MCP_REQUIRE_AUTH" envDefault:"true"`
    MaxRequestBytes    int64         `env:"KG_MCP_MAX_REQUEST_BYTES" envDefault:"2097152"`
    RequestTimeout     time.Duration `env:"KG_MCP_REQUEST_TIMEOUT" envDefault:"30s"`
    ShutdownTimeout    time.Duration `env:"KG_MCP_SHUTDOWN_TIMEOUT" envDefault:"10s"`
}
```

**Validation:** `Transport` must be `http` or `stdio`. In HTTP mode, refuse to start if `RequireAuth=true` and no token or verified auth gateway is configured. Never allow `RequireAuth=false` when binding to a non-loopback address without an explicitly documented development-only override. A reverse-proxy deployment must not accidentally bypass authentication.

The existing embedding configuration remains separate, for example:

```dotenv
KG_EMBEDDING_BASE_URL=https://api.openai.com/v1
KG_EMBEDDING_MODEL=text-embedding-3-small
KG_EMBEDDING_DIMENSIONS=1024
KG_EMBEDDING_API_KEY=...  # Load from secrets in production
```

The MCP server should depend on the already-configured embedding/search service, not copy this configuration into every MCP handler.

**Example private deployment:**

```dotenv
KG_MCP_TRANSPORT=http
KG_MCP_LISTEN_ADDRESS=127.0.0.1:8080
KG_MCP_REQUIRE_AUTH=true
KG_MCP_BEARER_TOKEN=<set-a-long-random-secret>
```

Do not commit the real bearer token or OpenAI key. Use the application's existing configuration loader and validation conventions.

## 10. Integration tests and acceptance criteria

Implement real Go tests (using the SDK client and transports) plus PostgreSQL-backed integration tests for nontrivial database operations. At minimum:

### 10.1 Protocol and schema discovery

1. A real MCP client can connect and list exactly the 15 registered tool names.
2. Every registered tool has an English description useful without separate documentation.
3. Every tool declares valid input/output JSON Schema, with required arguments enforced.
4. Bad input schema (`query` missing, blank required strings, wrong types) produces a tool failure or SDK-defined validation failure, never a Go panic.
5. A successful typed tool call produces valid `structuredContent`; the text fallback remains compatible.
6. A domain failure produces `isError=true` with safe, machine-readable error content.
7. Streamable HTTP works at `/mcp`; stdio works too if implemented. The same tool contracts apply.

### 10.2 Search and read behavior

8. `search` returns one hit per document, primary root-to-hit path, IDs and titles only; no embedded block text or scores.
9. `search` with `all_paths=true` respects `max_paths` and reports truncation.
10. Semantic search finds related concepts even without literal shared words when embeddings are available.
11. When the embedding provider fails, search continues lexically with an explicit warning if possible.
12. `get_root` returns the fixed root; `get_parents(root)` returns `[]`.
13. `get_children` returns direct children and current summaries only, ordered/paginated deterministically.
14. `get_fulltext` returns correct block order and stable IDs, supports empty docs and paging.
15. Multi-page `get_fulltext(expected_version=...)` rejects changes between pages instead of mixing versions silently.
16. `get_paths` follows primary parents by default and supports bounded alternatives.
17. `get_sources` returns references only and never fetches them.

### 10.3 Writes, transactions, and graph invariants

18. Create with two parents: first becomes primary; optional blocks and sources created atomically; embedding jobs queued.
19. Create with missing parent, empty summary, duplicate invalid input, or root-forbidden operation is rejected atomically.
20. Metadata update modifies only supplied fields, updates document version once, and invalidates only the document embedding when values actually change.
21. `edit_document` supports all six operations, atomic batch rollback, stable block IDs, sparse ordering, and midpoint insertion.
22. Force rebalancing and assert correct logical order, unique positions, and no unnecessary embedding jobs.
23. `edit_document` returns new block IDs by zero-based operation index; response is compact and contains no full document content.
24. A stale `expected_version` on every write tool yields `version_conflict` without partial changes.
25. Two concurrent edits with same version: one commit, one conflict; no lost update.
26. `add_parent` rejects cycles and root violations; duplicate edge is a no-op unless promotion requested.
27. `remove_parent` rejects deleting the last parent; removing the primary chooses a deterministic replacement.
28. `set_primary_parent` changes exactly one primary flag and does not alter embeddings.
29. `add_source` is idempotent for repeated `(document_id, source_uri)` and does not enqueue embeddings.
30. Every successful mutation preserves single-root, reachability, acyclicity, and exactly-one-primary-parent invariants.

### 10.4 Security and operations

31. Unauthenticated HTTP requests to `/mcp` are rejected for every supported method.
32. Invalid bearer tokens are rejected; no token or API key is logged.
33. Request-size and per-tool limits are enforced.
34. A canceled context is propagated to service/DB calls; graceful shutdown stops accepting requests and closes resources.
35. No handler bypasses the repository/services with SQL or manually updates block `position`.
36. The README includes build/run commands, local/authenticated MCP client configuration, all tools and usage examples, and a troubleshooting section.

**Preferred test strategy:** Use `mcp.NewInMemoryTransports()` for protocol/tool tests and Testcontainers (PostgreSQL + pgvector) or the existing DB integration harness for graph/edit tests. No mocks as the sole proof of transactional semantics.

## 11. Definition of done / delivery instructions

The implementation agent must:

1. Inspect the existing Go module, application service interfaces, migrations, and embedding/search configuration. Preserve the earlier specifications and existing working code.
2. Implement/register **all 15** tools with the **exact public names and English behavior** described here.
3. Expose working Streamable HTTP MCP at `/mcp`, plus liveness and readiness handlers; optionally stdio transport.
4. Implement stable typed input/output structs, good JSON Schemas, strict Go validation, and sanitized agent-readable domain error mapping.
5. Integrate all reads/writes with existing Go business services—**no alternative block editor, graph mutation path, embedding pipeline, or search algorithm**.
6. Implement concurrency-safe expected-version checking also for graph/source writes, if not already present.
7. Add bearer-token authentication and safe defaults; support reverse-proxy deployment without bypassing auth.
8. Add tests covering the acceptance criteria, run formatting/lint/tests, and fix compilation/test failures.
9. Provide `README` examples showing Claude-compatible MCP configuration and sample `tools/call` JSON for search, browse, create, edit, and graph operations.
10. Report any existing repository incompatibilities or missing service functionality explicitly; implement minimal missing service adapters instead of silently reducing the tool contract.

### 11.1 Recommended agent usage pattern (also document in README)

```text
User: "Save what we discussed about Go HTTP server security."

Agent:
  1. search({"query":"Go HTTP server security middleware timeout"})
  2. get_summary({"document_id":"..."})
  3. get_fulltext({"document_id":"..."})
  4. edit_document({"document_id":"...","expected_version":7,
     "operations":[{"type":"insert","after_block_id":"...","content":"..."}]})
  5. add_source({"document_id":"...","expected_version":8,
     "kind":"conversation","source_uri":"https://..."})

If no existing document is a suitable match:
  1. search for a suitable parent category
  2. get_children on candidate parents
  3. create_document with existing parent IDs, title, summary, initial blocks, and source
  4. optionally add_parent to provide another navigation path
```

An agent must **not** call `create_document` for every conversation unconditionally. The graph should remain discoverable, concise, and reusable.

## 12. Reference documentation (official sources)

These references identify the SDK and protocol facilities used by the specification. Check compatibility with the pinned Go SDK release at implementation time:

- Official SDK repository: <https://github.com/modelcontextprotocol/go-sdk>
- Official Go SDK quick start: <https://github.com/modelcontextprotocol/go-sdk/blob/main/docs/quick_start.md>
- Official server/tool documentation (`mcp.AddTool`, typed handlers, structured output): <https://github.com/modelcontextprotocol/go-sdk/blob/main/docs/server.md>
- Official transport and authorization documentation: <https://github.com/modelcontextprotocol/go-sdk/blob/main/docs/protocol.md>
- Go package API reference: <https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp>
- MCP protocol: <https://modelcontextprotocol.io/specification>

**Priority order:** (1) this document for MCP names, validation and transport semantics; (2) the block-editing addendum for edits, sparse positions, rebalancing and embeddings; (3) the database specification for hierarchy, search, persistence and source semantics. In case of SDK-version differences, adapt the Go syntax without changing the published MCP contracts.
