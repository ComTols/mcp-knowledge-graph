package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"mcp-knowledge-graph/internal/knowledge"
)

const warningSemanticSearchUnavailable = "semantic_search_unavailable"

func (h *Handler) search(ctx context.Context, in SearchInput) (SearchOutput, error) {
	query := strings.TrimSpace(in.Query)
	if !utf8.ValidString(in.Query) {
		return SearchOutput{}, invalidInput("query must be valid UTF-8")
	}
	if query == "" {
		return SearchOutput{}, invalidInput("query must not be blank")
	}
	if n := utf8.RuneCountInString(in.Query); n > maxQueryRunes {
		return SearchOutput{}, invalidInput("query exceeds %d characters (got %d)", maxQueryRunes, n)
	}
	limit, err := limitOrDefault("limit", in.Limit, defaultSearchLimit, maxSearchLimit)
	if err != nil {
		return SearchOutput{}, err
	}
	maxPaths, err := limitOrDefault("max_paths", in.MaxPaths, defaultMaxPaths, maxMaxPaths)
	if err != nil {
		return SearchOutput{}, err
	}

	resp, err := h.service.Search(ctx, knowledge.SearchParams{
		Query:           query,
		Limit:           limit,
		AllPaths:        in.AllPaths,
		LexicalFallback: true,
	})
	if err != nil {
		return SearchOutput{}, err
	}

	out := SearchOutput{
		Query:                   resp.Query,
		Results:                 make([]SearchHit, 0, len(resp.Results)),
		SemanticSearchAvailable: resp.SemanticSearchAvailable,
		Warnings:                []string{},
	}
	if !resp.SemanticSearchAvailable {
		out.Warnings = append(out.Warnings, warningSemanticSearchUnavailable)
		if resp.SemanticError != nil {
			h.logger.WarnContext(ctx, "search fell back to lexical matching",
				slog.String("error", resp.SemanticError.Error()))
		}
	}
	for _, r := range resp.Results {
		if r.ID == knowledge.RootID {
			continue
		}
		paths, truncated, err := limitPaths(r.ID, knowledge.PathSet{Paths: r.Paths, Truncated: r.PathsTruncated}, in.AllPaths, maxPaths)
		if err != nil {
			return SearchOutput{}, err
		}
		out.Results = append(out.Results, SearchHit{ID: r.ID.String(), Paths: paths, PathsTruncated: truncated})
	}
	return out, nil
}

// limitPaths converts a path set and applies the per-call path limit. Every
// path must start at root and end at the document.
func limitPaths(id uuid.UUID, set knowledge.PathSet, allPaths bool, maxPaths int) ([]DocumentPath, bool, error) {
	if len(set.Paths) == 0 {
		return nil, false, fmt.Errorf("%w: no root path for document %s", knowledge.ErrIntegrityViolation, id)
	}
	limit := maxPaths
	if !allPaths {
		limit = 1
	}
	truncated := set.Truncated && allPaths
	paths := set.Paths
	if len(paths) > limit {
		paths, truncated = paths[:limit], allPaths
	}
	out := make([]DocumentPath, len(paths))
	for i, p := range paths {
		if err := knowledge.ValidatePath(p, id); err != nil {
			return nil, false, err
		}
		out[i] = toDocumentPath(p)
	}
	return out, truncated, nil
}

func toDocumentPath(p knowledge.Path) DocumentPath {
	out := make(DocumentPath, len(p))
	for i, e := range p {
		out[i] = DocumentReference{ID: e.ID.String(), Title: e.Title}
	}
	return out
}

func (h *Handler) getRoot(ctx context.Context, _ GetRootInput) (SummaryOutput, error) {
	s, err := h.service.GetSummary(ctx, knowledge.RootID)
	if errors.Is(err, knowledge.ErrNotFound) {
		// The root is seeded by the migrations and never repaired here.
		return SummaryOutput{}, fmt.Errorf("%w: root document is missing", knowledge.ErrIntegrityViolation)
	}
	if err != nil {
		return SummaryOutput{}, err
	}
	return toSummaryOutput(s), nil
}

func (h *Handler) getSummary(ctx context.Context, in DocumentIDInput) (SummaryOutput, error) {
	id, err := parseID("document_id", in.DocumentID)
	if err != nil {
		return SummaryOutput{}, err
	}
	s, err := h.service.GetSummary(ctx, id)
	if err != nil {
		return SummaryOutput{}, err
	}
	return toSummaryOutput(s), nil
}

func toSummaryOutput(s knowledge.DocumentSummary) SummaryOutput {
	return SummaryOutput{ID: s.ID.String(), Title: s.Title, Summary: s.Summary, Version: s.Version}
}

func (h *Handler) getParents(ctx context.Context, in DocumentIDInput) (GetParentsOutput, error) {
	id, err := parseID("document_id", in.DocumentID)
	if err != nil {
		return GetParentsOutput{}, err
	}
	list, err := h.service.GetParentList(ctx, id)
	if err != nil {
		return GetParentsOutput{}, err
	}
	primaries := 0
	out := GetParentsOutput{DocumentID: id.String(), Version: list.Version, Parents: make([]ParentOutput, 0, len(list.Parents))}
	for _, p := range list.Parents {
		if p.IsPrimary {
			primaries++
		}
		out.Parents = append(out.Parents, ParentOutput{ID: p.ID.String(), Title: p.Title, IsPrimary: p.IsPrimary})
	}
	switch {
	case id == knowledge.RootID && len(list.Parents) != 0:
		return GetParentsOutput{}, fmt.Errorf("%w: root has parents", knowledge.ErrIntegrityViolation)
	case id != knowledge.RootID && (len(list.Parents) == 0 || primaries != 1):
		return GetParentsOutput{}, fmt.Errorf("%w: document %s has %d parents and %d primary parents",
			knowledge.ErrIntegrityViolation, id, len(list.Parents), primaries)
	}
	return out, nil
}

