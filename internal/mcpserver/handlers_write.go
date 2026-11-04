package mcpserver

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"mcp-knowledge-graph/internal/knowledge"
)

func (h *Handler) createDocument(ctx context.Context, in CreateDocumentInput) (CreateDocumentOutput, error) {
	if err := checkText("title", in.Title, h.limits.MaxTitleBytes); err != nil {
		return CreateDocumentOutput{}, err
	}
	if err := checkText("summary", in.Summary, h.limits.MaxSummaryBytes); err != nil {
		return CreateDocumentOutput{}, err
	}
	if len(in.ParentIDs) == 0 || len(in.ParentIDs) > maxCreateParents {
		return CreateDocumentOutput{}, invalidInput("parent_ids must contain between 1 and %d IDs", maxCreateParents)
	}
	parents := make([]uuid.UUID, len(in.ParentIDs))
	seen := make(map[uuid.UUID]struct{}, len(in.ParentIDs))
	for i, raw := range in.ParentIDs {
		id, err := parseID(fmt.Sprintf("parent_ids[%d]", i), raw)
		if err != nil {
			return CreateDocumentOutput{}, err
		}
		if _, dup := seen[id]; dup {
			return CreateDocumentOutput{}, invalidInput("parent_ids must be distinct; %s is listed twice", id)
		}
		seen[id] = struct{}{}
		parents[i] = id
	}
	if len(in.Blocks) > maxCreateBlocks {
		return CreateDocumentOutput{}, invalidInput("at most %d initial blocks are allowed", maxCreateBlocks)
	}
	blocks := make([]string, len(in.Blocks))
	for i, b := range in.Blocks {
		if err := checkText(fmt.Sprintf("blocks[%d].content", i), b.Content, h.limits.MaxBlockBytes); err != nil {
			return CreateDocumentOutput{}, err
		}
		blocks[i] = b.Content
	}
	if len(in.Sources) > maxCreateSources {
		return CreateDocumentOutput{}, invalidInput("at most %d sources are allowed", maxCreateSources)
	}
	sources := make([]knowledge.SourceInput, len(in.Sources))
	for i, s := range in.Sources {
		src, err := h.sourceInput(s.Kind, s.SourceURI, s.Title)
		if err != nil {
			return CreateDocumentOutput{}, prefixInvalid(fmt.Sprintf("sources[%d]", i), err)
		}
		sources[i] = src
	}

	res, err := h.service.CreateDocument(ctx, knowledge.CreateDocumentInput{
		Title:     in.Title,
		Summary:   in.Summary,
		ParentIDs: parents,
		Blocks:    blocks,
		Sources:   sources,
	})
	if err != nil {
		return CreateDocumentOutput{}, err
	}
	return CreateDocumentOutput{
		DocumentID:      res.ID.String(),
		Version:         res.Version,
		PrimaryParentID: res.PrimaryParentID.String(),
		BlockIDs:        idStrings(res.BlockIDs),
		SourceIDs:       idStrings(res.SourceIDs),
	}, nil
}

func (h *Handler) updateDocument(ctx context.Context, in UpdateDocumentInput) (ChangeOutput, error) {
	id, err := parseID("document_id", in.DocumentID)
	if err != nil {
		return ChangeOutput{}, err
	}
	if err := checkVersion("expected_version", in.ExpectedVersion); err != nil {
		return ChangeOutput{}, err
	}
	if in.Title == nil && in.Summary == nil {
		return ChangeOutput{}, invalidInput("at least one of title and summary must be provided")
	}
	if in.Title != nil {
		if err := checkText("title", *in.Title, h.limits.MaxTitleBytes); err != nil {
			return ChangeOutput{}, err
		}
	}
	if in.Summary != nil {
		if err := checkText("summary", *in.Summary, h.limits.MaxSummaryBytes); err != nil {
			return ChangeOutput{}, err
		}
	}
	if id == knowledge.RootID {
		// The root keeps its identity for predictable navigation.
		return ChangeOutput{}, fmt.Errorf("%w: root metadata cannot be changed", knowledge.ErrRootViolation)
	}
	s, err := h.service.UpdateDocumentMetadata(ctx, id, in.ExpectedVersion, knowledge.MetadataUpdate{Title: in.Title, Summary: in.Summary})
	if err != nil {
		return ChangeOutput{}, err
	}
	return ChangeOutput{DocumentID: id.String(), Version: s.Version, Changed: s.Version != in.ExpectedVersion}, nil
}

