# Implementation Specification: Hierarchical AI Knowledge Base (Go + PostgreSQL)

**Status:** Binding specification for an MVP  
**Scope:** Persistence, data model, indexing, search, hierarchy and the corresponding Go data access layer only.  
**Out of scope:** ChatGPT export/authentication, LLM prompts, CLI/MCP/HTTP transport, S3 synchronization, Obsidian plugin and frontend.

## 1. Goal and core principle

Build a central knowledge base in which **all knowledge documents are stored completely in PostgreSQL**. Each document describes one topic and has:

- a stable, immutable **UUID**,
- a **title** (required),
- a **short summary** (required),
- **at least one parent** (required, except for the single root document),
- optionally any number of **ordered Markdown content blocks** (including zero),
- optionally references to **source conversations** (external, permanent URI),
- an embedding vector for **title + summary**,
- one additional embedding vector per content block.

A conversation will later be split by an LLM into several independent topics. For each topic, either an existing document is updated or a new document is created under at least one suitable parent. **The database itself does not make the semantic decision about which topics are created or how they are classified.** It provides correct search, read and write operations for that purpose.

The structure is **not a strict tree** but a **directed acyclic graph (DAG)**: a document may have several parents but physically exists only once. Every node is reachable from the root. The root is the only node without a parent.

Example paths:

```text
root -> Minkter -> Reliable Backend Structures -> GORM AutoMigrate
root -> Minkter -> Reliable Backend Structures -> Go HTTP Server Hardening
root -> AI -> Analytics with Go
```

A search for "Backend" should be able to find `Go HTTP Server Hardening` as well, **without the word "Backend" having to appear in the content**. Hence hybrid search: semantic vector search **plus** lexical PostgreSQL full-text search and exact/prefix-based signals.

## 2. Binding design decisions

1. **PostgreSQL is the source of truth.** No Markdown files as primary storage, no second graph database.
2. **PostgreSQL 16+** with the **pgvector** extension. Additionally `pg_trgm` for technical substrings/trigram search.
3. **Go** with `pgx/v5`, `pgxpool` and `github.com/pgvector/pgvector-go/pgx` for vector type registration. Version the SQL migrations; **no GORM AutoMigrate as the migration source**.
4. **The root ID is fixed:** `00000000-0000-0000-0000-000000000001`; title `root`. The seed is created idempotently during initialization.
5. Every non-root node has at least one parent and **exactly one primary parent**. Further parents are additional navigation paths. A primary parent only serves to select the default path; all parents are equivalent in content.
6. **No cycles**, no self-referencing parent relationships, no isolated subgraphs. Structural changes are transactional and serialized.
7. `summary` describes **the document itself**, not all descendants recursively. A parent lists the **current summaries of its direct children** instead of copying them as redundant Markdown text.
8. Markdown is the **content format of the blocks**. Title, summary and child lists are rendered **deterministically from the DB columns**.
9. Embeddings are created by **the backend service** from stored texts; neither a CLI nor an LLM submits vectors. There is one document vector and one vector per block.
10. Search hits are **documents**, not blocks. Several hits in the same document are merged into exactly one document result.
11. The search response returns **only document IDs and titles in root paths**. Internal rankings/scores may be used for ordering but are not returned by default.
12. No mandatory tag taxonomy, no separate topic folders, no redundant copies of parent summaries in the MVP. The hierarchy **replaces** the original tag requirement.

## 3. Data model

### 3.1 `documents`

| Field | Type | Meaning |
|---|---|---|
| `id` | UUID PK | Immutable document ID |
| `title` | TEXT NOT NULL | Human-readable title, not globally unique |
| `summary` | TEXT NOT NULL | Short summary of the content |
| `is_root` | BOOLEAN NOT NULL | Only a single document may be the root |
| `embedding` | VECTOR(1024) NULL | Semantic vector for title + summary |
| `embedding_model` | TEXT NULL | Model identifier of the most recently computed vector |
| `embedding_input_hash` | TEXT NULL | SHA-256 of the exact embedded input text |
| `embedding_updated_at` | TIMESTAMPTZ NULL | Last successful indexing |
| `search_vector` | TSVECTOR GENERATED | PostgreSQL full-text index of title + summary |
| `version` | BIGINT NOT NULL | Optimistic locking; starts at 1 |
| `created_at` / `updated_at` | TIMESTAMPTZ | Creation / last change |

