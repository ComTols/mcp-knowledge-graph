package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"

	"mcp-knowledge-graph/internal/knowledge"
)

// Public error codes of the tool contract.
const (
	CodeInvalidInput              = "invalid_input"
	CodeNotFound                  = "not_found"
	CodeRelationshipNotFound      = "relationship_not_found"
	CodeVersionConflict           = "version_conflict"
	CodeCycleDetected             = "cycle_detected"
	CodeRootViolation             = "root_violation"
	CodeLastParentViolation       = "last_parent_violation"
	CodeIntegrityViolation        = "integrity_violation"
	CodeSemanticSearchUnavailable = "semantic_search_unavailable"
	CodeDependencyUnavailable     = "dependency_unavailable"
	CodePermissionDenied          = "permission_denied"
	CodeInternalError             = "internal_error"
)

// PublicToolError is the agent-readable error of a failed tool call. Its
// Error method returns the JSON serialization, which the SDK places as the
// single text content of a result with isError=true.
type PublicToolError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *PublicToolError) Error() string {
	b, err := json.Marshal(e)
	if err != nil {
		// Details contain only plain values; this cannot happen in practice.
		return fmt.Sprintf(`{"code":%q,"message":%q,"retryable":%t}`, e.Code, e.Message, e.Retryable)
	}
	return string(b)
}

// invalidInput returns an invalid_input error with a caller-facing message.
func invalidInput(format string, args ...any) *PublicToolError {
	return &PublicToolError{Code: CodeInvalidInput, Message: fmt.Sprintf(format, args...)}
}

// mapError translates a service error into a sanitized public error.
// Messages of domain errors are composed by this code base from IDs and
// fixed texts; unexpected errors are logged with a request ID and replaced
// by a generic message.
func (h *Handler) mapError(ctx context.Context, tool string, err error) *PublicToolError {
	var pub *PublicToolError
	if errors.As(err, &pub) {
		return pub
	}

	var conflict *knowledge.VersionConflictError
	switch {
	case errors.As(err, &conflict):
		return &PublicToolError{
			Code:      CodeVersionConflict,
			Message:   "Document changed since it was read. Retrieve the current state and retry.",
			Retryable: true,
			Details: map[string]any{
				"document_id":      conflict.DocumentID.String(),
				"expected_version": conflict.ExpectedVersion,
				"current_version":  conflict.CurrentVersion,
			},
		}
	case errors.Is(err, knowledge.ErrVersionConflict):
		return &PublicToolError{Code: CodeVersionConflict, Message: err.Error(), Retryable: true}
	case errors.Is(err, knowledge.ErrInvalidInput):
		return &PublicToolError{Code: CodeInvalidInput, Message: err.Error()}
	case errors.Is(err, knowledge.ErrRelationshipNotFound):
		return &PublicToolError{Code: CodeRelationshipNotFound, Message: err.Error()}
	case errors.Is(err, knowledge.ErrNotFound):
		return &PublicToolError{Code: CodeNotFound, Message: err.Error()}
	case errors.Is(err, knowledge.ErrCycleDetected):
		return &PublicToolError{Code: CodeCycleDetected, Message: err.Error()}
	case errors.Is(err, knowledge.ErrRootViolation):
		return &PublicToolError{Code: CodeRootViolation, Message: err.Error()}
	case errors.Is(err, knowledge.ErrLastParentViolation):
		return &PublicToolError{Code: CodeLastParentViolation, Message: err.Error()}
	case errors.Is(err, knowledge.ErrIntegrityViolation):
		id := h.logInternal(ctx, tool, err)
		return &PublicToolError{
			Code:    CodeIntegrityViolation,
			Message: "The knowledge graph violates a structural invariant. Administrator action is required.",
			Details: map[string]any{"request_id": id},
		}
	case errors.Is(err, knowledge.ErrEmbeddingUnavailable):
		id := h.logInternal(ctx, tool, err)
		return &PublicToolError{
			Code:      CodeSemanticSearchUnavailable,
			Message:   "The embedding provider is unavailable.",
			Retryable: true,
			Details:   map[string]any{"request_id": id},
		}
	case isDependencyFailure(err):
		id := h.logInternal(ctx, tool, err)
		return &PublicToolError{
			Code:      CodeDependencyUnavailable,
			Message:   "A required backend service is unavailable or timed out. Retry later.",
			Retryable: true,
			Details:   map[string]any{"request_id": id},
		}
	default:
		id := h.logInternal(ctx, tool, err)
		return &PublicToolError{
			Code:      CodeInternalError,
			Message:   "Unexpected server error.",
			Retryable: true,
			Details:   map[string]any{"request_id": id},
		}
	}
}

// isDependencyFailure reports timeouts and network errors (e.g. an
// unreachable PostgreSQL server).
func isDependencyFailure(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// logInternal logs an error that is not shown to the caller and returns the
// request ID that correlates the log entry with the public error.
func (h *Handler) logInternal(ctx context.Context, tool string, err error) string {
	id := newRequestID()
	h.logger.ErrorContext(ctx, "tool call failed", slog.String("tool", tool),
		slog.String("request_id", id), slog.String("error", err.Error()))
	return id
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
