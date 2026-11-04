-- Reverts 0002_sparse_block_positions.up.sql safely: positions are first
-- renumbered contiguously (0..N-1 per document, order preserved) and only
-- then narrowed to INTEGER, which is the representation the pre-addendum code
-- expects. Block IDs, contents and embeddings are kept.

SET CONSTRAINTS content_blocks_document_position_unique DEFERRED;

WITH ordered AS (
    SELECT
        id,
        row_number() OVER (
            PARTITION BY document_id
            ORDER BY position ASC, id ASC
        ) - 1 AS ordinal
    FROM content_blocks
)
UPDATE content_blocks AS cb
SET position = ordered.ordinal
FROM ordered
WHERE cb.id = ordered.id
  AND cb.position IS DISTINCT FROM ordered.ordinal;

-- Run the deferred uniqueness checks now; ALTER TABLE is not allowed while
-- deferred trigger events are pending.
SET CONSTRAINTS content_blocks_document_position_unique IMMEDIATE;

ALTER TABLE content_blocks
    ALTER COLUMN position TYPE INTEGER;
