package knowledge

import (
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// BlockOpType discriminates the block operations of EditDocument.
type BlockOpType string

// Supported block operations.
const (
	OpReplace BlockOpType = "replace" // BlockID, Content
	OpInsert  BlockOpType = "insert"  // Content and exactly one of AfterBlockID / BeforeBlockID
	OpAppend  BlockOpType = "append"  // Content
	OpPrepend BlockOpType = "prepend" // Content
	OpDelete  BlockOpType = "delete"  // BlockID
	OpMove    BlockOpType = "move"    // BlockID and exactly one of AfterBlockID / BeforeBlockID
)

// BlockOperation is a tagged union of block edits. Which fields are required
// or forbidden depends on Type; Validate enforces this strictly. Callers
// reference blocks only by ID, never by position. The constructor functions
// (ReplaceBlock, InsertBlockAfter, ...) always produce valid shapes.
type BlockOperation struct {
	Type          BlockOpType `json:"type"`
	BlockID       *uuid.UUID  `json:"blockId,omitempty"`
	AfterBlockID  *uuid.UUID  `json:"afterBlockId,omitempty"`
	BeforeBlockID *uuid.UUID  `json:"beforeBlockId,omitempty"`
	Content       string      `json:"content,omitempty"`
}

// ReplaceBlock replaces the content of an existing block.
func ReplaceBlock(id uuid.UUID, content string) BlockOperation {
	return BlockOperation{Type: OpReplace, BlockID: &id, Content: content}
}

// InsertBlockAfter inserts a new block directly after anchor.
func InsertBlockAfter(anchor uuid.UUID, content string) BlockOperation {
	return BlockOperation{Type: OpInsert, AfterBlockID: &anchor, Content: content}
}

// InsertBlockBefore inserts a new block directly before anchor.
func InsertBlockBefore(anchor uuid.UUID, content string) BlockOperation {
	return BlockOperation{Type: OpInsert, BeforeBlockID: &anchor, Content: content}
}

// AppendBlock adds a new block at the end.
func AppendBlock(content string) BlockOperation {
	return BlockOperation{Type: OpAppend, Content: content}
}

// PrependBlock adds a new block at the beginning.
func PrependBlock(content string) BlockOperation {
	return BlockOperation{Type: OpPrepend, Content: content}
}

// DeleteBlock removes a block.
func DeleteBlock(id uuid.UUID) BlockOperation {
	return BlockOperation{Type: OpDelete, BlockID: &id}
}

// MoveBlockAfter moves an existing block directly after anchor.
func MoveBlockAfter(id, anchor uuid.UUID) BlockOperation {
	return BlockOperation{Type: OpMove, BlockID: &id, AfterBlockID: &anchor}
}

// MoveBlockBefore moves an existing block directly before anchor.
func MoveBlockBefore(id, anchor uuid.UUID) BlockOperation {
	return BlockOperation{Type: OpMove, BlockID: &id, BeforeBlockID: &anchor}
}

// Validate checks the shape of the operation (not whether the referenced
// blocks exist).
func (op BlockOperation) Validate() error {
	hasBlock := op.BlockID != nil
	anchors := 0
	if op.AfterBlockID != nil {
		anchors++
	}
	if op.BeforeBlockID != nil {
		anchors++
	}

	var needBlock, needContent bool
	var wantAnchors int
	switch op.Type {
	case OpReplace:
		needBlock, needContent = true, true
	case OpInsert:
		needContent, wantAnchors = true, 1
	case OpAppend, OpPrepend:
		needContent = true
	case OpDelete:
		needBlock = true
	case OpMove:
		needBlock, wantAnchors = true, 1
	default:
		return fmt.Errorf("%w: unknown operation type %q", ErrInvalidInput, op.Type)
	}

	switch {
	case needBlock && !hasBlock:
		return fmt.Errorf("%w: %s requires blockId", ErrInvalidInput, op.Type)
	case !needBlock && hasBlock:
		return fmt.Errorf("%w: %s does not accept blockId", ErrInvalidInput, op.Type)
	case wantAnchors == 1 && anchors != 1:
		return fmt.Errorf("%w: %s requires exactly one of afterBlockId and beforeBlockId", ErrInvalidInput, op.Type)
	case wantAnchors == 0 && anchors != 0:
		return fmt.Errorf("%w: %s does not accept afterBlockId or beforeBlockId", ErrInvalidInput, op.Type)
	case !needContent && op.Content != "":
		return fmt.Errorf("%w: %s does not accept content", ErrInvalidInput, op.Type)
	}
	if hasBlock && *op.BlockID == uuid.Nil {
		return fmt.Errorf("%w: blockId must not be the nil UUID", ErrInvalidInput)
	}
	if anchor := op.anchor(); anchor != nil {
		if *anchor == uuid.Nil {
			return fmt.Errorf("%w: anchor block id must not be the nil UUID", ErrInvalidInput)
		}
		if op.Type == OpMove && *anchor == *op.BlockID {
			return fmt.Errorf("%w: a block cannot be moved relative to itself", ErrInvalidInput)
		}
	}
	if needContent {
		return ValidateBlockContent(op.Content)
	}
	return nil
}

func (op BlockOperation) anchor() *uuid.UUID {
	if op.AfterBlockID != nil {
		return op.AfterBlockID
	}
	return op.BeforeBlockID
}

// EditDocumentInput is an atomic batch of block operations on one document.
// Operations are applied strictly in the given order.
type EditDocumentInput struct {
	DocumentID      uuid.UUID
	ExpectedVersion int64
	Operations      []BlockOperation
}

// Validate checks the batch shape; block references are checked against
// the stored state when the batch is planned.
func (in EditDocumentInput) Validate() error {
	if in.DocumentID == uuid.Nil {
		return fmt.Errorf("%w: document id must not be the nil UUID", ErrInvalidInput)
	}
	if in.ExpectedVersion <= 0 {
		return fmt.Errorf("%w: expected version must be positive", ErrInvalidInput)
	}
	if len(in.Operations) == 0 {
		return fmt.Errorf("%w: at least one operation is required", ErrInvalidInput)
	}
	for i, op := range in.Operations {
		if err := op.Validate(); err != nil {
			return fmt.Errorf("operation %d: %w", i, err)
		}
	}
	return nil
}

// CreatedBlock maps an insert/append/prepend operation to the new block ID.
type CreatedBlock struct {
	OperationIndex int       `json:"operationIndex"`
	BlockID        uuid.UUID `json:"blockId"`
}

// EditDocumentResult reports the outcome of EditDocument.
type EditDocumentResult struct {
	DocumentID uuid.UUID `json:"documentId"`
	// NewVersion is the document version after the batch; it equals the
	// expected version if the batch had no effect.
	NewVersion int64 `json:"newVersion"`
	// Changed is false for batches without any effect (e.g. replacing with
	// identical content or moving a block to where it already is).
	Changed       bool           `json:"changed"`
	CreatedBlocks []CreatedBlock `json:"createdBlocks"`
	// DeletedBlocks are the IDs of the removed blocks.
	DeletedBlocks []uuid.UUID `json:"deletedBlocks"`
	// BlockOrder is the resulting block order.
	BlockOrder []uuid.UUID `json:"blockOrder"`
}

// BlockState is the stored state of a block used for planning an edit.
type BlockState struct {
	ID       uuid.UUID
	Position int64
	Content  string
}

// PlannedBlock is a block of the planned final state.
type PlannedBlock struct {
	ID       uuid.UUID
	Position int64
	Content  string
}

// NewBlock is a block to insert.
type NewBlock struct {
	OperationIndex int
	ID             uuid.UUID
	Position       int64
	Content        string
}

// BlockEditPlan is the minimal set of changes that turns the stored block
// state into the requested one.
type BlockEditPlan struct {
	// Final is the resulting order with final positions.
	Final []PlannedBlock
	// Inserted are new blocks with their final positions.
	Inserted []NewBlock
	// Deleted are removed existing blocks.
	Deleted []uuid.UUID
	// ContentChanged are existing blocks whose content differs from the
	// stored content.
	ContentChanged []PlannedBlock
	// Repositioned are existing blocks whose position changed (moves and
	// rebalancing). Content changes are not included here.
	Repositioned []PlannedBlock
	// Rebalanced reports whether the document had to be renumbered.
	Rebalanced bool
}

// Changed reports whether the plan has any effect.
func (p BlockEditPlan) Changed() bool {
	return len(p.Inserted) > 0 || len(p.Deleted) > 0 || len(p.ContentChanged) > 0 || len(p.Repositioned) > 0
}

// CreatedBlocks returns the operation index to block ID mapping.
func (p BlockEditPlan) CreatedBlocks() []CreatedBlock {
	out := make([]CreatedBlock, len(p.Inserted))
	for i, b := range p.Inserted {
		out[i] = CreatedBlock{OperationIndex: b.OperationIndex, BlockID: b.ID}
	}
	return out
}

// Order returns the final block IDs in order.
func (p BlockEditPlan) Order() []uuid.UUID {
	out := make([]uuid.UUID, len(p.Final))
	for i, b := range p.Final {
		out[i] = b.ID
	}
	return out
}

// PlanBlockEdit applies ops sequentially to an in-memory copy of current
// (which must be ordered by position, id with strictly increasing
// positions) and returns the resulting changes.
//
// Positions follow the sparse ordering rules: midpoint between neighbours,
// last+PositionGap for append, first/2 for prepend. Only if no integer
// position is free, the whole document is renumbered in the intended order.
// A moved block keeps its position if it still fits between its new
// neighbours, so moving a block to where it already is changes nothing.
// newID generates IDs for new blocks.
func PlanBlockEdit(current []BlockState, ops []BlockOperation, newID func() uuid.UUID) (BlockEditPlan, error) {
	for i := 1; i < len(current); i++ {
		if current[i].Position <= current[i-1].Position {
			return BlockEditPlan{}, fmt.Errorf("%w: block positions are not strictly increasing", ErrIntegrityViolation)
		}
	}

	type entry struct {
		id       uuid.UUID
		pos      int64
		content  string
		isNew    bool
		opIndex  int
		original BlockState
	}
	order := make([]*entry, len(current))
	for i, b := range current {
		order[i] = &entry{id: b.ID, pos: b.Position, content: b.Content, original: b}
	}
	var deleted []uuid.UUID
	rebalanced := false

	indexOf := func(id uuid.UUID) int {
		return slices.IndexFunc(order, func(e *entry) bool { return e.id == id })
	}
	find := func(opIndex int, id uuid.UUID) (int, error) {
		idx := indexOf(id)
		if idx < 0 {
			return -1, fmt.Errorf("operation %d: %w: block %s is not part of the document", opIndex, ErrNotFound, id)
		}
		return idx, nil
	}
	// place inserts e at index and assigns a position, rebalancing the
	// whole (already reordered) list if no gap is free. With keep, e keeps
	// its current position if it still fits between the new neighbours.
	place := func(e *entry, index int, keep bool) error {
		hasLeft, hasRight := index > 0, index < len(order)
		var left, right int64
		if hasLeft {
			left = order[index-1].pos
		}
		if hasRight {
			right = order[index].pos
		}

		var pos int64
		ok := false
		switch {
		case keep && (!hasLeft || e.pos > left) && (!hasRight || e.pos < right):
			pos, ok = e.pos, true
		case !hasLeft && !hasRight:
			pos, ok = FirstPosition, true
		case !hasRight:
			pos, ok = PositionAfter(left)
		case !hasLeft:
			pos, ok = PositionBefore(right)
		default:
			pos, ok = PositionBetween(left, right)
		}

		order = slices.Insert(order, index, e)
		if ok {
			e.pos = pos
			return nil
		}
		positions, err := SequentialPositions(len(order))
		if err != nil {
			return err
		}
		for i, o := range order {
			o.pos = positions[i]
		}
		rebalanced = true
		return nil
	}
	targetIndex := func(opIndex int, op BlockOperation) (int, error) {
		if op.AfterBlockID != nil {
			idx, err := find(opIndex, *op.AfterBlockID)
			return idx + 1, err
		}
		return find(opIndex, *op.BeforeBlockID)
	}

	for i, op := range ops {
		if err := op.Validate(); err != nil {
			return BlockEditPlan{}, fmt.Errorf("operation %d: %w", i, err)
		}
		switch op.Type {
		case OpReplace:
			idx, err := find(i, *op.BlockID)
			if err != nil {
				return BlockEditPlan{}, err
			}
			order[idx].content = op.Content

		case OpInsert, OpAppend, OpPrepend:
			index := 0
			switch op.Type {
			case OpInsert:
				var err error
				if index, err = targetIndex(i, op); err != nil {
					return BlockEditPlan{}, err
				}
			case OpAppend:
				index = len(order)
			}
			e := &entry{id: newID(), content: op.Content, isNew: true, opIndex: i}
			if err := place(e, index, false); err != nil {
				return BlockEditPlan{}, err
			}

		case OpDelete:
			idx, err := find(i, *op.BlockID)
			if err != nil {
				return BlockEditPlan{}, err
			}
			if !order[idx].isNew {
				deleted = append(deleted, order[idx].id)
			}
			order = slices.Delete(order, idx, idx+1)

		case OpMove:
			idx, err := find(i, *op.BlockID)
			if err != nil {
				return BlockEditPlan{}, err
			}
			e := order[idx]
			order = slices.Delete(order, idx, idx+1)
			index, err := targetIndex(i, op)
			if err != nil {
				return BlockEditPlan{}, err
			}
			if err := place(e, index, true); err != nil {
				return BlockEditPlan{}, err
			}
		}
	}

	plan := BlockEditPlan{Deleted: deleted, Rebalanced: rebalanced, Final: make([]PlannedBlock, len(order))}
	for i, e := range order {
		if i > 0 && e.pos <= order[i-1].pos {
			return BlockEditPlan{}, fmt.Errorf("%w: planned positions are not strictly increasing", ErrIntegrityViolation)
		}
		pb := PlannedBlock{ID: e.id, Position: e.pos, Content: e.content}
		plan.Final[i] = pb
		switch {
		case e.isNew:
			plan.Inserted = append(plan.Inserted, NewBlock{OperationIndex: e.opIndex, ID: e.id, Position: e.pos, Content: e.content})
		default:
			if e.content != e.original.Content {
				plan.ContentChanged = append(plan.ContentChanged, pb)
			}
			if e.pos != e.original.Position {
				plan.Repositioned = append(plan.Repositioned, pb)
			}
		}
	}
	slices.SortFunc(plan.Inserted, func(a, b NewBlock) int { return a.OperationIndex - b.OperationIndex })
	return plan, nil
}
