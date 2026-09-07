package scenario

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// schemaResourceURL is the $id of the embedded schema; the compiler needs it
// as the resource name to compile against.
const schemaResourceURL = "https://github.com/foxzi/baton/scenario.schema.json"

// compileSchema compiles the embedded schema from memory, once per call.
func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(Schema()))
	if err != nil {
		t.Fatalf("unmarshal schema json: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaResourceURL, doc); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	sch, err := c.Compile(schemaResourceURL)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return sch
}

// toInstance turns scenario YAML into the JSON-compatible value the
// validator expects: it decodes YAML into a generic value (map keys already
// come out as strings with yaml.v3) and round-trips it through JSON so that
// numbers and nested values match what the validator sees for real documents.
func toInstance(t *testing.T, src string) any {
	t.Helper()
	var doc any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatalf("unmarshal yaml: %v", err)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal to json: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	return inst
}

// TestSchema_Compiles checks that Schema() is valid JSON and compiles as a
// draft 2020-12 schema.
func TestSchema_Compiles(t *testing.T) {
	var doc any
	if err := json.Unmarshal(Schema(), &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	compileSchema(t)
}

// TestSchema_AcceptsExamples checks that every example scenario shipped in
// examples/ validates against the schema. It globs the directory so that
// examples added later are covered automatically.
func TestSchema_AcceptsExamples(t *testing.T) {
	sch := compileSchema(t)

	files, err := filepath.Glob("../../examples/*.yaml")
	if err != nil {
		t.Fatalf("glob examples: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no example files found under examples/*.yaml")
	}

	for _, f := range files {
		f := f
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			inst := toInstance(t, string(data))
			if err := sch.Validate(inst); err != nil {
				t.Errorf("%s does not validate against the schema:\n%v", f, err)
			}
		})
	}
}

// validCase is a small scenario document that a format feature accepts.
// skipParseReason, when set, documents why scenario.Parse legitimately
// disagrees with the schema for this document, so TestSchema_MatchesParser
// skips it instead of asserting agreement.
type validCase struct {
	name            string
	yaml            string
	skipParseReason string
}