**Embedding input text** (exact, stable formatting):

```text
Title: <title>
Summary: <summary>
```

The path/parent is **not part** of this vector: otherwise every reorganization of the hierarchy would require re-embedding the content. The hierarchy is taken into account separately for paths and context.

### 3.2 `document_parents`

| Field | Type | Meaning |
|---|---|---|
| `child_id` | UUID FK | Child document |
| `parent_id` | UUID FK | Direct parent document |
| `is_primary` | BOOLEAN | Primary parent for the canonical path |
| `created_at` | TIMESTAMPTZ | Time the relationship was created |

- Primary key `(child_id, parent_id)`; the same edge can exist only once.
- At most one relationship per child has `is_primary = true` (partial unique index).
- On creation, **the first given parent ID** is the primary parent; the others are secondary. The Go logic above must ensure that **exactly one** is set for non-root nodes.
- No self edges; no edges **with the root as child**.
- Renaming documents does not affect relationships.

### 3.3 `content_blocks`

A block is a **logically coherent Markdown section**, not necessarily exactly one typographic paragraph. It may contain headings, several paragraphs, code blocks, tables, etc. This prevents tearing apart code or explanations that belong together.

| Field | Type | Meaning |
|---|---|---|
| `id` | UUID PK | Stable block ID |
| `document_id` | UUID FK | Owning document |
| `position` | INT NOT NULL | Zero-based order within the document |
| `content` | TEXT NOT NULL | Original Markdown text |
| `embedding` | VECTOR(1024) NULL | Semantic vector of the block |
| `embedding_model`, `embedding_input_hash`, `embedding_updated_at` | as above | Indexing status |
| `search_vector` | TSVECTOR GENERATED | Lexical search in the content |
| `version` | BIGINT NOT NULL | Optimistic locking of the block |
| `created_at`, `updated_at` | TIMESTAMPTZ | Timestamps |

Rules:

- A document can have **zero to any number** of blocks.
- Block order by `position`, contiguous `0..N-1` after every successfully completed write transaction.
- `(document_id, position)` is unique; position shifts are performed **within one transaction**.
- Only changed blocks receive new embeddings; unchanged blocks keep their vectors.
- The embedding input of a block is its `content` in a normalized, stable UTF-8 form. Otherwise **do not rewrite Markdown content unasked**.
- Very long blocks must be split sensibly according to defined model limits before embedding; the database still stores the original logical blocks. For the MVP, capture should be designed so that individual blocks do not exceed the model limit; otherwise mark indexing errors visibly, do not truncate silently.

> **Note:** The block-editing addendum (`knowledge_graph_db_block_editing_addendum.md`) supersedes the contiguous `INTEGER` positions of this section with sparse `BIGINT` positions.

### 3.4 `document_sources`

Sources are **external references**, not copies of the entire chat history in every topic note.

| Field | Type | Meaning |
|---|---|---|
| `id` | UUID PK | Source reference |
| `document_id` | UUID FK | Document that refers to the source |
| `kind` | TEXT | Initially `conversation`, optionally `other` |
| `source_uri` | TEXT | Permanent link to the original conversation/MD file |
| `title` | TEXT NULL | Optional source title |
| `created_at` | TIMESTAMPTZ | Time of the reference |

A conversation may feed several topics; therefore identical `source_uri` values are allowed for **different documents**. Within one document the same URI appears only once. **Do not persist short-lived S3 presigned URLs**; store a stable object reference/resolvable URL instead.

### 3.5 `embedding_jobs`

