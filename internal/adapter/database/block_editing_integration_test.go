//go:build integration

package database_test

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/adapter/database"
	"mcp-knowledge-graph/internal/knowledge"
)

// Integration tests for docs/knowledge_graph_db_block_editing_addendum.md,
// section 9. Test names carry the number of the acceptance criterion.

const gap = knowledge.PositionGap

// blockRow is the raw stored state of a block.
type blockRow struct {
	ID        uuid.UUID
	Position  int64
	Content   string
	Version   int64
	UpdatedAt time.Time
	Embedding *string
	Model     *string
	Hash      *string
}

func (db *testDB) blockRows(t *testing.T, documentID uuid.UUID) []blockRow {
	t.Helper()
	rows, err := db.Raw.Query(context.Background(), `
		SELECT id, position, content, version, updated_at,
		       embedding::text, embedding_model, embedding_input_hash
		FROM content_blocks WHERE document_id = $1
		ORDER BY position, id`, documentID)
	require.NoError(t, err)
	defer rows.Close()
	var out []blockRow
	for rows.Next() {
		var r blockRow
		require.NoError(t, rows.Scan(&r.ID, &r.Position, &r.Content, &r.Version, &r.UpdatedAt, &r.Embedding, &r.Model, &r.Hash))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func rowPositions(rows []blockRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.Position
	}
	return out
}

func rowContents(rows []blockRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Content
	}
	return out
}