func (h *Handler) getChildren(ctx context.Context, in GetChildrenInput) (GetChildrenOutput, error) {
	id, err := parseID("document_id", in.DocumentID)
	if err != nil {
		return GetChildrenOutput{}, err
	}
	limit, err := limitOrDefault("limit", in.Limit, defaultPageLimit, maxPageLimit)
	if err != nil {
		return GetChildrenOutput{}, err
	}
	after, err := decodeChildrenCursor(in.Cursor, id)
	if err != nil {
		return GetChildrenOutput{}, err
	}
	page, err := h.service.GetChildrenPage(ctx, id, knowledge.ChildrenPageRequest{After: after, Limit: limit})
	if err != nil {
		return GetChildrenOutput{}, err
	}
	out := GetChildrenOutput{
		DocumentID: id.String(),
		Children:   make([]ChildOutput, len(page.Children)),
		HasMore:    page.Next != nil,
		NextCursor: encodeChildrenCursor(id, page.Next),
	}
	for i, c := range page.Children {
		out.Children[i] = ChildOutput{ID: c.ID.String(), Title: c.Title, Summary: c.Summary, IsPrimary: c.IsPrimary}
	}
	return out, nil
}

func (h *Handler) getFulltext(ctx context.Context, in GetFulltextInput) (GetFulltextOutput, error) {
	id, err := parseID("document_id", in.DocumentID)
	if err != nil {
		return GetFulltextOutput{}, err
	}
	limit, err := limitOrDefault("limit", in.Limit, defaultPageLimit, maxPageLimit)
	if err != nil {
		return GetFulltextOutput{}, err
	}
	after, err := parseOptionalID("after_block_id", in.AfterBlockID)
	if err != nil {
		return GetFulltextOutput{}, err
	}
	if in.ExpectedVersion < 0 {
		return GetFulltextOutput{}, invalidInput("expected_version must be a positive integer")
	}
	page, err := h.service.GetFulltextPage(ctx, id, knowledge.BlockPageRequest{AfterBlockID: after, Limit: limit})
	if err != nil {
		return GetFulltextOutput{}, err
	}
	// Optimistic check against the version read in the same snapshot as
	// the blocks, so pages of different versions are never mixed silently.
	if in.ExpectedVersion > 0 && page.Version != in.ExpectedVersion {
		return GetFulltextOutput{}, knowledge.NewVersionConflict(id, in.ExpectedVersion, page.Version)
	}
	out := GetFulltextOutput{
		DocumentID: id.String(),
		Version:    page.Version,
		Blocks:     make([]BlockOutput, len(page.Blocks)),
		HasMore:    page.HasMore,
	}
	for i, b := range page.Blocks {
		out.Blocks[i] = BlockOutput{ID: b.ID.String(), Version: b.Version, Content: b.Content}
	}
	if page.HasMore && len(page.Blocks) > 0 {
		next := page.Blocks[len(page.Blocks)-1].ID.String()
		out.NextAfterBlockID = &next
	}
	return out, nil
}

func (h *Handler) getPaths(ctx context.Context, in GetPathsInput) (GetPathsOutput, error) {
	id, err := parseID("document_id", in.DocumentID)
	if err != nil {
		return GetPathsOutput{}, err
	}
	maxPaths, err := limitOrDefault("max_paths", in.MaxPaths, defaultMaxPaths, maxMaxPaths)
	if err != nil {
		return GetPathsOutput{}, err
	}
	set, err := h.service.GetPaths(ctx, id, in.AllPaths)
	if err != nil {
		return GetPathsOutput{}, err
	}
	paths, truncated, err := limitPaths(id, set, in.AllPaths, maxPaths)
	if err != nil {
		return GetPathsOutput{}, err
	}
	return GetPathsOutput{DocumentID: id.String(), Paths: paths, PathsTruncated: truncated}, nil
}

func (h *Handler) getSources(ctx context.Context, in GetSourcesInput) (GetSourcesOutput, error) {
	id, err := parseID("document_id", in.DocumentID)
	if err != nil {
		return GetSourcesOutput{}, err
	}
	limit, err := limitOrDefault("limit", in.Limit, defaultPageLimit, maxPageLimit)
	if err != nil {
		return GetSourcesOutput{}, err
	}
	after, err := decodeSourcesCursor(in.Cursor, id)
	if err != nil {
		return GetSourcesOutput{}, err
	}
	page, err := h.service.GetSourcesPage(ctx, id, knowledge.SourcesPageRequest{After: after, Limit: limit})
	if err != nil {
		return GetSourcesOutput{}, err
	}
	out := GetSourcesOutput{
		DocumentID: id.String(),
		Sources:    make([]SourceOutput, len(page.Sources)),
		HasMore:    page.Next != nil,
		NextCursor: encodeSourcesCursor(id, page.Next),
	}
	for i, src := range page.Sources {
		out.Sources[i] = SourceOutput{ID: src.ID.String(), Kind: string(src.Kind), SourceURI: src.URI, Title: src.Title}
	}
	return out, nil
}
