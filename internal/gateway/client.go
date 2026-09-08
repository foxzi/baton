package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/version"
)

// Client talks to a gateway endpoint. Engines that drive an agent in-process,
// such as fake, use it, and so do the tests: the same path a real agent takes.
type Client struct {
	session *mcp.ClientSession
}

// Connect opens a session with the step's endpoint.
func Connect(ctx context.Context, info agent.GatewayInfo) (*Client, error) {
	transport := &mcp.StreamableClientTransport{
		Endpoint: info.URL,
		HTTPClient: &http.Client{
			Transport: &bearerTransport{base: http.DefaultTransport, token: info.Token},
		},
		// The client only ever answers its own calls; a standalone stream
		// would keep a connection open for notifications nobody reads.
		DisableStandaloneSSE: true,
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "baton", Version: version.Version}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to the gateway: %w", err)
	}
	return &Client{session: session}, nil
}

// Close ends the session.
func (c *Client) Close() error { return c.session.Close() }

// ToolInfo is one tool as the agent sees it.
type ToolInfo struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Tools lists the tools of the step this client is connected to.
func (c *Client) Tools(ctx context.Context) ([]ToolInfo, error) {
	res, err := c.session.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list the gateway tools: %w", err)
	}

	tools := make([]ToolInfo, 0, len(res.Tools))
	for _, tool := range res.Tools {
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("tool %s has an unreadable input schema: %w", tool.Name, err)
		}
		tools = append(tools, ToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: schema,
		})
	}
	return tools, nil
}

// CallResult is what one call answered. IsError marks an answer the agent is
// meant to react to, not a failure of the call itself.
type CallResult struct {
	Text    string
	IsError bool
}

// Call invokes one tool with raw JSON arguments.
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	params := &mcp.CallToolParams{Name: name}
	if len(args) > 0 {
		params.Arguments = args
	}

	res, err := c.session.CallTool(ctx, params)
	if err != nil {
		return CallResult{}, fmt.Errorf("call %s: %w", name, err)
	}

	var text strings.Builder
	for _, content := range res.Content {
		if item, ok := content.(*mcp.TextContent); ok {
			text.WriteString(item.Text)
		}
	}
	return CallResult{Text: text.String(), IsError: res.IsError}, nil
}

// bearerTransport carries the run's token. The SDK's transport has no header
// field, so the header goes on the round tripper.
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}
