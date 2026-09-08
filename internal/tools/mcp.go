package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/version"
)

// MCPServer is one third-party MCP server the runner may start for a step,
// as the configuration declares it (spec section 12).
type MCPServer struct {
	// Command is the argument vector of the server process. It is executed
	// without a shell, like a command of section 7.5.
	Command []string

	// Env is the process environment on top of the runner's allow list.
	Env map[string]scenario.EnvValue
}

// MCPOptions is what the runner hands over for one step's proxied servers.
type MCPOptions struct {
	// Policy names the servers the step may reach and says whether the tools
	// marked unsafe are open to it (spec sections 7.3 and 7.6).
	Policy agent.Policy

	// Servers are the servers the configuration declares, by name.
	Servers map[string]MCPServer

	// Workspace is the working directory of a server process.
	Workspace string

	Secrets SecretSource
}

// mcpHandshakeTimeout bounds starting a server and listing its tools, so
// that a server that never answers cannot spend the step's whole timeout
// before the agent has even started.
const mcpHandshakeTimeout = 30 * time.Second

// MCP is the proxied tool set of one step: the runner starts the servers the
// step named, and every tool they offer becomes a gateway tool called
// <server>.<tool> (spec section 7.6).
type MCP struct {
	sessions []*mcp.ClientSession
	set      []gateway.Tool

	// skipped names the tools left out because they are marked unsafe and
	// the step did not ask for them, so that a step missing a tool can be
	// told why.
	skipped []string
}

// NewMCP starts the servers the step's policy names. A step that names none
// gets an empty set, and a name the configuration does not declare is a
// configuration error: the scenario meant a server that is not there.
func NewMCP(ctx context.Context, opts MCPOptions) (*MCP, error) {
	set := &MCP{}
	if len(opts.Policy.MCP) == 0 {
		return set, nil
	}
	if opts.Workspace == "" {
		return nil, fmt.Errorf("mcp: no workspace")
	}

	for _, name := range opts.Policy.MCP {
		server, ok := opts.Servers[name]
		if !ok {
			set.Close()
			return nil, fmt.Errorf("mcp: the step allows %q, which the configuration does not declare", name)
		}
		if err := set.start(ctx, name, server, opts); err != nil {
			set.Close()
			return nil, fmt.Errorf("mcp: %s: %w", name, err)
		}
	}
	return set, nil
}

// Tools returns the proxied tools of the step.
func (m *MCP) Tools() []gateway.Tool { return m.set }

// Unsafe returns the tools left out of the set because they are unsafe and
// the step has no allow_unsafe.
func (m *MCP) Unsafe() []string { return m.skipped }

// Close stops every server the set started. Closing a session closes the
// process's stdin, which is how an MCP server over stdio is asked to exit.
func (m *MCP) Close() {
	for i := len(m.sessions) - 1; i >= 0; i-- {
		_ = m.sessions[i].Close()
	}
	m.sessions = nil
}

// start launches one server, lists its tools and adds them to the set.
func (m *MCP) start(ctx context.Context, name string, server MCPServer, opts MCPOptions) error {
	if len(server.Command) == 0 {
		return fmt.Errorf("no command")
	}
	env, err := processEnv(opts.Secrets, server.Env)
	if err != nil {
		return err
	}

	// The step's context owns the process: when the step ends or is
	// cancelled, the server goes with it.
	cmd := exec.CommandContext(ctx, server.Command[0], server.Command[1:]...)
	cmd.Args = server.Command
	cmd.Dir = opts.Workspace
	cmd.Env = env
	// A server's diagnostics are not the agent's business, and they must not
	// reach the run directory: a server started with a secret in its
	// environment may well print it.
	cmd.Stderr = io.Discard

	client := mcp.NewClient(&mcp.Implementation{Name: "baton", Version: version.Version}, nil)

	handshake, cancel := context.WithTimeout(ctx, mcpHandshakeTimeout)
	defer cancel()

	session, err := client.Connect(handshake, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	m.sessions = append(m.sessions, session)

	listed, err := session.ListTools(handshake, nil)
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}
	for _, tool := range listed.Tools {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return fmt.Errorf("tool %s: input schema: %w", tool.Name, err)
		}
		// A server may advertise no schema at all; the gateway fills in the
		// empty object for a tool without one.
		if string(schema) == "null" {
			schema = nil
		}
		if unsafeSchema(schema) && !opts.Policy.AllowUnsafe {
			m.skipped = append(m.skipped, name+"."+tool.Name)
			continue
		}
		m.set = append(m.set, gateway.Tool{
			Name:        name + "." + tool.Name,
			Description: tool.Description,
			InputSchema: schema,
			Handler:     proxyHandler(session, tool.Name),
		})
	}
	return nil
}

// proxyHandler forwards one call to the server and turns its answer into
// what a gateway tool returns.
func proxyHandler(session *mcp.ClientSession, name string) gateway.Handler {
	return func(ctx context.Context, args json.RawMessage) (any, error) {
		params := &mcp.CallToolParams{Name: name}
		if len(args) > 0 {
			params.Arguments = args
		}
		res, err := session.CallTool(ctx, params)
		if err != nil {
			return nil, err
		}
		text := contentText(res.Content)
		if res.IsError {
			if text == "" {
				text = "the server reported an error"
			}
			return nil, fmt.Errorf("%s", text)
		}
		if res.StructuredContent != nil {
			return res.StructuredContent, nil
		}
		return text, nil
	}
}

// contentText keeps the parts of an answer an agent can read. Anything that
// is not text (an image, audio, an embedded resource) is named rather than
// inlined: the gateway hands the agent text.
func contentText(content []mcp.Content) string {
	parts := make([]string, 0, len(content))
	for _, part := range content {
		switch value := part.(type) {
		case *mcp.TextContent:
			parts = append(parts, value.Text)
		case *mcp.ResourceLink:
			parts = append(parts, value.URI)
		default:
			parts = append(parts, fmt.Sprintf("[%T omitted]", part))
		}
	}
	return strings.Join(parts, "\n")
}

// unsafeNames are the argument names that let a tool be pointed at a host of
// the model's choosing, which turns a proxied server into an open door out of
// the sandbox (spec section 7.6).
var unsafeNames = map[string]bool{
	"url": true, "urls": true, "uri": true, "uris": true,
	"header": true, "headers": true,
}

// unsafeFormats are the JSON Schema formats that say a string is an address,
// whatever the property is called.
var unsafeFormats = map[string]bool{
	"uri": true, "url": true, "uri-reference": true, "uri-template": true,
	"iri": true, "iri-reference": true,
}

// unsafeSchema reports whether an input schema takes an address or headers
// anywhere in it, at any depth: a nested object is as reachable as a
// top-level property.
func unsafeSchema(schema json.RawMessage) bool {
	if len(schema) == 0 {
		// A tool that advertises no schema takes no arguments, so there is
		// nothing to point at a host of the model's choosing.
		return false
	}

	var decoded any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		// A schema that cannot be read cannot be judged, and a tool that
		// cannot be judged is not handed out without allow_unsafe.
		return true
	}
	return unsafeNode(decoded, "")
}

func unsafeNode(node any, key string) bool {
	switch value := node.(type) {
	case map[string]any:
		if key == "properties" {
			for name, child := range value {
				if unsafeNames[strings.ToLower(name)] || unsafeNode(child, "") {
					return true
				}
			}
			return false
		}
		if format, ok := value["format"].(string); ok && unsafeFormats[strings.ToLower(format)] {
			return true
		}
		for name, child := range value {
			if unsafeNode(child, name) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if unsafeNode(child, key) {
				return true
			}
		}
	}
	return false
}
