package knowledge

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// NormalizeCreateInput validates in and returns a cleaned copy: title and
// summary are trimmed, parent IDs deduplicated (order preserved, so the first
// ID stays the primary parent), source kinds defaulted and sources
// deduplicated by URI. Block contents are kept byte-for-byte.
func NormalizeCreateInput(in CreateDocumentInput) (CreateDocumentInput, error) {
	out := CreateDocumentInput{
		Title:   strings.TrimSpace(in.Title),
		Summary: strings.TrimSpace(in.Summary),
	}
	if out.Title == "" {
		return CreateDocumentInput{}, fmt.Errorf("%w: title must not be empty", ErrInvalidInput)
	}
	if out.Summary == "" {
		return CreateDocumentInput{}, fmt.Errorf("%w: summary must not be empty", ErrInvalidInput)
	}

	parents, err := DedupeParentIDs(in.ParentIDs)
	if err != nil {
		return CreateDocumentInput{}, err
	}
	out.ParentIDs = parents

	if err := ValidateBlockContents(in.Blocks); err != nil {
		return CreateDocumentInput{}, err
	}
	out.Blocks = append([]string(nil), in.Blocks...)

	seen := make(map[string]struct{}, len(in.Sources))
	for _, s := range in.Sources {
		ns, err := NormalizeSourceInput(s)
		if err != nil {
			return CreateDocumentInput{}, err
		}
		if _, dup := seen[ns.URI]; dup {
			continue
		}
		seen[ns.URI] = struct{}{}
		out.Sources = append(out.Sources, ns)
	}
	return out, nil
}

// DedupeParentIDs removes duplicates while preserving order. It rejects an
// empty list and the nil UUID.
func DedupeParentIDs(ids []uuid.UUID) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: at least one parent is required", ErrInvalidInput)
	}
	out := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			return nil, fmt.Errorf("%w: parent id must not be the nil UUID", ErrInvalidInput)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

// ValidateBlockContent rejects blank block content.
func ValidateBlockContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("%w: block content must not be empty", ErrInvalidInput)
	}
	return nil
}

// ValidateBlockContents validates every block content.
func ValidateBlockContents(contents []string) error {
	for i, c := range contents {
		if err := ValidateBlockContent(c); err != nil {
			return fmt.Errorf("block %d: %w", i, err)
		}
	}
	return nil
}

// NormalizeSourceInput trims URI and title and applies the default kind.
func NormalizeSourceInput(in SourceInput) (SourceInput, error) {
	out := SourceInput{Kind: in.Kind, URI: strings.TrimSpace(in.URI)}
	if out.Kind == "" {
		out.Kind = SourceKindConversation
	}
	if !out.Kind.Valid() {
		return SourceInput{}, fmt.Errorf("%w: unknown source kind %q", ErrInvalidInput, in.Kind)
	}
	if out.URI == "" {
		return SourceInput{}, fmt.Errorf("%w: source uri must not be empty", ErrInvalidInput)
	}
	if in.Title != nil {
		if t := strings.TrimSpace(*in.Title); t != "" {
			out.Title = &t
		}
	}
	return out, nil
}

// NormalizeMetadataUpdate trims the given fields and rejects empty updates
// and blank values.
func NormalizeMetadataUpdate(upd MetadataUpdate) (MetadataUpdate, error) {
	if upd.Title == nil && upd.Summary == nil {
		return MetadataUpdate{}, fmt.Errorf("%w: nothing to update", ErrInvalidInput)
	}
	var out MetadataUpdate
	if upd.Title != nil {
		t := strings.TrimSpace(*upd.Title)
		if t == "" {
			return MetadataUpdate{}, fmt.Errorf("%w: title must not be empty", ErrInvalidInput)
		}
		out.Title = &t
	}
	if upd.Summary != nil {
		s := strings.TrimSpace(*upd.Summary)
		if s == "" {
			return MetadataUpdate{}, fmt.Errorf("%w: summary must not be empty", ErrInvalidInput)
		}
		out.Summary = &s
	}
	return out, nil
}
