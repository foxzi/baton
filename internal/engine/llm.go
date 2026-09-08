package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/jsonschema"
	"github.com/foxzi/baton/internal/provider"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
)

// reservedTemplateKeys are the top-level names of the template context
// (section 5.2). A with: variable may not shadow them, because a prompt that
// silently lost .steps would be very hard to debug.
var reservedTemplateKeys = map[string]bool{"inputs": true, "steps": true, "run": true, "iter": true}

// execLLM calls a language model and returns its validated answer (section
// 3.5). The schema is mandatory: the result of an llm step is the parsed
// JSON, never free text.
func (e *Engine) execLLM(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	body := step.LLM

	call, stepErr := e.prepareLLM(step)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}

	input := map[string]any{
		"model":  call.chain[0],
		"system": call.system,
		"prompt": call.prompt,
		"schema": call.schemaPath,
		"mode":   string(body.StructuredMode),
	}
	e.writeStepJSON(path, "input.json", input)

	// The schema travels by content, not by path: editing the file must
	// invalidate the entry (section 10.3).
	cacheKey, cached, hit := e.cacheGet(step, path, input, map[string]string{
		call.schemaPath: hashBytes(call.schemaRaw),
	})
	if hit {
		return cached, nil
	}

	// The chain is tried on transient provider failures only; a schema
	// failure stays on the same model (section 3.5).
	var lastErr *Error
	for i, ref := range call.chain {
		out, stepErr := e.callModel(ctx, step, path, call, ref)
		if stepErr == nil {
			e.cachePut(cacheKey, step, out)
			return out, nil
		}
		lastErr = stepErr
		if !modelFallback(stepErr) || i == len(call.chain)-1 {
			break
		}
		e.emit(Event{Type: "llm_fallback", Step: step.ID, Message: stepErr.Msg, Fields: map[string]any{
			"model": call.chain[i+1],
		}})
	}

	e.writeStepJSON(path, "output.json", map[string]any{
		"status": expr.StatusFailed,
		"error":  map[string]any{"class": lastErr.Class, "message": lastErr.Msg},
	})
	return expr.Step{}, lastErr
}

// llmCall is everything an llm step needs that does not depend on which
// model of the chain is being tried.
type llmCall struct {
	chain      []string
	system     string
	prompt     string
	schemaPath string
	schemaRaw  json.RawMessage
	schema     *jsonschema.Schema
	// schemaRetries is how many extra attempts a schema failure gets on the
	// same model, each with the validation message added to the context.
	schemaRetries int
	// startedAt is when the step began, for the duration in cost.json.
	startedAt time.Time
}

// prepareLLM renders the prompts and loads the schema. Everything here is a
// config error: it does not depend on the provider answering.
func (e *Engine) prepareLLM(step *scenario.Step) (*llmCall, *Error) {
	body := step.LLM
	call := &llmCall{schemaRetries: e.llmSchemaRetries(step), startedAt: e.opts.Now()}

	model := body.Model
	if model == "" {
		model = e.opts.Scenario.Defaults.Model
	}
	if model == "" && e.opts.Config != nil {
		model = e.opts.Config.Defaults.Model
	}
	if model == "" {
		return nil, errorf(ClassConfig, "step %s: llm.model is not set and defaults.model is empty", step.ID)
	}
	call.chain = append([]string{model}, body.FallbackModels...)

	if body.Schema == "" {
		return nil, errorf(ClassConfig, "step %s: llm.schema is required", step.ID)
	}
	call.schemaPath = body.Schema
	raw, err := os.ReadFile(e.resolvePath(body.Schema))
	if err != nil {
		return nil, wrapf(ClassConfig, err, "step %s: llm.schema", step.ID)
	}
	call.schemaRaw = raw
	if call.schema, err = jsonschema.Compile(body.Schema, raw); err != nil {
		return nil, wrapf(ClassConfig, err, "step %s: llm.schema", step.ID)
	}

	data, stepErr := e.llmTemplateData(step)
	if stepErr != nil {
		return nil, stepErr
	}
	if call.system, stepErr = e.llmText(step.ID, "system", body.System, data); stepErr != nil {
		return nil, stepErr
	}
	if call.prompt, stepErr = e.llmText(step.ID, "prompt", body.Prompt, data); stepErr != nil {
		return nil, stepErr
	}
	if strings.TrimSpace(call.prompt) == "" {
		return nil, errorf(ClassConfig, "step %s: llm.prompt is empty", step.ID)
	}
	return call, nil
}