func (h *Handler) editDocument(ctx context.Context, in EditDocumentInput) (EditDocumentOutput, error) {
	id, err := parseID("document_id", in.DocumentID)
	if err != nil {
		return EditDocumentOutput{}, err
	}
	if err := checkVersion("expected_version", in.ExpectedVersion); err != nil {
		return EditDocumentOutput{}, err
	}
	if len(in.Operations) == 0 || len(in.Operations) > h.limits.MaxEditOperations {
		return EditDocumentOutput{}, invalidInput("operations must contain between 1 and %d entries", h.limits.MaxEditOperations)
	}
	ops := make([]knowledge.BlockOperation, len(in.Operations))
	for i, raw := range in.Operations {
		op, err := h.blockOperation(raw)
		if err != nil {
			return EditDocumentOutput{}, prefixInvalid(fmt.Sprintf("operations[%d]", i), err)
		}
		ops[i] = op
	}

	res, err := h.service.EditDocument(ctx, knowledge.EditDocumentInput{
		DocumentID:      id,
		ExpectedVersion: in.ExpectedVersion,
		Operations:      ops,
	})
	if err != nil {
		return EditDocumentOutput{}, err
	}
	out := EditDocumentOutput{
		DocumentID:      id.String(),
		Version:         res.NewVersion,
		Changed:         res.Changed,
		CreatedBlocks:   make([]CreatedBlockOutput, len(res.CreatedBlocks)),
		DeletedBlockIDs: idStrings(res.DeletedBlocks),
		BlockIDsInOrder: idStrings(res.BlockOrder),
	}
	for i, c := range res.CreatedBlocks {
		out.CreatedBlocks[i] = CreatedBlockOutput{OperationIndex: c.OperationIndex, BlockID: c.BlockID.String()}
	}
	return out, nil
}

// opShape lists which fields an operation type requires (true) or forbids
// (false). Anchors are handled separately.
type opShape struct {
	blockID, content bool
	anchors          int // number of required anchors (0 or 1)
}

var opShapes = map[knowledge.BlockOpType]opShape{
	knowledge.OpReplace: {blockID: true, content: true},
	knowledge.OpInsert:  {content: true, anchors: 1},
	knowledge.OpAppend:  {content: true},
	knowledge.OpPrepend: {content: true},
	knowledge.OpDelete:  {blockID: true},
	knowledge.OpMove:    {blockID: true, anchors: 1},
}

// blockOperation validates one edit operation strictly: required fields
// must be present, forbidden fields must be absent (even if empty).
func (h *Handler) blockOperation(in EditOperationInput) (knowledge.BlockOperation, error) {
	t := knowledge.BlockOpType(in.Type)
	shape, ok := opShapes[t]
	if !ok {
		return knowledge.BlockOperation{}, invalidInput("unknown operation type %q", truncateForMessage(in.Type))
	}
	op := knowledge.BlockOperation{Type: t}

	switch {
	case shape.blockID && in.BlockID == nil:
		return op, invalidInput("%s requires block_id", t)
	case !shape.blockID && in.BlockID != nil:
		return op, invalidInput("%s does not accept block_id", t)
	case shape.content && in.Content == nil:
		return op, invalidInput("%s requires content", t)
	case !shape.content && in.Content != nil:
		return op, invalidInput("%s does not accept content", t)
	}
	anchors := 0
	if in.BeforeBlockID != nil {
		anchors++
	}
	if in.AfterBlockID != nil {
		anchors++
	}
	if shape.anchors == 1 && anchors != 1 {
		return op, invalidInput("%s requires exactly one of before_block_id and after_block_id", t)
	}
	if shape.anchors == 0 && anchors != 0 {
		return op, invalidInput("%s does not accept before_block_id or after_block_id", t)
	}

	var err error
	if in.BlockID != nil {
		if op.BlockID, err = requiredPtrID("block_id", *in.BlockID); err != nil {
			return op, err
		}
	}
	if in.BeforeBlockID != nil {
		if op.BeforeBlockID, err = requiredPtrID("before_block_id", *in.BeforeBlockID); err != nil {
			return op, err
		}
	}
	if in.AfterBlockID != nil {
		if op.AfterBlockID, err = requiredPtrID("after_block_id", *in.AfterBlockID); err != nil {
			return op, err
		}
	}
	if in.Content != nil {
		if err := checkText("content", *in.Content, h.limits.MaxBlockBytes); err != nil {
			return op, err
		}
		op.Content = *in.Content
	}
	if err := op.Validate(); err != nil {
		return op, err
	}
	return op, nil
}