func rowIDs(rows []blockRow) []uuid.UUID {
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

// createWithBlocks creates a document below root with the given blocks and
// returns it with the block IDs in order.
func (db *testDB) createWithBlocks(t *testing.T, title string, contents ...string) (knowledge.DocumentSummary, []uuid.UUID) {
	t.Helper()
	doc, err := db.Store.CreateDocument(context.Background(), knowledge.CreateDocumentInput{
		Title: title, Summary: title + " summary", ParentIDs: []uuid.UUID{root}, Blocks: contents,
	})
	require.NoError(t, err)
	return doc.DocumentSummary, rowIDs(db.blockRows(t, doc.ID))
}

// edit runs a batch against the current document version.
func (db *testDB) edit(t *testing.T, documentID uuid.UUID, ops ...knowledge.BlockOperation) knowledge.EditDocumentResult {
	t.Helper()
	res, err := db.tryEdit(documentID, ops...)
	require.NoError(t, err)
	return res
}

func (db *testDB) tryEdit(documentID uuid.UUID, ops ...knowledge.BlockOperation) (knowledge.EditDocumentResult, error) {
	ctx := context.Background()
	s, err := db.Store.GetSummary(ctx, documentID)
	if err != nil {
		return knowledge.EditDocumentResult{}, err
	}
	return db.Store.EditDocument(ctx, knowledge.EditDocumentInput{
		DocumentID: documentID, ExpectedVersion: s.Version, Operations: ops,
	})
}

// setPositions overwrites positions directly to provoke edge cases.
func (db *testDB) setPositions(t *testing.T, ids []uuid.UUID, positions []int64) {
	t.Helper()
	_, err := db.Raw.Exec(context.Background(), `
		UPDATE content_blocks b SET position = u.p
		FROM unnest($1::uuid[], $2::bigint[]) AS u(id, p)
		WHERE b.id = u.id`, ids, positions)
	require.NoError(t, err)
}

func (db *testDB) version(t *testing.T, documentID uuid.UUID) int64 {
	t.Helper()
	s, err := db.Store.GetSummary(context.Background(), documentID)
	require.NoError(t, err)
	return s.Version
}

func (db *testDB) blockJobs(t *testing.T, ids ...uuid.UUID) int {
	t.Helper()
	return db.count(t, `SELECT count(*) FROM embedding_jobs WHERE target_kind = 'block' AND target_id = ANY($1::uuid[])`, ids)
}

// 1: upgrade of an existing installation with INTEGER positions.
func TestBlockEditing01MigrationFromIntegerPositions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dbURL, raw := newEmptyDB(t)

	legacy, err := os.ReadFile("testdata/legacy_0001_init.up.sql")
	require.NoError(t, err)
	_, err = raw.Exec(ctx, string(legacy))
	require.NoError(t, err)
	_, err = raw.Exec(ctx, `
		CREATE TABLE schema_migrations (
			version BIGINT PRIMARY KEY, name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now());
		INSERT INTO schema_migrations (version, name) VALUES (1, 'init')`)
	require.NoError(t, err)

	docID, otherID := uuid.New(), uuid.New()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	_, err = raw.Exec(ctx, `INSERT INTO documents (id, title, summary) VALUES ($1, 'Doc', 'd'), ($2, 'Other', 'o')`, docID, otherID)
	require.NoError(t, err)
	_, err = raw.Exec(ctx, `
		INSERT INTO document_parents (child_id, parent_id, is_primary)
		VALUES ($1, '00000000-0000-0000-0000-000000000001', true),
		       ($2, '00000000-0000-0000-0000-000000000001', true)`, docID, otherID)
	require.NoError(t, err)
	_, err = raw.Exec(ctx, `
		INSERT INTO content_blocks (id, document_id, position, content, embedding, embedding_model, embedding_input_hash)
		VALUES ($3, $1, 0, 'zero', array_fill(0.25::real, ARRAY[1024])::vector, 'm', 'h0'),
		       ($4, $1, 1, 'one', NULL, NULL, NULL),
		       ($5, $1, 2, 'two', array_fill(0.5::real, ARRAY[1024])::vector, 'm', 'h2'),
		       ($6, $2, 0, 'other', NULL, NULL, NULL)`,
		docID, otherID, ids[0], ids[1], ids[2], uuid.New())
	require.NoError(t, err)

	readRows := func() []blockRow {
		t.Helper()
		rows, err := raw.Query(ctx, `
			SELECT id, position::bigint, content, version, updated_at,
			       embedding::text, embedding_model, embedding_input_hash
			FROM content_blocks WHERE document_id = $1 ORDER BY position, id`, docID)
		require.NoError(t, err)
		defer rows.Close()
		var out []blockRow
		for rows.Next() {
			var r blockRow
			require.NoError(t, rows.Scan(&r.ID, &r.Position, &r.Content, &r.Version, &r.UpdatedAt, &r.Embedding, &r.Model, &r.Hash))
			out = append(out, r)
		}
		return out
	}
	columnType := func() string {
		var typ string
		require.NoError(t, raw.QueryRow(ctx, `
			SELECT data_type FROM information_schema.columns
			WHERE table_name = 'content_blocks' AND column_name = 'position'`).Scan(&typ))
		return typ
	}
	before := readRows()
	assert.Equal(t, "integer", columnType())

	require.NoError(t, database.Migrate(ctx, dbURL))
	assert.Equal(t, "bigint", columnType())
	// 0003 replaces the former German default root summary.
	var rootSummary string
	var rootVersion int64
	require.NoError(t, raw.QueryRow(ctx,
		`SELECT summary, version FROM documents WHERE id = '00000000-0000-0000-0000-000000000001'`).Scan(&rootSummary, &rootVersion))
	assert.Equal(t, "Top-level entry point of the knowledge base.", rootSummary)
	assert.Equal(t, int64(2), rootVersion)
	after := readRows()
	assert.Equal(t, []int64{1 * gap, 2 * gap, 3 * gap}, rowPositions(after))
	assert.Equal(t, ids, rowIDs(after))
	for i := range before {
		assert.Equal(t, before[i].Content, after[i].Content)
		assert.Equal(t, before[i].Embedding, after[i].Embedding, "embeddings are preserved")
		assert.Equal(t, before[i].Hash, after[i].Hash)
		assert.Equal(t, before[i].Version, after[i].Version)
		assert.Equal(t, before[i].UpdatedAt, after[i].UpdatedAt)
	}

	// The migrated database works with the store.
	pool, err := database.OpenPool(ctx, dbURL)
	require.NoError(t, err)
	defer pool.Close()
	store, err := database.New(pool, database.WithConfig(testConfig(dbURL, nil)))
	require.NoError(t, err)
	res, err := store.EditDocument(ctx, knowledge.EditDocumentInput{
		DocumentID: docID, ExpectedVersion: 1,
		Operations: []knowledge.BlockOperation{knowledge.InsertBlockAfter(ids[0], "half")},
	})
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{ids[0], res.CreatedBlocks[0].BlockID, ids[1], ids[2]}, res.BlockOrder)
	violations, err := store.CheckIntegrity(ctx)
	require.NoError(t, err)
	assert.Empty(t, violations)

	// The down migration renumbers contiguously before narrowing the type
	// (0003 is reverted first and is a no-op).
	pool.Close()
	require.NoError(t, database.MigrateDown(ctx, dbURL, 2))
	assert.Equal(t, "integer", columnType())
	assert.Equal(t, []int64{0, 1, 2, 3}, rowPositions(readRows()))
	require.NoError(t, database.Migrate(ctx, dbURL))
	assert.Equal(t, []int64{1 * gap, 2 * gap, 3 * gap, 4 * gap}, rowPositions(readRows()))
}

