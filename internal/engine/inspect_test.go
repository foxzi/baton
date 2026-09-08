package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// toolNames lists the tools by name, in the order StepTools reports them.
func toolNames(t *testing.T, eng *Engine, stepID string) []string {
	t.Helper()
	list, err := eng.StepTools(stepID)
	if err != nil {
		t.Fatalf("StepTools(%q): %v", stepID, err)
	}
	names := make([]string, len(list))
	for i, tool := range list {
		names[i] = tool.Name
	}
	return names
}

// 1. The listing of a step is the tool set of a run plus submit_result: the
// step's commands, the git and state tools its profile opens, and nothing
// else.
func TestStepTools_CommandsStateAndSubmit(t *testing.T) {
	yamlText := `
version: 1
name: inspect-commands
commands:
  hello:
    argv: ["echo", "hi"]
    description: Say hi
    readonly: true
    max_calls: 3
  bye:
    argv: ["echo", "bye"]
    readonly: true
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: fix
      result: result.json
`
	eng, _, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, "calls: []")

	list, err := eng.StepTools("review")
	if err != nil {
		t.Fatalf("StepTools: %v", err)
	}

	names := make([]string, len(list))
	for i, tool := range list {
		names[i] = tool.Name
	}
	want := []string{
		"bye", "hello",
		"fs.read", "fs.glob", "fs.grep", "fs.write",
		"git.status", "git.diff", "git.log", "git.show", "git.blame", "git.commit",
		"state.get", "submit_result",
	}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", names, want)
	}

	// The listing carries what a caller needs to see: the description, the
	// argument schema and the tool's own call cap.
	for _, tool := range list {
		if tool.Name == "hello" {
			if tool.Description != "Say hi" {
				t.Errorf("hello description = %q, want %q", tool.Description, "Say hi")
			}
			if tool.MaxCalls != 3 {
				t.Errorf("hello max_calls = %d, want 3", tool.MaxCalls)
			}
			if !strings.Contains(string(tool.InputSchema), `"type":"object"`) {
				t.Errorf("hello schema = %s, want an object schema", tool.InputSchema)
			}
		}
		if tool.Name == "bye" && tool.MaxCalls != 0 {
			t.Errorf("bye max_calls = %d, want 0: it declares none", tool.MaxCalls)
		}
	}

	// submit_result is last, since the gateway adds it after the step's own
	// tools, and it is described.
	submit := list[len(list)-1]
	if submit.Description == "" || len(submit.InputSchema) == 0 {
		t.Errorf("submit_result = %#v, want a description and a schema", submit)
	}
}

// 2. The profile narrows the listing the same way it narrows a run: review
// runs no commands and never writes the state.
func TestStepTools_NarrowedByProfile(t *testing.T) {
	yamlText := `
version: 1
name: inspect-profiles
commands:
  hello:
    argv: ["echo", "hi"]
    readonly: true
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: review
      result: result.json
  - id: research
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: research
      result: result.json
      tools:
        fetch:
          allow: ["pkg.go.dev"]
          max_calls: 4
`
	eng, _, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, "calls: []")

	wantReview := "fs.read,fs.glob,fs.grep,git.status,git.diff,git.log,git.show,git.blame,state.get,submit_result"
	got := strings.Join(toolNames(t, eng, "review"), ",")
	if got != wantReview {
		t.Errorf("review tools = %s, want %s: the profile runs no commands and cannot commit", got, wantReview)
	}

	list, err := eng.StepTools("research")
	if err != nil {
		t.Fatalf("StepTools(research): %v", err)
	}
	names := make([]string, len(list))
	var fetch *ToolInfo
	for i := range list {
		names[i] = list[i].Name
		if list[i].Name == "fetch" {
			fetch = &list[i]
		}
	}
	want := "fs.read,fs.glob,fs.grep,git.status,git.diff,git.log,git.show,git.blame,state.get,state.set,fetch,submit_result"
	if strings.Join(names, ",") != want {
		t.Fatalf("research tools = %v, want %s", names, want)
	}
	if fetch.MaxCalls != 4 {
		t.Errorf("fetch max_calls = %d, want the step's 4", fetch.MaxCalls)
	}
	if !strings.Contains(fetch.Description, "pkg.go.dev") {
		t.Errorf("fetch description = %q, want the allowed host named", fetch.Description)
	}
}