Embeddings may be created asynchronously after a DB commit. So that changes are not left unindexed after a process crash, a **persistent job table** is required.

Per document or block there is at most one current job whose `input_hash` identifies the state to be indexed. States: `pending`, `processing`, `failed`. `lease_until` allows resuming crashed jobs; `attempts` and `last_error` serve retries/diagnosis. A worker may store a result **only** if the input hash still matches the current text. Otherwise the newer job must be kept.

## 4. Initial SQL migration (proposal of the binding target structure)

The following migration is a concrete basis. If tests require small technical corrections, **keep the semantics and constraints unchanged**.

```sql
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE documents (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    title                TEXT NOT NULL CHECK (length(btrim(title)) > 0),
    summary              TEXT NOT NULL CHECK (length(btrim(summary)) > 0),
    is_root              BOOLEAN NOT NULL DEFAULT FALSE,
    embedding            VECTOR(1024),
    embedding_model      TEXT,
    embedding_input_hash TEXT,
    embedding_updated_at TIMESTAMPTZ,
    search_vector        TSVECTOR GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', title), 'A') ||
        setweight(to_tsvector('simple', summary), 'B')
    ) STORED,
    version              BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX documents_single_root_idx ON documents ((is_root)) WHERE is_root;
CREATE INDEX documents_search_idx ON documents USING gin (search_vector);
CREATE INDEX documents_title_trgm_idx ON documents USING gin (title gin_trgm_ops);
CREATE INDEX documents_embedding_hnsw_idx
    ON documents USING hnsw (embedding vector_cosine_ops);

CREATE TABLE document_parents (
    child_id   UUID NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
    parent_id  UUID NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
    is_primary BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (child_id, parent_id),
    CHECK (child_id <> parent_id)
);

CREATE UNIQUE INDEX document_parents_one_primary_idx
    ON document_parents (child_id) WHERE is_primary;
CREATE INDEX document_parents_parent_idx ON document_parents (parent_id, child_id);

CREATE TABLE content_blocks (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id          UUID NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
    position             INTEGER NOT NULL CHECK (position >= 0),
    content              TEXT NOT NULL CHECK (length(btrim(content)) > 0),
    embedding            VECTOR(1024),
    embedding_model      TEXT,
    embedding_input_hash TEXT,
    embedding_updated_at TIMESTAMPTZ,
    search_vector        TSVECTOR GENERATED ALWAYS AS (
        to_tsvector('simple', content)
    ) STORED,
    version              BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT content_blocks_document_position_unique
        UNIQUE (document_id, position) DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX content_blocks_document_idx ON content_blocks (document_id, position);
CREATE INDEX content_blocks_search_idx ON content_blocks USING gin (search_vector);
CREATE INDEX content_blocks_embedding_hnsw_idx
    ON content_blocks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE document_sources (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id UUID NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
    kind        TEXT NOT NULL DEFAULT 'conversation'
                CHECK (kind IN ('conversation', 'other')),
    source_uri  TEXT NOT NULL CHECK (length(btrim(source_uri)) > 0),
    title       TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (document_id, source_uri)
);
CREATE INDEX document_sources_document_idx ON document_sources (document_id);

CREATE TABLE embedding_jobs (
    target_kind  TEXT NOT NULL CHECK (target_kind IN ('document', 'block')),
    target_id    UUID NOT NULL,
    input_hash   TEXT NOT NULL,
    state        TEXT NOT NULL DEFAULT 'pending'
                 CHECK (state IN ('pending', 'processing', 'failed')),
    attempts     INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_until  TIMESTAMPTZ,
    last_error   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (target_kind, target_id)
);
CREATE INDEX embedding_jobs_pending_idx
    ON embedding_jobs (state, lease_until, updated_at);

INSERT INTO documents (id, title, summary, is_root)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'root',
    'Top-level entry point of the knowledge base.',
    TRUE
)
ON CONFLICT (id) DO NOTHING;
```

