package packs

import (
	"reflect"
	"strings"
	"testing"
)

// validOpsYAML is a minimal, valid ops block appended to pack-level test
// cases that do not care about operation details.
const validOpsYAML = `ops:
  get_thing:
    get: /things/{id}
    params:
      id: { pattern: '^\d+$' }
`

// packYAML builds a pack document with fields inserted between the pack
// header and a default valid ops block, for cases that vary one pack-level
// field at a time.
func packYAML(fields string) string {
	return "pack: demo\nversion: 1\n" + fields + validOpsYAML
}

// packOpsYAML builds a pack document with a caller-supplied ops block, for
// cases that vary operation or parameter fields.
func packOpsYAML(ops string) string {
	return "pack: demo\nversion: 1\n" + ops
}

// TestParseValidPack checks that a pack exercising every HTTP method
// compiles with the expected method, path, readonly flag and transform.
func TestParseValidPack(t *testing.T) {
	data := []byte(`
pack: demo
version: 1
description: Demo pack
ops:
  get_thing:
    get: /things/{id}
    readonly: true
    params:
      id: { pattern: '^\d+$' }
    transform: '{ id }'
  create_thing:
    post: /things
    params:
      name: { in: body }
  replace_thing:
    put: /things/{id}
    params:
      id: { pattern: '^\d+$' }
  patch_thing:
    patch: /things/{id}
    params:
      id: { pattern: '^\d+$' }
  delete_thing:
    delete: /things/{id}
    params:
      id: { pattern: '^\d+$' }
`)
	pack, err := Parse(data, "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	cases := []struct {
		op     string
		method string
		path   string
	}{
		{"get_thing", "GET", "/things/{id}"},
		{"create_thing", "POST", "/things"},
		{"replace_thing", "PUT", "/things/{id}"},
		{"patch_thing", "PATCH", "/things/{id}"},
		{"delete_thing", "DELETE", "/things/{id}"},
	}
	for _, c := range cases {
		op, err := pack.Op(c.op)
		if err != nil {
			t.Fatalf("Op(%q) error = %v", c.op, err)
		}
		if op.Name() != c.op {
			t.Errorf("Op(%q).Name() = %q, want %q", c.op, op.Name(), c.op)
		}
		if op.Method() != c.method {
			t.Errorf("Op(%q).Method() = %q, want %q", c.op, op.Method(), c.method)
		}
		if op.Path() != c.path {
			t.Errorf("Op(%q).Path() = %q, want %q", c.op, op.Path(), c.path)
		}
	}

	getThing, _ := pack.Op("get_thing")
	if !getThing.Readonly {
		t.Errorf("get_thing.Readonly = false, want true")
	}
	if getThing.transform == nil {
		t.Errorf("get_thing.transform = nil, want compiled")
	}
	createThing, _ := pack.Op("create_thing")
	if createThing.Readonly {
		t.Errorf("create_thing.Readonly = true, want false (default)")
	}
}

// TestParseUnknownField checks that strict decoding rejects a field that is
// not part of the pack format.
func TestParseUnknownField(t *testing.T) {
	data := []byte(`
pack: demo
version: 1
bogus: true
ops:
  x:
    get: /a
`)
	_, err := Parse(data, "test.yaml")
	if err == nil {
		t.Fatalf("Parse() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Parse() error = %q, want mention of field %q", err, "bogus")
	}
}

