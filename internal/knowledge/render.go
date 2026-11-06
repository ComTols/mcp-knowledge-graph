package knowledge

import (
	"strings"
)

// RenderMarkdown renders a document deterministically from its structured
// data:
//
//  1. title (H1)
//  2. summary (one paragraph)
//  3. "## Subtopics": direct children with their current summaries
//  4. "## Content": content blocks by position, Markdown unchanged
//  5. "## Sources": external source references
//
// Sections 3 to 5 are omitted when empty. Child links use the symbolic
// document://<uuid> scheme; they are placeholders for a later export step and
// are never derived from titles.
func RenderMarkdown(doc Document) string {
	var b strings.Builder

	b.WriteString("# ")
	b.WriteString(singleLine(doc.Title))
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(doc.Summary))
	b.WriteString("\n")

	if len(doc.Children) > 0 {
		b.WriteString("\n## Subtopics\n\n")
		for _, c := range doc.Children {
			b.WriteString("- [")
			b.WriteString(escapeLinkText(c.Title))
			b.WriteString("](document://")
			b.WriteString(c.ID.String())
			b.WriteString(") — ")
			b.WriteString(singleLine(c.Summary))
			b.WriteString("\n")
		}
	}

	if len(doc.Blocks) > 0 {
		b.WriteString("\n## Content\n")
		for _, blk := range doc.Blocks {
			b.WriteString("\n")
			b.WriteString(strings.TrimRight(blk.Content, "\r\n"))
			b.WriteString("\n")
		}
	}

	if len(doc.Sources) > 0 {
		b.WriteString("\n## Sources\n\n")
		for _, s := range doc.Sources {
			label := s.URI
			if s.Title != nil && strings.TrimSpace(*s.Title) != "" {
				label = *s.Title
			}
			b.WriteString("- [")
			b.WriteString(escapeLinkText(label))
			b.WriteString("](")
			b.WriteString(escapeLinkTarget(s.URI))
			b.WriteString(")\n")
		}
	}
	return b.String()
}

// singleLine collapses all whitespace (including newlines) into single
// spaces so that a value cannot break the surrounding Markdown structure.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

var linkTextEscaper = strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`)

func escapeLinkText(s string) string {
	return linkTextEscaper.Replace(singleLine(s))
}

var linkTargetEscaper = strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29")

func escapeLinkTarget(s string) string {
	return linkTargetEscaper.Replace(strings.TrimSpace(s))
}

// ViewerPath is the path of the browser viewer; parent links in
// RenderViewerMarkdown point to it.
const ViewerPath = "/ui/viewer"

// RenderViewerMarkdown renders the reading view of a document for the web
// viewer:
//
//	# Title
//	Summary
//	---
//	## Parents          (omitted for the root document)
//	- [Parent](<baseURL>/ui/viewer?id=<uuid>)
//	---
//	content blocks, Markdown unchanged
//
// baseURL is the public origin (and optional path prefix) of the server, for
// example "https://kg.example.com", without trailing slash; empty yields
// root-relative links. Parents are listed primary first. Titles are escaped
// so they cannot break the structure; block content is passed through
// unchanged.
func RenderViewerMarkdown(doc Document, baseURL string) string {
	var b strings.Builder

	b.WriteString("# ")
	b.WriteString(singleLine(doc.Title))
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(doc.Summary))
	b.WriteString("\n\n---\n")

	if len(doc.Parents) > 0 {
		b.WriteString("\n## Parents\n\n")
		for _, p := range doc.Parents {
			b.WriteString("- [")
			b.WriteString(escapeLinkText(p.Title))
			b.WriteString("](")
			b.WriteString(baseURL)
			b.WriteString(ViewerPath)
			b.WriteString("?id=")
			b.WriteString(p.ID.String())
			b.WriteString(")\n")
		}
		b.WriteString("\n---\n")
	}

	for _, blk := range doc.Blocks {
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(blk.Content, "\r\n"))
		b.WriteString("\n")
	}
	return b.String()
}
