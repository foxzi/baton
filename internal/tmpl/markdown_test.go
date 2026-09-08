package tmpl

import (
	"strings"
	"testing"
)

// TestMD2HTML checks md2html on the Markdown constructs report scenarios
// actually use: a heading, a list, a GFM table, a link, and raw HTML, which
// must not be passed through unescaped.
func TestMD2HTML(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "heading and emphasis",
			text: "# Title\n\nHello **world**\n",
			want: "<h1>Title</h1>\n<p>Hello <strong>world</strong></p>\n",
		},
		{
			name: "unordered list",
			text: "- one\n- two\n",
			want: "<ul>\n<li>one</li>\n<li>two</li>\n</ul>\n",
		},
		{
			name: "gfm table",
			text: "| a | b |\n|---|---|\n| 1 | 2 |\n",
			want: "<table>\n<thead>\n<tr>\n<th>a</th>\n<th>b</th>\n</tr>\n</thead>\n" +
				"<tbody>\n<tr>\n<td>1</td>\n<td>2</td>\n</tr>\n</tbody>\n</table>\n",
		},
		{
			name: "link",
			text: "[link](https://example.com)\n",
			want: "<p><a href=\"https://example.com\">link</a></p>\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := md2html(tc.text)
			if err != nil {
				t.Fatalf("md2html(%q) returned error: %v", tc.text, err)
			}
			if got != tc.want {
				t.Errorf("md2html(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestMD2HTMLRawHTMLDisabled checks that raw HTML embedded in the Markdown
// source is not passed through into the rendered output: a model that puts
// a <script> tag in generated text cannot smuggle it into a Confluence page.
func TestMD2HTMLRawHTMLDisabled(t *testing.T) {
	got, err := md2html("plain <b>bold</b> text\n")
	if err != nil {
		t.Fatalf("md2html returned error: %v", err)
	}
	if strings.Contains(got, "<b>") || strings.Contains(got, "</b>") {
		t.Errorf("md2html output contains raw HTML: %q", got)
	}
}

// TestMD2Text checks md2text against the layout rules: headings and
// paragraphs separated by a blank line, nested bullet lists, an ordered
// list using the list's start, a fenced code block, a block quote, links
// with and without a matching destination, and inline emphasis/strong/code
// spans rendered without their markers.
func TestMD2Text(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "heading and paragraph",
			text: "# Title\n\nSome paragraph text.\n",
			want: "Title\n\nSome paragraph text.",
		},
		{
			name: "nested bullet lists",
			text: "- one\n  - nested a\n  - nested b\n- two\n",
			want: "- one\n  - nested a\n  - nested b\n- two",
		},
		{
			name: "ordered list",
			text: "5. first\n6. second\n7. third\n",
			want: "5. first\n6. second\n7. third",
		},
		{
			name: "fenced code block",
			text: "```\ncode line 1\ncode line 2\n```\n",
			want: "code line 1\ncode line 2",
		},
		{
			name: "block quote",
			text: "> quoted text\n",
			want: "> quoted text",
		},
		{
			name: "link with different text and destination",
			text: "[example](https://example.com)\n",
			want: "example (https://example.com)",
		},
		{
			name: "link whose text equals its destination",
			text: "[https://example.com](https://example.com)\n",
			want: "https://example.com",
		},
		{
			name: "emphasis, strong and code span",
			text: "*em* **strong** `code`\n",
			want: "em strong code",
		},
		{
			name: "gfm table keeps its cells",
			text: "| a | b |\n|---|---|\n| 1 | 2 |\n",
			want: "a | b\n1 | 2",
		},
		{
			name: "empty input",
			text: "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := md2text(tc.text)
			if err != nil {
				t.Fatalf("md2text(%q) returned error: %v", tc.text, err)
			}
			if got != tc.want {
				t.Errorf("md2text(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestRenderMarkdownFunctions checks md2html and md2text through
// Renderer.Render, the same way tmpl_test.go exercises the rest of the
// function set.
func TestRenderMarkdownFunctions(t *testing.T) {
	r := NewRenderer(t.TempDir())

	t.Run("md2text", func(t *testing.T) {
		data := map[string]any{"body": "# Title\n\nHello **world**\n"}
		got, err := r.Render("md2text", `{{ md2text .body }}`, data)
		if err != nil {
			t.Fatalf("Render returned error: %v", err)
		}
		want := "Title\n\nHello world"
		if got != want {
			t.Errorf("Render() = %q, want %q", got, want)
		}
	})

	t.Run("md2html", func(t *testing.T) {
		data := map[string]any{"body": "Hello **world**\n"}
		got, err := r.Render("md2html", `{{ md2html .body }}`, data)
		if err != nil {
			t.Fatalf("Render returned error: %v", err)
		}
		want := "<p>Hello <strong>world</strong></p>\n"
		if got != want {
			t.Errorf("Render() = %q, want %q", got, want)
		}
	})
}