// validCases covers one document per scenario format feature (docs/ru/spec.md
// section 3). Each is otherwise minimal so a failure points at the feature
// under test.
var validCases = []validCase{
	{
		name: "bare-string run body",
		yaml: `
version: 1
steps:
  - id: s
    run: echo hi
`,
	},
	{
		name: "run body with env secret refs and readonly",
		yaml: `
version: 1
secrets:
  tok:
    from: env
    key: TOKEN
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
      env:
        TOKEN:
          secret: tok
      readonly: true
`,
	},
	{
		name: "http operation form with args and dedupe_key",
		yaml: `
version: 1
apis:
  forge:
    pack: gitlab
    from: ../apis/
steps:
  - id: s
    dedupe_key: "dk-{{ .run.id }}"
    http:
      op: forge.get_change
      args:
        project: "x"
`,
	},
	{
		name: "http raw form with api/method/path/headers/query/body/expect_status/parse",
		yaml: `
version: 1
apis:
  forge:
    pack: gitlab
    from: ../apis/
steps:
  - id: s
    http:
      api: forge
      method: GET
      path: "/foo"
      headers:
        X-Test: "1"
      query:
        q: "1"
      body: "{}"
      expect_status: [200]
      parse: json
`,
	},
	{
		name: "assert step",
		yaml: `
version: 1
steps:
  - id: s
    assert:
      condition: "1 == 1"
      message: "unreachable"
`,
	},
	{
		name: "apis auth as { secret: name }",
		yaml: `
version: 1
apis:
  forge:
    pack: gitlab
    from: ../apis/
    auth: { secret: gitlab_rw }
steps:
  - id: s
    run: echo hi
`,
	},
	{
		name: "defaults and budget blocks",
		yaml: `
version: 1
defaults:
  engine: claudecode
  model: sonnet
  timeout: 5m
  budget_usd: 1.5
budget:
  usd: 10
  time: 1h
steps:
  - id: s
    run: echo hi
`,
	},
	{
		name: "on_failure steps",
		yaml: `
version: 1
steps:
  - id: s
    run: echo hi
on_failure:
  - id: cleanup
    run: echo bye
`,
	},
	{
		name: "step with retry/on_error/fallback/when/needs/cache/timeout",
		yaml: `
version: 1
steps:
  - id: a
    run: echo hi
  - id: b
    when: "steps.a.exit_code == 0"
    needs: [a]
    timeout: 30s
    cache: true
    retry:
      attempts: 2
      backoff: 1s
      on: ["transient"]
    on_error: fallback
    fallback:
      id: fb
      run: echo fallback
    run: echo hi
`,
	},
	{
		name: "llm step as an object",
		yaml: `
version: 1
steps:
  - id: s
    llm:
      model: anthropic/claude-haiku-4-5
      prompt: "hi"
      schema: schemas/x.json
`,
	},
	{
		name: "llm step with every field set",
		yaml: `
version: 1
steps:
  - id: classify
    llm:
      model: anthropic/claude-haiku-4-5
      fallback_models:
        - openrouter/google/gemini-2.5-flash
        - openai/gpt-4.1-mini
      system: prompts/classify.system.md
      prompt: prompts/classify.md
      with: { diff: "{{ .steps.diff.stdout }}" }
      schema: schemas/classify.json
      tools: [apis.jira.get_issue]
      max_tokens: 2000
      temperature: 0
      structured_mode: native
`,
	},
	{
		name: "foreach step as an object",
		yaml: `
version: 1
steps:
  - id: s
    foreach:
      items: "{{ .inputs.list }}"
      step: { run: echo hi }
`,
	},
	{
		name: "foreach step with every field set",
		yaml: `
version: 1
inputs:
  repos:
    type: list
steps:
  - id: per_repo
    foreach:
      items: "{{ .inputs.repos }}"
      as: repo
      max_parallel: 3
      on_item_error: fail
      min_success: 1.0
      step: { llm: { model: anthropic/claude-haiku-4-5, prompt: "hi", schema: schemas/x.json } }
`,
	},
	{
		name: "switch step with cases",
		yaml: `
version: 1
steps:
  - switch: "inputs.mode"
    cases:
      a: { id: a1, run: echo a }
      b: { id: b1, run: echo b }
    default: { id: d1, run: echo default }
`,
	},
}

// TestSchema_AcceptsValidScenarios checks that the schema accepts one small
// scenario per format feature.
func TestSchema_AcceptsValidScenarios(t *testing.T) {
	sch := compileSchema(t)
	for _, c := range validCases {
		t.Run(c.name, func(t *testing.T) {
			inst := toInstance(t, c.yaml)
			if err := sch.Validate(inst); err != nil {
				t.Errorf("expected the schema to accept this document:\n%v", err)
			}
		})
	}
}

// invalidCase is a small scenario document the schema must reject.
// matchParser, when true, asserts that scenario.Parse also fails on this
// document (TestSchema_MatchesParser); when false, skipReason explains why
// the schema and the parser legitimately disagree here.
type invalidCase struct {
	name        string
	yaml        string
	matchParser bool
	skipReason  string
}