func requiredPtrID(field, value string) (*uuid.UUID, error) {
	id, err := parseID(field, value)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (h *Handler) parentChange(documentID, parentID string, expectedVersion int64) (knowledge.ParentChange, error) {
	child, err := parseID("document_id", documentID)
	if err != nil {
		return knowledge.ParentChange{}, err
	}
	parent, err := parseID("parent_id", parentID)
	if err != nil {
		return knowledge.ParentChange{}, err
	}
	if err := checkVersion("expected_version", expectedVersion); err != nil {
		return knowledge.ParentChange{}, err
	}
	return knowledge.ParentChange{DocumentID: child, ParentID: parent, ExpectedVersion: expectedVersion}, nil
}

func (h *Handler) addParent(ctx context.Context, in AddParentInput) (AddParentOutput, error) {
	change, err := h.parentChange(in.DocumentID, in.ParentID, in.ExpectedVersion)
	if err != nil {
		return AddParentOutput{}, err
	}
	change.MakePrimary = in.MakePrimary
	res, err := h.service.AddParentChecked(ctx, change)
	if err != nil {
		return AddParentOutput{}, err
	}
	return AddParentOutput{
		DocumentID: res.DocumentID.String(),
		ParentID:   res.ParentID.String(),
		Version:    res.Version,
		Changed:    res.Changed,
		IsPrimary:  res.IsPrimary,
	}, nil
}

func (h *Handler) removeParent(ctx context.Context, in ParentEdgeInput) (ParentChangeOutput, error) {
	change, err := h.parentChange(in.DocumentID, in.ParentID, in.ExpectedVersion)
	if err != nil {
		return ParentChangeOutput{}, err
	}
	res, err := h.service.RemoveParentChecked(ctx, change)
	if err != nil {
		return ParentChangeOutput{}, err
	}
	return toParentChangeOutput(res), nil
}

func (h *Handler) setPrimaryParent(ctx context.Context, in ParentEdgeInput) (ParentChangeOutput, error) {
	change, err := h.parentChange(in.DocumentID, in.ParentID, in.ExpectedVersion)
	if err != nil {
		return ParentChangeOutput{}, err
	}
	res, err := h.service.SetPrimaryParentChecked(ctx, change)
	if err != nil {
		return ParentChangeOutput{}, err
	}
	return toParentChangeOutput(res), nil
}

func toParentChangeOutput(res knowledge.ParentChangeResult) ParentChangeOutput {
	return ParentChangeOutput{
		DocumentID:      res.DocumentID.String(),
		Version:         res.Version,
		Changed:         res.Changed,
		PrimaryParentID: res.PrimaryParentID.String(),
	}
}

func (h *Handler) addSource(ctx context.Context, in AddSourceInput) (AddSourceOutput, error) {
	id, err := parseID("document_id", in.DocumentID)
	if err != nil {
		return AddSourceOutput{}, err
	}
	if err := checkVersion("expected_version", in.ExpectedVersion); err != nil {
		return AddSourceOutput{}, err
	}
	src, err := h.sourceInput(in.Kind, in.SourceURI, in.Title)
	if err != nil {
		return AddSourceOutput{}, err
	}
	res, err := h.service.AddSourceChecked(ctx, id, in.ExpectedVersion, src)
	if err != nil {
		return AddSourceOutput{}, err
	}
	return AddSourceOutput{
		DocumentID: id.String(),
		SourceID:   res.Source.ID.String(),
		Version:    res.Version,
		Changed:    res.Changed,
	}, nil
}

// prefixInvalid adds the location of a nested argument to an invalid_input
// message.
func prefixInvalid(location string, err error) error {
	if pub, ok := err.(*PublicToolError); ok && pub.Code == CodeInvalidInput {
		return &PublicToolError{Code: pub.Code, Message: location + ": " + pub.Message, Details: pub.Details}
	}
	return fmt.Errorf("%s: %w", location, err)
}

func idStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
