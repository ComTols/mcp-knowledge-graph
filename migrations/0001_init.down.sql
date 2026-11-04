-- Reverts 0001_init.up.sql. The extensions (vector, pg_trgm) are intentionally
-- kept because other schemas in the same database may depend on them.

DROP TABLE IF EXISTS embedding_jobs;
DROP TABLE IF EXISTS document_sources;
DROP TABLE IF EXISTS content_blocks;
DROP TABLE IF EXISTS document_parents;
DROP TABLE IF EXISTS documents;
