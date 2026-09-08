package packs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeForgePack writes a pack file (and optionally an examples/ directory
// beside it) to dir/name.yaml, returning the parsed pack. ops is the ops
// block of the pack, including the leading "ops:" line.
func writeForgePack(t *testing.T, dir, name, ops string) *Pack {
	t.Helper()
	data := "pack: " + name + "\nversion: 1\n" + ops
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write pack: %v", err)
	}
	pack, err := Parse([]byte(data), path)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return pack
}

// writeExample writes an examples/<op>.json file beside a pack loaded from
// dir, creating the examples directory if needed.
func writeExample(t *testing.T, dir, op, body string) {
	t.Helper()
	examplesDir := filepath.Join(dir, "examples")
	if err := os.MkdirAll(examplesDir, 0o755); err != nil {
		t.Fatalf("mkdir examples: %v", err)
	}
	if err := os.WriteFile(filepath.Join(examplesDir, op+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write example: %v", err)
	}
}

// forgeGetChangeOps is a get_change operation that fully conforms to
// forge/v1: it names its params project and id, and its transform yields a
// result the interface's schema accepts.
const forgeGetChangeOps = `ops:
  get_change:
    get: /projects/{project}/changes/{id}
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
      id: { pattern: '^\d+$' }
    transform: |
      { id, title, files: [ .changes[] | { path: .new_path } ] }
    implements: forge/v1.get_change
`

const forgeGetChangeExample = `{
	"id": 7,
	"title": "Speed things up",
	"changes": [ { "new_path": "main.go" } ]
}`

func TestCheckImplementsValid(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", forgeGetChangeOps)
	writeExample(t, dir, "get_change", forgeGetChangeExample)

	if err := pack.CheckImplements(); err != nil {
		t.Fatalf("CheckImplements() error = %v", err)
	}

	loaded, err := Load(Source{From: dir, Pack: "demo"})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Pack != "demo" {
		t.Errorf("Load().Pack = %q, want demo", loaded.Pack)
	}
}

func TestCheckImplementsUnknownInterface(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", `ops:
  get_change:
    get: /a/{project}
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
    implements: bogus/v1.get_change
`)
	err := pack.CheckImplements()
	if err == nil {
		t.Fatalf("CheckImplements() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unknown interface") {
		t.Errorf("CheckImplements() error = %q, want mention of unknown interface", err)
	}
}

func TestCheckImplementsUnknownInterfaceOp(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", `ops:
  get_change:
    get: /a/{project}
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
    implements: forge/v1.bogus_op
`)
	err := pack.CheckImplements()
	if err == nil {
		t.Fatalf("CheckImplements() error = nil, want error")
	}
	if !strings.Contains(err.Error(), `has no operation "bogus_op"`) {
		t.Errorf("CheckImplements() error = %q, want mention of the unknown operation", err)
	}
}

func TestCheckImplementsMissingRequiredArg(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", `ops:
  get_change:
    get: /projects/{project}/changes
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
    implements: forge/v1.get_change
`)
	writeExample(t, dir, "get_change", forgeGetChangeExample)

	err := pack.CheckImplements()
	if err == nil {
		t.Fatalf("CheckImplements() error = nil, want error")
	}
	if !strings.Contains(err.Error(), `interface argument "id" is missing from params`) {
		t.Errorf("CheckImplements() error = %q, want mention of missing argument id", err)
	}
}

func TestCheckImplementsExtraRequiredParam(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", `ops:
  get_change:
    get: /projects/{project}/changes/{id}
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
      id: { pattern: '^\d+$' }
      token: { pattern: '^\w+$' }
    transform: |
      { id, title, files: [ .changes[] | { path: .new_path } ] }
    implements: forge/v1.get_change
`)
	writeExample(t, dir, "get_change", forgeGetChangeExample)

	err := pack.CheckImplements()
	if err == nil {
		t.Fatalf("CheckImplements() error = nil, want error")
	}
	want := "params.token is required but is not an argument of the interface"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("CheckImplements() error = %q, want %q", err, want)
	}
	if !strings.Contains(err.Error(), "ops.get_change.params.token:") {
		t.Errorf("CheckImplements() error = %q, want the params.token prefix", err)
	}
}

func TestCheckImplementsOptionalArgRequiredByPack(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", `ops:
  get_file:
    get: /projects/{project}/files/{path}/{ref}
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
      path: { pattern: '^[\w./-]+$', encode: path }
      ref: { pattern: '^[\w./-]+$', encode: path }
    transform: '{ content, path }'
    implements: forge/v1.get_file
`)
	writeExample(t, dir, "get_file", `{ "content": "hi", "path": "main.go" }`)

	err := pack.CheckImplements()
	if err == nil {
		t.Fatalf("CheckImplements() error = nil, want error")
	}
	want := `interface argument "ref" is optional but the pack requires it`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("CheckImplements() error = %q, want %q", err, want)
	}
}

func TestCheckImplementsMissingExample(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", forgeGetChangeOps)

	err := pack.CheckImplements()
	if err == nil {
		t.Fatalf("CheckImplements() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "is required") {
		t.Errorf("CheckImplements() error = %q, want mention of \"is required\"", err)
	}
}

func TestCheckImplementsResultDoesNotMatch(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", forgeGetChangeOps)
	// No "files" field: the transform above always sets it (even to an
	// empty list), so drop changes entirely by returning them absent from
	// the recorded body, which the transform still turns into files: [].
	// To trigger the schema failure we transform to a shape lacking title
	// instead, by recording a body without the title field.
	writeExample(t, dir, "get_change", `{ "id": 7, "changes": [] }`)

	err := pack.CheckImplements()
	if err == nil {
		t.Fatalf("CheckImplements() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "does not match the interface") {
		t.Errorf("CheckImplements() error = %q, want mention of the schema mismatch", err)
	}
}

func TestLoadFailsWithPackPathOnMissingExample(t *testing.T) {
	dir := t.TempDir()
	writeForgePack(t, dir, "demo", forgeGetChangeOps)

	_, err := Load(Source{From: dir, Pack: "demo"})
	if err == nil {
		t.Fatalf("Load() error = nil, want error")
	}
	packPath := filepath.Join(dir, "demo.yaml")
	if !strings.Contains(err.Error(), packPath) {
		t.Errorf("Load() error = %q, want it to mention %q", err, packPath)
	}
	if !strings.Contains(err.Error(), "is required") {
		t.Errorf("Load() error = %q, want mention of the missing example", err)
	}
}

func TestParseKeepsWorkingWithoutExamples(t *testing.T) {
	// Parse has no examples directory to check against and must not try:
	// a made-up interface parses fine, exactly as before this feature was
	// added (packs_test.go's "implements valid" case relies on this).
	data := []byte(packOpsYAML(`ops:
  x:
    get: /a
    implements: demo/v1.get_thing
`))
	if _, err := Parse(data, "test.yaml"); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
}

// notifySendOps is a send operation that fully conforms to notify/v1.
const notifySendOps = `ops:
  send:
    post: /notify
    params:
      target: { in: body }
      text: { in: body }
    transform: '{ id, target }'
    implements: notify/v1.send
`

func TestCheckInterfaceComplete(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", notifySendOps)
	writeExample(t, dir, "send", `{ "id": 1, "target": "#general" }`)

	if err := pack.CheckInterface("notify/v1"); err != nil {
		t.Fatalf("CheckInterface() error = %v", err)
	}
}

func TestCheckInterfaceMissingOps(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", forgeGetChangeOps)
	writeExample(t, dir, "get_change", forgeGetChangeExample)

	err := pack.CheckInterface("forge/v1")
	if err == nil {
		t.Fatalf("CheckInterface() error = nil, want error")
	}
	want := "pack demo does not implement forge/v1: missing get_file, list_files, post_comment, post_review"
	if err.Error() != want {
		t.Errorf("CheckInterface() error = %q, want %q", err, want)
	}
}

func TestCheckInterfaceUnknownName(t *testing.T) {
	dir := t.TempDir()
	pack := writeForgePack(t, dir, "demo", forgeGetChangeOps)
	writeExample(t, dir, "get_change", forgeGetChangeExample)

	err := pack.CheckInterface("bogus/v1")
	if err == nil {
		t.Fatalf("CheckInterface() error = nil, want error")
	}
	if !strings.Contains(err.Error(), `unknown interface "bogus/v1"`) {
		t.Errorf("CheckInterface() error = %q, want mention of the unknown interface", err)
	}
}