// callModel runs one model of the chain, retrying a schema failure on the
// same model with the validation message appended to the conversation
// (section 3.5).
func (e *Engine) callModel(ctx context.Context, step *scenario.Step, path string, call *llmCall, ref string) (expr.Step, *Error) {
	providerName, model, err := provider.SplitModel(ref)
	if err != nil {
		return expr.Step{}, wrapf(ClassConfig, err, "step %s: llm.model", step.ID)
	}
	p, stepErr := e.provider(providerName)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}

	mode := provider.ResolveMode(provider.Mode(step.LLM.StructuredMode), p.Capabilities())
	prompt := call.prompt
	if mode == provider.ModePrompt {
		prompt += "\n\n" + provider.PromptInstruction(call.schemaRaw)
	}
	req := provider.Request{
		Model:       model,
		System:      call.system,
		Messages:    []provider.Message{{Role: provider.RoleUser, Text: prompt}},
		Schema:      call.schemaRaw,
		Mode:        mode,
		MaxTokens:   step.LLM.MaxTokens,
		Temperature: step.LLM.Temperature,
	}

	for attempt := 0; ; attempt++ {
		resp, err := p.Complete(ctx, req)
		if err != nil {
			return expr.Step{}, e.llmFailure(ctx, step, err)
		}
		out, stepErr := e.llmResult(step, path, call, ref, mode, resp)
		if stepErr == nil {
			return out, nil
		}
		if stepErr.Class != ClassSchema || attempt >= call.schemaRetries {
			return expr.Step{}, stepErr
		}
		req.Messages = appendCorrection(req.Messages, resp, validationText(stepErr))
		e.emit(Event{Type: "step_retry", Step: step.ID, Message: stepErr.Error(), Fields: map[string]any{
			"attempt": attempt + 2,
			"class":   ClassSchema,
			"model":   ref,
		}})
	}
}

// llmResult extracts the answer, validates it against the schema and
// records the step output. A malformed or non-conforming answer is a schema
// failure, which callModel retries with the message in context.
func (e *Engine) llmResult(step *scenario.Step, path string, call *llmCall, ref string, mode provider.Mode, resp provider.Response) (expr.Step, *Error) {
	raw, err := provider.Result(resp, mode)
	if err != nil {
		return expr.Step{}, providerError(step.ID, err)
	}
	value, err := call.schema.ValidateJSON(raw)
	if err != nil {
		return expr.Step{}, wrapf(ClassSchema, err, "step %s: the answer does not match the schema", step.ID)
	}

	cost := e.costOf(ref, resp)
	budgetErr := e.spend(runstore.CostEntry{
		Step:         path,
		Model:        ref,
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
		CachedTokens: resp.Usage.CachedTokens,
		CostUSD:      cost,
		Duration:     e.opts.Now().Sub(call.startedAt).String(),
	})
	if budgetErr != nil {
		return expr.Step{}, budgetErr
	}

	e.writeStepJSON(path, "output.json", map[string]any{
		"status":     expr.StatusSuccess,
		"result":     value,
		"usage":      usageMap(resp.Usage),
		"cost_usd":   cost,
		"model_used": ref,
		"mode":       string(mode),
	})
	return expr.Step{Result: value}, nil
}

// appendCorrection puts the rejected answer and the validation message back
// into the conversation. A tool-call answer must be answered as a tool
// result, otherwise the next request is malformed for the provider.
func appendCorrection(messages []provider.Message, resp provider.Response, validation string) []provider.Message {
	instruction := "Your previous answer did not validate against the schema: " + validation +
		"\nAnswer again, correcting exactly these problems."
	if len(resp.ToolCalls) > 0 {
		messages = append(messages, provider.Message{
			Role:      provider.RoleAssistant,
			Text:      resp.Text,
			ToolCalls: resp.ToolCalls,
		})
		for _, call := range resp.ToolCalls {
			messages = append(messages, provider.Message{
				Role:       provider.RoleTool,
				ToolCallID: call.ID,
				Text:       instruction,
			})
		}
		return messages
	}
	return append(messages,
		provider.Message{Role: provider.RoleAssistant, Text: resp.Text},
		provider.Message{Role: provider.RoleUser, Text: instruction},
	)
}

// validationText is the part of a schema failure that is worth showing the
// model: the validation message itself, without the step framing a human
// reader needs.
func validationText(stepErr *Error) string {
	if stepErr.Err != nil {
		return stepErr.Err.Error()
	}
	return stepErr.Msg
}

// llmFailure classifies a provider failure. An expired step deadline is a
// timeout whatever the transport called it, as in an http step.
func (e *Engine) llmFailure(ctx context.Context, step *scenario.Step, err error) *Error {
	stepErr := providerError(step.ID, err)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Class: ClassTimeout, Msg: stepErr.Msg, Err: stepErr.Err}
	}
	return stepErr
}

// llmSchemaRetries is how many times a schema failure is retried inside the
// step. The engine's own retry loop cannot do it: it would start from a
// fresh conversation and lose the validation message (section 9.1).
func (e *Engine) llmSchemaRetries(step *scenario.Step) int {
	if step.Retry != nil {
		if len(step.Retry.On) > 0 && !containsClass(step.Retry.On, ClassSchema) {
			return 0
		}
		if step.Retry.Attempts > 0 {
			return step.Retry.Attempts
		}
		return 0
	}
	return defaultRetries[ClassSchema].attempts
}

