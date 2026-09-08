package openapi

import (
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/packs"
)

// demoSpec is a small OpenAPI 3 document exercising a path parameter, an
// optional query parameter, a header parameter (which the pack format
// cannot place), a JSON body with a required and an enum field, and an
// apiKey-in-header security scheme.
const demoSpec = `
openapi: 3.0.3
info:
  title: Demo API
  version: "1"
servers:
  - url: https://api.example.com/v1
components:
  securitySchemes:
    apiKeyAuth:
      type: apiKey
      in: header
      name: X-Api-Key
security:
  - apiKeyAuth: []
paths:
  /things/{id}:
    get:
      operationId: getThing
      summary: Get a thing
      parameters:
        - name: id
          in: path
          required: true
          schema: { type: integer }
        - name: verbose
          in: query
          required: false
          schema: { type: boolean }
        - name: X-Trace-Id
          in: header
          required: false
          schema: { type: string }
      responses:
        "200": { description: ok }
  /things:
    post:
      operationId: createThing
      summary: Create a thing
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              properties:
                name: { type: string }
                tags:
                  type: string
                  enum: [a, b]
      responses:
        "201": { description: created }
`

// parsePack runs Import and feeds the result through packs.Parse, failing
// the test with the generated YAML on either error, so a broken skeleton is
// easy to see.
func parsePack(t *testing.T, spec string, opts Options) (*packs.Pack, string) {
	t.Helper()
	out, err := Import([]byte(spec), opts)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	pack, err := packs.Parse(out, "generated.yaml")
	if err != nil {
		t.Fatalf("packs.Parse: %v\n--- generated ---\n%s", err, out)
	}
	return pack, string(out)
}

func TestImportBasicOps(t *testing.T) {
	pack, out := parsePack(t, demoSpec, Options{Ops: []string{"getThing", "createThing"}})

	if pack.Pack != "demo-api" {
		t.Errorf("pack name = %q, want demo-api (slug of the document title)", pack.Pack)
	}
	if pack.Auth == nil || pack.Auth.Kind != packs.AuthHeader || pack.Auth.Name != "X-Api-Key" {
		t.Errorf("auth = %+v, want header X-Api-Key", pack.Auth)
	}
	if got := pack.Config["base_url"]; got.Default != "https://api.example.com/v1" {
		t.Errorf("config.base_url.default = %q, want the server URL", got.Default)
	}

	get, err := pack.Op("get_thing")
	if err != nil {
		t.Fatalf("Op(get_thing): %v", err)
	}
	if get.Method() != "GET" || get.Path() != "/things/{id}" {
		t.Errorf("get_thing method/path = %s %s, want GET /things/{id}", get.Method(), get.Path())
	}
	if got := get.Params["id"]; got == nil || got.Pattern != `^\d+$` {
		t.Errorf("get_thing.params.id = %+v, want an integer pattern", got)
	}
	if got := get.Params["verbose"]; got == nil || got.Pattern != `^(true|false)$` || got.IsRequired() {
		t.Errorf("get_thing.params.verbose = %+v, want an optional boolean pattern", got)
	}
	if !strings.Contains(out, "skipped parameters") || !strings.Contains(out, "X-Trace-Id") {
		t.Errorf("expected a comment naming the skipped header parameter, got:\n%s", out)
	}

	create, err := pack.Op("create_thing")
	if err != nil {
		t.Fatalf("Op(create_thing): %v", err)
	}
	if create.Method() != "POST" || create.Path() != "/things" {
		t.Errorf("create_thing method/path = %s %s, want POST /things", create.Method(), create.Path())
	}
	if got := create.Params["name"]; got == nil || !got.IsRequired() {
		t.Errorf("create_thing.params.name = %+v, want required", got)
	}
	if got := create.Params["tags"]; got == nil || got.IsRequired() {
		t.Errorf("create_thing.params.tags = %+v, want optional (not in the body schema's required list)", got)
	}
	if len(create.Params["tags"].Enum) != 0 {
		t.Errorf("create_thing.params.tags.Enum = %v, body/form fields need no enum, only path/query do", create.Params["tags"].Enum)
	}
}

func TestImportUnknownOperationIdListsKnownOnes(t *testing.T) {
	_, err := Import([]byte(demoSpec), Options{Ops: []string{"noSuchOp"}})
	if err == nil {
		t.Fatal("expected an error for an unknown operationId")
	}
	if !strings.Contains(err.Error(), "createThing") || !strings.Contains(err.Error(), "getThing") {
		t.Errorf("error should list the known operationIds, got: %v", err)
	}
}

func TestImportDuplicateOpRejected(t *testing.T) {
	_, err := Import([]byte(demoSpec), Options{Ops: []string{"getThing", "getThing"}})
	if err == nil {
		t.Fatal("expected an error for a duplicate op request")
	}
}

func TestImportNoOpsRejected(t *testing.T) {
	if _, err := Import([]byte(demoSpec), Options{}); err == nil {
		t.Fatal("expected an error when no ops are requested")
	}
}

