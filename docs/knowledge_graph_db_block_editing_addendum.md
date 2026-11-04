# Addendum to the Database Specification: Block-Based Editing and Sparse Ordering

**Project:** Hierarchical AI knowledge base (Go + PostgreSQL/pgvector)  
**Status:** Binding change specification for Claude  
**Reference:** `knowledge_graph_db_spec_go.md`  
**Scope:** Database, migrations, transactions, Go domain/repository layer and tests only. **No** implementation of MCP, REST or CLI transport.

## 1. Goal and precedence

Documents still consist of independently editable, ordered Markdown content blocks with **stable UUIDs**. An agent should be able to replace individual blocks, insert blocks anywhere in between, move or delete them, **without transferring the entire document content** and **without rewriting all following positions on every insert**.

This addendum **replaces** the previous requirement of the main specification that `content_blocks.position` is an `INTEGER` with a **contiguous sequence `0..N-1`**. From now on:

- `position` is `BIGINT NOT NULL`, internal, **unique per document**, **non-negative** and **not contiguous**.
- New and rebalanced positions have large gaps (sparse ordering).
- Inserts and moves normally change **only the position of the affected block**.
- **Only if no integer slot is free** (or no valid value at the edge of the list is possible) is the affected document renumbered (*rebalancing*).
- The public order results exclusively from `ORDER BY position, id`; the actual numeric values are **not an API contract**.
- The originally planned `content_blocks.version` remains; in addition, `documents.version` remains the optimistic locking token for **every** document edit.

**All other invariants of the main specification remain valid**, in particular: root/DAG/parents, embeddings per block and for title+summary, hybrid search, sources and persistent embedding jobs.

## 2. Target schema for `content_blocks`

For a new installation, the original `CREATE TABLE content_blocks` migration must use this structure directly (not create `INTEGER` first). For existing databases see section 3.

```sql
CREATE TABLE content_blocks (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id          UUID NOT NULL REFERENCES documents(id) ON DELETE RESTRICT,
    position             BIGINT NOT NULL CHECK (position >= 0),
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
```

**Notes:**

- The unique constraint on `(document_id, position)` **must remain `DEFERRABLE INITIALLY DEFERRED`** so that renumbering within a transaction allows temporary collisions and uniqueness is checked at commit.
- Keep the index `content_blocks_document_idx` from the main specification; avoid duplicating existing indexes.
- All list queries sort by `position ASC, id ASC`. The ID is only a deterministic tie-breaker for faulty/temporary states; no duplicate positions may exist at commit.
- Block UUIDs are stable and must **never** be regenerated on a position change or rebalancing.
- Positions are **not** part of the embedding input text or the input hash.

## 3. Migration for existing installations

The existing `position INTEGER` column must be widened to `BIGINT`. Already stored blocks are initially given a gap of `1_000_000` **while preserving their previous order**. Every existing block ID, content and all embeddings are kept.

**Example of a versioned up migration** (in one transaction; the migration system must support transactional DDL):

```sql
BEGIN;

ALTER TABLE content_blocks
    ALTER COLUMN position TYPE BIGINT;

SET CONSTRAINTS content_blocks_document_position_unique DEFERRED;

WITH ordered AS (
    SELECT
        id,
        row_number() OVER (
            PARTITION BY document_id
            ORDER BY position ASC, id ASC
        ) AS ordinal
    FROM content_blocks
)
UPDATE content_blocks AS cb
SET position = ordered.ordinal * 1000000::bigint
FROM ordered
WHERE cb.id = ordered.id;

COMMIT;
```

The example migration assumes that the existing unique constraint is already deferrable (as in the main specification). **Do not** drop or recreate the tables to achieve the conversion. In the real migration, follow the project's migration conventions; do not nest `BEGIN/COMMIT` if the runner already opens a transaction.

The switch to sparse ordering is a **deliberate schema change that is not losslessly backward compatible**. A down migration must not simply cast `BIGINT -> INTEGER` unless the positions were first renumbered safely and contiguously. If a backward migration is not required, mark it explicitly as unsupported instead of delivering a faulty down migration.

## 4. Position model and rebalancing

### 4.1 Constant and invariants

Go constant:

```go
const PositionGap int64 = 1_000_000
```

For a document with three blocks the internal order could look like this:

| Block ID (simplified) | Position | Content |
|---|---:|---|
| A | 1000000 | Introduction |
| B | 2000000 | Main part |
| C | 3000000 | Conclusion |

