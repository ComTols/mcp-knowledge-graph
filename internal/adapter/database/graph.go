package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"mcp-knowledge-graph/internal/knowledge"
)

// GetGraph returns all documents as nodes and all non-primary parent
// relations as edges from one consistent snapshot. The primary parent of a
// document is stored in the node's data.parent.
func (s *Store) GetGraph(ctx context.Context) (knowledge.Graph, error) {
	g := knowledge.Graph{Elements: knowledge.GraphElements{
		Nodes: []knowledge.GraphNode{},
		Edges: []knowledge.GraphEdge{},
	}}
	err := s.inReadTx(ctx, func(tx pgx.Tx) error {
		nodes := map[uuid.UUID]int{}
		rows, err := tx.Query(ctx, `SELECT id, title, summary FROM documents ORDER BY lower(title), id`)
		if err != nil {
			return fmt.Errorf("get graph nodes: %w", err)
		}
		for rows.Next() {
			var n knowledge.GraphNode
			if err := rows.Scan(&n.Data.ID, &n.Data.Name, &n.Data.Summary); err != nil {
				rows.Close()
				return fmt.Errorf("scan graph node: %w", err)
			}
			nodes[n.Data.ID] = len(g.Elements.Nodes)
			g.Elements.Nodes = append(g.Elements.Nodes, n)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("get graph nodes: %w", err)
		}

		rows, err = tx.Query(ctx,
			`SELECT parent_id, child_id, is_primary FROM document_parents ORDER BY child_id, is_primary DESC, parent_id`)
		if err != nil {
			return fmt.Errorf("get graph edges: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var parent, child uuid.UUID
			var primary bool
			if err := rows.Scan(&parent, &child, &primary); err != nil {
				return fmt.Errorf("scan graph edge: %w", err)
			}
			if primary {
				p := parent
				g.Elements.Nodes[nodes[child]].Data.Parent = &p
				continue
			}
			g.Elements.Edges = append(g.Elements.Edges, knowledge.GraphEdge{
				Data: knowledge.GraphEdgeData{
					ID:     parent.String() + ">" + child.String(),
					Source: parent,
					Target: child,
				},
				Classes: knowledge.ClassSecondaryParent,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return knowledge.Graph{}, err
	}
	return g, nil
}
