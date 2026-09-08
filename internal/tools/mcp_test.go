package tools

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/scenario"
)

// helperEnvVar makes the test binary run as an MCP server instead of as a
// test: a server over stdio has to be a process of its own, and re-executing
// the test binary is cheaper than building one.
const helperEnvVar = "BATON_TEST_MCP_SERVER"

// TestHelperMCPServer is the server the tests below start. It only does
// anything when the parent asked for it, and it exits without letting the
// test framework print: anything on stdout other than MCP traffic would
// break the protocol.
func TestHelperMCPServer(t *testing.T) {
	if os.Getenv(helperEnvVar) == "" {
		t.Skip("helper process, run by the tests in this file")
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "test-mcp", Version: "0.0.1"}, nil)

	text := func(value string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: value}}}
	}

	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "echoes its argument back",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return text(args.Text), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "crawl",
		Description: "fetches a page",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return text("crawled"), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "boom",
		Description: "always fails",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: "no luck"}},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "whoami",
		Description: "reports a variable of its own environment",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			StructuredContent: map[string]any{"token": os.Getenv("HELPER_TOKEN")},
		}, nil
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}

// helperServer describes the helper process as a configured server.
func helperServer(env map[string]scenario.EnvValue) MCPServer {
	declared := map[string]scenario.EnvValue{helperEnvVar: {Value: "1"}}
	for name, value := range env {
		declared[name] = value
	}
	return MCPServer{
		Command: []string{os.Args[0], "-test.run=TestHelperMCPServer"},
		Env:     declared,
	}
}

// newHelperMCP starts the helper server under one policy.
func newHelperMCP(t *testing.T, policy agent.Policy, opts MCPOptions) *MCP {
	t.Helper()
	opts.Policy = policy
	if opts.Servers == nil {
		opts.Servers = map[string]MCPServer{"helper": helperServer(nil)}
	}
	if opts.Workspace == "" {
		opts.Workspace = t.TempDir()
	}
	set, err := NewMCP(context.Background(), opts)
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	t.Cleanup(set.Close)
	return set
}

// toolByName finds a proxied tool in the set.
func toolByName(t *testing.T, set *MCP, name string) gateway.Tool {
	t.Helper()
	for _, tool := range set.Tools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no tool %q in %v", name, toolNames(set.Tools()))
	return gateway.Tool{}
}

func TestMCPNoServers(t *testing.T) {
	set, err := NewMCP(context.Background(), MCPOptions{})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	defer set.Close()
	if len(set.Tools()) != 0 {
		t.Fatalf("tools = %v, want none: the step names no server", toolNames(set.Tools()))
	}
}

func TestMCPUndeclaredServer(t *testing.T) {
	_, err := NewMCP(context.Background(), MCPOptions{
		Policy:    agent.Policy{MCP: []string{"context7"}},
		Workspace: t.TempDir(),
	})
	if err == nil {
		t.Fatal("NewMCP succeeded, want an error: the configuration declares no such server")
	}
	if !strings.Contains(err.Error(), "context7") {
		t.Fatalf("error = %v, want it to name the server", err)
	}
}