**Important limitation:** SQL FKs and unique indexes alone do **not** guarantee "at least one parent", "root is never a child", "no cycles", "reachable from root" or contiguous block positions. These rules **must be enforced in transactional Go domain operations**. The application's database user must not get general write access outside these operations if such invariants are to be guaranteed against arbitrary SQL writers as well; that would additionally require constraint triggers or exclusively controlled stored procedures. For the MVP: all application writes strictly go through the repository, plus invariant tests.

**Vector dimension:** `1024` is the MVP dimension defined here (as in the previous draft). The embedding model chosen later **must return 1024 values** or be explicitly configurable to this dimension. A different dimension requires a schema/index migration and a complete re-indexing. All stored document, block and query vectors must come from **the same compatible model**. The model has not been chosen yet.

## 5. Hierarchy invariants and transactional write rules

### 5.1 Root

- The root always exists exactly once, with the fixed UUID above.
- The root has **no parent** and must neither be deleted nor changed to `is_root = false`.
- Every other document has at least one parent edge and **exactly one** primary parent.
- All parents and children must exist, including with multiple parents.
- Every node must be reachable from the root via at least one parent path.

### 5.2 Cycle detection

Before `add_parent(child, parent)` ensure:

1. `child != root`, `child != parent`.
2. Both IDs exist.
3. The requested edge does not exist yet (define idempotent or clearly erroneous handling).
4. **`parent` must not be a descendant of `child`.** Determine all descendants of `child` along the child edges with a recursive CTE; if `parent` is among them, reject the edge.
5. Then insert the edge without breaking the graph apart.

For **every** hierarchy mutation (creating a node with parents, adding/removing an edge, changing the primary parent) use the same **transaction-scoped PostgreSQL advisory lock** within the transaction, e.g.:

```sql
SELECT pg_advisory_xact_lock(7654321001::bigint);
```

This prevents two concurrent requests from each checking a harmless edge that **together** create a cycle. For a system of this size, a global serialization of the rare graph write operations is acceptable.

### 5.3 Creating

`CreateDocument(title, summary, parentIDs[], blocks[]?, sources[]?)`:

- `title` and `summary` not empty after trimming.
- `parentIDs` not empty, deduplicate IDs, every ID must exist and be reachable from the root.
- The **first parent ID** becomes `is_primary=true`.
- One DB transaction creates the document, all parent edges, optional blocks/sources and the embedding jobs.
- The new UUID comes from the backend/DB and is returned.
- No creation of a new root document via the normal create method.
- **No automatic creation of intermediate nodes** by the database. If an LLM needs a new parent, it must be created explicitly first.

### 5.4 Changing parent edges

- `AddParent(childID, parentID, makePrimary bool)` checks the DAG and, if `makePrimary=true`, atomically switches the previous primary parent.
- `RemoveParent(childID, parentID)` must **not** remove the last parent. If the primary parent is removed and further parents exist, the operation must atomically choose a new primary parent (deterministically: oldest remaining edge, then ID) or use an explicitly given new primary parent.
- `SetPrimaryParent(childID, parentID)` only works for an existing direct parent edge.
- After every operation, root reachability and exactly one primary parent remain intact.
- **MVP: no general deletion of documents/blocks via cascade**; define a deletion concept explicitly later. Blocks may, however, be removed as part of controlled content editing.

### 5.5 Updating content

- `UpdateDocumentMetadata` changes title and/or summary with `expectedVersion` as an optimistic locking check (`UPDATE ... WHERE id=$id AND version=$expectedVersion`). On conflict, return an explicit concurrency error.
- If title or summary change: mark the document vector as stale (`NULL` or a suitable model/hash mismatch marker), write a **re-indexing job**. The child lists of all parents then automatically show the new summary without storing it in the parents.
- `AppendBlocks`, `UpdateBlock`, `RemoveBlock`, `ReorderBlocks` keep stable block IDs; on a block content change only re-index that block's vector.
- After every content operation, positions `0..N-1` must be contiguous. Reorder and delete use transactions; the deferrable unique constraint allows temporary position overlaps within the same transaction, but not at commit.
- Updating content blocks also increments the document version/`updated_at` so that a consumer detects content changes of the document.
- Merely adding/removing a parent changes the document version but **not** the title+summary embedding, because the parent path is not part of the embedding.

