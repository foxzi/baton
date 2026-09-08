package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/httpx"
	"github.com/foxzi/baton/internal/packs"
	"github.com/foxzi/baton/internal/values"
)

// apiPackYAML is a pack with one readonly operation and one that writes, the
// two cases the policy has to tell apart.
const apiPackYAML = `pack: demo
version: 1
config:
  base_url: {}
ops:
  get_thing:
    get: /things/{id}
    description: Fetch one thing
    readonly: true
    params:
      id: { pattern: '^\d+$' }
      fields: { pattern: '^[\w,]+$', required: false, max_len: 40 }
    transform: '{ id, name }'
  post_thing:
    post: /things
    readonly: false
    params:
      name: { max_len: 100, in: body }
`

// testAPI builds the API of apiPackYAML against a server.
func testAPI(t *testing.T, baseURL string) *httpx.API {
	t.Helper()
	pack, err := packs.Parse([]byte(apiPackYAML), "demo.yaml")
	if err != nil {
		t.Fatalf("packs.Parse() error = %v", err)
	}
	api, err := httpx.NewAPI("demo", pack, map[string]string{"base_url": baseURL}, values.Secret{})
	if err != nil {
		t.Fatalf("httpx.NewAPI() error = %v", err)
	}
	return api
}

// resolverFor answers with the same API whatever the name, recording what it
// was asked for.
func resolverFor(api *httpx.API, asked *[]string) func(string) (*httpx.API, error) {
	return func(name string) (*httpx.API, error) {
		*asked = append(*asked, name)
		if api == nil {
			return nil, fmt.Errorf("unknown api %q", name)
		}
		return api, nil
	}
}

// callTool finds a tool by name and calls it.
func callTool(t *testing.T, set *APIs, name, args string) (any, error) {
	t.Helper()
	for _, tool := range set.Tools() {
		if tool.Name == name {
			return tool.Handler(context.Background(), json.RawMessage(args))
		}
	}
	t.Fatalf("tool %q is not in the set", name)
	return nil, nil
}

func TestNewAPIsNoPolicyNoTools(t *testing.T) {
	set, err := NewAPIs(APIOptions{})
	if err != nil {
		t.Fatalf("NewAPIs() error = %v", err)
	}
	if tools := set.Tools(); len(tools) != 0 {
		t.Fatalf("Tools() = %v, want none for a step without apis", tools)
	}
}

func TestNewAPIsToolPerOperation(t *testing.T) {
	api := testAPI(t, "http://example.invalid")
	var asked []string
	set, err := NewAPIs(APIOptions{
		Policy:  agent.Policy{APIs: []string{"demo.get_thing"}},
		Resolve: resolverFor(api, &asked),
	})
	if err != nil {
		t.Fatalf("NewAPIs() error = %v", err)
	}

	tools := set.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools() = %d tools, want 1", len(tools))
	}
	tool := tools[0]
	if tool.Name != "demo.get_thing" {
		t.Errorf("Name = %q, want demo.get_thing", tool.Name)
	}
	if tool.Description != "Fetch one thing" {
		t.Errorf("Description = %q, want the operation's description", tool.Description)
	}
	if len(asked) != 1 || asked[0] != "demo" {
		t.Errorf("resolved %v, want [demo]", asked)
	}

	var schema struct {
		Properties map[string]struct {
			Type      string `json:"type"`
			Pattern   string `json:"pattern"`
			MaxLength int    `json:"maxLength"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("InputSchema is not a schema: %v", err)
	}
	if got := schema.Properties["id"].Pattern; got != `^\d+$` {
		t.Errorf("id.pattern = %q, want the pack's pattern", got)
	}
	if got := schema.Properties["fields"].MaxLength; got != 40 {
		t.Errorf("fields.maxLength = %d, want 40", got)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "id" {
		t.Errorf("required = %v, want [id]: fields is optional", schema.Required)
	}
	if schema.AdditionalProperties {
		t.Error("additionalProperties = true, want the schema closed")
	}
}

func TestNewAPIsStableOrder(t *testing.T) {
	api := testAPI(t, "http://example.invalid")
	var asked []string
	set, err := NewAPIs(APIOptions{
		Policy:  agent.Policy{APIs: []string{"other.get_thing", "demo.get_thing"}},
		Resolve: resolverFor(api, &asked),
	})
	if err != nil {
		t.Fatalf("NewAPIs() error = %v", err)
	}
	tools := set.Tools()
	if len(tools) != 2 || tools[0].Name != "demo.get_thing" || tools[1].Name != "other.get_thing" {
		t.Fatalf("Tools() = %v, want the two operations sorted by name", tools)
	}
}

func TestNewAPIsRejectsWriteOperation(t *testing.T) {
	api := testAPI(t, "http://example.invalid")
	var asked []string
	_, err := NewAPIs(APIOptions{
		Policy:  agent.Policy{APIs: []string{"demo.post_thing"}},
		Resolve: resolverFor(api, &asked),
	})
	if err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("NewAPIs() error = %v, want a complaint about readonly", err)
	}
}

func TestNewAPIsRejectsUnknownOperation(t *testing.T) {
	api := testAPI(t, "http://example.invalid")
	var asked []string
	_, err := NewAPIs(APIOptions{
		Policy:  agent.Policy{APIs: []string{"demo.get_nothing"}},
		Resolve: resolverFor(api, &asked),
	})
	if err == nil {
		t.Fatal("NewAPIs() error = nil, want an unknown operation to be an error")
	}
}

func TestNewAPIsRejectsMalformedEntry(t *testing.T) {
	api := testAPI(t, "http://example.invalid")
	var asked []string
	for _, entry := range []string{"demo", "demo.", ".get_thing", ""} {
		_, err := NewAPIs(APIOptions{
			Policy:  agent.Policy{APIs: []string{entry}},
			Resolve: resolverFor(api, &asked),
		})
		if err == nil {
			t.Errorf("NewAPIs(%q) error = nil, want <api>.<op> to be required", entry)
		}
	}
}

func TestNewAPIsRejectsUnknownAPI(t *testing.T) {
	var asked []string
	_, err := NewAPIs(APIOptions{
		Policy:  agent.Policy{APIs: []string{"nope.get_thing"}},
		Resolve: resolverFor(nil, &asked),
	})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("NewAPIs() error = %v, want the unresolved api named", err)
	}
}

func TestNewAPIsWithoutResolver(t *testing.T) {
	_, err := NewAPIs(APIOptions{Policy: agent.Policy{APIs: []string{"demo.get_thing"}}})
	if err == nil {
		t.Fatal("NewAPIs() error = nil, want a policy without a resolver to be an error")
	}
}

func TestAPIToolCallsTheOperation(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id": 7, "name": "thing", "secret_field": "x"}`)
	}))
	defer server.Close()

	var asked []string
	set, err := NewAPIs(APIOptions{
		Policy:  agent.Policy{APIs: []string{"demo.get_thing"}},
		Resolve: resolverFor(testAPI(t, server.URL), &asked),
	})
	if err != nil {
		t.Fatalf("NewAPIs() error = %v", err)
	}

	out, err := callTool(t, set, "demo.get_thing", `{"id": "7"}`)
	if err != nil {
		t.Fatalf("call error = %v", err)
	}
	if gotPath != "/things/7" {
		t.Errorf("path = %q, want /things/7", gotPath)
	}
	response, ok := out.(APIResponse)
	if !ok {
		t.Fatalf("call returned %T, want APIResponse", out)
	}
	fields, ok := response.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want the transformed object", response.Result)
	}
	if fields["name"] != "thing" {
		t.Errorf("result.name = %#v, want thing", fields["name"])
	}
	if _, present := fields["secret_field"]; present {
		t.Errorf("result = %#v, want the transform applied", fields)
	}
	if response.Truncated {
		t.Error("truncated = true, want false for a short answer")
	}
}

