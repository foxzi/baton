package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/httpx"
	"github.com/foxzi/baton/internal/packs"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/values"
)

// execHTTP executes an http step in either of the two forms of section 3.4:
// an operation of a pack, or a raw request.
func (e *Engine) execHTTP(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	if step.HTTP.Raw() {
		return e.execHTTPRaw(ctx, step, path)
	}
	return e.execHTTPOp(ctx, step, path)
}

// execHTTPOp calls an operation of a pack. The pack validates the arguments,
// applies the envelope, walks the pagination and transforms the result, so
// the step sees a normalised value.
func (e *Engine) execHTTPOp(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	body := step.HTTP
	apiName, opName := body.APIName()
	api, stepErr := e.api(apiName, body.Auth)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	args, stepErr := e.renderArgs(step.ID, body.Args)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	input := map[string]any{
		"op":   body.Op,
		"args": args,
	}
	e.writeStepJSON(path, "input.json", input)

	cacheKey, cached, hit := e.cacheGet(step, path, input, nil)
	if hit {
		return cached, nil
	}

	callCtx, cancel := apiContext(ctx, api)
	defer cancel()

	result, err := e.httpClient().Op(callCtx, api, opName, args)
	if err != nil {
		return expr.Step{}, e.httpFailure(callCtx, step, path, err)
	}

	out := expr.Step{
		Result:     result.Result,
		HTTPStatus: result.Status,
		Headers:    headerMap(result.Headers),
		Body:       result.Body,
	}
	e.writeStepJSON(path, "output.json", map[string]any{
		"status":      expr.StatusSuccess,
		"http_status": result.Status,
		"result":      result.Result,
		"pages":       result.Pages,
		"truncated":   result.Truncated || result.TruncatedPages,
	})
	e.cachePut(cacheKey, step, out)
	return out, nil
}

// execHTTPRaw performs a one-off request, for services without a pack.
func (e *Engine) execHTTPRaw(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	body := step.HTTP

	var (
		api     *httpx.API
		stepErr *Error
	)
	if body.API != "" {
		if api, stepErr = e.api(body.API, body.Auth); stepErr != nil {
			return expr.Step{}, stepErr
		}
	}
	request, stepErr := e.buildRawRequest(step)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	input := map[string]any{
		"api":     body.API,
		"method":  request.Method,
		"url":     request.URL,
		"path":    request.Path,
		"query":   request.Query,
		"headers": headerNames(request.Headers),
	}
	e.writeStepJSON(path, "input.json", input)

	// The request body is hashed rather than logged: it may carry a secret
	// that has no business in input.json.
	cacheKey, cached, hit := e.cacheGet(step, path, input, map[string]string{
		"body": hashBytes(request.Body),
	})
	if hit {
		return cached, nil
	}

	callCtx, cancel := apiContext(ctx, api)
	defer cancel()

	response, err := e.httpClient().Do(callCtx, api, request)
	if err != nil {
		return expr.Step{}, e.httpFailure(callCtx, step, path, err)
	}
	e.writeStepFile(path, "response.body", response.Body)

	result, stepErr := parseOutput(step.ID, body.Parse, string(response.Body))
	if stepErr != nil {
		e.writeStepJSON(path, "output.json", map[string]any{
			"status": expr.StatusFailed,
			"error":  map[string]any{"class": stepErr.Class, "message": stepErr.Msg},
		})
		return expr.Step{}, stepErr
	}

	out := expr.Step{
		Result:     result,
		HTTPStatus: response.Status,
		Headers:    headerMap(response.Headers),
		Body:       result,
	}
	e.writeStepJSON(path, "output.json", map[string]any{
		"status":      expr.StatusSuccess,
		"http_status": response.Status,
		"result":      result,
		"truncated":   response.Truncated,
	})
	e.cachePut(cacheKey, step, out)
	return out, nil
}

