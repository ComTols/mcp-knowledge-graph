-- Sparse ordering of content blocks (docs/knowledge_graph_db_block_editing_addendum.md).
--
-- Upgrades installations created with the original 0001 schema
-- (position INTEGER, contiguous 0..N-1). Fresh installations already create
-- the column as BIGINT; for them this migration is a no-op.
--
-- Existing blocks keep their IDs, contents and embeddings and get positions
-- 1000000, 2000000, ... in their previous order. The migration runner wraps
-- this file in a transaction.

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
WHERE cb.id = ordered.id
  AND cb.position IS DISTINCT FROM ordered.ordinal * 1000000::bigint;
