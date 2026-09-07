package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/foxzi/baton/internal/packs"
)

// OpResult is the outcome of a pack operation call.
type OpResult struct {
	Status  int
	Headers http.Header

	// Body is the decoded response body of the last page, after the
	// envelope and before the transform.
	Body any

	// Result is what the scenario sees: the pages concatenated, unwrapped
	// and transformed.
	Result any

	// Pages is how many requests the call made.
	Pages int

	// TruncatedPages reports that pagination stopped at max_pages with more
	// pages left, and Truncated that the result was cut to max_bytes.
	TruncatedPages bool
	Truncated      bool
}

// Op calls an operation of the pack bound to api. Args are the rendered
// arguments of the http step.
func (c *Client) Op(ctx context.Context, api *API, opName string, args map[string]any) (*OpResult, error) {
	op, err := api.Pack.Op(opName)
	if err != nil {
		return nil, errorf(ClassConfig, "%s", err.Error())
	}
	bound, err := op.BindArgs(args)
	if err != nil {
		class := ClassConfig
		if isConstraint(err) {
			class = ClassPolicy
		}
		return nil, &Error{Class: class, Msg: fmt.Sprintf("%s.%s: %s", api.Name, opName, err.Error())}
	}
	path, err := op.ExpandPath(bound.Path)
	if err != nil {
		return nil, errorf(ClassConfig, "%s.%s: %s", api.Name, opName, err.Error())
	}

	request, err := buildOpRequest(op, path, bound)
	if err != nil {
		return nil, err
	}
	what := fmt.Sprintf("%s.%s", api.Name, opName)

	result := &OpResult{}
	if op.Paginate {
		if err := c.paginate(ctx, api, op, request, what, result); err != nil {
			return nil, err
		}
	} else {
		response, err := c.Do(ctx, api, request)
		if err != nil {
			return nil, err
		}
		body, err := decodeJSON(response.Body, what)
		if err != nil {
			return nil, err
		}
		unwrapped, err := api.Pack.Envelope.Unwrapped(body)
		if err != nil {
			return nil, errorf(ClassCommand, "%s: %s", what, err.Error())
		}
		result.Status, result.Headers, result.Pages = response.Status, response.Headers, 1
		result.Body, result.Result = unwrapped, unwrapped
	}

	transformed, err := op.Transformed(result.Result)
	if err != nil {
		return nil, errorf(ClassCommand, "%s: %s", what, err.Error())
	}
	capped, truncated := capResult(transformed, op.MaxBytes.Bytes())
	result.Result, result.Truncated = capped, truncated
	return result, nil
}

// buildOpRequest turns bound arguments into a request.
func buildOpRequest(op *packs.Op, path string, bound *packs.BoundArgs) (*Request, error) {
	request := &Request{
		Method:  op.Method(),
		Path:    path,
		Query:   bound.Query,
		Headers: map[string]string{},
	}
	switch {
	case len(bound.Form) > 0:
		form := url.Values{}
		for name, value := range bound.Form {
			form.Set(name, value)
		}
		request.Body = []byte(form.Encode())
		request.Headers["Content-Type"] = "application/x-www-form-urlencoded"
	case len(bound.Body) > 0:
		encoded, err := json.Marshal(bound.Body)
		if err != nil {
			return nil, wrapf(ClassConfig, err, "%s: the arguments are not JSON", op.Name())
		}
		request.Body = encoded
		request.Headers["Content-Type"] = "application/json"
	}
	return request, nil
}