> **Note:** The block-editing addendum replaces `AppendBlocks`, `UpdateBlock`, `RemoveBlock` and `ReorderBlocks` with a single atomic `EditDocument` batch operation.

## 6. Semantic and lexical indexing

### 6.1 Document embeddings

- Create a vector for **`title + summary`** according to the template from section 3.1.
- Use the same embedding model for search queries.
- Store the model identifier, the SHA-256 input hash and a timestamp.
- A title or summary update must recompute exactly this vector.

### 6.2 Block embeddings

- Create a separate vector for **every single `content_block`**.
- A document can therefore rank via several blocks in a search; it still appears only **once** in the final result.
- Use HNSW with cosine distance (`<=>`) on `content_blocks` as well.
- Optionally later: split longer sections/blocks into additional indexing chunks; **not** part of the MVP schema.

### 6.3 Full-text search

- Use `search_vector` as a generated `tsvector` column with the `simple` configuration. Reason: mixed German/English technical texts, identifiers, programming languages and product names; no language-dependent stemmer commitment in the MVP.
- GIN indexes on documents and blocks.
- Query via `websearch_to_tsquery('simple', q)` or, for deliberately simple tokenization, `plainto_tsquery('simple', q)`; always use bound SQL parameters.
- Weight titles higher than summaries; block content as a complement.
- For exact technical hits (e.g. `AutoMigrate`, `pgxpool`, `CVE-2024-23674`) additionally take exact title equality, case-insensitive title substrings and possibly trigram similarity into account as search signals. For very short tokens (e.g. `Go`) trigram search can be weak: lexical search and exact comparison remain required.

### 6.4 Hybrid search and ranking

A query `q` uses **three parallel candidate lists**:

1. **Document vector:** nearest document embeddings to the query embedding.
2. **Block vector:** nearest content block embeddings; merge block hits onto their `document_id` (remember the best or several relevant blocks internally).
3. **Lexical:** full-text and exact/prefix-based hits across `documents` and `content_blocks`; deduplicate on `document_id`.

Then:

- Sort the candidates of each list by relevance.
- Merge with **Reciprocal Rank Fusion (RRF)**: `score(d) = Σ 1 / (60 + rank_i(d))` over the lists in which the document appears; optionally weight exact title hits more strongly.
- **Only one row** per document in the final result.
- For the MVP e.g. `top 50` per list, then globally `limit=10` (configurable, capped on the server).
- Do not set a hard global cosine similarity threshold without evaluation; RRF and top-k first.
- If no embedding exists yet, the document remains findable via lexical search.
- Search must **not be a pure keyword match**: `Backend` must potentially find topics such as "Go HTTP server, middleware, routing".

**Basic SQL form for semantic candidates:**

```sql
-- $1: vector(1024), $2: limit
SELECT id, (embedding <=> $1::vector) AS distance
FROM documents
WHERE embedding IS NOT NULL
ORDER BY embedding <=> $1::vector
LIMIT $2;

SELECT document_id, id AS block_id, (embedding <=> $1::vector) AS distance
FROM content_blocks
WHERE embedding IS NOT NULL
ORDER BY embedding <=> $1::vector
LIMIT $2;
```

### 6.5 Indexing jobs and consistency