func TestBlockEditingInitialPositions(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, _ := db.createWithBlocks(t, "Doc", "a", "b", "c")
	assert.Equal(t, []int64{gap, 2 * gap, 3 * gap}, rowPositions(db.blockRows(t, doc.ID)))
}

// 2: insert in the middle uses the midpoint and leaves neighbours alone.
func TestBlockEditing02InsertMiddle(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B")
	before := db.blockRows(t, doc.ID)

	res := db.edit(t, doc.ID, knowledge.InsertBlockAfter(ids[0], "new"))
	require.Len(t, res.CreatedBlocks, 1)
	assert.Equal(t, 0, res.CreatedBlocks[0].OperationIndex)
	newID := res.CreatedBlocks[0].BlockID

	after := db.blockRows(t, doc.ID)
	assert.Equal(t, []uuid.UUID{ids[0], newID, ids[1]}, rowIDs(after))
	assert.Equal(t, []int64{1_000_000, 1_500_000, 2_000_000}, rowPositions(after))
	assert.Equal(t, before[0], after[0], "A is untouched")
	assert.Equal(t, before[1], after[2], "B is untouched")
	assert.Equal(t, []uuid.UUID{ids[0], newID, ids[1]}, res.BlockOrder)
}

// 3: repeated inserts into the same gap use midpoints until none is left.
func TestBlockEditing03RepeatedInsertsUseMidpoints(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B")

	known := map[uuid.UUID]int64{}
	for i := range 20 {
		db.edit(t, doc.ID, knowledge.InsertBlockBefore(ids[1], "x"))
		rows := db.blockRows(t, doc.ID)
		assert.Equal(t, int64(gap), rows[0].Position, "A unchanged after insert %d", i)
		assert.Equal(t, int64(2*gap), rows[len(rows)-1].Position, "B unchanged after insert %d", i)
		for _, r := range rows {
			if p, ok := known[r.ID]; ok {
				assert.Equal(t, p, r.Position, "no block moves while a midpoint is free")
			}
			known[r.ID] = r.Position
		}
	}

	// The gap is exhausted: the next insert rebalances.
	db.edit(t, doc.ID, knowledge.InsertBlockBefore(ids[1], "y"))
	rows := db.blockRows(t, doc.ID)
	require.Len(t, rows, 23)
	for i, r := range rows {
		assert.Equal(t, int64(i+1)*gap, r.Position)
	}
	assert.Equal(t, "y", rows[len(rows)-2].Content, "the new block is directly before B")
}

