package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/scenario"
)

// helperEnvVar makes this test binary run as a third-party MCP server: the
// runner starts such a server as a process of its own, so a test needs one.
const helperEnvVar = "BATON_TEST_ENGINE_MCP_SERVER"

// TestHelperMCPServer is the server the tests below name in mcp_servers. It
// exits without letting the test framework print, since anything on stdout
// other than MCP traffic would break the protocol.
func TestHelperMCPServer(t *testing.T) {
	if os.Getenv(helperEnvVar) == "" {
		t.Skip("helper process, run by the tests in this file")
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test-mcp", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "search",
		Description: "searches the docs",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "found"}}}, nil
	})
	server.AddTool(&mcp.Tool{
		Name:        "open",
		Description: "opens a page",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "opened"}}}, nil
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

// helperMCPConfig declares the helper process as the docs server.
func helperMCPConfig() *config.Config {
	return &config.Config{
		MCPServers: map[string]config.MCPServer{
			"docs": {
				Command: []string{os.Args[0], "-test.run=TestHelperMCPServer"},
				Env:     map[string]scenario.EnvValue{helperEnvVar: {Value: "1"}},
			},
		},
	}
}

const mcpScenario = `
version: 1
name: mcp-step
steps:
  - id: research
    agent:
      engine: fake
      script: script.yaml
      prompt: work
      profile: research
      result: result.json
      tools:
        mcp: [docs]
`

// 1. A step naming an MCP server gets its tools through the gateway, under
// <server>.<tool>, and the ones taking a url are left out (spec 7.6).
func TestAgentMCPTools(t *testing.T) {
	eng, _, dir := newTestEngine(t, mcpScenario, func(o *Options) {
		o.Config = helperMCPConfig()
	})
	writeAgentFiles(t, dir, agentSchema, "calls: []")

	names := toolNames(t, eng, "research")
	if !slices.Contains(names, "docs.search") {
		t.Fatalf("tools = %v, want docs.search", names)
	}
	if slices.Contains(names, "docs.open") {
		t.Fatalf("tools = %v, want docs.open left out: it takes a url and the step has no allow_unsafe", names)
	}
}

// 2. With allow_unsafe the step gets the tools the gateway marks unsafe too.
func TestAgentMCPToolsAllowUnsafe(t *testing.T) {
	yamlText := mcpScenario + `      allow_unsafe: true
`
	eng, _, dir := newTestEngine(t, yamlText, func(o *Options) {
		o.Config = helperMCPConfig()
	})
	writeAgentFiles(t, dir, agentSchema, "calls: []")

	names := toolNames(t, eng, "research")
	for _, want := range []string{"docs.search", "docs.open"} {
		if !slices.Contains(names, want) {
			t.Fatalf("tools = %v, want %s", names, want)
		}
	}
}

// 3. A step naming a server the configuration does not declare is a
// configuration error, and it is reported before the agent starts.
func TestAgentMCPUndeclaredServer(t *testing.T) {
	eng, _, dir := newTestEngine(t, mcpScenario, nil)
	writeAgentFiles(t, dir, agentSchema, "calls: []")

	_, err := eng.StepTools(context.Background(), "research")
	if err == nil {
		t.Fatal("StepTools succeeded, want an error: no configuration declares the docs server")
	}
	var stepErr *Error
	if !errors.As(err, &stepErr) || stepErr.Class != ClassConfig {
		t.Fatalf("error = %v, want a config error", err)
	}
}