func TestImportSwagger2Rejected(t *testing.T) {
	spec := `
swagger: "2.0"
info:
  title: Old API
  version: "1"
paths:
  /things:
    get:
      operationId: listThings
      responses:
        "200": { description: ok }
`
	_, err := Import([]byte(spec), Options{Ops: []string{"listThings"}})
	if err == nil || !strings.Contains(err.Error(), "swagger 2.0") {
		t.Fatalf("expected a swagger 2.0 error, got %v", err)
	}
}

func TestImportObjectQueryParamRejected(t *testing.T) {
	spec := `
openapi: 3.0.3
info: { title: Demo, version: "1" }
paths:
  /things:
    get:
      operationId: listThings
      parameters:
        - name: filter
          in: query
          schema: { type: object }
      responses:
        "200": { description: ok }
`
	_, err := Import([]byte(spec), Options{Ops: []string{"listThings"}})
	if err == nil {
		t.Fatal("expected an error for an object-typed query parameter")
	}
	if !strings.Contains(err.Error(), "filter") {
		t.Errorf("error should name the parameter, got: %v", err)
	}
}

func TestImportMissingTypeQueryParamRejected(t *testing.T) {
	spec := `
openapi: 3.0.3
info: { title: Demo, version: "1" }
paths:
  /things:
    get:
      operationId: listThings
      parameters:
        - name: filter
          in: query
          schema: {}
      responses:
        "200": { description: ok }
`
	_, err := Import([]byte(spec), Options{Ops: []string{"listThings"}})
	if err == nil {
		t.Fatal("expected an error for a query parameter with no schema type")
	}
}

func TestImportFormEncodedBody(t *testing.T) {
	spec := `
openapi: 3.0.3
info: { title: Demo, version: "1" }
paths:
  /things:
    post:
      operationId: createThing
      requestBody:
        content:
          application/x-www-form-urlencoded:
            schema:
              type: object
              required: [name]
              properties:
                name: { type: string }
      responses:
        "201": { description: created }
`
	pack, _ := parsePack(t, spec, Options{Ops: []string{"createThing"}})
	op, err := pack.Op("create_thing")
	if err != nil {
		t.Fatalf("Op(create_thing): %v", err)
	}
	if op.Encode != packs.EncodeForm {
		t.Errorf("encode = %q, want form", op.Encode)
	}
}

func TestImportBasicAuthNeedsUserConfig(t *testing.T) {
	spec := `
openapi: 3.0.3
info: { title: Demo, version: "1" }
components:
  securitySchemes:
    basicAuth: { type: http, scheme: basic }
security:
  - basicAuth: []
paths:
  /things:
    get:
      operationId: listThings
      responses:
        "200": { description: ok }
`
	pack, out := parsePack(t, spec, Options{Ops: []string{"listThings"}})
	if pack.Auth == nil || pack.Auth.Kind != packs.AuthBasic {
		t.Fatalf("auth = %+v, want basic", pack.Auth)
	}
	if _, ok := pack.Config["user"]; !ok {
		t.Errorf("config.user is required for the basic scheme, generated:\n%s", out)
	}
}

func TestImportOAuth2LeavesAuthAsComment(t *testing.T) {
	spec := `
openapi: 3.0.3
info: { title: Demo, version: "1" }
components:
  securitySchemes:
    oauth: { type: oauth2, flows: {} }
security:
  - oauth: []
paths:
  /things:
    get:
      operationId: listThings
      responses:
        "200": { description: ok }
`
	pack, out := parsePack(t, spec, Options{Ops: []string{"listThings"}})
	if pack.Auth != nil {
		t.Errorf("auth = %+v, want none: oauth2 needs a human decision", pack.Auth)
	}
	if !strings.Contains(out, "oauth2") {
		t.Errorf("expected a comment naming oauth2, got:\n%s", out)
	}
}

func TestImportCombinedSchemesLeaveAuthAsComment(t *testing.T) {
	spec := `
openapi: 3.0.3
info: { title: Demo, version: "1" }
components:
  securitySchemes:
    apiKeyAuth: { type: apiKey, in: header, name: X-Api-Key }
    basicAuth: { type: http, scheme: basic }
security:
  - apiKeyAuth: []
    basicAuth: []
paths:
  /things:
    get:
      operationId: listThings
      responses:
        "200": { description: ok }
`
	pack, out := parsePack(t, spec, Options{Ops: []string{"listThings"}})
	if pack.Auth != nil {
		t.Errorf("auth = %+v, want none: several schemes required together need a human decision", pack.Auth)
	}
	if !strings.Contains(out, "several schemes") {
		t.Errorf("expected a comment naming several schemes, got:\n%s", out)
	}
}

func TestImportInterfaceAddsCommentedPlaceholder(t *testing.T) {
	_, out := parsePack(t, demoSpec, Options{Ops: []string{"getThing"}, Interface: "forge/v1"})
	if !strings.Contains(out, "# implements: forge/v1.get_thing") {
		t.Errorf("expected a commented implements placeholder, got:\n%s", out)
	}
}

func TestImportExplicitNameOverridesTitleSlug(t *testing.T) {
	pack, _ := parsePack(t, demoSpec, Options{Ops: []string{"getThing"}, Name: "custom-name"})
	if pack.Pack != "custom-name" {
		t.Errorf("pack name = %q, want custom-name", pack.Pack)
	}
}