// TestParseValidate runs the structural checks of pack validation against
// minimal packs, one field at a time.
func TestParseValidate(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
		checkOK bool
	}{
		// pack: name and version.
		{
			name:    "pack name empty",
			yaml:    strings.Replace(packYAML(""), "pack: demo", "pack: \"\"", 1),
			wantErr: "is not a pack name",
		},
		{
			name:    "pack name bad characters",
			yaml:    strings.Replace(packYAML(""), "pack: demo", "pack: Demo!", 1),
			wantErr: "is not a pack name",
		},
		{
			name:    "version not 1",
			yaml:    strings.Replace(packYAML(""), "version: 1", "version: 2", 1),
			wantErr: "version: must be 1, got 2",
		},
		{
			name:    "version missing",
			yaml:    strings.Replace(packYAML(""), "version: 1\n", "", 1),
			wantErr: "version: must be 1, got 0",
		},

		// auth.
		{
			name:    "auth header missing name",
			yaml:    packYAML("auth:\n  kind: header\n"),
			wantErr: "auth.name: required for the header scheme",
		},
		{
			name:    "auth query missing name",
			yaml:    packYAML("auth:\n  kind: query\n"),
			wantErr: "auth.name: required for the query scheme",
		},
		{
			name:    "auth bearer ok",
			yaml:    packYAML("auth:\n  kind: bearer\n"),
			checkOK: true,
		},
		{
			name:    "auth path ok",
			yaml:    packYAML("auth:\n  kind: path\n"),
			checkOK: true,
		},
		{
			name:    "auth basic missing config field",
			yaml:    packYAML("auth:\n  kind: basic\n"),
			wantErr: "auth.user: the basic scheme takes the user from config.user",
		},
		{
			name:    "auth basic with config.user",
			yaml:    packYAML("auth:\n  kind: basic\nconfig:\n  user: {}\n"),
			checkOK: true,
		},
		{
			name:    "auth exchange unsupported",
			yaml:    packYAML("auth:\n  kind: exchange\n"),
			wantErr: "auth.kind: exchange authorisation is not supported yet",
		},
		{
			name:    "auth kind unknown",
			yaml:    packYAML("auth:\n  kind: oauth\n"),
			wantErr: "is not an authorisation scheme",
		},
		{
			name:    "auth kind empty",
			yaml:    packYAML("auth: {}\n"),
			wantErr: "auth.kind: required",
		},

		// envelope.
		{
			name:    "envelope error_message without error_when",
			yaml:    packYAML("envelope:\n  error_message: .msg\n"),
			wantErr: "envelope.error_message: only used together with error_when",
		},
		{
			name:    "envelope broken jq",
			yaml:    packYAML("envelope:\n  unwrap: '.['\n"),
			wantErr: "envelope.unwrap:",
		},
		{
			name:    "envelope compiles",
			yaml:    packYAML("envelope:\n  unwrap: .data\n  error_when: '.error != null'\n  error_message: .error\n"),
			checkOK: true,
		},

		// pack-level pagination.
		{
			name:    "pagination page without param",
			yaml:    packYAML("pagination:\n  style: page\n"),
			wantErr: "pagination.param: required for the page style",
		},
		{
			name:    "pagination size_param without size",
			yaml:    packYAML("pagination:\n  style: page\n  param: page\n  size_param: per_page\n"),
			wantErr: "pagination.size: required when size_param is set",
		},
		{
			name:    "pagination offset without param",
			yaml:    packYAML("pagination:\n  style: offset\n"),
			wantErr: "pagination.param: required for the offset style",
		},
		{
			name:    "pagination limit_param without size",
			yaml:    packYAML("pagination:\n  style: offset\n  param: offset\n  limit_param: limit\n"),
			wantErr: "pagination.size: required when limit_param is set",
		},
		{
			name:    "pagination offset with a total",
			yaml:    packYAML("pagination:\n  style: offset\n  param: offset\n  limit_param: limit\n  size: 2\n  total: .total\n  items: .items\n"),
			checkOK: true,
		},
		{
			name:    "pagination cursor without next",
			yaml:    packYAML("pagination:\n  style: cursor\n  param: cursor\n"),
			wantErr: "pagination.next: required for the cursor style",
		},
		{
			name:    "pagination cursor without param",
			yaml:    packYAML("pagination:\n  style: cursor\n  next: .next\n"),
			wantErr: "pagination.param: required for the cursor style",
		},
		{
			name:    "pagination cursor",
			yaml:    packYAML("pagination:\n  style: cursor\n  param: cursor\n  next: .next\n  items: .items\n"),
			checkOK: true,
		},
		{
			name:    "pagination next is not jq",
			yaml:    packYAML("pagination:\n  style: cursor\n  param: cursor\n  next: \"[\"\n"),
			wantErr: "pagination.next",
		},
		{
			name:    "pagination unknown style",
			yaml:    packYAML("pagination:\n  style: bogus\n"),
			wantErr: "is not a pagination style",
		},
		{
			name:    "pagination empty style",
			yaml:    packYAML("pagination: {}\n"),
			wantErr: "pagination.style: required",
		},
		{
			name:    "pagination in path rejected",
			yaml:    packYAML("pagination:\n  style: link_header\n  in: path\n"),
			wantErr: "pagination.in:",
		},
		{
			name:    "pagination negative max_pages",
			yaml:    packYAML("pagination:\n  style: link_header\n  max_pages: -1\n"),
			wantErr: "max_pages: must not be negative",
		},
		{
			name:    "pagination link_header ok",
			yaml:    packYAML("pagination:\n  style: link_header\n"),
			checkOK: true,
		},

		// operations.
		{
			name: "two methods on one op",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    post: /a
`),
			wantErr: "declare exactly one method",
		},
		{
			name: "no method on op",
			yaml: packOpsYAML(`ops:
  x: {}
`),
			wantErr: "declare one of get, post, put, patch or delete",
		},
		{
			name: "path without leading slash",
			yaml: packOpsYAML(`ops:
  x:
    get: things
`),
			wantErr: "must start with /",
		},
		{
			name: "graphql op without a query",
			yaml: packOpsYAML(`ops:
  x:
    kind: graphql
`),
			wantErr: "query: required for a graphql operation",
		},
		{
			name: "graphql op with a missing query file",
			yaml: packOpsYAML(`ops:
  x:
    kind: graphql
    query: q.graphql
`),
			wantErr: "query: open",
		},
		{
			name: "graphql op with a query file outside the pack",
			yaml: packOpsYAML(`ops:
  x:
    kind: graphql
    query: ../q.graphql
`),
			wantErr: "must be a path inside the pack directory",
		},
		{
			name: "graphql op on another method",
			yaml: packOpsYAML(`ops:
  x:
    kind: graphql
    get: /graphql
    query: '{ me { id } }'
`),
			wantErr: "a graphql operation is a POST",
		},
		{
			name: "graphql op with an inline query",
			yaml: packOpsYAML(`ops:
  x:
    kind: graphql
    query: '{ me { id } }'
`),
			checkOK: true,
		},
		{
			name: "op kind unknown",
			yaml: packOpsYAML(`ops:
  x:
    kind: soap
    get: /a
`),
			wantErr: "is not an operation kind",
		},
		{
			name: "op encode invalid",
			yaml: packOpsYAML(`ops:
  x:
    post: /a
    encode: xml
`),
			wantErr: "is not a body encoding",
		},
		{
			name: "op max_bytes negative",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    max_bytes: -1
`),
			wantErr: "max_bytes: must not be negative",
		},
		{
			name: "implements malformed",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    implements: BadFormat
`),
			wantErr: "is not an interface operation",
		},
		{
			name: "implements valid",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    implements: demo/v1.get_thing
`),
			checkOK: true,
		},
		{
			name: "pick and transform conflict",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    pick: [id]
    transform: '{ id }'
`),
			wantErr: "pick and transform are two ways to say the same thing",
		},
		{
			name: "pick malformed field",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    pick: ["Bad-Field"]
`),
			wantErr: "is not a field name",
		},
		{
			name: "paginate true no strategy",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    paginate: true
`),
			wantErr: "neither the operation nor the pack declares a pagination strategy",
		},
		{
			name: "op pagination style page without param",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    paginate: true
    pagination:
      style: page
`),
			wantErr: "ops.x.pagination.param: required for the page style",
		},

		// params.
		{
			name: "param pattern required in path",
			yaml: packOpsYAML(`ops:
  x:
    get: /a/{id}
    params:
      id: {}
`),
			wantErr: "pattern: required for arguments that go into the path or the query",
		},
		{
			name: "param pattern not required in body",
			yaml: packOpsYAML(`ops:
  x:
    post: /a
    params:
      note: { in: body }
`),
			checkOK: true,
		},
		{
			name: "param invalid regexp",
			yaml: packOpsYAML(`ops:
  x:
    get: /a/{id}
    params:
      id: { pattern: '[' }
`),
			wantErr: "params.id: pattern:",
		},
		{
			name: "param unknown in",
			yaml: packOpsYAML(`ops:
  x:
    post: /a
    params:
      note: { in: bogus }
`),
			wantErr: "is not a placement",
		},
		{
			name: "param encode invalid",
			yaml: packOpsYAML(`ops:
  x:
    post: /a
    params:
      note: { in: body, encode: json }
`),
			wantErr: "encode: only path is a value here",
		},
		{
			name: "placeholder without declared param",
			yaml: packOpsYAML(`ops:
  x:
    get: /a/{id}
`),
			wantErr: "the path uses {id}, which is not declared in params",
		},
		{
			name: "param in path without matching placeholder",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    params:
      id: { in: path, pattern: '^\d+$' }
`),
			wantErr: "in: path but the operation path has no {id}",
		},
		{
			name: "param negative max_len",
			yaml: packOpsYAML(`ops:
  x:
    post: /a
    params:
      note: { in: body, max_len: -1 }
`),
			wantErr: "max_len: must not be negative",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml), "test.yaml")
			if c.checkOK {
				if err != nil {
					t.Fatalf("Parse() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Parse() error = nil, want error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Parse() error = %q, want substring %q", err.Error(), c.wantErr)
			}
		})
	}
}

// TestParamPlacementDefaults checks the default argument placement: a path
// placeholder, the query of a GET, the body otherwise, or the form when the
// operation encodes as form.
func TestParamPlacementDefaults(t *testing.T) {
	cases := []struct {
		name  string
		yaml  string
		param string
		want  ParamIn
	}{
		{
			name: "path placeholder",
			yaml: packOpsYAML(`ops:
  x:
    get: /a/{id}
    params:
      id: { pattern: '^\d+$' }
`),
			param: "id",
			want:  InPath,
		},
		{
			name: "GET defaults to query",
			yaml: packOpsYAML(`ops:
  x:
    get: /a
    params:
      q: { pattern: '.*' }
`),
			param: "q",
			want:  InQuery,
		},
		{
			name: "POST defaults to body",
			yaml: packOpsYAML(`ops:
  x:
    post: /a
    params:
      note: {}
`),
			param: "note",
			want:  InBody,
		},
		{
			name: "POST with encode form defaults to form",
			yaml: packOpsYAML(`ops:
  x:
    post: /a
    encode: form
    params:
      note: {}
`),
			param: "note",
			want:  InForm,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pack, err := Parse([]byte(c.yaml), "test.yaml")
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			op, err := pack.Op("x")
			if err != nil {
				t.Fatalf("Op(x) error = %v", err)
			}
			got := op.Params[c.param].In
			if got != c.want {
				t.Errorf("Params[%q].In = %q, want %q", c.param, got, c.want)
			}
		})
	}
}

// TestPickCompilesTransform checks that pick compiles into a transform
// equivalent to picking the named fields.
func TestPickCompilesTransform(t *testing.T) {
	data := []byte(packOpsYAML(`ops:
  x:
    get: /a
    pick: [id, name]
`))
	pack, err := Parse(data, "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	op, err := pack.Op("x")
	if err != nil {
		t.Fatalf("Op(x) error = %v", err)
	}
	if op.transform == nil {
		t.Fatalf("transform = nil, want compiled from pick")
	}
	input := map[string]any{"id": "42", "name": "Widget", "extra": "ignored"}
	got, err := op.Transformed(input)
	if err != nil {
		t.Fatalf("Transformed() error = %v", err)
	}
	want := map[string]any{"id": "42", "name": "Widget"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Transformed() = %#v, want %#v", got, want)
	}
}

// TestPaginationPages checks the page cap default and override.
func TestPaginationPages(t *testing.T) {
	var nilPagination *Pagination
	if got := nilPagination.Pages(); got != DefaultMaxPages {
		t.Errorf("nil.Pages() = %d, want %d", got, DefaultMaxPages)
	}
	zero := &Pagination{}
	if got := zero.Pages(); got != DefaultMaxPages {
		t.Errorf("zero.Pages() = %d, want %d", got, DefaultMaxPages)
	}
	set := &Pagination{MaxPages: 5}
	if got := set.Pages(); got != 5 {
		t.Errorf("set.Pages() = %d, want 5", got)
	}
}

// TestJoinedErrorsReportAllProblems checks that unrelated problems are all
// reported, not just the first one found.
func TestJoinedErrorsReportAllProblems(t *testing.T) {
	data := []byte(strings.Replace(
		packYAML("auth:\n  kind: oauth\n"),
		"version: 1", "version: 2", 1,
	))
	_, err := Parse(data, "test.yaml")
	if err == nil {
		t.Fatalf("Parse() error = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "version: must be 1") {
		t.Errorf("Parse() error = %q, want mention of version", msg)
	}
	if !strings.Contains(msg, "auth.kind:") {
		t.Errorf("Parse() error = %q, want mention of auth.kind", msg)
	}
}

// TestLoadFindsPack checks that Load finds both <name>.yaml and
// <name>/pack.yaml layouts.
func TestLoadFindsPack(t *testing.T) {
	pack, err := Load(Source{From: "testdata", Pack: "demo"})
	if err != nil {
		t.Fatalf("Load(demo) error = %v", err)
	}
	if pack.Pack != "demo" {
		t.Errorf("Load(demo).Pack = %q, want demo", pack.Pack)
	}

	nested, err := Load(Source{From: "testdata", Pack: "nested"})
	if err != nil {
		t.Fatalf("Load(nested) error = %v", err)
	}
	if nested.Pack != "nested" {
		t.Errorf("Load(nested).Pack = %q, want nested", nested.Pack)
	}
}

// TestLoadReadsGraphQLQueryFile checks that a graphql operation may keep its
// document in a file beside the pack.
func TestLoadReadsGraphQLQueryFile(t *testing.T) {
	pack, err := Load(Source{From: "testdata", Pack: "graph"})
	if err != nil {
		t.Fatalf("Load(graph) error = %v", err)
	}
	op, err := pack.Op("search_code")
	if err != nil {
		t.Fatalf("Op(search_code) error = %v", err)
	}
	if !op.IsGraphQL() {
		t.Errorf("IsGraphQL() = false, want true")
	}
	if op.Method() != "POST" {
		t.Errorf("Method() = %q, want POST", op.Method())
	}
	if !strings.Contains(op.Document(), "search(query: $q, type: CODE)") {
		t.Errorf("Document() = %q, want the contents of the query file", op.Document())
	}
}

// TestLoadNameMismatch checks that a pack file whose pack: field disagrees
// with the requested name is rejected.
func TestLoadNameMismatch(t *testing.T) {
	_, err := Load(Source{From: "testdata", Pack: "mismatch"})
	if err == nil {
		t.Fatalf("Load(mismatch) error = nil, want error")
	}
	if !strings.Contains(err.Error(), `declares pack "other"`) {
		t.Errorf("Load(mismatch) error = %q, want mention of the declared name", err)
	}
}

// TestLoadMissingName checks that an empty pack name is rejected.
func TestLoadMissingName(t *testing.T) {
	_, err := Load(Source{From: "testdata"})
	if err == nil {
		t.Fatalf("Load(\"\") error = nil, want error")
	}
	if !strings.Contains(err.Error(), "pack is required") {
		t.Errorf("Load(\"\") error = %q, want mention that pack is required", err)
	}
}

// TestLoadNotFound checks that a pack absent from the directory is
// rejected, and that the error mentions the directory searched.
func TestLoadNotFound(t *testing.T) {
	_, err := Load(Source{From: "testdata", Pack: "doesnotexist"})
	if err == nil {
		t.Fatalf("Load(doesnotexist) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "not found in") || !strings.Contains(err.Error(), "testdata") {
		t.Errorf("Load(doesnotexist) error = %q, want mention of testdata", err)
	}
}

// TestLoadRejectsSourcesWithoutAPin checks that a source which cannot be a
// local directory is rejected until it carries a version pin.
func TestLoadRejectsSourcesWithoutAPin(t *testing.T) {
	cases := []struct {
		source  string
		wantErr string
	}{
		{"github.com/org/baton-apis", "a version pin is required"},
		{"https://example.com/packs", "a version pin is required"},
		{"github.com/org/baton-apis@", `"" is not a version pin`},
		{"github.com/org/baton-apis@-flag", `"-flag" is not a version pin`},
		{"github.com/org/baton-apis@../etc", `"../etc" is not a version pin`},
		{"", "from is required"},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			_, err := Load(Source{From: c.source, Pack: "irrelevant"})
			if err == nil {
				t.Fatalf("Load(%q) error = nil, want error", c.source)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("Load(%q) error = %q, want substring %q", c.source, err, c.wantErr)
			}
		})
	}
}

// TestLoadRejectsBadNames checks that a pack name which is not a plain name
// is rejected.
func TestLoadRejectsBadNames(t *testing.T) {
	for _, name := range []string{"../x", "a/b"} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(Source{From: "testdata", Pack: name})
			if err == nil {
				t.Fatalf("Load(%q) error = nil, want error", name)
			}
			if !strings.Contains(err.Error(), "must be a plain name") {
				t.Errorf("Load(%q) error = %q, want mention that the name must be plain", name, err)
			}
		})
	}
}

// TestEnvelopeUnwrapped checks the envelope's unwrap and error detection.
func TestEnvelopeUnwrapped(t *testing.T) {
	t.Run("no envelope passes through", func(t *testing.T) {
		var e *Envelope
		body := map[string]any{"a": 1}
		got, err := e.Unwrapped(body)
		if err != nil {
			t.Fatalf("Unwrapped() error = %v", err)
		}
		if !reflect.DeepEqual(got, body) {
			t.Errorf("Unwrapped() = %#v, want %#v", got, body)
		}
	})

	t.Run("unwrap extracts a field", func(t *testing.T) {
		e := &Envelope{Unwrap: ".result"}
		if err := e.compile(); err != nil {
			t.Fatalf("compile() error = %v", err)
		}
		got, err := e.Unwrapped(map[string]any{"result": "ok"})
		if err != nil {
			t.Fatalf("Unwrapped() error = %v", err)
		}
		if got != "ok" {
			t.Errorf("Unwrapped() = %#v, want %q", got, "ok")
		}
	})

	t.Run("error_when true reports error_message", func(t *testing.T) {
		e := &Envelope{ErrorWhen: ".failed", ErrorMessage: ".msg"}
		if err := e.compile(); err != nil {
			t.Fatalf("compile() error = %v", err)
		}
		_, err := e.Unwrapped(map[string]any{"failed": true, "msg": "boom"})
		if err == nil {
			t.Fatalf("Unwrapped() error = nil, want error")
		}
		if err.Error() != "boom" {
			t.Errorf("Unwrapped() error = %q, want %q", err.Error(), "boom")
		}
	})

	t.Run("error_when false passes through", func(t *testing.T) {
		e := &Envelope{ErrorWhen: ".failed", Unwrap: ".result"}
		if err := e.compile(); err != nil {
			t.Fatalf("compile() error = %v", err)
		}
		got, err := e.Unwrapped(map[string]any{"failed": false, "result": "ok"})
		if err != nil {
			t.Fatalf("Unwrapped() error = %v", err)
		}
		if got != "ok" {
			t.Errorf("Unwrapped() = %#v, want %q", got, "ok")
		}
	})
}

// TestOpTransformed checks Transformed's passthrough and error cases.
func TestOpTransformed(t *testing.T) {
	t.Run("no transform passes through", func(t *testing.T) {
		op := &Op{}
		got, err := op.Transformed(42)
		if err != nil {
			t.Fatalf("Transformed() error = %v", err)
		}
		if got != 42 {
			t.Errorf("Transformed() = %v, want 42", got)
		}
	})

	t.Run("more than one value errors", func(t *testing.T) {
		code, err := compileJQ("transform", ".[]")
		if err != nil {
			t.Fatalf("compileJQ() error = %v", err)
		}
		op := &Op{transform: code}
		_, err = op.Transformed([]any{1, 2})
		if err == nil {
			t.Fatalf("Transformed() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "more than one value") {
			t.Errorf("Transformed() error = %q, want mention of multiple values", err)
		}
	})

	t.Run("no value errors", func(t *testing.T) {
		code, err := compileJQ("transform", "empty")
		if err != nil {
			t.Fatalf("compileJQ() error = %v", err)
		}
		op := &Op{transform: code}
		_, err = op.Transformed(1)
		if err == nil {
			t.Fatalf("Transformed() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "no value") {
			t.Errorf("Transformed() error = %q, want mention of no value", err)
		}
	})
}

// TestPaginationPageItems checks item extraction, including passthrough
// when the pack does not name a location.
func TestPaginationPageItems(t *testing.T) {
	var unset *Pagination
	page := []any{1, 2, 3}
	got, err := unset.PageItems(page)
	if err != nil {
		t.Fatalf("PageItems() error = %v", err)
	}
	if !reflect.DeepEqual(got, page) {
		t.Errorf("PageItems() = %#v, want %#v", got, page)
	}

	code, err := compileJQ("items", ".list")
	if err != nil {
		t.Fatalf("compileJQ() error = %v", err)
	}
	withItems := &Pagination{items: code}
	got, err = withItems.PageItems(map[string]any{"list": []any{"a", "b"}})
	if err != nil {
		t.Fatalf("PageItems() error = %v", err)
	}
	want := []any{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PageItems() = %#v, want %#v", got, want)
	}
}

// TestPaginationNextCursorUnset checks that NextCursor is a no-op when the
// pack does not declare next.
func TestPaginationNextCursorUnset(t *testing.T) {
	p := &Pagination{}
	got, err := p.NextCursor(map[string]any{"cursor": "abc"})
	if err != nil {
		t.Fatalf("NextCursor() error = %v", err)
	}
	if got != nil {
		t.Errorf("NextCursor() = %#v, want nil", got)
	}
}

// TestTransformIsPure checks that transforms cannot reach the process
// environment or the runner's stdin, per spec section 7.4.2.
func TestTransformIsPure(t *testing.T) {
	t.Setenv("BATON_PACKS_TEST_VAR", "leaked")

	t.Run("env is empty", func(t *testing.T) {
		code, err := compileJQ("transform", "env")
		if err != nil {
			t.Fatalf("compileJQ() error = %v", err)
		}
		op := &Op{transform: code}
		got, err := op.Transformed(nil)
		if err != nil {
			t.Fatalf("Transformed() error = %v", err)
		}
		m, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("Transformed() = %#v (%T), want an empty map", got, got)
		}
		if len(m) != 0 {
			t.Errorf("Transformed() = %#v, want empty, env leaked", m)
		}
	})

	t.Run("env.HOME is null", func(t *testing.T) {
		code, err := compileJQ("transform", "env.HOME")
		if err != nil {
			t.Fatalf("compileJQ() error = %v", err)
		}
		op := &Op{transform: code}
		got, err := op.Transformed(nil)
		if err != nil {
			t.Fatalf("Transformed() error = %v", err)
		}
		if got != nil {
			t.Errorf("Transformed() = %#v, want nil (env.HOME must not resolve)", got)
		}
	})

	t.Run("input is rejected", func(t *testing.T) {
		// gojq refuses input/inputs at compile time when no input iterator
		// is supplied, which is exactly what compileJQ does not supply.
		_, err := compileJQ("transform", "input")
		if err == nil {
			t.Fatalf("compileJQ() error = nil, want error (input must be rejected)")
		}
	})
}

// argsDemoYAML declares one operation exercising every argument placement,
// encoding and constraint used by the BindArgs tests.
const argsDemoYAML = `pack: argsdemo
version: 1
ops:
  create:
    post: /projects/{project}/items/{id}
    encode: form
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
      id: { pattern: '^\d+$' }
      status: { in: query, pattern: '^\w+$', enum: [open, closed] }
      note: { in: body, max_len: 5, required: false }
      flag: { in: form, default: "yes" }
      meta: { in: body, required: false }
`

func argsDemoOp(t *testing.T) *Op {
	t.Helper()
	pack, err := Parse([]byte(argsDemoYAML), "test.yaml")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	op, err := pack.Op("create")
	if err != nil {
		t.Fatalf("Op(create) error = %v", err)
	}
	return op
}

// TestBindArgsSuccess checks placement, default application, explicit-nil
// handling and path escaping in one successful call.
func TestBindArgsSuccess(t *testing.T) {
	op := argsDemoOp(t)
	bound, err := op.BindArgs(map[string]any{
		"project": "group/repo",
		"id":      42,
		"status":  "open",
		"flag":    nil, // explicit nil must be treated as absent
	})
	if err != nil {
		t.Fatalf("BindArgs() error = %v", err)
	}
	if bound.Path["project"] != "group%2Frepo" {
		t.Errorf("Path[project] = %q, want escaped slash", bound.Path["project"])
	}
	if bound.Path["id"] != "42" {
		t.Errorf("Path[id] = %q, want %q (not escaped)", bound.Path["id"], "42")
	}
	if bound.Query["status"] != "open" {
		t.Errorf("Query[status] = %q, want %q", bound.Query["status"], "open")
	}
	if bound.Form["flag"] != "yes" {
		t.Errorf("Form[flag] = %q, want default %q", bound.Form["flag"], "yes")
	}
	if _, ok := bound.Body["note"]; ok {
		t.Errorf("Body[note] present, want absent (not supplied, no default)")
	}
}

// TestBindArgsRequiredMissing checks that a missing required argument names
// itself in the error.
func TestBindArgsRequiredMissing(t *testing.T) {
	op := argsDemoOp(t)
	_, err := op.BindArgs(map[string]any{
		"project": "group/repo",
		"id":      "42",
	})
	if err == nil {
		t.Fatalf("BindArgs() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "args.status: required") {
		t.Errorf("BindArgs() error = %q, want mention of args.status", err)
	}
}

// TestBindArgsUnknownArgument checks that an argument the operation does
// not declare is rejected.
func TestBindArgsUnknownArgument(t *testing.T) {
	op := argsDemoOp(t)
	_, err := op.BindArgs(map[string]any{
		"project": "group/repo",
		"id":      "42",
		"status":  "open",
		"bogus":   "x",
	})
	if err == nil {
		t.Fatalf("BindArgs() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "args.bogus") || !strings.Contains(err.Error(), "no such argument") {
		t.Errorf("BindArgs() error = %q, want mention of the unknown argument", err)
	}
}

// TestBindArgsConstraints checks the pattern, max_len and enum checks.
func TestBindArgsConstraints(t *testing.T) {
	base := map[string]any{
		"project": "group/repo",
		"id":      "42",
		"status":  "open",
	}
	withOverride := func(key string, value any) map[string]any {
		args := map[string]any{}
		for k, v := range base {
			args[k] = v
		}
		args[key] = value
		return args
	}

	cases := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{
			name:    "pattern mismatch",
			args:    withOverride("status", "bad status"),
			wantErr: "args.status: does not match",
		},
		{
			name:    "enum violation",
			args:    withOverride("status", "weird"),
			wantErr: "args.status: must be one of open, closed",
		},
		{
			name:    "max_len exceeded",
			args:    withOverride("note", "toolong"),
			wantErr: "args.note: 7 bytes exceeds max_len 5",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			op := argsDemoOp(t)
			_, err := op.BindArgs(c.args)
			if err == nil {
				t.Fatalf("BindArgs() error = nil, want error")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("BindArgs() error = %q, want substring %q", err, c.wantErr)
			}
		})
	}
}

// TestBindArgsMapValue checks that a map value is rejected for a
// text-constrained query argument but accepted for an unconstrained body
// argument.
func TestBindArgsMapValue(t *testing.T) {
	t.Run("map for query argument errors", func(t *testing.T) {
		op := argsDemoOp(t)
		_, err := op.BindArgs(map[string]any{
			"project": "group/repo",
			"id":      "42",
			"status":  map[string]any{"a": 1},
		})
		if err == nil {
			t.Fatalf("BindArgs() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "args.status") {
			t.Errorf("BindArgs() error = %q, want mention of args.status", err)
		}
	})

	t.Run("map for body argument is fine", func(t *testing.T) {
		op := argsDemoOp(t)
		meta := map[string]any{"a": 1}
		bound, err := op.BindArgs(map[string]any{
			"project": "group/repo",
			"id":      "42",
			"status":  "open",
			"meta":    meta,
		})
		if err != nil {
			t.Fatalf("BindArgs() error = %v", err)
		}
		if !reflect.DeepEqual(bound.Body["meta"], meta) {
			t.Errorf("Body[meta] = %#v, want %#v", bound.Body["meta"], meta)
		}
	})
}

// TestScalarRendering checks how scalar renders argument values as request
// text.
func TestScalarRendering(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"int", 7, "7"},
		{"whole float64", 2.0, "2"},
		{"fractional float64", 2.5, "2.5"},
		{"string", "abc", "abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := scalar("x", c.value)
			if err != nil {
				t.Fatalf("scalar() error = %v", err)
			}
			if got != c.want {
				t.Errorf("scalar(%#v) = %q, want %q", c.value, got, c.want)
			}
		})
	}
}

// TestExpandPath checks placeholder substitution and the error naming a
// missing placeholder.
func TestExpandPath(t *testing.T) {
	op := argsDemoOp(t)

	t.Run("substitutes placeholders", func(t *testing.T) {
		got, err := op.ExpandPath(map[string]string{
			"project": "group%2Frepo",
			"id":      "42",
		})
		if err != nil {
			t.Fatalf("ExpandPath() error = %v", err)
		}
		want := "/projects/group%2Frepo/items/42"
		if got != want {
			t.Errorf("ExpandPath() = %q, want %q", got, want)
		}
	})

	t.Run("missing value names the placeholder", func(t *testing.T) {
		_, err := op.ExpandPath(map[string]string{
			"project": "group",
		})
		if err == nil {
			t.Fatalf("ExpandPath() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "{id}") {
			t.Errorf("ExpandPath() error = %q, want mention of {id}", err)
		}
	})
}
