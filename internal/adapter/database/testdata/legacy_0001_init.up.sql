-- Fixture: schema of migrations/0001_init.up.sql BEFORE the sparse ordering
-- addendum (content_blocks.position INTEGER, contiguous 0..N-1). Used to test
-- the upgrade of existing installations by 0002_sparse_block_positions.

-- Initial schema of the hierarchical knowledge database.
-- See docs/knowledge_graph_db_spec_go.md, section 4.

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
    CHECK (child_id <> parent_id),
    -- Defense in depth: the root (fixed UUID) can never be a child.
    CONSTRAINT document_parents_root_not_child
        CHECK (child_id <> '00000000-0000-0000-0000-000000000001'::uuid)
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
    'Oberster Einstiegspunkt in die Wissensdatenbank.',
    TRUE
)
ON CONFLICT (id) DO NOTHING;
