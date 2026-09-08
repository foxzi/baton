// Package gateway is the only door an agent has to the outside world: an MCP
// server on the loopback interface, with the tool set of one step behind a
// per-run bearer token, every call audited and counted (spec section 7.7).
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/jsonschema"
	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/version"
)

// Options configure a gateway. One gateway serves a whole run; the tool set
// belongs to a step, not to the gateway.
type Options struct {
	// Addr is the listen address. The default is the loopback interface on an
	// ephemeral port, which is the only address the spec allows.
	Addr string

	// Token is the bearer token every request must carry. Empty means one is
	// generated for this run.
	Token string

	// Redactor is applied to every audit line, so that an argument carrying a
	// secret cannot reach the run directory.
	Redactor *secrets.Redactor
}

// Gateway serves the steps of one run.
type Gateway struct {
	token   string
	baseURL string

	ln       net.Listener
	srv      *http.Server
	redactor *secrets.Redactor

	mu       sync.Mutex
	sessions map[string]*Session
}

// New starts the gateway and returns it ready to serve steps.
func New(opts Options) (*Gateway, error) {
	addr := opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	token := opts.Token
	if token == "" {
		var err error
		if token, err = newToken(); err != nil {
			return nil, err
		}
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	g := &Gateway{
		token:    token,
		baseURL:  "http://" + ln.Addr().String(),
		ln:       ln,
		redactor: opts.Redactor,
		sessions: make(map[string]*Session),
	}

	handler := mcp.NewStreamableHTTPHandler(g.serverFor, nil)
	mux := http.NewServeMux()
	mux.Handle(stepPrefix, g.authorize(handler))
	g.srv = &http.Server{Handler: mux}

	go func() { _ = g.srv.Serve(ln) }()
	return g, nil
}

// stepPrefix is where a step's MCP endpoint lives: one path per step, so that
// a token alone does not give an agent the tools of another step.
const stepPrefix = "/steps/"

// Token returns the bearer token of this run.
func (g *Gateway) Token() string { return g.token }

// BaseURL returns the gateway's base URL, without a step path.
func (g *Gateway) BaseURL() string { return g.baseURL }

// Close stops serving. Open sessions stop answering with it.
func (g *Gateway) Close() error {
	err := g.srv.Close()
	if err != nil && !strings.Contains(err.Error(), "use of closed") {
		return err
	}
	return nil
}

// authorize rejects a request that does not carry this run's token, and one
// that names a step with no open session, before the MCP handler sees it.
func (g *Gateway) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+g.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if g.sessionFor(r) == nil {
			http.Error(w, "no such step", http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serverFor hands the MCP handler the server of the requested step.
func (g *Gateway) serverFor(r *http.Request) *mcp.Server {
	session := g.sessionFor(r)
	if session == nil {
		return nil
	}
	return session.server
}

// sessionFor resolves the step id in the request path to an open session.
func (g *Gateway) sessionFor(r *http.Request) *Session {
	id, ok := strings.CutPrefix(r.URL.Path, stepPrefix)
	if !ok {
		return nil
	}
	id = strings.TrimSuffix(id, "/mcp")

	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions[id]
}

// StepOptions describe the tool set of one step.
type StepOptions struct {
	// StepID names the step; it becomes part of the endpoint URL, so it must
	// be unique within the run.
	StepID string

	// Tools are the step's tools, already narrowed by its policy. The
	// gateway adds submit_result itself.
	Tools []Tool

	// Result is the schema submit_result validates against. It is required:
	// a step ends successfully only through a valid submit_result.
	Result *jsonschema.Schema

	// MaxToolCalls caps the calls of the whole step, MaxResultBytes the size
	// of one tool's answer. Zero means the package default.
	MaxToolCalls   int
	MaxResultBytes int64

	// Audit receives one JSON line per call (spec section 7.7). Nil discards
	// the audit trail, which only tests have a reason to do.
	Audit io.Writer
}

// Open registers a step and returns its session. The caller closes it when
// the step ends.
func (g *Gateway) Open(opts StepOptions) (*Session, error) {
	if opts.StepID == "" {
		return nil, fmt.Errorf("gateway: step id must not be empty")
	}
	if opts.Result == nil {
		return nil, fmt.Errorf("gateway: step %s has no result schema", opts.StepID)
	}

	maxCalls := opts.MaxToolCalls
	if maxCalls <= 0 {
		maxCalls = agent.DefaultMaxToolCalls
	}
	maxBytes := opts.MaxResultBytes
	if maxBytes <= 0 {
		maxBytes = agent.DefaultMaxResultBytes
	}

	session := &Session{
		gateway:  g,
		stepID:   opts.StepID,
		result:   opts.Result,
		maxCalls: maxCalls,
		maxBytes: maxBytes,
		audit:    newAuditor(opts.Audit, g.redactor),
		perTool:  make(map[string]int),
		done:     make(chan struct{}),
		info: agent.GatewayInfo{
			URL:   g.baseURL + stepPrefix + opts.StepID + "/mcp",
			Token: g.token,
		},
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "baton", Version: version.Version}, nil)
	for _, tool := range append(opts.Tools, session.submitTool()) {
		if err := session.add(server, tool); err != nil {
			return nil, err
		}
	}
	session.server = server

	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.sessions[opts.StepID]; exists {
		return nil, fmt.Errorf("gateway: step %s is already open", opts.StepID)
	}
	g.sessions[opts.StepID] = session
	return session, nil
}

// add registers one tool on the step's MCP server, wrapped in the counting,
// auditing and capping the gateway owes every call.
func (s *Session) add(server *mcp.Server, tool Tool) error {
	if tool.Name == "" {
		return fmt.Errorf("gateway: step %s has a tool without a name", s.stepID)
	}
	schema := tool.InputSchema
	if len(schema) == 0 {
		schema = emptyObjectSchema
	}
	var decoded any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return fmt.Errorf("gateway: tool %s has an invalid input schema: %w", tool.Name, err)
	}

	server.AddTool(&mcp.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: decoded,
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return s.call(ctx, tool, req.Params.Arguments), nil
	})
	return nil
}

// emptyObjectSchema is what a tool without arguments advertises: MCP requires
// an input schema, and an object with no properties is the honest one.
var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{}}`)

// newToken returns a token good for one run.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate gateway token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