// buildRawRequest renders the templated fields of a raw request.
func (e *Engine) buildRawRequest(step *scenario.Step) (*httpx.Request, *Error) {
	body := step.HTTP
	request := &httpx.Request{
		Method:       http.MethodGet,
		ExpectStatus: body.ExpectStatus,
		MaxBytes:     body.MaxBytes.Bytes(),
		Headers:      map[string]string{},
		Query:        map[string]string{},
	}
	if body.Method != "" {
		request.Method = body.Method
	}
	for _, field := range []struct {
		name   string
		source string
		target *string
	}{
		{"url", body.URL, &request.URL},
		{"path", body.Path, &request.Path},
	} {
		if field.source == "" {
			continue
		}
		value, stepErr := e.render(fmt.Sprintf("%s.http.%s", step.ID, field.name), field.source)
		if stepErr != nil {
			return nil, stepErr
		}
		*field.target = value
	}
	for _, name := range sortedKeys(body.Headers) {
		value, stepErr := e.render(fmt.Sprintf("%s.http.headers.%s", step.ID, name), body.Headers[name])
		if stepErr != nil {
			return nil, stepErr
		}
		request.Headers[name] = value
	}
	for _, name := range sortedKeys(body.Query) {
		value, stepErr := e.render(fmt.Sprintf("%s.http.query.%s", step.ID, name), body.Query[name])
		if stepErr != nil {
			return nil, stepErr
		}
		request.Query[name] = value
	}
	if body.Body != "" {
		rendered, stepErr := e.render(fmt.Sprintf("%s.http.body", step.ID), body.Body)
		if stepErr != nil {
			return nil, stepErr
		}
		request.Body = []byte(rendered)
	}
	return request, nil
}

// renderArgs renders the templates inside operation arguments. Only strings
// carry templates; numbers, booleans and nested values pass through.
func (e *Engine) renderArgs(stepID string, args map[string]any) (map[string]any, *Error) {
	rendered := make(map[string]any, len(args))
	for _, name := range sortedKeys(args) {
		value, stepErr := e.renderArg(fmt.Sprintf("%s.http.args.%s", stepID, name), args[name])
		if stepErr != nil {
			return nil, stepErr
		}
		rendered[name] = value
	}
	return rendered, nil
}

func (e *Engine) renderArg(name string, value any) (any, *Error) {
	switch typed := value.(type) {
	case string:
		return e.render(name, typed)
	case []any:
		items := make([]any, 0, len(typed))
		for i, item := range typed {
			rendered, stepErr := e.renderArg(fmt.Sprintf("%s[%d]", name, i), item)
			if stepErr != nil {
				return nil, stepErr
			}
			items = append(items, rendered)
		}
		return items, nil
	case map[string]any:
		fields := make(map[string]any, len(typed))
		for _, key := range sortedKeys(typed) {
			rendered, stepErr := e.renderArg(name+"."+key, typed[key])
			if stepErr != nil {
				return nil, stepErr
			}
			fields[key] = rendered
		}
		return fields, nil
	default:
		return value, nil
	}
}

// api resolves an apis entry into a bound pack, loading and caching the pack
// on first use. The authorisation secret comes from the entry unless the step
// overrides it (section 3.4).
func (e *Engine) api(name, authOverride string) (*httpx.API, *Error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	entry, ok := e.opts.Scenario.APIs[name]
	if !ok {
		return nil, errorf(ClassConfig, "unknown api %q", name)
	}
	secretName := entry.Auth.Secret
	if authOverride != "" {
		secretName = authOverride
	}
	key := name + "\x00" + secretName
	if api, ok := e.apis[key]; ok {
		return api, nil
	}

	packName, stepErr := e.render("apis."+name+".pack", entry.Pack)
	if stepErr != nil {
		return nil, stepErr
	}
	pack, err := packs.Load(entry.From, packName, e.dir)
	if err != nil {
		return nil, wrapf(ClassConfig, err, "apis.%s", name)
	}
	config := make(map[string]string, len(entry.Config))
	for _, field := range sortedKeys(entry.Config) {
		value, stepErr := e.render(fmt.Sprintf("apis.%s.config.%s", name, field), entry.Config[field])
		if stepErr != nil {
			return nil, stepErr
		}
		config[field] = value
	}
	auth, stepErr := e.authSecret(name, secretName)
	if stepErr != nil {
		return nil, stepErr
	}

	api, err := httpx.NewAPI(name, pack, config, auth)
	if err != nil {
		return nil, httpError("", err)
	}
	api.Timeout = entry.Timeout.Duration()
	if e.apis == nil {
		e.apis = map[string]*httpx.API{}
	}
	e.apis[key] = api
	return api, nil
}