A new block directly between A and B gets `1_500_000`. The existing blocks A, B, C stay unchanged. The next higher neighbor is therefore **not** shifted by `+1` across the board.

### 4.2 Inserting between two blocks

Determine the **direct neighbors of the requested target position** from the sorted list. Both neighbors must belong to the same document.

- With predecessor `left` and successor `right`, `left < right` holds.
- If `right - left > 1`, set `newPos = left + (right - left) / 2` (integer division). `newPos` is then guaranteed to be strictly between both positions.
- If `right - left <= 1`, first **rebalance the affected document**; then determine the neighbors again and insert.
- Operate exclusively with `int64`; check for overflow, especially for an append operation.

**Important:** Agents and external callers only specify `before_block_id` **or** `after_block_id`, **never a position**. The Go layer computes the position.

### 4.3 At the edges of the list

- **Empty document:** insert the first block with `position = PositionGap`.
- **Append:** `last.position + PositionGap` if there is no `int64` overflow; otherwise rebalance and try again.
- **Prepend:** if `first.position > 1`, `first.position / 2`; otherwise rebalance and then try again. No negative position value.
- **Insert before / after:** determine predecessor/successor from the current, requested state. Operations must also work correctly with exactly one block.
- **Move:** **logically remove the source block from the list** before determining the target neighbors. A move relative to itself is invalid; if the block is already at the requested place, the operation is a no-op.

### 4.4 Rebalancing

If no free position is available, renumber **only the blocks of this document** while keeping the *intended logical order*:

```text
1st block -> 1000000
2nd block -> 2000000
3rd block -> 3000000
...
Nth block -> N * 1000000
```

- Rebalancing happens **within the same transaction and under the same document lock** as the triggering change.
- Without further planned move/insert changes, the current order can be renumbered via SQL:

```sql
WITH ordered AS (
    SELECT
        id,
        row_number() OVER (ORDER BY position ASC, id ASC) AS ordinal
    FROM content_blocks
    WHERE document_id = $1
)
UPDATE content_blocks AS cb
SET position = ordered.ordinal * 1000000::bigint
FROM ordered
WHERE cb.id = ordered.id
  AND cb.position IS DISTINCT FROM ordered.ordinal * 1000000::bigint;
```

- Before rebalancing, ensure that `N * PositionGap` fits into `BIGINT`. Fail in a controlled way if the size is impossible; never accept integer overflow.
- For a **move** for which no free gap exists, the **new requested block order** is authoritative: virtually remove the source block, insert it at the target place and then distribute all positions according to this new order. Do not accidentally rebalance the old order and lose the move.
- Rebalancing may change many DB rows, but must **not change IDs, contents, summaries, embeddings or input hashes**. It triggers **no embedding jobs**.
- Rebalancing as a purely internal maintenance action changes neither `documents.version` nor `updated_at`. For a user-requested `insert`/`move`/`delete` operation, however, the **functional change** increments the document version exactly once per edit batch.
- A small, locally bounded rebalance is optional; for the MVP, **renumbering all blocks of the affected document** is explicitly accepted and easier to verify.

## 5. Atomic block editing in Go

Implement a central method in the transport-neutral Go domain layer, for example:

```go
EditDocument(ctx context.Context, input EditDocumentInput) (EditDocumentResult, error)
```

Input fields:

```go
type EditDocumentInput struct {
    DocumentID      uuid.UUID
    ExpectedVersion int64
    Operations      []BlockOperation // at least one operation
}
```

A `BlockOperation` is a **validated, unambiguously typed union**. Support exactly these basic operations:

| Type | Required fields | Result |
|---|---|---|
| `replace` | `block_id`, `content` | Replace only the content of the given block |
| `insert` | `content`, **exactly one** of `after_block_id` / `before_block_id` | Insert a new block before/after an existing block |
| `append` | `content` | New block at the end |
| `prepend` | `content` | New block at the beginning |
| `delete` | `block_id` | Remove only this block |
| `move` | `block_id`, **exactly one** of `after_block_id` / `before_block_id` | Reorder an existing block |

Binding validation:

1. `document_id` exists; `expected_version` is positive and equals the currently stored document version.
2. All referenced block IDs exist, belong to the **same document** and still exist at the time they are used.
3. For `insert`/`move` **exactly one** target anchor must be set; for `append`/`prepend` none may be set.
4. `replace`/`insert`/`append`/`prepend` only accept non-empty Markdown text (after trimming). **Do not silently trim or reformat** the content when storing it; only check the requirement.
5. For `move`: the target anchor must not be identical to the block ID being moved.
6. Operations are executed **in the given order**. A block deleted earlier in the same batch must not be referenced afterwards.
7. New block IDs are generated **on the server**. The result type contains the **operation index and new UUID** of each insert operation so that the agent can reference them afterwards.
8. If a batch contains several `insert after A`, the result is unambiguous through **sequential execution**: the second insert is placed directly after A and therefore before the first. Anyone who wants a different order uses consecutive batches with the returned UUIDs. No silent reordering of the operations.
9. An empty batch, or one consisting only of changes without effect, is rejected or treated as a no-op without a version increment; document the behavior consistently. Recommendation: reject an empty batch, return no-ops without a version increment.

**Example logical input (not a binding HTTP/MCP syntax):**

```json
{
  "document_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "expected_version": 7,
  "operations": [
    {
      "type": "replace",
      "block_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      "content": "Revised explanation of Go HTTP timeouts."
    },
    {
      "type": "insert",
      "after_block_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      "content": "ReadHeaderTimeout limits reading the request headers."
    },
    {
      "type": "delete",
      "block_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
    }
  ]
}
```

**Response:** `document_id`, `new_version`, `created_blocks` (operation index -> UUID), optionally the current order of the UUIDs. Retransmitting the complete Markdown document is not necessary.

`UpdateDocumentMetadata` from the main specification remains and still changes **only title and summary**. It must neither implicitly replace nor renumber content.

## 6. Transaction and concurrency rules

**All block mutations**, including `replace`, `insert`, `move`, `delete`, `append`, `prepend` and rebalancing, must **lock the same document row**. This prevents contradictory neighbor calculations within a document.

Sequence per `EditDocument`:

1. Start a DB transaction (`pgx.Tx`).
2. Execute `SELECT id, version FROM documents WHERE id = $1 FOR UPDATE`; return `NotFound` or `VersionConflict` if required.
3. Read the ordered block state of the document in the same transaction and validate/execute all operations in order. No other write routine may change block positions without the same document lock.
4. For inserts/moves, perform only the necessary position updates; if no gap is free, rebalance within this transaction. The **deferrable unique constraint** may be violated temporarily until commit, never afterwards.
5. Create/update changed or new embedding jobs **in the same transaction** (see section 7).
6. With at least one functional change, set `documents.version = version + 1`, `documents.updated_at = now()`. **Only once per batch**, not once per operation.
7. Commit; a commit error caused by constraints must be returned as an error, never as success.
8. On any error, roll back completely: no part of the batch may become visible, not even embedding jobs.

A block-level `version` is incremented on **content changes**, not on mere moves and not on internal rebalancing. The **document version** is the authoritative optimistic locking token for the entire batch. If existing single-block write methods remain, they must also follow the document lock/version/job rules; they must not bypass `EditDocument`.

**Important regarding the order of several operations:** The implementation may use an in-memory list of `(id, position)` for the document to determine the requested state before each step. The database, however, remains the source of truth. Operations must be reconciled against the current state within the same transaction; do not use unprotected results read beforehand.

**Root/parent mutations:** The global advisory lock for DAG writes defined in the main specification remains **unchanged**. A mere block edit does **not** need this global DAG lock. If an operation in the future changes parent edges and block content at the same time, define a consistent lock order (global DAG lock first, then document rows) to avoid deadlocks.

## 7. Embedding invalidation and jobs

Only **content** changes affect embeddings:

| Operation | Block embedding | Document embedding for title+summary |
|---|---|---|
| `replace` (actually different content) | Invalidate the affected block's embedding and create a job | unchanged |
| `insert` / `append` / `prepend` | Create jobs for new blocks | unchanged |
| `delete` | Delete the block including its embedding; delete an open job if present | unchanged |
| `move` | unchanged | unchanged |
| Internal rebalancing | unchanged | unchanged |
| `UpdateDocumentMetadata` (title or summary changed) | unchanged | Invalidate the document embedding and create a job |

On an actual `replace`: set `embedding = NULL`, `embedding_model = NULL`, `embedding_input_hash = NULL`, `embedding_updated_at = NULL`; compute a new SHA-256 input hash from **the content** and upsert `embedding_jobs` for `target_kind='block'`, `target_id=<block-id>`. Set the persisted job input hash to the **new** text and reset it to `pending`. The original job/lease/retry semantics of the main specification still apply.