- Document/block change and insert/upsert of the matching `embedding_job` **in the same DB transaction**.
- On changes update `input_hash`, `state=pending`, `attempts=0`, `lease_until=NULL`.
- The worker claims pending or expired jobs via `FOR UPDATE SKIP LOCKED` with a lease and releases the DB transaction **before** the external embedding is computed.
- After computing, verify in a new transaction: the current data content/hash and the job hash are still identical; only then write the vector, model/hash/timestamp and remove the job. Otherwise discard the stale result and keep the new job for the current text.
- Retry failed jobs with bounded backoff; after the maximum number of attempts set `failed` and store the error. Implement an administrative requeue option.
- `NULL` embeddings must not prevent the rest of the database from working.
- On a model change: rebuild the entire index in a controlled way and do not mix queries with incompatible models. A change of the vector dimension requires a migration.

## 7. Path resolution and navigation

### 7.1 Default path

For every document compute exactly **one canonical path** via `is_primary=true` from the root to the document.

Example:

```json
[
  {"id":"00000000-0000-0000-0000-000000000001","title":"root"},
  {"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","title":"Minkter"},
  {"id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","title":"Backend Structures"},
  {"id":"cccccccc-cccc-cccc-cccc-cccccccccccc","title":"Go HTTP Server Hardening"}
]
```

Path resolution is based on a **recursive CTE** over `document_parents`, not on a permanently stored path string. Title changes are therefore immediately current everywhere.

Possible basic SQL form for a default path (from the hit up to the root, then reversed):

```sql
WITH RECURSIVE ancestry AS (
  SELECT d.id, d.title, 0 AS depth, ARRAY[d.id] AS visited
  FROM documents d WHERE d.id = $1
  UNION ALL
  SELECT p.id, p.title, a.depth + 1, a.visited || p.id
  FROM ancestry a
  JOIN document_parents dp ON dp.child_id = a.id AND dp.is_primary
  JOIN documents p ON p.id = dp.parent_id
  WHERE NOT p.id = ANY(a.visited)
    AND a.depth < 64
)
SELECT id, title FROM ancestry ORDER BY depth DESC;
```

The backend code must check that the first element is `root`; otherwise the structure is invalid and an integrity error must be reported.

### 7.2 Alternative paths

- Since multiple parents are allowed, a document can have **several root paths**.
- **Default:** exactly one path (primary parent chain).
- **Optional:** `allPaths=true` returns several valid root paths; bound them against combinatorial explosion (`maxPaths`, e.g. 10; maximum depth e.g. 64). If truncated, mark this explicitly.
- Every path component contains **only `id` and `title`**, no summary or full text.
- `get_parents` returns **direct** parents; `get_children` returns **direct** children with **`id`, `title`, `summary`** and optionally a primary marker.

### 7.3 Search result

**Binding functional response format**:

```json
{
  "query": "Backend",
  "results": [
    {
      "id": "cccccccc-cccc-cccc-cccc-cccccccccccc",
      "paths": [
        [
          {"id":"00000000-0000-0000-0000-000000000001","title":"root"},
          {"id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","title":"Minkter"},
          {"id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","title":"Backend Structures"},
          {"id":"cccccccc-cccc-cccc-cccc-cccccccccccc","title":"Go HTTP Server Hardening"}
        ]
      ]
    }
  ]
}
```

The implementation should not return redundant summaries in the search result: for details the client uses the document ID with the specialized read methods.

## 8. Data access layer in Go

Write a typed **repository/service layer**; no binding to a particular HTTP, CLI or MCP transport. The following methods or their functional equivalents are required:

### Reading

- `Search(ctx, query, limit, allPaths)`: hybrid search, deduplicated documents and title/ID paths.
- `GetSummary(ctx, documentID)`: `id`, `title`, `summary`, `version`.
- `GetParents(ctx, documentID)`: direct parents with `id`, `title`, `isPrimary`.
- `GetChildren(ctx, documentID)`: direct children with `id`, `title`, `summary`, `isPrimary`, stably sorted by `lower(title), id`.
- `GetFulltext(ctx, documentID)`: ordered content blocks (`id`, `position`, `content`, `version`), **without** automatically generated child lists.
- `GetSources(ctx, documentID)`: external source references.
- `GetDocument(ctx, documentID)`: aggregated structured view of summary, parents, children, blocks, sources.
- `GetPaths(ctx, documentID, allPaths)`: return the canonical or several root paths.