// llmTemplateData is the template context of a prompt: the usual one plus
// the rendered with: variables at the top level (section 3.5).
func (e *Engine) llmTemplateData(step *scenario.Step) (map[string]any, *Error) {
	data := e.templateData()
	for _, name := range sortedKeys(step.LLM.With) {
		if reservedTemplateKeys[name] {
			return nil, errorf(ClassConfig, "step %s: llm.with.%s shadows the %s of the template context", step.ID, name, name)
		}
		value, stepErr := e.render(fmt.Sprintf("step %s: llm.with.%s", step.ID, name), step.LLM.With[name])
		if stepErr != nil {
			return nil, stepErr
		}
		data[name] = value
	}
	return data, nil
}

// llmText renders a prompt that is either a file path or an inline
// template. A value that names an existing file is read from it, so a
// one-line inline prompt is still possible as long as no such file exists.
func (e *Engine) llmText(stepID, field, value string, data map[string]any) (string, *Error) {
	if value == "" {
		return "", nil
	}
	name := fmt.Sprintf("step %s: llm.%s", stepID, field)
	if isPromptFile(e.resolvePath(value)) {
		out, err := e.renderer.RenderFile(value, data)
		if err != nil {
			return "", wrapf(ClassConfig, err, "render %s", name)
		}
		return out, nil
	}
	out, err := e.renderer.Render(name, value, data)
	if err != nil {
		return "", wrapf(ClassConfig, err, "render %s", name)
	}
	return out, nil
}

// isPromptFile reports whether a prompt value points at a readable file. A
// value carrying a template action or a newline is inline by construction
// and is never touched on disk.
func isPromptFile(path string) bool {
	if strings.ContainsAny(path, "\n{") {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// resolvePath resolves a scenario-relative path against the scenario
// directory. An absolute path is left alone.
func (e *Engine) resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(e.dir, path)
}

// provider builds (or reuses) the provider named by a model reference. The
// api key is resolved by the caller of the engine, so that it is part of the
// redactor before any provider can echo it back (section 13).
func (e *Engine) provider(name string) (provider.Provider, *Error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if p, ok := e.providers[name]; ok {
		return p, nil
	}
	if e.opts.Config == nil {
		return nil, errorf(ClassConfig, "unknown provider %q: no configuration is loaded", name)
	}
	cfg, ok := e.opts.Config.Providers[name]
	if !ok {
		return nil, errorf(ClassConfig, "unknown provider %q", name)
	}
	p, err := provider.New(name, cfg, e.opts.ProviderKeys[name])
	if err != nil {
		return nil, providerError("", err)
	}
	if e.providers == nil {
		e.providers = map[string]provider.Provider{}
	}
	e.providers[name] = p
	return p, nil
}

// costOf prices a completion: the provider's own figure when it reports one,
// otherwise usage against the pricing table. A model missing from the table
// has no cost, not a zero cost (section 8.3).
func (e *Engine) costOf(ref string, resp provider.Response) *float64 {
	if resp.CostUSD != nil {
		return resp.CostUSD
	}
	if e.opts.Config == nil {
		return nil
	}
	price, ok := e.opts.Config.Pricing[ref]
	if !ok {
		return nil
	}
	cost := float64(resp.Usage.InputTokens)/1e6*price.InputPerMTok +
		float64(resp.Usage.OutputTokens)/1e6*price.OutputPerMTok
	return &cost
}

// modelFallback reports whether a failure moves the step to the next model
// of fallback_models (section 3.5). A budget of the provider does, the budget
// of the run does not: another model would spend money that is already gone.
func modelFallback(stepErr *Error) bool {
	if errors.Is(stepErr.Err, errRunBudget) {
		return false
	}
	return stepErr.Class == ClassTransient || stepErr.Class == ClassBudget
}

// providerError turns a failure of internal/provider into a step error. Both
// packages spell the classes of section 9.1 the same way.
func providerError(stepID string, err error) *Error {
	var typed *provider.Error
	if !errors.As(err, &typed) {
		return wrapf(ClassCommand, err, "step %s: llm", stepID)
	}
	message := typed.Error()
	if stepID != "" {
		message = fmt.Sprintf("step %s: %s", stepID, message)
	}
	return &Error{Class: typed.Class, Msg: message, Err: typed.Err}
}

// usageMap is the usage block of output.json (section 10.1).
func usageMap(usage provider.Usage) map[string]any {
	out := map[string]any{
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
	}
	if usage.CachedTokens > 0 {
		out["cached_tokens"] = usage.CachedTokens
	}
	return out
}