// 4: rebalancing when neighbours are adjacent.
func TestBlockEditing04Rebalancing(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B")
	db.setPositions(t, ids, []int64{1_000_000, 1_000_001})

	res := db.edit(t, doc.ID, knowledge.InsertBlockAfter(ids[0], "new"))
	rows := db.blockRows(t, doc.ID)
	assert.Equal(t, []string{"A", "new", "B"}, rowContents(rows))
	assert.Equal(t, []uuid.UUID{ids[0], res.CreatedBlocks[0].BlockID, ids[1]}, rowIDs(rows))
	assert.Equal(t, []int64{gap, 2 * gap, 3 * gap}, rowPositions(rows))
}

// 5: append and prepend at the edges.
func TestBlockEditing05AppendPrepend(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)

	t.Run("empty document", func(t *testing.T) {
		doc, _ := db.createWithBlocks(t, "Empty")
		db.edit(t, doc.ID, knowledge.AppendBlock("a"))
		assert.Equal(t, []int64{gap}, rowPositions(db.blockRows(t, doc.ID)))

		doc2, _ := db.createWithBlocks(t, "Empty2")
		db.edit(t, doc2.ID, knowledge.PrependBlock("p"))
		assert.Equal(t, []int64{gap}, rowPositions(db.blockRows(t, doc2.ID)))
	})
	t.Run("one block", func(t *testing.T) {
		doc, _ := db.createWithBlocks(t, "One", "A")
		db.edit(t, doc.ID, knowledge.PrependBlock("0"), knowledge.AppendBlock("Z"))
		rows := db.blockRows(t, doc.ID)
		assert.Equal(t, []string{"0", "A", "Z"}, rowContents(rows))
		assert.Equal(t, []int64{gap / 2, gap, 2 * gap}, rowPositions(rows))
	})
	t.Run("many blocks", func(t *testing.T) {
		doc, _ := db.createWithBlocks(t, "Many", "A", "B", "C", "D", "E")
		db.edit(t, doc.ID, knowledge.AppendBlock("Z"), knowledge.PrependBlock("0"))
		rows := db.blockRows(t, doc.ID)
		assert.Equal(t, []string{"0", "A", "B", "C", "D", "E", "Z"}, rowContents(rows))
		assert.Equal(t, int64(6*gap), rows[6].Position)
		assert.Equal(t, int64(gap/2), rows[0].Position)
	})
	t.Run("no room at the start", func(t *testing.T) {
		doc, ids := db.createWithBlocks(t, "Start", "A", "B")
		db.setPositions(t, ids, []int64{1, 2})
		db.edit(t, doc.ID, knowledge.PrependBlock("0"))
		rows := db.blockRows(t, doc.ID)
		assert.Equal(t, []string{"0", "A", "B"}, rowContents(rows))
		assert.Equal(t, []int64{gap, 2 * gap, 3 * gap}, rowPositions(rows))
	})
	t.Run("bigint limit at the end", func(t *testing.T) {
		doc, ids := db.createWithBlocks(t, "End", "A", "B")
		db.setPositions(t, ids, []int64{gap, math.MaxInt64 - 5})
		db.edit(t, doc.ID, knowledge.AppendBlock("Z"))
		rows := db.blockRows(t, doc.ID)
		assert.Equal(t, []string{"A", "B", "Z"}, rowContents(rows))
		assert.Equal(t, []int64{gap, 2 * gap, 3 * gap}, rowPositions(rows))
	})
}

