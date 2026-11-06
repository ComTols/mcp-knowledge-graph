package knowledge

import "github.com/google/uuid"

// ClassSecondaryParent is the Cytoscape class of edges that represent a
// non-primary parent relation. The primary parent is expressed through the
// compound-node field data.parent of the child instead of an edge.
const ClassSecondaryParent = "secondary-parent"

// Graph is the complete knowledge graph in the JSON shape of Cytoscape.js:
// the value can be passed to cytoscape({...graph}) without conversion.
type Graph struct {
	Elements GraphElements `json:"elements"`
}

// GraphElements holds the nodes and edges of a Graph. Both slices are never
// nil so they always serialize as JSON arrays.
type GraphElements struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// GraphNode is one document.
type GraphNode struct {
	Data GraphNodeData `json:"data"`
}

// GraphNodeData is the data of a node. Parent is the id of the primary
// parent (Cytoscape compound parent) and empty for the root document.
type GraphNodeData struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	// Summary is the short document summary (shown on hover in the UI).
	Summary string     `json:"summary"`
	Parent  *uuid.UUID `json:"parent,omitempty"`
}

// GraphEdge is a non-primary parent relation, directed from the parent
// (source) to the child (target).
type GraphEdge struct {
	Data    GraphEdgeData `json:"data"`
	Classes string        `json:"classes"`
}

// GraphEdgeData is the data of an edge.
type GraphEdgeData struct {
	ID     string    `json:"id"`
	Source uuid.UUID `json:"source"`
	Target uuid.UUID `json:"target"`
}