### Writing

- `CreateDocument(ctx, CreateDocumentInput)`: title, summary, at least one parent, optional blocks/sources.
- `UpdateDocumentMetadata(ctx, id, expectedVersion, title?, summary?)`.
- `AddParent(ctx, childID, parentID, makePrimary)`.
- `RemoveParent(ctx, childID, parentID)`.
- `SetPrimaryParent(ctx, childID, parentID)`.
- `AppendBlocks(ctx, documentID, blocks[])`.
- `UpdateBlock(ctx, blockID, expectedVersion, content)`.
- `RemoveBlock(ctx, blockID)` and `ReorderBlocks(ctx, documentID, orderedBlockIDs[])`.
- `AddSource(ctx, documentID, kind, sourceURI, title?)` (idempotent per document + URI).
- `RetryEmbedding(ctx, targetKind, targetID)` for administrative repair.

**Go behavior requirements:**

- Pass `context.Context` to all DB/embedding operations; respect query timeouts and cancellation.
- Use `pgxpool`; register `pgvector` types via `AfterConnect`.
- Parameterize all SQL queries; no string concatenation for user input.
- Explicit transactions for multi-table operations/graph mutations.
- Typed domain errors: `NotFound`, `InvalidInput`, `CycleDetected`, `RootViolation`, `LastParentViolation`, `VersionConflict`, `EmbeddingUnavailable`, `IntegrityViolation`.
- Idempotent migration/root initialization; detect a broken root structure at startup, do not repair it silently.
- Clearly separate SQL and Go code (`migrations/`, `internal/db/`, `internal/repository/`, `internal/search/` or similar).
- No implicit LLM decisions in SQL or repository methods; CRUD/search are deterministic.

**Illustrative pgx pool configuration:**

```go
config, err := pgxpool.ParseConfig(databaseURL)
if err != nil { return err }
config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
    return pgxvec.RegisterTypes(ctx, conn)
}
pool, err := pgxpool.NewWithConfig(ctx, config)
```

Imports:

```go
import (
    "context"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
    pgxvec "github.com/pgvector/pgvector-go/pgx"
)
```

## 9. Deterministic Markdown rendering

The database does **not** store the same child lists or parent summaries several times. `RenderDocument(documentID)`, and later exports, always generate the following format:

1. **Title** (H1)
2. **Summary** (one paragraph)
3. **Direct subtopics**: list of a link to the child ID/file, title and the **current** child summary (if empty: omit the section or consistently define "No subtopics")
4. **Full text**: `content_blocks` by position, Markdown unchanged
5. **Sources**: external links to original conversations, if present

Example:

```markdown
# Go HTTP Server Hardening

Security measures for HTTP servers in Go.

## Subtopics

- [HTTP Timeouts](document://<child-uuid>) — Protection against slow clients.
- [Request Validation](document://<child-uuid>) — Validation of incoming requests.

## Content

HTTP servers should define safe timeouts ...

Middleware can validate incoming requests ...

## Sources

- [Original conversation](https://example.invalid/permanent/conversation/123)
```

The `document://<uuid>` links are **only symbolic placeholders for later client/export resolution**, not standardized Obsidian links. When exporting to Obsidian, stable file-based Markdown links or Obsidian wikilinks with **ID-stable file names** can be generated. Titles can change, therefore **never derive relationships from titles**.

## 10. Non-goals and explicitly later extensions

- No automatic AI categorization or prompt implementation in this task.
- No CLI, MCP or REST server; only transport-neutral Go methods that can be called later.
- No S3/Obsidian file storage as a data source.
- No separate `tags` tables for the MVP (possible later, but not required).
- No mandatory additional graph database, materialized paths or closure table.
- No automatic merging/refactoring of topics; the AI decides that later and triggers explicit change operations.
- No automatic deletion of complete documents; a safe reparenting/archiving model has to be defined for that later.
- No model change without an explicit re-indexing process.