// 6: moves, invalid self moves and no-op moves.
func TestBlockEditing06Move(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B", "C", "D")
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]

	steps := []struct {
		op   knowledge.BlockOperation
		want []string
	}{
		{knowledge.MoveBlockAfter(a, d), []string{"B", "C", "D", "A"}},  // front to back
		{knowledge.MoveBlockBefore(a, c), []string{"B", "A", "C", "D"}}, // back to middle
		{knowledge.MoveBlockAfter(b, a), []string{"A", "B", "C", "D"}},  // directly after neighbour
		{knowledge.MoveBlockBefore(d, c), []string{"A", "B", "D", "C"}}, // directly before neighbour
	}
	for _, st := range steps {
		before := db.blockRows(t, doc.ID)
		res := db.edit(t, doc.ID, st.op)
		after := db.blockRows(t, doc.ID)
		assert.Equal(t, st.want, rowContents(after))
		assert.True(t, res.Changed)
		changed := 0
		for _, r := range after {
			for _, o := range before {
				if o.ID == r.ID && o.Position != r.Position {
					changed++
					assert.Equal(t, *st.op.BlockID, r.ID, "only the moved block changes position")
				}
			}
		}
		assert.Equal(t, 1, changed)
	}

	_, err := db.tryEdit(doc.ID, knowledge.MoveBlockAfter(a, a))
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput)

	v := db.version(t, doc.ID)
	before := db.blockRows(t, doc.ID)
	res, err := db.Store.EditDocument(ctx, knowledge.EditDocumentInput{
		DocumentID: doc.ID, ExpectedVersion: v,
		Operations: []knowledge.BlockOperation{knowledge.MoveBlockAfter(b, a), knowledge.MoveBlockBefore(a, b)},
	})
	require.NoError(t, err)
	assert.False(t, res.Changed, "moving blocks to where they are is a no-op")
	assert.Equal(t, v, res.NewVersion)
	assert.Equal(t, v, db.version(t, doc.ID))
	assert.Equal(t, before, db.blockRows(t, doc.ID))
}

// 7: a move that needs rebalancing keeps the requested order.
func TestBlockEditing07MoveWithRebalancing(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B", "C", "D")
	db.setPositions(t, ids, []int64{gap, gap + 1, gap + 2, gap + 3})

	db.edit(t, doc.ID, knowledge.MoveBlockBefore(ids[3], ids[1]))
	rows := db.blockRows(t, doc.ID)
	assert.Equal(t, []string{"A", "D", "B", "C"}, rowContents(rows))
	assert.Equal(t, []uuid.UUID{ids[0], ids[3], ids[1], ids[2]}, rowIDs(rows))
	assert.Equal(t, []int64{gap, 2 * gap, 3 * gap, 4 * gap}, rowPositions(rows))
}

