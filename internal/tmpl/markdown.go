package tmpl

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// md2html renders Markdown into XHTML, the shape the Confluence storage
// format and similar APIs expect. GFM's tables and strikethrough are enabled
// because report scenarios use them; raw HTML input is left disabled, which
// is goldmark's default, so a rendered value cannot smuggle markup into the
// destination page.
func md2html(input string) (string, error) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRendererOptions(html.WithXHTML()),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(input), &buf); err != nil {
		return "", fmt.Errorf("md2html: %w", err)
	}
	return buf.String(), nil
}

// md2text renders Markdown into plain text, for APIs that reject markup
// altogether. It walks the parsed AST directly instead of stripping tags out
// of md2html's output, so headings, lists, code blocks and quotes keep a
// predictable layout instead of collapsing into a single paragraph.
func md2text(input string) (string, error) {
	source := []byte(input)
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	doc := md.Parser().Parse(text.NewReader(source))
	out := blockSequence(doc, source)
	return strings.TrimRight(out, " \t\n"), nil
}

// blockSequence renders every block-level child of parent, one per
// paragraph/heading/list/etc., separated by a single blank line.
func blockSequence(parent ast.Node, source []byte) string {
	var parts []string
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		if t := blockText(c, source); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// blockText renders a single block node to text, without the blank line that
// separates it from its siblings.
func blockText(node ast.Node, source []byte) string {
	switch n := node.(type) {
	case *ast.Heading:
		return inlineText(n, source)
	case *ast.Paragraph:
		return inlineText(n, source)
	case *ast.TextBlock:
		// Tight list items wrap their content in a TextBlock rather than a
		// Paragraph; it is otherwise the same thing for our purposes.
		return inlineText(n, source)
	case *ast.FencedCodeBlock:
		return strings.TrimRight(string(n.Lines().Value(source)), "\n")
	case *ast.CodeBlock:
		return strings.TrimRight(string(n.Lines().Value(source)), "\n")
	case *ast.ThematicBreak:
		return "---"
	case *ast.Blockquote:
		return prefixLines(blockSequence(n, source), "> ")
	case *ast.List:
		return renderList(n, source)
	case *east.Table:
		return renderTable(n, source)
	default:
		return blockSequence(node, source)
	}
}

// renderTable renders a GFM table as one line per row with cells joined by
// " | ". The alignment row is dropped: a plain-text API has nothing to do
// with it, and keeping the cells is what matters, since a report that loses
// its table loses its numbers.
func renderTable(t *east.Table, source []byte) string {
	var rows []string
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, inlineText(cell, source))
		}
		rows = append(rows, strings.Join(cells, " | "))
	}
	return strings.Join(rows, "\n")
}

// renderList renders a list's items with a "- " or "N. " marker. Nested
// lists are indented by two spaces per level, applied by the recursive call
// on the way back up.
func renderList(l *ast.List, source []byte) string {
	index := l.Start
	if index <= 0 {
		index = 1
	}

	var lines []string
	for c := l.FirstChild(); c != nil; c = c.NextSibling() {
		item, ok := c.(*ast.ListItem)
		if !ok {
			continue
		}
		marker := "- "
		if l.IsOrdered() {
			marker = strconv.Itoa(index) + ". "
			index++
		}

		itemLines := listItemLines(item, source)
		if len(itemLines) == 0 {
			itemLines = []string{""}
		}
		lines = append(lines, marker+itemLines[0])
		lines = append(lines, itemLines[1:]...)
	}
	return strings.Join(lines, "\n")
}

// listItemLines renders the block children of a single list item. A nested
// list is indented two spaces relative to the item; anything else is
// rendered as its own text, one line per produced line.
func listItemLines(item *ast.ListItem, source []byte) []string {
	var lines []string
	for c := item.FirstChild(); c != nil; c = c.NextSibling() {
		if sub, ok := c.(*ast.List); ok {
			for _, line := range strings.Split(renderList(sub, source), "\n") {
				lines = append(lines, "  "+line)
			}
			continue
		}
		if t := blockText(c, source); t != "" {
			lines = append(lines, strings.Split(t, "\n")...)
		}
	}
	return lines
}

// prefixLines prepends prefix to every line of s.
func prefixLines(s, prefix string) string {
	if s == "" {
		return prefix
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// inlineText concatenates the rendered text of node's inline children.
func inlineText(node ast.Node, source []byte) string {
	var sb strings.Builder
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		writeInline(&sb, c, source)
	}
	return sb.String()
}

// writeInline appends the plain-text rendering of a single inline node to sb.
func writeInline(sb *strings.Builder, node ast.Node, source []byte) {
	switch n := node.(type) {
	case *ast.Text:
		sb.Write(n.Value(source))
		switch {
		case n.HardLineBreak():
			sb.WriteByte('\n')
		case n.SoftLineBreak():
			sb.WriteByte(' ')
		}
	case *ast.String:
		sb.Write(n.Value)
	case *ast.AutoLink:
		sb.Write(n.URL(source))
	case *ast.Link:
		text := inlineText(n, source)
		dest := string(n.Destination)
		sb.WriteString(text)
		if dest != text {
			sb.WriteString(" (")
			sb.WriteString(dest)
			sb.WriteString(")")
		}
	case *ast.RawHTML:
		// Raw HTML is dropped rather than passed through as literal markup.
	default:
		// Emphasis, strong (Emphasis with Level 2), code spans, images,
		// strikethrough and any other inline container render as the plain
		// text of their children, with no markers of their own.
		for c := node.FirstChild(); c != nil; c = c.NextSibling() {
			writeInline(sb, c, source)
		}
	}
}
