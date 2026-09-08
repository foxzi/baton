package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/httpx"
	"github.com/foxzi/baton/internal/packs"
)

// APIOptions is what the runner hands over for one step's API tools.
type APIOptions struct {
	// Policy is the step's tool policy; Policy.APIs names the operations
	// the step may call, each as <api>.<op> (spec section 7.3).
	Policy agent.Policy

	// Resolve prepares the client of one apis: entry, with its pack, its
	// configuration and its credential. The runner keeps the credential to
	// itself: the agent only ever names an operation.
	Resolve func(api string) (*httpx.API, error)

	// Client performs the calls. A nil client gets the package default.
	Client *httpx.Client
}

// APIs is the API tool set of one step.
type APIs struct {
	client *httpx.Client
	ops    []apiOp
}

// apiOp is one operation the step may call, with its API already resolved.
type apiOp struct {
	// name is how the tool is advertised: <api>.<op> (section 7.4.8).
	name   string
	opName string
	api    *httpx.API
	op     *packs.Op
}

// NewAPIs resolves the operations the step's policy names. An operation that
// the pack does not have, or that is not readonly, is a configuration error:
// only readonly operations may reach an agent (section 7.4.1), and the
// scenario meant a call that cannot be made.
func NewAPIs(opts APIOptions) (*APIs, error) {
	set := &APIs{client: opts.Client}
	if len(opts.Policy.APIs) == 0 {
		return set, nil
	}
	if opts.Resolve == nil {
		return nil, errors.New("apis: no way to resolve an api")
	}
	if set.client == nil {
		set.client = httpx.New(0)
	}

	var problems []error
	for _, entry := range opts.Policy.APIs {
		apiName, opName, ok := strings.Cut(entry, ".")
		if !ok || apiName == "" || opName == "" {
			problems = append(problems, fmt.Errorf("apis: %q is not <api>.<op>", entry))
			continue
		}
		api, err := opts.Resolve(apiName)
		if err != nil {
			problems = append(problems, fmt.Errorf("apis: %s: %w", entry, err))
			continue
		}
		op, err := api.Pack.Op(opName)
		if err != nil {
			problems = append(problems, fmt.Errorf("apis: %s: %w", entry, err))
			continue
		}
		if !op.Readonly {
			problems = append(problems, fmt.Errorf("apis: %s is not readonly and cannot be an agent tool", entry))
			continue
		}
		set.ops = append(set.ops, apiOp{name: entry, opName: opName, api: api, op: op})
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}

	sort.Slice(set.ops, func(i, j int) bool { return set.ops[i].name < set.ops[j].name })
	return set, nil
}

// Tools returns one tool per allowed operation, in a stable order.
func (a *APIs) Tools() []gateway.Tool {
	tools := make([]gateway.Tool, 0, len(a.ops))
	for _, entry := range a.ops {
		tools = append(tools, gateway.Tool{
			Name:        entry.name,
			Description: entry.op.Description,
			InputSchema: paramsSchema(entry.op.Params),
			Handler:     a.handler(entry),
		})
	}
	return tools
}

// APIResponse is what an API call returns to the agent: the transformed
// result of the operation, and whether the runner had to cut it short.
type APIResponse struct {
	Result    any  `json:"result"`
	Truncated bool `json:"truncated,omitempty"`
	Pages     int  `json:"pages,omitempty"`
}

// handler calls one operation on behalf of the agent. The pack checks the
// arguments, applies the envelope, walks the pagination and transforms the
// answer, so the agent sees the same value a http step would.
func (a *APIs) handler(entry apiOp) gateway.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		args, err := decodeOpArgs(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.name, err)
		}

		if entry.api.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, entry.api.Timeout)
			defer cancel()
		}

		result, err := a.client.Op(ctx, entry.api, entry.opName, args)
		if err != nil {
			return nil, err
		}
		return APIResponse{
			Result:    result.Result,
			Truncated: result.Truncated || result.TruncatedPages,
			Pages:     result.Pages,
		}, nil
	}
}

// decodeOpArgs reads the arguments of a call. Unlike a command's arguments
// these are not restricted to strings: an operation may take a number or a
// list, and the pack's params validate what arrives.
func decodeOpArgs(raw json.RawMessage) (map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return map[string]any{}, nil
	}

	var args map[string]any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("arguments are not a JSON object: %w", err)
	}
	return args, nil
}

// paramsSchema describes an operation's arguments to the agent. The pattern
// and the length limit go into the schema as well as being enforced, so that
// the agent can see the shape it has to meet.
func paramsSchema(params map[string]*packs.Param) json.RawMessage {
	properties := make(map[string]any, len(params))
	required := make([]string, 0, len(params))

	for _, name := range sortedNames(params) {
		param := params[name]
		if param == nil {
			continue
		}
		property := map[string]any{"type": "string"}
		if param.Pattern != "" {
			property["pattern"] = param.Pattern
		}
		if param.MaxLen > 0 {
			property["maxLength"] = param.MaxLen
		}
		if len(param.Enum) > 0 {
			property["enum"] = param.Enum
		}
		if param.Default != nil {
			property["default"] = param.Default
		}
		properties[name] = property
		if param.IsRequired() {
			required = append(required, name)
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	data, err := json.Marshal(schema)
	if err != nil {
		// Only strings and numbers go in, so this cannot fail; a tool
		// without a schema would still be usable.
		return nil
	}
	return data
}
