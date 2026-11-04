package knowledge

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Domain errors. Adapters wrap these sentinels with additional context, so
// callers must test for them with errors.Is.
var (
	// ErrNotFound reports that a referenced document, block, edge or job
	// does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalidInput reports a validation failure of caller supplied data.
	ErrInvalidInput = errors.New("invalid input")
	// ErrCycleDetected reports that a hierarchy change would introduce a
	// cycle (including self references).
	ErrCycleDetected = errors.New("cycle detected")
	// ErrRootViolation reports an operation that would break the root
	// invariants (root as child, second root, ...).
	ErrRootViolation = errors.New("root violation")
	// ErrLastParentViolation reports an attempt to remove the last parent
	// of a non-root document.
	ErrLastParentViolation = errors.New("last parent violation")
	// ErrVersionConflict reports a failed optimistic locking check.
	ErrVersionConflict = errors.New("version conflict")
	// ErrEmbeddingUnavailable reports that no (compatible) embedding could
	// be produced, e.g. because no provider is configured or it failed.
	ErrEmbeddingUnavailable = errors.New("embedding unavailable")
	// ErrIntegrityViolation reports that persisted data violates a
	// structural invariant (e.g. a path that does not end at root).
	ErrIntegrityViolation = errors.New("integrity violation")
)

// ErrRelationshipNotFound reports that a referenced parent edge does not
// exist. It wraps ErrNotFound, so errors.Is(err, ErrNotFound) also holds.
var ErrRelationshipNotFound = fmt.Errorf("relationship %w", ErrNotFound)

// VersionConflictError is the detailed form of ErrVersionConflict. It
// carries the expected and the current (locked) document version so that
// callers can report both.
type VersionConflictError struct {
	DocumentID      uuid.UUID
	ExpectedVersion int64
	CurrentVersion  int64
}

// NewVersionConflict returns a *VersionConflictError.
func NewVersionConflict(documentID uuid.UUID, expected, current int64) error {
	return &VersionConflictError{DocumentID: documentID, ExpectedVersion: expected, CurrentVersion: current}
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("%v: document %s has version %d, expected %d",
		ErrVersionConflict, e.DocumentID, e.CurrentVersion, e.ExpectedVersion)
}

// Unwrap makes errors.Is(err, ErrVersionConflict) work.
func (e *VersionConflictError) Unwrap() error { return ErrVersionConflict }