func TestMCPToolsProxied(t *testing.T) {
	set := newHelperMCP(t, agent.Policy{MCP: []string{"helper"}}, MCPOptions{})

	// crawl takes a url, so it is unsafe and left out without allow_unsafe.
	want := []string{"helper.echo", "helper.boom", "helper.whoami"}
	got := toolNames(set.Tools())
	if len(got) != len(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for _, name := range want {
		if !slices.Contains(got, name) {
			t.Fatalf("tools = %v, want it to contain %s", got, name)
		}
	}
	if skipped := set.Unsafe(); len(skipped) != 1 || skipped[0] != "helper.crawl" {
		t.Fatalf("unsafe = %v, want [helper.crawl]", skipped)
	}

	echo := toolByName(t, set, "helper.echo")
	if echo.Description != "echoes its argument back" {
		t.Fatalf("description = %q, want the server's own", echo.Description)
	}
	var schema map[string]any
	if err := json.Unmarshal(echo.InputSchema, &schema); err != nil {
		t.Fatalf("input schema %s: %v", echo.InputSchema, err)
	}
	if schema["type"] != "object" {
		t.Fatalf("input schema = %s, want the server's own", echo.InputSchema)
	}

	result, err := echo.Handler(context.Background(), json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatalf("call helper.echo: %v", err)
	}
	if result != "hello" {
		t.Fatalf("helper.echo = %#v, want %q", result, "hello")
	}
}

func TestMCPUnsafeAllowed(t *testing.T) {
	set := newHelperMCP(t, agent.Policy{MCP: []string{"helper"}, AllowUnsafe: true}, MCPOptions{})
	if !slices.Contains(toolNames(set.Tools()), "helper.crawl") {
		t.Fatalf("tools = %v, want helper.crawl: allow_unsafe is set", toolNames(set.Tools()))
	}
	if skipped := set.Unsafe(); len(skipped) != 0 {
		t.Fatalf("unsafe = %v, want none", skipped)
	}
}

func TestMCPToolError(t *testing.T) {
	set := newHelperMCP(t, agent.Policy{MCP: []string{"helper"}}, MCPOptions{})
	_, err := toolByName(t, set, "helper.boom").Handler(context.Background(), nil)
	if err == nil {
		t.Fatal("helper.boom succeeded, want the server's error")
	}
	if !strings.Contains(err.Error(), "no luck") {
		t.Fatalf("error = %v, want the server's text", err)
	}
}

func TestMCPStructuredResult(t *testing.T) {
	set := newHelperMCP(t, agent.Policy{MCP: []string{"helper"}}, MCPOptions{
		Servers: map[string]MCPServer{"helper": helperServer(map[string]scenario.EnvValue{
			"HELPER_TOKEN": {Secret: "token"},
		})},
		Secrets: stubSecrets{"token": "s3cret"},
	})

	result, err := toolByName(t, set, "helper.whoami").Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("call helper.whoami: %v", err)
	}
	decoded, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("helper.whoami = %#v, want the server's structured content", result)
	}
	if decoded["token"] != "s3cret" {
		t.Fatalf("token = %#v, want the secret the configuration declared", decoded["token"])
	}
}

func TestMCPUnknownSecret(t *testing.T) {
	_, err := NewMCP(context.Background(), MCPOptions{
		Policy:    agent.Policy{MCP: []string{"helper"}},
		Workspace: t.TempDir(),
		Servers: map[string]MCPServer{"helper": helperServer(map[string]scenario.EnvValue{
			"HELPER_TOKEN": {Secret: "nope"},
		})},
		Secrets: stubSecrets{},
	})
	if err == nil {
		t.Fatal("NewMCP succeeded, want an error: the env names an unknown secret")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v, want it to name the secret", err)
	}
}

func TestMCPServerThatDoesNotStart(t *testing.T) {
	_, err := NewMCP(context.Background(), MCPOptions{
		Policy:    agent.Policy{MCP: []string{"helper"}},
		Workspace: t.TempDir(),
		Servers:   map[string]MCPServer{"helper": {Command: []string{"baton-no-such-mcp-server"}}},
	})
	if err == nil {
		t.Fatal("NewMCP succeeded, want an error: there is no such executable")
	}
}

func TestUnsafeSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		unsafe bool
	}{
		{"plain", `{"type":"object","properties":{"text":{"type":"string"}}}`, false},
		{"url property", `{"type":"object","properties":{"url":{"type":"string"}}}`, true},
		{"uris property", `{"type":"object","properties":{"uris":{"type":"array"}}}`, true},
		{"headers property", `{"type":"object","properties":{"headers":{"type":"object"}}}`, true},
		{"mixed case", `{"type":"object","properties":{"URL":{"type":"string"}}}`, true},
		{
			"nested property",
			`{"type":"object","properties":{"req":{"type":"object","properties":{"uri":{}}}}}`,
			true,
		},
		{
			"format only",
			`{"type":"object","properties":{"target":{"type":"string","format":"uri"}}}`,
			true,
		},
		{
			"a property named properties",
			`{"type":"object","properties":{"properties":{"type":"string"}}}`,
			false,
		},
		{"no schema", ``, false},
		{"unreadable", `{`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unsafeSchema(json.RawMessage(tc.schema)); got != tc.unsafe {
				t.Fatalf("unsafeSchema(%s) = %v, want %v", tc.schema, got, tc.unsafe)
			}
		})
	}
}
