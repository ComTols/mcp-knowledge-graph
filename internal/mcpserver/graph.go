package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"mcp-knowledge-graph/internal/knowledge"
)

// GraphSource provides the data of the HTTP /graph and /graph/fulltext
// endpoints.
type GraphSource interface {
	GetGraph(ctx context.Context) (knowledge.Graph, error)
	GetDocument(ctx context.Context, documentID uuid.UUID) (knowledge.Document, error)
}

// fulltextHandler serves one document as Markdown (reading view for the web
// viewer). The document id is the query parameter "id". Authentication is
// applied by the caller.
func fulltextHandler(src GraphSource, baseURL string, timeout time.Duration, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id, err := uuid.Parse(r.URL.Query().Get("id"))
		if err != nil {
			http.Error(w, "query parameter id must be a document UUID", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		doc, err := src.GetDocument(ctx, id)
		if errors.Is(err, knowledge.ErrNotFound) {
			http.Error(w, "document not found", http.StatusNotFound)
			return
		}
		if err != nil {
			logger.ErrorContext(ctx, "document export failed", slog.String("error", err.Error()))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(knowledge.RenderViewerMarkdown(doc, baseURL)))
	})
}

// graphHandler serves the whole graph as Cytoscape.js JSON. Authentication is
// applied by the caller.
func graphHandler(src GraphSource, timeout time.Duration, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		g, err := src.GetGraph(ctx)
		if err != nil {
			// Details stay in the server log.
			logger.ErrorContext(ctx, "graph export failed", slog.String("error", err.Error()))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(g)
	})
}