func TestAPIToolRejectsBadArgument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the operation was called with a rejected argument: %s", r.URL.Path)
	}))
	defer server.Close()

	var asked []string
	set, err := NewAPIs(APIOptions{
		Policy:  agent.Policy{APIs: []string{"demo.get_thing"}},
		Resolve: resolverFor(testAPI(t, server.URL), &asked),
	})
	if err != nil {
		t.Fatalf("NewAPIs() error = %v", err)
	}

	if _, err := callTool(t, set, "demo.get_thing", `{"id": "../../etc"}`); err == nil {
		t.Fatal("call error = nil, want the pack's pattern to reject the argument")
	}
	if _, err := callTool(t, set, "demo.get_thing", `{}`); err == nil {
		t.Fatal("call error = nil, want a missing required argument to be an error")
	}
	if _, err := callTool(t, set, "demo.get_thing", `[1]`); err == nil {
		t.Fatal("call error = nil, want arguments that are not an object to be an error")
	}
}

func TestAPIToolReportsTruncation(t *testing.T) {
	pack, err := packs.Parse([]byte(`pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    max_bytes: 20
    transform: '[ .[] | .name ]'
`), "demo.yaml")
	if err != nil {
		t.Fatalf("packs.Parse() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"name":"aaaaaaaaaa"},{"name":"bbbbbbbbbb"},{"name":"cccccccccc"}]`)
	}))
	defer server.Close()

	api, err := httpx.NewAPI("demo", pack, map[string]string{"base_url": server.URL}, values.Secret{})
	if err != nil {
		t.Fatalf("httpx.NewAPI() error = %v", err)
	}
	var asked []string
	set, err := NewAPIs(APIOptions{
		Policy:  agent.Policy{APIs: []string{"demo.list_things"}},
		Resolve: resolverFor(api, &asked),
	})
	if err != nil {
		t.Fatalf("NewAPIs() error = %v", err)
	}

	out, err := callTool(t, set, "demo.list_things", `null`)
	if err != nil {
		t.Fatalf("call error = %v", err)
	}
	response := out.(APIResponse)
	if !response.Truncated {
		t.Errorf("truncated = false, want the max_bytes cut reported: %#v", response.Result)
	}
}

func TestAPIToolReportsCallFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such thing", http.StatusNotFound)
	}))
	defer server.Close()

	var asked []string
	set, err := NewAPIs(APIOptions{
		Policy:  agent.Policy{APIs: []string{"demo.get_thing"}},
		Resolve: resolverFor(testAPI(t, server.URL), &asked),
	})
	if err != nil {
		t.Fatalf("NewAPIs() error = %v", err)
	}

	// A failed call is the agent's answer, not a broken step: the handler
	// reports it and the step goes on.
	if _, err := callTool(t, set, "demo.get_thing", `{"id": "7"}`); err == nil {
		t.Fatal("call error = nil, want the 404 reported to the agent")
	}
}