// 8: replace changes one block and its job only.
func TestBlockEditing08Replace(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B", "C")
	db.indexAll(t)
	before := db.blockRows(t, doc.ID)

	db.edit(t, doc.ID, knowledge.ReplaceBlock(ids[1], "B v2"))
	after := db.blockRows(t, doc.ID)

	assert.Equal(t, rowPositions(before), rowPositions(after))
	assert.Equal(t, before[0], after[0])
	assert.Equal(t, before[2], after[2])
	assert.Equal(t, "B v2", after[1].Content)
	assert.Equal(t, before[1].Version+1, after[1].Version)
	assert.Nil(t, after[1].Embedding, "the replaced block's vector is invalidated")
	assert.Nil(t, after[1].Hash)
	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM embedding_jobs`))
	job, err := db.Store.GetEmbeddingJob(t.Context(), knowledge.TargetBlock, ids[1])
	require.NoError(t, err)
	assert.Equal(t, knowledge.InputHash(knowledge.BlockEmbeddingInput("B v2")), job.InputHash)
	assert.Equal(t, database.JobPending, job.State)

	// The document vector (title + summary) is not affected.
	st, err := db.Store.GetEmbeddingStatus(t.Context(), knowledge.TargetDocument, doc.ID)
	require.NoError(t, err)
	assert.True(t, st.HasEmbedding)
}

// 9: delete removes the block and its open job; nothing is renumbered.
func TestBlockEditing09Delete(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B", "C")
	require.Equal(t, 3, db.blockJobs(t, ids...), "open jobs exist for all blocks")

	db.edit(t, doc.ID, knowledge.DeleteBlock(ids[1]))
	rows := db.blockRows(t, doc.ID)
	assert.Equal(t, []uuid.UUID{ids[0], ids[2]}, rowIDs(rows))
	assert.Equal(t, []int64{gap, 3 * gap}, rowPositions(rows), "no automatic renumbering")
	assert.Equal(t, 0, db.blockJobs(t, ids[1]))
	assert.Equal(t, 2, db.blockJobs(t, ids[0], ids[2]))

	_, err := db.tryEdit(doc.ID, knowledge.DeleteBlock(ids[1]))
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
}

// 10: an error in the last operation rolls back the whole batch.
func TestBlockEditing10BatchAtomicity(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B")
	db.indexAll(t)
	before := db.blockRows(t, doc.ID)
	v := db.version(t, doc.ID)

	_, err := db.Store.EditDocument(t.Context(), knowledge.EditDocumentInput{
		DocumentID: doc.ID, ExpectedVersion: v,
		Operations: []knowledge.BlockOperation{
			knowledge.ReplaceBlock(ids[0], "changed"),
			knowledge.InsertBlockAfter(ids[0], "inserted"),
			knowledge.DeleteBlock(uuid.New()),
		},
	})
	require.ErrorIs(t, err, knowledge.ErrNotFound)
	assert.Contains(t, err.Error(), "operation 2")

	assert.Equal(t, before, db.blockRows(t, doc.ID))
	assert.Equal(t, v, db.version(t, doc.ID))
	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM embedding_jobs`), "no job leaks out of the rolled back batch")

	// Blocks of another document cannot be referenced either.
	other, otherIDs := db.createWithBlocks(t, "Other", "X")
	_, err = db.tryEdit(doc.ID, knowledge.InsertBlockAfter(otherIDs[0], "x"))
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	_, err = db.tryEdit(doc.ID, knowledge.ReplaceBlock(otherIDs[0], "x"))
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	assert.Equal(t, []string{"X"}, rowContents(db.blockRows(t, other.ID)))

	// Validation errors and unknown documents.
	_, err = db.Store.EditDocument(t.Context(), knowledge.EditDocumentInput{DocumentID: doc.ID, ExpectedVersion: v})
	assert.ErrorIs(t, err, knowledge.ErrInvalidInput, "empty batches are rejected")
	_, err = db.Store.EditDocument(t.Context(), knowledge.EditDocumentInput{
		DocumentID: uuid.New(), ExpectedVersion: 1, Operations: []knowledge.BlockOperation{knowledge.AppendBlock("x")},
	})
	assert.ErrorIs(t, err, knowledge.ErrNotFound)
	_, err = db.Store.EditDocument(t.Context(), knowledge.EditDocumentInput{
		DocumentID: doc.ID, ExpectedVersion: v + 1, Operations: []knowledge.BlockOperation{knowledge.AppendBlock("x")},
	})
	assert.ErrorIs(t, err, knowledge.ErrVersionConflict)
}

// 11: concurrent edits with the same expected version.
func TestBlockEditing11ConcurrentVersionConflict(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()

	for round := range 10 {
		doc, ids := db.createWithBlocks(t, "Doc", "A")
		contents := []string{"writer one", "writer two"}
		results := make([]error, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, results[i] = db.Store.EditDocument(ctx, knowledge.EditDocumentInput{
					DocumentID: doc.ID, ExpectedVersion: doc.Version,
					Operations: []knowledge.BlockOperation{knowledge.ReplaceBlock(ids[0], contents[i])},
				})
			}()
		}
		close(start)
		wg.Wait()

		winner := -1
		for i, err := range results {
			if err == nil {
				winner = i
			} else {
				assert.ErrorIs(t, err, knowledge.ErrVersionConflict)
			}
		}
		require.NotEqual(t, -1, winner, "round %d", round)
		assert.ErrorIs(t, results[1-winner], knowledge.ErrVersionConflict, "exactly one wins")
		assert.Equal(t, contents[winner], db.blockRows(t, doc.ID)[0].Content, "no lost update")
		assert.Equal(t, doc.Version+1, db.version(t, doc.ID))
	}
}