// authSecret looks up the secret that feeds the authorisation scheme of a
// pack. An entry without auth is allowed; the pack decides whether it needs
// one.
func (e *Engine) authSecret(apiName, secretName string) (values.Secret, *Error) {
	if secretName == "" {
		return values.Secret{}, nil
	}
	if e.opts.Secrets == nil {
		return values.Secret{}, errorf(ClassConfig, "apis.%s.auth.secret: undeclared secret %q", apiName, secretName)
	}
	secret, ok := e.opts.Secrets.Lookup(secretName)
	if !ok {
		return values.Secret{}, errorf(ClassConfig, "apis.%s.auth.secret: undeclared secret %q", apiName, secretName)
	}
	return secret, nil
}

// httpClient returns the shared HTTP client. Deadlines come from the step
// context, so the client itself has no timeout.
func (e *Engine) httpClient() *httpx.Client {
	if e.http == nil {
		e.http = httpx.New(0)
	}
	return e.http
}

// apiContext applies the timeout of the apis entry on top of the step
// timeout (section 7.4.1).
func apiContext(ctx context.Context, api *httpx.API) (context.Context, context.CancelFunc) {
	if api != nil && api.Timeout > 0 {
		return context.WithTimeout(ctx, api.Timeout)
	}
	return context.WithCancel(ctx)
}

// httpFailure classifies a failed call and records it in the step directory.
// An expired deadline is a timeout, whatever the transport called it.
func (e *Engine) httpFailure(ctx context.Context, step *scenario.Step, path string, err error) *Error {
	stepErr := httpError(step.ID, err)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		stepErr = &Error{Class: ClassTimeout, Msg: stepErr.Msg, Err: stepErr.Err}
	}
	e.writeStepJSON(path, "output.json", map[string]any{
		"status": expr.StatusFailed,
		"error":  map[string]any{"class": stepErr.Class, "message": stepErr.Msg},
	})
	return stepErr
}

// httpError turns a failure of internal/httpx into a step error, keeping the
// class: both packages spell the classes of section 9.1 the same way.
func httpError(stepID string, err error) *Error {
	var typed *httpx.Error
	if !errors.As(err, &typed) {
		return wrapf(ClassCommand, err, "step %s: http", stepID)
	}
	message := typed.Error()
	if stepID != "" {
		message = fmt.Sprintf("step %s: %s", stepID, message)
	}
	return &Error{Class: typed.Class, Msg: message, Err: typed.Err}
}

// httpReadonly reports whether an http step can be repeated safely. A raw
// request is judged by its method, an operation by the readonly flag of the
// pack; an operation whose pack cannot be loaded is not retried (section
// 9.4).
func (e *Engine) httpReadonly(body *scenario.HTTPStep) bool {
	if body.Raw() {
		switch strings.ToUpper(body.Method) {
		case "", http.MethodGet, http.MethodHead:
			return true
		default:
			return false
		}
	}
	apiName, opName := body.APIName()
	api, stepErr := e.api(apiName, body.Auth)
	if stepErr != nil {
		return false
	}
	op, err := api.Pack.Op(opName)
	if err != nil {
		return false
	}
	return op.Readonly
}

// headerMap flattens response headers for the expression context, keeping the
// first value of each header.
func headerMap(headers http.Header) map[string]string {
	flat := make(map[string]string, len(headers))
	for name := range headers {
		flat[name] = headers.Get(name)
	}
	return flat
}

// headerNames lists the header names of a request. Only the names are
// recorded, because a header may carry a token.
func headerNames(headers map[string]string) []string {
	return sortedKeys(headers)
}
