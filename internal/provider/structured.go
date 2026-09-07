package provider

import (
	"encoding/json"
	"strings"
)

// Mode is a structured-output strategy. The specification lists three levels
// in section 8.3, in descending order of provider support.
type Mode string

// Structured-output modes.
const (
	// ModeUnset asks for the best mode the provider supports.
	ModeUnset Mode = ""

	// ModeNative uses the provider's own JSON Schema response format.
	ModeNative Mode = "native"

	// ModeTool forces a call of one wrapper tool whose arguments are the
	// step's schema.
	ModeTool Mode = "tool"

	// ModePrompt asks for the JSON in the prompt and validates the answer.
	ModePrompt Mode = "prompt"
)

// SubmitToolName is the wrapper tool of ModeTool. The name is part of the
// contract with the model, so it is fixed.
const SubmitToolName = "submit"

// ResolveMode picks the mode to use. An explicit mode from the step is
// honoured as far as the provider allows: a provider without structured
// output cannot do ModeNative, one without tools cannot do ModeTool, and
// either falls back to ModePrompt, which every provider can do. Downgrading
// is automatic, upgrading is not (spec section 8.3).
func ResolveMode(want Mode, caps Caps) Mode {
	switch want {
	case ModeNative:
		if caps.StructuredOutput {
			return ModeNative
		}
		if caps.Tools {
			return ModeTool
		}
		return ModePrompt
	case ModeTool:
		if caps.Tools {
			return ModeTool
		}
		return ModePrompt
	case ModePrompt:
		return ModePrompt
	default:
		switch {
		case caps.StructuredOutput:
			return ModeNative
		case caps.Tools:
			return ModeTool
		default:
			return ModePrompt
		}
	}
}

// SubmitTool is the wrapper tool of ModeTool: one tool whose arguments are
// the result the step wants.
func SubmitTool(schema json.RawMessage) Tool {
	return Tool{
		Name:        SubmitToolName,
		Description: "Submit the result. Call this exactly once, with the result as its arguments.",
		Schema:      schema,
	}
}

// PromptInstruction is the text ModePrompt appends to the prompt. The model
// has no schema support to lean on, so the schema goes into the prompt and
// the answer is validated afterwards.
func PromptInstruction(schema json.RawMessage) string {
	var buf strings.Builder
	buf.WriteString("Answer with a single JSON document and nothing else: no prose, no code fence.\n")
	buf.WriteString("It must validate against this JSON Schema:\n\n")
	buf.Write(schema)
	buf.WriteString("\n")
	return buf.String()
}

// Result extracts the step's result from a response. In ModeTool it is the
// arguments of the submit call; otherwise it is the response text, which
// ModePrompt models like to wrap in a code fence even when told not to.
func Result(resp Response, mode Mode) (json.RawMessage, error) {
	if mode == ModeTool {
		for _, call := range resp.ToolCalls {
			if call.Name == SubmitToolName {
				return call.Arguments, nil
			}
		}
		return nil, errorf(ClassSchema, "the model did not call %s", SubmitToolName)
	}

	text := unfence(resp.Text)
	if text == "" {
		return nil, errorf(ClassSchema, "the model answered with no text")
	}
	var probe any
	if err := json.Unmarshal([]byte(text), &probe); err != nil {
		return nil, wrapf(ClassSchema, err, "the answer is not JSON")
	}
	return json.RawMessage(text), nil
}

// unfence strips a Markdown code fence around a response, and any prose
// around the fence with it.
func unfence(text string) string {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "```")
	if start < 0 {
		return text
	}
	rest := text[start+3:]
	// A fence may name its language on the opening line.
	if newline := strings.IndexByte(rest, '\n'); newline >= 0 {
		rest = rest[newline+1:]
	}
	if end := strings.Index(rest, "```"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// schemaName is the name a provider gives the response schema when its wire
// format asks for one.
const schemaName = "result"

// ensureObjectSchema rejects a schema a native JSON Schema mode cannot take.
// Both OpenAI and Anthropic require the root to be an object.
func ensureObjectSchema(schema json.RawMessage) error {
	var root struct {
		Type any `json:"type"`
	}
	if err := json.Unmarshal(schema, &root); err != nil {
		return wrapf(ClassConfig, err, "the schema is not valid JSON")
	}
	if text, ok := root.Type.(string); ok && text == "object" {
		return nil
	}
	return errorf(ClassConfig, `the schema must have "type": "object" at its root, got %v`, root.Type)
}