// paginate walks the pages of an operation and concatenates their items.
func (c *Client) paginate(ctx context.Context, api *API, op *packs.Op, request *Request, what string, result *OpResult) error {
	strategy := api.Pack.PageStrategy(op)
	if strategy == nil {
		return errorf(ClassConfig, "%s: paginate is set but no pagination strategy is declared", what)
	}
	maxPages := strategy.Pages()
	collected := []any{}

	for page := 1; page <= maxPages; page++ {
		attempt := *request
		if err := applyPageParams(&attempt, strategy, page); err != nil {
			return errorf(ClassConfig, "%s: %s", what, err.Error())
		}
		response, err := c.Do(ctx, api, &attempt)
		if err != nil {
			return err
		}
		body, err := decodeJSON(response.Body, what)
		if err != nil {
			return err
		}
		unwrapped, err := api.Pack.Envelope.Unwrapped(body)
		if err != nil {
			return errorf(ClassCommand, "%s: %s", what, err.Error())
		}
		items, err := strategy.PageItems(unwrapped)
		if err != nil {
			return errorf(ClassCommand, "%s: %s", what, err.Error())
		}
		list, ok := items.([]any)
		if !ok {
			return errorf(ClassCommand, "%s: a page of a paginated operation must be a list, got %T; set pagination.items", what, items)
		}
		collected = append(collected, list...)
		result.Status, result.Headers, result.Body, result.Pages = response.Status, response.Headers, unwrapped, page

		next, done := nextPage(strategy, response, list)
		if done {
			result.Result = collected
			return nil
		}
		if next != "" {
			attempt.URL, attempt.Path, attempt.Query = next, "", nil
			*request = attempt
		}
	}
	result.Result, result.TruncatedPages = collected, true
	return nil
}

// applyPageParams sets the page parameters of the request for one page. The
// link_header style needs none: the next URL carries them.
func applyPageParams(request *Request, strategy *packs.Pagination, page int) error {
	if strategy.Style != packs.PagePage {
		return nil
	}
	if strategy.In == packs.InBody {
		return fmt.Errorf("pagination.in: body is not supported for the page style yet")
	}
	query := map[string]string{}
	for name, value := range request.Query {
		query[name] = value
	}
	query[strategy.Param] = strconv.Itoa(page)
	if strategy.SizeParam != "" && strategy.Size > 0 {
		query[strategy.SizeParam] = strconv.Itoa(strategy.Size)
	}
	request.Query = query
	return nil
}

// nextPage decides how the walk continues: the URL of the following page, or
// done when the last page has been seen.
func nextPage(strategy *packs.Pagination, response *Response, page []any) (next string, done bool) {
	switch strategy.Style {
	case packs.PageLinkHeader:
		next = linkNext(response.Headers.Get("Link"))
		return next, next == ""
	case packs.PagePage:
		switch {
		case len(page) == 0:
			return "", true
		case strategy.Size > 0 && len(page) < strategy.Size:
			return "", true
		}
		return "", false
	default:
		return "", true
	}
}

// linkNext returns the URL of the rel="next" link of a Link header.
func linkNext(header string) string {
	for _, link := range strings.Split(header, ",") {
		parts := strings.Split(link, ";")
		if len(parts) < 2 {
			continue
		}
		target := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, param := range parts[1:] {
			value := strings.ReplaceAll(strings.TrimSpace(param), `"`, "")
			if value == "rel=next" {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}

// capResult cuts a result down to max bytes, measured as JSON. Lists lose
// their tail and text is cut; other values are reported as truncated without
// being changed, because cutting them would change their shape.
func capResult(result any, maxBytes int64) (any, bool) {
	if maxBytes <= 0 || jsonSize(result) <= maxBytes {
		return result, false
	}
	switch typed := result.(type) {
	case []any:
		kept := typed
		for len(kept) > 0 && jsonSize(kept) > maxBytes {
			kept = kept[:len(kept)-1]
		}
		return kept, true
	case string:
		if int64(len(typed)) > maxBytes {
			return typed[:maxBytes], true
		}
		return typed, true
	default:
		return result, true
	}
}

// jsonSize is the size of a value as JSON, which is how max_bytes is
// measured.
func jsonSize(value any) int64 {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return int64(len(encoded))
}

// isConstraint reports whether an argument error came from a params
// constraint, which section 9.1 classifies as policy.
func isConstraint(err error) bool {
	return errors.Is(err, packs.ErrConstraint)
}
