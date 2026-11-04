package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// NormalizeText returns the stable form of s used as embedding input:
// Unicode NFC and LF line endings. Stored Markdown is never rewritten; the
// normalization only applies to what is sent to the embedding model.
func NormalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return norm.NFC.String(s)
}

// DocumentEmbeddingInput builds the exact embedding input for a document's
// title and summary. The hierarchy is deliberately not part of it, so that
// reorganizing the graph never requires re-embedding.
func DocumentEmbeddingInput(title, summary string) string {
	return NormalizeText("Title: " + title + "\nSummary: " + summary)
}

// BlockEmbeddingInput builds the embedding input of a content block.
func BlockEmbeddingInput(content string) string {
	return NormalizeText(content)
}

// InputHash returns the hex encoded SHA-256 of an embedding input.
func InputHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}