var invalidCases = []invalidCase{
	{
		name: "missing version",
		yaml: `
steps:
  - id: s
    run: echo hi
`,
		skipReason: `version defaults to 0 on decode; "version: must be 1" is a Validate check, not a Parse error`,
	},
	{
		name: "version 2",
		yaml: `
version: 2
steps:
  - id: s
    run: echo hi
`,
		skipReason: `version is a plain int field; Validate, not Parse, rejects a value other than 1`,
	},
	{
		name: "missing steps",
		yaml: `
version: 1
`,
		skipReason: `an absent steps key decodes to a nil slice; Validate reports "must declare at least one step"`,
	},
	{
		name: "step without id",
		yaml: `
version: 1
steps:
  - run: echo hi
`,
		skipReason: `id defaults to "" on decode; Validate reports "must not be empty"`,
	},
	{
		name: "apis auth as a plain string",
		yaml: `
version: 1
apis:
  forge:
    pack: gitlab
    from: ../apis/
    auth: gitlab_rw
steps:
  - id: s
    run: echo hi
`,
		// Only a step overrides the secret by name (http.auth); an apis entry
		// always spells it out as { secret: name }.
		matchParser: true,
	},
	{
		name: "step id with a dash",
		yaml: `
version: 1
steps:
  - id: "bad-id"
    run: echo hi
`,
		skipReason: "the id pattern is checked by Validate, not by Step.UnmarshalYAML",
	},
	{
		name: "step with both run and http",
		yaml: `
version: 1
steps:
  - id: s
    run: echo hi
    http:
      op: a.b
`,
		skipReason: `Step.UnmarshalYAML only rejects unknown keys; "must declare exactly one body" is a Validate check`,
	},
	{
		name: "step with no body at all",
		yaml: `
version: 1
steps:
  - id: s
`,
		skipReason: `a step with every body field nil still decodes; Validate reports the missing body`,
	},
	{
		name: "unknown top-level key",
		yaml: `
version: 1
foo: bar
steps:
  - id: s
    run: echo hi
`,
		matchParser: true,
	},
	{
		name: "unknown key inside run",
		yaml: `
version: 1
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
      bogus: 1
`,
		matchParser: true,
	},
	{
		name: "unknown key inside http",
		yaml: `
version: 1
steps:
  - id: s
    http:
      op: a.b
      bogus: 1
`,
		matchParser: true,
	},
	{
		name: "secret with from: env and no key",
		yaml: `
version: 1
secrets:
  tok:
    from: env
steps:
  - id: s
    run: echo hi
`,
		skipReason: `Secret has no UnmarshalYAML; Validate reports "from: env needs key"`,
	},
	{
		name: "secret with from: file and no path",
		yaml: `
version: 1
secrets:
  tok:
    from: file
steps:
  - id: s
    run: echo hi
`,
		skipReason: `Secret has no UnmarshalYAML; Validate reports "from: file needs path"`,
	},
	{
		name: "api entry without from",
		yaml: `
version: 1
apis:
  forge:
    pack: gitlab
steps:
  - id: s
    run: echo hi
`,
		skipReason: `From defaults to ""; Validate reports "must name a pack source"`,
	},
	{
		name: "http step with both op and url",
		yaml: `
version: 1
steps:
  - id: s
    http:
      op: a.b
      url: "https://example.com"
`,
		skipReason: `HTTPStep.UnmarshalYAML only rejects unknown keys; Validate reports "op cannot be combined with api or url"`,
	},
	{
		name: "http step with op and headers",
		yaml: `
version: 1
steps:
  - id: s
    http:
      op: a.b
      headers:
        X-Test: "1"
`,
		skipReason: "headers alongside op decodes fine; the raw-only fields are only checked structurally by the schema",
	},
	{
		name: "http step with neither op nor api nor url",
		yaml: `
version: 1
steps:
  - id: s
    http:
      method: GET
`,
		skipReason: `Validate reports "must set op, api or url"; HTTPStep decodes regardless`,
	},
	{
		name: "invalid duration string",
		yaml: `
version: 1
defaults:
  timeout: "10x"
steps:
  - id: s
    run: echo hi
`,
		matchParser: true,
	},
	{
		name: "invalid size string",
		yaml: `
version: 1
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
      max_output_bytes: "10x"
`,
		matchParser: true,
	},
	{
		name: "on_error with an unknown value",
		yaml: `
version: 1
steps:
  - id: s
    on_error: bogus
    run: echo hi
`,
		skipReason: `OnError is a plain string field; Validate reports the unknown value, not Parse`,
	},
	{
		name: "parse with an unknown value",
		yaml: `
version: 1
steps:
  - id: s
    run:
      argv: ["echo", "hi"]
      parse: xml
`,
		skipReason: `ParseMode is a plain string field; Validate reports the unknown value, not Parse`,
	},
	{
		name: "llm missing schema",
		yaml: `
version: 1
steps:
  - id: s
    llm:
      model: anthropic/claude-haiku-4-5
      prompt: "hi"
`,
		skipReason: `schema requires the schema field; LLMStep.UnmarshalYAML has no requiredness check, Validate reports "must not be empty"`,
	},
	{
		name: "llm with an unknown field",
		yaml: `
version: 1
steps:
  - id: s
    llm:
      schema: schemas/x.json
      bogus: 1
`,
		matchParser: true,
	},
	{
		name: "llm structured_mode outside the enum",
		yaml: `
version: 1
steps:
  - id: s
    llm:
      schema: schemas/x.json
      structured_mode: bogus
`,
		skipReason: `StructuredMode is a plain string field; Validate reports the unknown value, not Parse`,
	},
	{
		name: "foreach missing step",
		yaml: `
version: 1
steps:
  - id: s
    foreach:
      items: "{{ .inputs.list }}"
`,
		skipReason: `schema requires the step field; ForeachStep.UnmarshalYAML has no requiredness check, Validate reports "must declare a body"`,
	},
	{
		name: "foreach with on_item_error outside the enum",
		yaml: `
version: 1
steps:
  - id: s
    foreach:
      items: "{{ .inputs.list }}"
      step: { run: echo hi }
      on_item_error: bogus
`,
		skipReason: `ItemErrorMode is a plain string field; Validate reports the unknown value, not Parse`,
	},
}

