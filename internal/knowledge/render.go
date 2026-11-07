package knowledge

import (
	"strings"

	"github.com/google/uuid"
)

// ViewerPath is the path of the browser viewer; parent and child links in
// RenderViewerMarkdown point to it.
const ViewerPath = "/ui/viewer"

// RenderViewerMarkdown renders a document deterministically from its
// structured data. It is the reading view of the web viewer and the
// canonical Markdown representation of a document:
//
//	# Title
//
//	Summary
//
//	---
//
//	## Parents          (omitted for the root document)
//
//	- [Parent](<baseURL>/ui/viewer?id=<uuid>)<br>Parent Summary
//
//	## Children         (omitted if there are none)
//
//	- [Child](<baseURL>/ui/viewer?id=<uuid>)<br>Child Summary
//
//	---                 (omitted together with Parents and Children)
//
//	content blocks, Markdown unchanged
//
//	## Sources          (omitted if there are none)
//
//	- [Source title or URI](<uri>)
//
// baseURL is the public origin (and optional path prefix) of the server, for
// example "https://kg.example.com", without trailing slash; empty yields
// root-relative links. Parents are listed primary first, children and
// sources in the order given. Parent and child summaries are always the
// current ones and are never stored in the document. Titles, summaries and
// source URIs are escaped or collapsed to one line so they cannot break the
// structure; block content is passed through unchanged.
func RenderViewerMarkdown(doc Document, baseURL string) string {
	var b strings.Builder

	b.WriteString("# ")
	b.WriteString(singleLine(doc.Title))
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(doc.Summary))
	b.WriteString("\n\n---\n")

	link := func(title string, id uuid.UUID, summary string) {
		b.WriteString("- [")
		b.WriteString(escapeLinkText(title))
		b.WriteString("](")
		b.WriteString(baseURL)
		b.WriteString(ViewerPath)
		b.WriteString("?id=")
		b.WriteString(id.String())
		b.WriteString(")")
		if s := singleLine(summary); s != "" {
			b.WriteString("<br>")
			b.WriteString(s)
		}
		b.WriteString("\n")
	}
	if len(doc.Parents) > 0 {
		b.WriteString("\n## Parents\n\n")
		for _, p := range doc.Parents {
			link(p.Title, p.ID, p.Summary)
		}
	}
	if len(doc.Children) > 0 {
		b.WriteString("\n## Children\n\n")
		for _, c := range doc.Children {
			link(c.Title, c.ID, c.Summary)
		}
	}
	if len(doc.Parents) > 0 || len(doc.Children) > 0 {
		b.WriteString("\n---\n")
	}

	for _, blk := range doc.Blocks {
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(blk.Content, "\r\n"))
		b.WriteString("\n")
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
