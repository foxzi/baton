package packs

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// itemsDemoYAML declares a list argument whose elements are renamed for the
// wire, the way the gitea pack sends the comments of a review.
const itemsDemoYAML = `pack: itemsdemo
version: 1
ops:
  review:
    post: /reviews
    encode: json
    params:
      comments:
        in: body
        items:
          path: { pattern: '^[^\s?#]+$' }
          line: { name: new_position, pattern: '^\d+$' }
          body: { max_len: 10 }
`

func itemsDemoOp(t *testing.T) *Op {
	t.Helper()
	pack, err := Parse([]byte(itemsDemoYAML), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	op, err := pack.Op("review")
	if err != nil {
		t.Fatalf("Op(review) error = %v", err)
	}
	return op
}

// TestBindArgsItemsRenamed checks that the fields of every element are sent
// under their wire names.
func TestBindArgsItemsRenamed(t *testing.T) {
	op := itemsDemoOp(t)
	bound, err := op.BindArgs(map[string]any{
		"comments": []any{
			map[string]any{"path": "main.go", "line": 12, "body": "nit"},
		},
	})
	if err != nil {
		t.Fatalf("BindArgs() error = %v", err)
	}
	want := []any{map[string]any{"path": "main.go", "new_position": 12, "body": "nit"}}
	if !reflect.DeepEqual(bound.Body["comments"], want) {
		t.Errorf("Body[comments] = %#v, want %#v", bound.Body["comments"], want)
	}
}

// TestBindArgsItemsRejected covers the ways a list argument breaks its
// declared items.
func TestBindArgsItemsRejected(t *testing.T) {
	cases := map[string]struct {
		value any
		want  string
		class bool
	}{
		"unknown field": {
			value: []any{map[string]any{"path": "a", "line": 1, "body": "x", "side": "RIGHT"}},
			want:  "args.comments[0].side: no such field",
		},
		"missing required field": {
			value: []any{map[string]any{"path": "a", "body": "x"}},
			want:  "args.comments[0].line: required",
		},
		"not a list": {
			value: "main.go",
			want:  "items are declared, but the value is string, not a list",
		},
		"not an object": {
			value: []any{"main.go"},
			want:  "args.comments[0]: not an object",
		},
		"constraint": {
			value: []any{map[string]any{"path": "a", "line": "x", "body": "y"}},
			want:  "args.comments[0].line: does not match",
			class: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := itemsDemoOp(t).BindArgs(map[string]any{"comments": tc.value})
			if err == nil {
				t.Fatalf("BindArgs() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("BindArgs() error = %q, want it to contain %q", err, tc.want)
			}
			if got := errors.Is(err, ErrConstraint); got != tc.class {
				t.Errorf("errors.Is(err, ErrConstraint) = %v, want %v", got, tc.class)
			}
		})
	}
}

// TestItemsRejected covers the pack errors of an items declaration.
func TestItemsRejected(t *testing.T) {
	cases := map[string]struct {
		params string
		want   string
	}{
		"query argument": {
			params: "      comments: { in: query, pattern: '^x$', items: { path: {} } }",
			want:   "items: only a body argument is a list of objects",
		},
		"placement": {
			params: "      comments: { in: body, items: { path: { in: query } } }",
			want:   "items.path: in: an item field is always in the element",
		},
		"default": {
			params: "      comments: { in: body, items: { path: { default: a } } }",
			want:   "items.path: default: not a value here",
		},
		"nested": {
			params: "      comments: { in: body, items: { path: { items: { x: {} } } } }",
			want:   "items.path: items: one level only",
		},
		"dotted name": {
			params: "      comments: { in: body, items: { path: { name: a.b } } }",
			want:   "items.path: name: an item field is one key",
		},
		"collision": {
			params: "      comments: { in: body, items: { path: { name: file }, file: {} } }",
			want:   `items.path: name: "file" is already sent for items.file`,
		},
		"pattern": {
			params: "      comments: { in: body, items: { path: { pattern: '(' } } }",
			want:   "items.path: pattern:",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			yaml := "pack: demo\nversion: 1\nops:\n  review:\n    post: /reviews\n    encode: json\n    params:\n" + tc.params + "\n"
			_, err := Parse([]byte(yaml), "test.yaml")
			if err == nil {
				t.Fatalf("Parse() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse() error = %q, want it to contain %q", err, tc.want)
			}
		})
	}
}