On `delete`: delete the block and remove the corresponding `embedding_jobs` entry (if present) in the same transaction. Since the existing `embedding_jobs` table stores polymorphic target IDs without an FK, this is explicitly necessary. A worker that is already computing must check before storing that its target still exists and its hash is still valid; otherwise it discards the result.

**Neither position changes nor rebalancing may trigger new embedding calls.** `content_blocks.updated_at` stays unchanged on pure rebalancing; on a user-initiated move the document timestamp may be updated, but not the block content timestamp if that denotes the last content change.

## 8. Changes to the read and search methods

- `GetFulltext` and `GetDocument` return blocks **in `ORDER BY position, id`**, including the stable `id`, `content` and `version`. Additionally return the current `documents.version` so that an agent can then safely call `EditDocument(expected_version=...)`.
- Later API/MCP responses may optionally show the internal position values, **but they are never a permitted input for inserting/moving**.
- `RenderDocument` outputs the Markdown content in this order as well.
- For `Search`, vector search stays unchanged: moves/rebalancing change no semantic vectors, no hit assignment and no ranking scores.
- The earlier repository methods `AppendBlocks`, `UpdateBlock`, `RemoveBlock`, `ReorderBlocks` of the main specification should delegate to the **same central editing service** or be replaced by it. **Do not implement a second, deviating position logic.**

## 9. Tests / binding acceptance criteria

Automated integration tests against real PostgreSQL + pgvector, not only mock SQL:

1. **Migration:** Existing `INTEGER` positions `0,1,2` are converted into `BIGINT` positions `1000000,2000000,3000000` while preserving IDs/contents/embeddings.
2. **Insert in the middle:** Positions `1000000,2000000` -> inserting between the blocks yields `1500000`; existing positions stay unchanged.
3. **Multiple inserts:** Repeated inserts into the same gap use midpoints first; no rebalancing as long as an integer value is free.
4. **Rebalancing:** With adjacent positions (e.g. `1000000,1000001`), the document is renumbered and the insert then succeeds. The final order is correct; positions are unique.
5. **Append/prepend:** Empty document, one block, many blocks; correct even when there is no room at the beginning or at the `BIGINT` limit at the end.
6. **Move:** Block from the front to the back, from the back to the middle, directly before/after a neighbor; referencing itself is rejected; a no-op move stays a no-op.
7. **Move plus rebalancing:** With a full gap, the result matches **exactly the requested new order**; no silent re-sorting.
8. **Replace:** Only one block's content and its embedding job change; positions and other block embeddings stay the same.
9. **Delete:** The block and its open embedding job are deleted; other blocks keep their IDs and positions (no automatic contiguity).
10. **Batch atomicity:** An error in operation 3 of 3 completely rolls back changes 1 and 2 as well as embedding jobs.
11. **Version conflict:** Two concurrent edits with the same `expected_version`: only one commits, the other reports `VersionConflict`; no lost updates.
12. **Version counter:** A successful batch with several changes increments `documents.version` **by exactly 1**; rebalancing alone does not; replace operations without a content change create no job.
13. **Embedding stability:** Moves and rebalancing leave embedding values, model identifier and hash unchanged.
14. **Ordering:** `GetFulltext`, `GetDocument` and Markdown rendering return the order produced by move/insert, independent of the numeric size of the gaps.
15. **Document isolation:** Editing one document never changes block positions or job entries of another document.
16. **Concurrent inserts:** Simultaneous inserts into the same gap create neither duplicate positions nor a faulty commit; the document lock/version controls the conflict.

## 10. Deliverables for Claude

1. Switch the SQL schema for new installations to `position BIGINT` and sparse ordering.
2. Write a separately versioned **up migration** for existing databases, including a test.
3. Implement the Go position logic with `int64`, `PositionGap`, overflow checks, midpoint, append/prepend and rebalancing.
4. Add an atomic `EditDocument` domain/repository method with the six operation types, document-wide optimistic locking and correct responses.
5. Consolidate all existing block write routines and check `GetFulltext`/rendering/SQL queries for a stable order.
6. Perform embedding jobs and hash invalidation only for changed texts; clean up jobs on deletion.
7. Add the integration tests above; replace the main specification test "contiguous `0..N-1` positions" with a test for **uniqueness + correct logical order**.

**Priority:** correct transactions, stable block IDs, no lost updates and no unnecessary re-embedding. **No** silent return to contiguous positions or client-side position calculation.
