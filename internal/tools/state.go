package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
	"github.com/foxzi/baton/internal/scenario"
)

// StateOptions is what the runner hands over for one step's state tools.
type StateOptions struct {
	// Path is the file that holds the scenario's state:
	// state/<scenario-name>.json next to runs/ (spec section 7.6).
	Path string

	// Policy decides whether the step may read the state, write it, or see
	// it at all (spec section 7.3).
	Policy agent.Policy
}

// MaxStateValueBytes is the size limit of one stored value (section 7.6).
const MaxStateValueBytes = 64 * 1024

// State is the state tool set of one step. The file is read and written per
// call rather than held open: a run is not the only writer over time, and a
// state file small enough for this tool is cheap to re-read.
type State struct {
	path  string
	write bool

	// mu keeps two calls of the same step from losing each other's write.
	mu sync.Mutex
}

// NewState prepares the state tools of a step. A step whose policy grants no
// access gets no tools, not an error: the profiles of section 7.2 leave the
// state out on purpose.
func NewState(opts StateOptions) (*State, error) {
	switch opts.Policy.State {
	case scenario.StateUnset, scenario.StateNone:
		return &State{}, nil
	case scenario.StateRead, scenario.StateReadWrite:
	default:
		return nil, fmt.Errorf("state: unknown access %q", opts.Policy.State)
	}
	if opts.Path == "" {
		return nil, errors.New("state: no state file")
	}
	return &State{
		path:  opts.Path,
		write: opts.Policy.State == scenario.StateReadWrite,
	}, nil
}

// Tools returns state.get, and state.set when the step may write.
func (s *State) Tools() []gateway.Tool {
	if s.path == "" {
		return nil
	}

	tools := []gateway.Tool{{
		Name:        "state.get",
		Description: "Read a value the scenario stored between runs.",
		InputSchema: stateKeySchema(false),
		Handler:     s.get,
	}}
	if s.write {
		tools = append(tools, gateway.Tool{
			Name:        "state.set",
			Description: "Store a value for the next run of this scenario.",
			InputSchema: stateKeySchema(true),
			Handler:     s.set,
		})
	}
	return tools
}

// StateValue is what state.get returns. Found tells a stored null from a key
// that was never written.
type StateValue struct {
	Value any  `json:"value"`
	Found bool `json:"found"`
}

// StateWritten is what state.set returns.
type StateWritten struct {
	Key   string `json:"key"`
	Bytes int    `json:"bytes"`
}

// stateArgs are the arguments of both tools; Value is absent for get.
type stateArgs struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func (s *State) get(ctx context.Context, raw json.RawMessage) (any, error) {
	args, err := decodeStateArgs(raw)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.load()
	if err != nil {
		return nil, err
	}

	value, found := stored[args.Key]
	if !found {
		return StateValue{}, nil
	}
	var decoded any
	if err := unmarshalNumbers(value, &decoded); err != nil {
		return nil, fmt.Errorf("state.get: %s is not valid JSON: %w", args.Key, err)
	}
	return StateValue{Value: decoded, Found: true}, nil
}

func (s *State) set(ctx context.Context, raw json.RawMessage) (any, error) {
	args, err := decodeStateArgs(raw)
	if err != nil {
		return nil, err
	}
	if len(args.Value) == 0 {
		return nil, errors.New("state.set: value is required")
	}
	if len(args.Value) > MaxStateValueBytes {
		return nil, fmt.Errorf("state.set: value is %d bytes, the limit is %d", len(args.Value), MaxStateValueBytes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.load()
	if err != nil {
		return nil, err
	}
	stored[args.Key] = args.Value

	if err := s.save(stored); err != nil {
		return nil, err
	}
	return StateWritten{Key: args.Key, Bytes: len(args.Value)}, nil
}

// load reads the state file. A missing file is an empty state: the first run
// of a scenario has nothing stored yet.
func (s *State) load() (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", s.path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, nil
	}

	stored := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("state: %s is not a JSON object: %w", s.path, err)
	}
	return stored, nil
}

// save replaces the state file. The write goes through a temporary file in
// the same directory so that an interrupted run cannot leave the state
// half-written.
func (s *State) save(stored map[string]json.RawMessage) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("state: create %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(dir, filepath.Base(s.path)+".*")
	if err != nil {
		return fmt.Errorf("state: create a temporary file in %s: %w", dir, err)
	}
	name := temp.Name()
	defer os.Remove(name)

	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("state: write %s: %w", name, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("state: write %s: %w", name, err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("state: replace %s: %w", s.path, err)
	}
	return nil
}

// decodeStateArgs reads the arguments of a state call.
func decodeStateArgs(raw json.RawMessage) (stateArgs, error) {
	var args stateArgs
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := unmarshalNumbers(raw, &args); err != nil {
			return args, fmt.Errorf("state: arguments are not a JSON object: %w", err)
		}
	}
	if args.Key == "" {
		return args, errors.New("state: key is required")
	}
	return args, nil
}

// unmarshalNumbers decodes JSON keeping numbers as written, so that a stored
// identifier does not come back as 1.234567890123e+18.
func unmarshalNumbers(data []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(into)
}

// stateKeySchema describes the arguments of a state tool.
func stateKeySchema(withValue bool) json.RawMessage {
	properties := map[string]any{
		"key": map[string]any{
			"type":        "string",
			"description": "Name of the stored value.",
		},
	}
	required := []string{"key"}
	if withValue {
		properties["value"] = map[string]any{
			"description": "Any JSON value, up to 64 KB encoded.",
		}
		required = append(required, "value")
	}

	schema, err := json.Marshal(map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	})
	if err != nil {
		// The map above is a literal: it cannot fail to encode.
		panic(err)
	}
	return schema
}