## 11. Binding test cases / acceptance criteria

Implement automated integration tests against **real PostgreSQL with pgvector** (e.g. Testcontainers or Docker Compose), plus Go unit tests of the pure ranking and validation logic.

1. **Bootstrap:** The root exists exactly once under the fixed UUID, without a parent; repeated migration/initialization does not create a second root.
2. **Creating:** A document without title, summary or parent is rejected; a valid document is created transactionally, is reachable from the root and has a primary parent.
3. **Multiple parents:** A document with two parents remains exactly **one** document; `GetParents` returns both; path resolution finds at least the canonical path and optionally alternative paths.
4. **Cycles:** A self edge and an indirect cycle (`A -> B -> C`, then `C -> A` in parent edge semantics) are prevented, also with two concurrent changes.
5. **Removing a parent:** The last parent must not be removed; removing the primary parent atomically chooses a valid replacement.
6. **Updating a document:** A title/summary update increments the version and triggers a re-indexing job; `GetChildren(parent)` immediately shows the new title/summary without changing the parent's full text.
7. **Blocks:** A document with zero blocks works; several blocks keep their order; reorder/delete lead to contiguous positions; only changed blocks are re-indexed.
8. **Search deduplication:** Hits in three blocks of the same document lead to **exactly one** document in `Search`.
9. **Lexical search:** An exact technical term (e.g. `AutoMigrate`) is findable, even if the vector is still `NULL`.
10. **Semantic search:** With a real suitable embedding model, the query `Backend` returns relevant documents about "Go, HTTP server, middleware, routing", even though the term `Backend` does not occur in the tested title/summary/content. This is a **qualitative integration test** whose success depends on the chosen model; document suitable test data/model choice, do not fake it by hardcoding.
11. **Paths:** Every returned path starts with `root`, ends at the found document and contains only `id` + `title`. Multiple paths are bounded and cycle protection prevents infinite loops.
12. **Rendering:** The output contains title, summary, the current child list with summaries, ordered Markdown blocks and sources; child summaries are not duplicated in the parent's full text.
13. **Embedding jobs:** A job remains retryable after a crash; a slow, stale embedding must not overwrite a newer document/block state.
14. **Concurrency:** Optimistic locking reports version conflicts; concurrent hierarchy mutations cannot create cycles or nodes without a parent.
15. **Roundtrip:** A topic node with several parents and with sources remains completely reconstructible after a DB restart.

## 12. Expected deliverables from Claude

For the **DB part**, Claude should deliver at least:

- complete, executable and versioned **up/down SQL migrations** and an idempotent root seed,
- idiomatic Go domain models and repository/service interfaces,
- concrete **pgx** implementations for all methods in section 8,
- transactional cycle/root/parent/position validation,
- hybrid search with document and block embeddings, full text and RRF,
- SQL-based, bounded root path resolution for primary/multiple-parent cases,
- persistent embedding job logic and an exchangeable embedding provider interface (the provider itself may be a stub, but job states and hash checks must be implemented),
- a deterministic aggregation/Markdown render function,
- database integration tests including concurrency cases,
- short documentation on the local PostgreSQL/pgvector setup and on running migrations and tests.

**Priority:** Correctness of the data invariants before optimization. No silent fallback to a flat topic list or pure keyword search. Claude may resolve unclear non-functional implementation details idiomatically; the functional properties defined above must not be weakened.

---

### Technical references (for the implementation)

- pgvector and HNSW/cosine distance: https://github.com/pgvector/pgvector
- Go bindings for pgx: https://github.com/pgvector/pgvector-go
- PostgreSQL recursive CTEs/cycle detection: https://www.postgresql.org/docs/current/queries-with.html
- PostgreSQL full-text search and GIN: https://www.postgresql.org/docs/current/textsearch-tables.html
