// Package knowledge contains the transport- and storage-neutral domain model
// of the hierarchical knowledge database: documents arranged in a directed
// acyclic graph below a single root, their ordered Markdown content blocks,
// source references and the search result format.
//
// The package defines the Repository port that storage adapters implement,
// the Embedder port for embedding providers, typed domain errors and pure
// helper logic (input validation, embedding input construction, path
// enumeration, Markdown rendering). It has no dependency on a database.
package knowledge