// TestSchema_RejectsInvalidScenarios checks that the schema rejects one
// broken document per validation rule.
func TestSchema_RejectsInvalidScenarios(t *testing.T) {
	sch := compileSchema(t)
	for _, c := range invalidCases {
		t.Run(c.name, func(t *testing.T) {
			inst := toInstance(t, c.yaml)
			if err := sch.Validate(inst); err == nil {
				t.Error("expected the schema to reject this document, it did not")
			}
		})
	}
}

// TestSchema_MatchesParser checks that the schema and scenario.Parse agree
// where the task requires them to: everything the schema accepts must also
// parse, and a document the schema rejects for an unknown field or an
// unknown enum value must also fail Parse. Cases that legitimately disagree
// (Parse defers a check to Validate, or accepts a shorthand the parser does
// not implement) are skipped with the reason recorded on the case.
func TestSchema_MatchesParser(t *testing.T) {
	sch := compileSchema(t)

	t.Run("valid", func(t *testing.T) {
		for _, c := range validCases {
			t.Run(c.name, func(t *testing.T) {
				if c.skipParseReason != "" {
					t.Skip(c.skipParseReason)
				}
				inst := toInstance(t, c.yaml)
				if err := sch.Validate(inst); err != nil {
					t.Fatalf("schema unexpectedly rejected this document: %v", err)
				}
				if _, err := Parse([]byte(c.yaml), "test.yaml"); err != nil {
					t.Errorf("schema accepted this document but Parse failed: %v", err)
				}
			})
		}
	})

	t.Run("invalid", func(t *testing.T) {
		for _, c := range invalidCases {
			t.Run(c.name, func(t *testing.T) {
				if !c.matchParser {
					t.Skip(c.skipReason)
				}
				inst := toInstance(t, c.yaml)
				if err := sch.Validate(inst); err == nil {
					t.Fatal("schema unexpectedly accepted this document")
				}
				if _, err := Parse([]byte(c.yaml), "test.yaml"); err == nil {
					t.Error("schema rejected this document but Parse accepted it")
				}
			})
		}
	})
}
