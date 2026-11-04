package knowledge_test

import (
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/knowledge"
)

// blocks builds a stored state with the given positions; contents are the
// letters A, B, C, ...
func blocks(positions ...int64) []knowledge.BlockState {
	out := make([]knowledge.BlockState, len(positions))
	for i, p := range positions {
		out[i] = knowledge.BlockState{ID: uuid.New(), Position: p, Content: string(rune('A' + i))}
	}
	return out
}

// seqIDs returns a deterministic ID generator.
func seqIDs() func() uuid.UUID {
	n := 0
	return func() uuid.UUID {
		n++
		return uuid.MustParse("00000000-0000-0000-0000-" + leftPad(n))
	}
}

func leftPad(n int) string {
	s := ""
	for i := 0; i < 12; i++ {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func plan(t *testing.T, cur []knowledge.BlockState, ops ...knowledge.BlockOperation) knowledge.BlockEditPlan {
	t.Helper()
	p, err := knowledge.PlanBlockEdit(cur, ops, seqIDs())
	require.NoError(t, err)
	return p
}

func contents(p knowledge.BlockEditPlan) []string {
	out := make([]string, len(p.Final))
	for i, b := range p.Final {
		out[i] = b.Content
	}
	return out
}

func positions(p knowledge.BlockEditPlan) []int64 {
	out := make([]int64, len(p.Final))
	for i, b := range p.Final {
		out[i] = b.Position
	}
	return out
}

func TestPlanInsertBetweenUsesMidpoint(t *testing.T) {
	cur := blocks(1_000_000, 2_000_000)
	p := plan(t, cur, knowledge.InsertBlockAfter(cur[0].ID, "new"))

	assert.Equal(t, []string{"A", "new", "B"}, contents(p))
	assert.Equal(t, []int64{1_000_000, 1_500_000, 2_000_000}, positions(p))
	assert.Empty(t, p.Repositioned, "existing blocks keep their positions")
	assert.False(t, p.Rebalanced)
	require.Len(t, p.Inserted, 1)
	assert.Equal(t, 0, p.Inserted[0].OperationIndex)
}

func TestPlanRepeatedInsertsUseMidpointsBeforeRebalancing(t *testing.T) {
	cur := blocks(1_000_000, 2_000_000)
	// Inserting before B repeatedly halves the remaining gap.
	ops := make([]knowledge.BlockOperation, 20)
	for i := range ops {
		ops[i] = knowledge.InsertBlockBefore(cur[1].ID, "x")
	}
	p := plan(t, cur, ops...)
	assert.False(t, p.Rebalanced, "the gap 1e6 can be halved (rounding up) 20 times")
	assert.Empty(t, p.Repositioned)

	ops = append(ops, knowledge.InsertBlockBefore(cur[1].ID, "x"))
	p = plan(t, cur, ops...)
	assert.True(t, p.Rebalanced, "the 21st insert finds no free integer")
	assert.Equal(t, []int64{1_000_000, 2_000_000}, positions(p)[:2])
}

func TestPlanRebalanceWhenNoGap(t *testing.T) {
	cur := blocks(1_000_000, 1_000_001)
	p := plan(t, cur, knowledge.InsertBlockAfter(cur[0].ID, "new"))

	assert.True(t, p.Rebalanced)
	assert.Equal(t, []string{"A", "new", "B"}, contents(p))
	assert.Equal(t, []int64{1_000_000, 2_000_000, 3_000_000}, positions(p))
	require.Len(t, p.Repositioned, 1, "only B actually moved")
	assert.Equal(t, cur[1].ID, p.Repositioned[0].ID)
}

func TestPlanAppendPrepend(t *testing.T) {
	t.Run("empty document", func(t *testing.T) {
		p := plan(t, nil, knowledge.AppendBlock("a"))
		assert.Equal(t, []int64{knowledge.PositionGap}, positions(p))
		p = plan(t, nil, knowledge.PrependBlock("a"))
		assert.Equal(t, []int64{knowledge.PositionGap}, positions(p))
	})
	t.Run("one block", func(t *testing.T) {
		cur := blocks(1_000_000)
		p := plan(t, cur, knowledge.AppendBlock("z"), knowledge.PrependBlock("0"))
		assert.Equal(t, []string{"0", "A", "z"}, contents(p))
		assert.Equal(t, []int64{500_000, 1_000_000, 2_000_000}, positions(p))
	})
	t.Run("no room at the start", func(t *testing.T) {
		cur := blocks(1, 2)
		p := plan(t, cur, knowledge.PrependBlock("0"))
		assert.True(t, p.Rebalanced)
		assert.Equal(t, []string{"0", "A", "B"}, contents(p))
		assert.Equal(t, []int64{1_000_000, 2_000_000, 3_000_000}, positions(p))
	})
	t.Run("int64 limit at the end", func(t *testing.T) {
		cur := blocks(1_000_000, math.MaxInt64-10)
		p := plan(t, cur, knowledge.AppendBlock("z"))
		assert.True(t, p.Rebalanced)
		assert.Equal(t, []string{"A", "B", "z"}, contents(p))
		assert.Equal(t, []int64{1_000_000, 2_000_000, 3_000_000}, positions(p))
	})
}

func TestPlanMove(t *testing.T) {
	cur := blocks(1_000_000, 2_000_000, 3_000_000, 4_000_000) // A B C D
	a, b, c, d := cur[0].ID, cur[1].ID, cur[2].ID, cur[3].ID

	tests := []struct {
		name string
		op   knowledge.BlockOperation
		want []string
	}{
		{"front to back", knowledge.MoveBlockAfter(a, d), []string{"B", "C", "D", "A"}},
		{"back to middle", knowledge.MoveBlockBefore(d, b), []string{"A", "D", "B", "C"}},
		{"directly after neighbour", knowledge.MoveBlockAfter(b, c), []string{"A", "C", "B", "D"}},
		{"directly before neighbour", knowledge.MoveBlockBefore(c, b), []string{"A", "C", "B", "D"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := plan(t, cur, tc.op)
			assert.Equal(t, tc.want, contents(p))
			require.Len(t, p.Repositioned, 1, "only the moved block changes")
			assert.Equal(t, *tc.op.BlockID, p.Repositioned[0].ID)
			assert.Empty(t, p.Inserted)
			assert.Empty(t, p.ContentChanged)
		})
	}

	t.Run("no-op move", func(t *testing.T) {
		for _, op := range []knowledge.BlockOperation{
			knowledge.MoveBlockAfter(b, a),
			knowledge.MoveBlockBefore(b, c),
			knowledge.MoveBlockBefore(a, b),
		} {
			p := plan(t, cur, op)
			assert.False(t, p.Changed())
			assert.Equal(t, []string{"A", "B", "C", "D"}, contents(p))
		}
	})

	t.Run("move relative to itself", func(t *testing.T) {
		_, err := knowledge.PlanBlockEdit(cur, []knowledge.BlockOperation{knowledge.MoveBlockAfter(a, a)}, seqIDs())
		assert.ErrorIs(t, err, knowledge.ErrInvalidInput)
	})
}

func TestPlanMoveWithRebalanceKeepsRequestedOrder(t *testing.T) {
	cur := blocks(1_000_000, 1_000_001, 1_000_002) // A B C, no gaps
	p := plan(t, cur, knowledge.MoveBlockBefore(cur[2].ID, cur[1].ID))

	assert.True(t, p.Rebalanced)
	assert.Equal(t, []string{"A", "C", "B"}, contents(p), "the move is not lost by rebalancing")
	assert.Equal(t, []int64{1_000_000, 2_000_000, 3_000_000}, positions(p))
}

func TestPlanSequentialSemantics(t *testing.T) {
	cur := blocks(1_000_000, 2_000_000) // A B
	a := cur[0].ID

	p := plan(t, cur,
		knowledge.InsertBlockAfter(a, "first"),
		knowledge.InsertBlockAfter(a, "second"),
	)
	assert.Equal(t, []string{"A", "second", "first", "B"}, contents(p),
		"the second insert after A lands directly after A")
	require.Len(t, p.Inserted, 2)
	assert.Equal(t, 0, p.Inserted[0].OperationIndex)
	assert.Equal(t, 1, p.Inserted[1].OperationIndex)
	assert.Equal(t, []knowledge.CreatedBlock{
		{OperationIndex: 0, BlockID: p.Inserted[0].ID},
		{OperationIndex: 1, BlockID: p.Inserted[1].ID},
	}, p.CreatedBlocks())
}

func TestPlanReplaceAndDelete(t *testing.T) {
	cur := blocks(1_000_000, 2_000_000, 3_000_000)
	p := plan(t, cur,
		knowledge.ReplaceBlock(cur[0].ID, "A2"),
		knowledge.ReplaceBlock(cur[0].ID, "A3"),
		knowledge.DeleteBlock(cur[1].ID),
		knowledge.ReplaceBlock(cur[2].ID, "C"), // unchanged content
	)
	assert.Equal(t, []string{"A3", "C"}, contents(p))
	assert.Equal(t, []int64{1_000_000, 3_000_000}, positions(p), "no renumbering after delete")
	assert.Equal(t, []uuid.UUID{cur[1].ID}, p.Deleted)
	require.Len(t, p.ContentChanged, 1)
	assert.Equal(t, "A3", p.ContentChanged[0].Content)
	assert.Empty(t, p.Repositioned)
}

func TestPlanInsertThenDeleteInSameBatch(t *testing.T) {
	// New IDs are unknown to the caller, so they can only be referenced by
	// later batches; deleting an existing block after inserts still works.
	cur := blocks(1_000_000)
	p := plan(t, cur, knowledge.AppendBlock("x"), knowledge.DeleteBlock(cur[0].ID))
	assert.Equal(t, []string{"x"}, contents(p))
	assert.Equal(t, []uuid.UUID{cur[0].ID}, p.Deleted)
}

func TestPlanReferenceErrors(t *testing.T) {
	cur := blocks(1_000_000, 2_000_000)
	unknown := uuid.New()

	for name, ops := range map[string][]knowledge.BlockOperation{
		"unknown replace":       {knowledge.ReplaceBlock(unknown, "x")},
		"unknown anchor":        {knowledge.InsertBlockAfter(unknown, "x")},
		"unknown move":          {knowledge.MoveBlockAfter(unknown, cur[0].ID)},
		"unknown move anchor":   {knowledge.MoveBlockAfter(cur[0].ID, unknown)},
		"deleted then replaced": {knowledge.DeleteBlock(cur[0].ID), knowledge.ReplaceBlock(cur[0].ID, "x")},
		"deleted then anchor":   {knowledge.DeleteBlock(cur[0].ID), knowledge.InsertBlockBefore(cur[0].ID, "x")},
		"deleted twice":         {knowledge.DeleteBlock(cur[0].ID), knowledge.DeleteBlock(cur[0].ID)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := knowledge.PlanBlockEdit(cur, ops, seqIDs())
			assert.ErrorIs(t, err, knowledge.ErrNotFound)
		})
	}

	_, err := knowledge.PlanBlockEdit(blocks(2, 1), []knowledge.BlockOperation{knowledge.AppendBlock("x")}, seqIDs())
	assert.ErrorIs(t, err, knowledge.ErrIntegrityViolation, "unordered input is rejected")
}

func TestBlockOperationValidate(t *testing.T) {
	id, other := uuid.New(), uuid.New()
	valid := []knowledge.BlockOperation{
		knowledge.ReplaceBlock(id, "x"),
		knowledge.InsertBlockAfter(id, "x"),
		knowledge.InsertBlockBefore(id, "x"),
		knowledge.AppendBlock("x"),
		knowledge.PrependBlock(" x "),
		knowledge.DeleteBlock(id),
		knowledge.MoveBlockAfter(id, other),
		knowledge.MoveBlockBefore(id, other),
	}
	for _, op := range valid {
		assert.NoError(t, op.Validate(), op.Type)
	}

	invalid := map[string]knowledge.BlockOperation{
		"unknown type":            {Type: "rename"},
		"replace without block":   {Type: knowledge.OpReplace, Content: "x"},
		"replace blank content":   knowledge.ReplaceBlock(id, " \n"),
		"insert without anchor":   {Type: knowledge.OpInsert, Content: "x"},
		"insert with two anchors": {Type: knowledge.OpInsert, Content: "x", AfterBlockID: &id, BeforeBlockID: &other},
		"insert with block id":    {Type: knowledge.OpInsert, Content: "x", AfterBlockID: &id, BlockID: &other},
		"append with anchor":      {Type: knowledge.OpAppend, Content: "x", AfterBlockID: &id},
		"prepend empty":           knowledge.PrependBlock(""),
		"delete with content":     {Type: knowledge.OpDelete, BlockID: &id, Content: "x"},
		"move without anchor":     {Type: knowledge.OpMove, BlockID: &id},
		"move onto itself":        knowledge.MoveBlockBefore(id, id),
		"nil block id":            knowledge.DeleteBlock(uuid.Nil),
	}
	for name, op := range invalid {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, op.Validate(), knowledge.ErrInvalidInput)
		})
	}
}

func TestEditDocumentInputValidate(t *testing.T) {
	ok := knowledge.EditDocumentInput{DocumentID: uuid.New(), ExpectedVersion: 1, Operations: []knowledge.BlockOperation{knowledge.AppendBlock("x")}}
	assert.NoError(t, ok.Validate())

	for name, mutate := range map[string]func(*knowledge.EditDocumentInput){
		"nil document": func(in *knowledge.EditDocumentInput) { in.DocumentID = uuid.Nil },
		"version 0":    func(in *knowledge.EditDocumentInput) { in.ExpectedVersion = 0 },
		"empty batch":  func(in *knowledge.EditDocumentInput) { in.Operations = nil },
		"invalid op":   func(in *knowledge.EditDocumentInput) { in.Operations = []knowledge.BlockOperation{{Type: "x"}} },
	} {
		t.Run(name, func(t *testing.T) {
			in := ok
			mutate(&in)
			assert.ErrorIs(t, in.Validate(), knowledge.ErrInvalidInput)
		})
	}
}