// 3. An api operation becomes a tool in the listing too, which is the point:
// the step names an operation and the pack decides its schema.
func TestStepTools_APIOperation(t *testing.T) {
	yamlText := `
version: 1
name: inspect-apis
apis:
  gitlab:
    pack: gitlab
    from: ./apis/
    config:
      base_url: "http://example.invalid"
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      result: result.json
      tools:
        apis: [gitlab.get_project]
`
	eng, _, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, "calls: []")
	writePack(t, dir, "gitlab", gitlabLikePack)

	list, err := eng.StepTools("review")
	if err != nil {
		t.Fatalf("StepTools: %v", err)
	}
	var found *ToolInfo
	for i := range list {
		if list[i].Name == "gitlab.get_project" {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("gitlab.get_project is not in the listing: %#v", list)
	}
	if !strings.Contains(string(found.InputSchema), `"id"`) {
		t.Errorf("schema = %s, want the operation's id argument", found.InputSchema)
	}
}

// 4. A step inside a foreach can be asked about: it is a step with an agent
// like any other.
func TestStepTools_InsideForeach(t *testing.T) {
	yamlText := `
version: 1
name: inspect-foreach
steps:
  - id: each
    foreach:
      items: "[1, 2]"
      as: item
      step:
        id: body
        agent:
          engine: fake
          script: script.yaml
          prompt: work
          result: result.json
`
	eng, _, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, "calls: []")

	want := "fs.read,fs.glob,fs.grep,git.status,git.diff,git.log,git.show,git.blame,state.get,submit_result"
	if got := strings.Join(toolNames(t, eng, "body"), ","); got != want {
		t.Errorf("body tools = %s, want %s", got, want)
	}
}

// 5. Asking about a step that is not there, or about a step that is not an
// agent, is an error naming what went wrong.
func TestStepTools_Rejects(t *testing.T) {
	yamlText := `
version: 1
name: inspect-rejects
steps:
  - id: build
    run: "echo hi"
`
	eng, _, _ := newTestEngine(t, yamlText, nil)

	_, err := eng.StepTools("nope")
	if err == nil {
		t.Fatalf("StepTools(nope) succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %v, want the step id named", err)
	}

	_, err = eng.StepTools("build")
	if err == nil {
		t.Fatalf("StepTools(build) succeeded, want an error: a run step has no tools")
	}
	if !strings.Contains(err.Error(), "run") {
		t.Errorf("error = %v, want the step kind named", err)
	}
}

// 6. What a run would reject before starting the agent, the listing rejects
// as well: this is where a missing result schema shows up first.
func TestStepTools_ConfigErrorSurfaces(t *testing.T) {
	yamlText := `
version: 1
name: inspect-config
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      result: missing.json
`
	eng, _, _ := newTestEngine(t, yamlText, nil)

	_, err := eng.StepTools("review")
	if err == nil {
		t.Fatalf("StepTools succeeded, want an error about the result schema")
	}
	stepErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error is %T, want *Error", err)
	}
	if stepErr.Class != ClassConfig {
		t.Errorf("class = %q, want %q", stepErr.Class, ClassConfig)
	}
}

// 7. Listing tools changes nothing on disk: no step directory, no state file.
func TestStepTools_WritesNothing(t *testing.T) {
	yamlText := `
version: 1
name: inspect-readonly
steps:
  - id: review
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: research
      result: result.json
`
	eng, store, dir := newTestEngine(t, yamlText, nil)
	writeAgentFiles(t, dir, agentSchema, "calls: []")

	if _, err := eng.StepTools("review"); err != nil {
		t.Fatalf("StepTools: %v", err)
	}

	// The store creates steps/ when the run directory is opened; what the
	// listing must not create is the step's own directory in it.
	if _, err := os.Stat(filepath.Join(store.Dir(), "steps", "review")); !os.IsNotExist(err) {
		t.Errorf("the run has a directory for the step (%v), want the listing to write nothing", err)
	}
	if _, err := os.Stat(eng.stateFile()); !os.IsNotExist(err) {
		t.Errorf("the state file exists (%v), want the listing to leave it alone", err)
	}
}