// 12: the version increases exactly once per effective batch.
func TestBlockEditing12VersionCounter(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B", "C")
	db.indexAll(t)
	v := db.version(t, doc.ID)

	res := db.edit(t, doc.ID,
		knowledge.ReplaceBlock(ids[0], "A2"),
		knowledge.InsertBlockAfter(ids[0], "new"),
		knowledge.MoveBlockBefore(ids[2], ids[1]),
		knowledge.DeleteBlock(ids[1]),
		knowledge.AppendBlock("end"),
	)
	assert.True(t, res.Changed)
	assert.Equal(t, v+1, res.NewVersion)
	assert.Equal(t, v+1, db.version(t, doc.ID))
	assert.Equal(t, []string{"A2", "new", "C", "end"}, rowContents(db.blockRows(t, doc.ID)))
	assert.Equal(t, []int{1, 4}, []int{res.CreatedBlocks[0].OperationIndex, res.CreatedBlocks[1].OperationIndex})

	// A batch that needs rebalancing still counts once.
	db.indexAll(t)
	rows := db.blockRows(t, doc.ID)
	db.setPositions(t, rowIDs(rows), []int64{10, 11, 12, 13})
	res = db.edit(t, doc.ID, knowledge.InsertBlockAfter(rows[0].ID, "x"), knowledge.PrependBlock("y"))
	assert.Equal(t, v+2, res.NewVersion)

	// No-op batches: identical content and no-op moves.
	db.indexAll(t)
	rows = db.blockRows(t, doc.ID)
	res = db.edit(t, doc.ID,
		knowledge.ReplaceBlock(rows[1].ID, rows[1].Content),
		knowledge.MoveBlockAfter(rows[2].ID, rows[1].ID),
	)
	assert.False(t, res.Changed)
	assert.Equal(t, v+2, res.NewVersion)
	assert.Equal(t, rows, db.blockRows(t, doc.ID))
	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM embedding_jobs`), "unchanged replace creates no job")

	// A replace that only changes line endings stores the text but keeps
	// the vector, because the normalized embedding input is identical.
	db.edit(t, doc.ID, knowledge.ReplaceBlock(rows[1].ID, "line 1\nline 2"))
	db.indexAll(t)
	res = db.edit(t, doc.ID, knowledge.ReplaceBlock(rows[1].ID, "line 1\r\nline 2"))
	assert.True(t, res.Changed)
	after := db.blockRows(t, doc.ID)
	assert.Equal(t, "line 1\r\nline 2", after[1].Content, "markdown is stored unchanged")
	assert.NotNil(t, after[1].Embedding)
	assert.Equal(t, 0, db.count(t, `SELECT count(*) FROM embedding_jobs`))
}

// 13: moves and rebalancing never touch embeddings.
func TestBlockEditing13EmbeddingStability(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B", "C", "D")
	db.indexAll(t)
	db.setPositions(t, ids, []int64{gap, gap + 1, gap + 2, gap + 3}) // force rebalancing
	before := map[uuid.UUID]blockRow{}
	for _, r := range db.blockRows(t, doc.ID) {
		before[r.ID] = r
	}

	// Moving B between C and D finds no free position -> rebalance.
	res := db.edit(t, doc.ID,
		knowledge.MoveBlockAfter(ids[1], ids[2]),
		knowledge.InsertBlockAfter(ids[2], "new"),
	)
	newID := res.CreatedBlocks[0].BlockID
	rows := db.blockRows(t, doc.ID)
	assert.Equal(t, []string{"A", "C", "new", "B", "D"}, rowContents(rows))
	assert.Equal(t, []int64{gap, 2 * gap, 2*gap + gap/2, 3 * gap, 4 * gap}, rowPositions(rows))

	for _, r := range rows {
		if r.ID == newID {
			continue
		}
		o := before[r.ID]
		require.NotNil(t, r.Embedding)
		assert.Equal(t, *o.Embedding, *r.Embedding)
		assert.Equal(t, o.Model, r.Model)
		assert.Equal(t, o.Hash, r.Hash)
		assert.Equal(t, o.Version, r.Version, "moves do not change the block version")
		assert.Equal(t, o.UpdatedAt, r.UpdatedAt, "moves do not change the content timestamp")
	}
	assert.Equal(t, 1, db.count(t, `SELECT count(*) FROM embedding_jobs`), "only the new block needs an embedding")
	assert.Equal(t, 1, db.blockJobs(t, newID))
}

// 14: all read paths return the edited order regardless of gap sizes.
func TestBlockEditing14Ordering(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := t.Context()
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B", "C")
	db.setPositions(t, ids, []int64{5, 999_999_999_999, math.MaxInt64 - 1})

	res := db.edit(t, doc.ID,
		knowledge.MoveBlockBefore(ids[2], ids[0]),
		knowledge.InsertBlockAfter(ids[0], "new"),
	)
	want := []string{"C", "A", "new", "B"}

	fulltext, err := db.Store.GetFulltext(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, res.NewVersion, fulltext.Version, "GetFulltext returns the version for the next edit")
	assert.Equal(t, want, blockContents(fulltext.Blocks))
	assert.Equal(t, res.BlockOrder, []uuid.UUID{fulltext.Blocks[0].ID, fulltext.Blocks[1].ID, fulltext.Blocks[2].ID, fulltext.Blocks[3].ID})

	full, err := db.Store.GetDocument(ctx, doc.ID)
	require.NoError(t, err)
	assert.Equal(t, want, blockContents(full.Blocks))
	assert.Equal(t, res.NewVersion, full.Version)

	md, err := db.Store.RenderDocument(ctx, doc.ID)
	require.NoError(t, err)
	assert.Contains(t, md, "## Content\n\nC\n\nA\n\nnew\n\nB\n")
}

// 15: edits never touch other documents.
func TestBlockEditing15DocumentIsolation(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B", "C")
	other, otherIDs := db.createWithBlocks(t, "Other", "X", "Y")
	db.setPositions(t, ids, []int64{gap, gap + 1, gap + 2})
	otherBefore := db.blockRows(t, other.ID)
	otherJobs := db.blockJobs(t, otherIDs...)
	otherVersion := db.version(t, other.ID)

	db.edit(t, doc.ID,
		knowledge.InsertBlockAfter(ids[0], "new"), // rebalances doc
		knowledge.DeleteBlock(ids[1]),
		knowledge.ReplaceBlock(ids[2], "C2"),
	)
	assert.Equal(t, otherBefore, db.blockRows(t, other.ID))
	assert.Equal(t, otherJobs, db.blockJobs(t, otherIDs...))
	assert.Equal(t, otherVersion, db.version(t, other.ID))
}

// 16: concurrent inserts into the same gap.
func TestBlockEditing16ConcurrentInserts(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	doc, ids := db.createWithBlocks(t, "Doc", "A", "B")

	const writers = 8
	var wg sync.WaitGroup
	conflicts := make([]int, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, err := db.tryEdit(doc.ID, knowledge.InsertBlockAfter(ids[0], "w"))
				if err == nil {
					return
				}
				if !errors.Is(err, knowledge.ErrVersionConflict) {
					t.Errorf("writer %d: unexpected error: %v", i, err)
					return
				}
				conflicts[i]++
			}
		}()
	}
	wg.Wait()

	rows := db.blockRows(t, doc.ID)
	require.Len(t, rows, writers+2)
	assert.Equal(t, ids[0], rows[0].ID)
	assert.Equal(t, ids[1], rows[len(rows)-1].ID)
	seen := map[int64]bool{}
	for _, r := range rows {
		assert.False(t, seen[r.Position], "duplicate position %d", r.Position)
		seen[r.Position] = true
	}
	assert.Equal(t, int64(1+writers), db.version(t, doc.ID), "every successful insert bumped the version once")
	assert.Equal(t, strings.Repeat("w", writers), strings.Join(rowContents(rows[1:len(rows)-1]), ""))
}
