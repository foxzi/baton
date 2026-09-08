package schemadoc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// node is the part of a JSON Schema this reference renders. Keywords the
// scenario schema does not use are ignored rather than reported: the schema
// is our own file, and a new keyword shows up as a missing line in the
// generated document, not as a broken build.
type node struct {
	Ref                  string          `json:"$ref"`
	Description          string          `json:"description"`
	Type                 json.RawMessage `json:"type"`
	Properties           members         `json:"properties"`
	Required             []string        `json:"required"`
	AdditionalProperties json.RawMessage `json:"additionalProperties"`
	Items                *node           `json:"items"`
	Enum                 []any           `json:"enum"`
	Const                any             `json:"const"`
	OneOf                []*node         `json:"oneOf"`
	AnyOf                []*node         `json:"anyOf"`
	AllOf                []*node         `json:"allOf"`
	Pattern              string          `json:"pattern"`
	Format               string          `json:"format"`
	Default              any             `json:"default"`
	Minimum              *float64        `json:"minimum"`
	Maximum              *float64        `json:"maximum"`
	MinItems             *int            `json:"minItems"`
	MinProperties        *int            `json:"minProperties"`
	Defs                 members         `json:"$defs"`

	// always is set when the schema was written as a bare boolean, which is
	// how the scenario schema says "any value here" for an input default.
	always *bool
}

// UnmarshalJSON accepts both forms a JSON Schema takes: an object of
// keywords, and the boolean that stands for "everything" or "nothing".
func (n *node) UnmarshalJSON(data []byte) error {
	var always bool
	if err := json.Unmarshal(data, &always); err == nil {
		*n = node{always: &always}
		return nil
	}
	type plain node
	return json.Unmarshal(data, (*plain)(n))
}

// types is the declared type, or types, of the node.
func (n *node) types() []string {
	if len(n.Type) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(n.Type, &single); err == nil {
		return []string{single}
	}
	var many []string
	if err := json.Unmarshal(n.Type, &many); err == nil {
		return many
	}
	return nil
}

// branches are the alternatives of a combined schema, in the order a reader
// meets them.
func (n *node) branches() []*node {
	var all []*node
	all = append(all, n.OneOf...)
	all = append(all, n.AnyOf...)
	all = append(all, n.AllOf...)
	return all
}

// additional is the value schema of a map, or nil when the node either
// forbids extra fields or puts no constraint on them.
func (n *node) additional() *node {
	if len(n.AdditionalProperties) == 0 {
		return nil
	}
	var closed bool
	if err := json.Unmarshal(n.AdditionalProperties, &closed); err == nil {
		return nil
	}
	value := &node{}
	if err := json.Unmarshal(n.AdditionalProperties, value); err != nil {
		return nil
	}
	return value
}

// closed reports whether the node rejects fields it does not declare.
func (n *node) closed() bool {
	var forbidden bool
	return json.Unmarshal(n.AdditionalProperties, &forbidden) == nil && !forbidden
}

// members is a JSON object whose keys keep the order of the file, so that the
// reference lists fields the way the schema was written rather than
// alphabetically.
type members struct {
	keys   []string
	values map[string]*node
}

func (m members) len() int { return len(m.keys) }

func (m members) value(key string) *node { return m.values[key] }

func (m *members) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	open, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := open.(json.Delim); !ok || delimiter != '{' {
		return fmt.Errorf("schemadoc: expected an object, got %v", open)
	}

	m.values = map[string]*node{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok {
			return fmt.Errorf("schemadoc: expected a field name, got %v", key)
		}
		value := &node{}
		if err := decoder.Decode(value); err != nil {
			return fmt.Errorf("schemadoc: %s: %w", name, err)
		}
		m.keys = append(m.keys, name)
		m.values[name] = value
	}
	_, err = decoder.Token()
	return err
}
