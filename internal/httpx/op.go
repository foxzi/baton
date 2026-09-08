package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
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
		if err := graphQLError(api.Pack, op, body, what); err != nil {
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
	if op.IsGraphQL() {
		payload := map[string]any{graphQLQueryField: op.Document()}
		if len(bound.Body) > 0 {
			payload[graphQLVariablesField] = bound.Body
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, wrapf(ClassConfig, err, "%s: the arguments are not JSON", op.Name())
		}
		request.Body = encoded
		request.Headers["Content-Type"] = "application/json"
		return request, nil
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

// pageWalk is where the walk of a paginated operation has got to: which page
// comes next and how the strategy asks for it.
type pageWalk struct {
	// number is the page number, from 1, for the page style.
	number int
	// offset is the item offset, for the offset style.
	offset int
	// cursor is the token of the next page, for the cursor style, empty
	// before the first page has been read.
	cursor string
	// url is the whole URL of the next page, when the service gives one.
	url string
}

// paginate walks the pages of an operation and concatenates their items.
func (c *Client) paginate(ctx context.Context, api *API, op *packs.Op, request *Request, what string, result *OpResult) error {
	strategy := api.Pack.PageStrategy(op)
	if strategy == nil {
		return errorf(ClassConfig, "%s: paginate is set but no pagination strategy is declared", what)
	}
	maxPages := strategy.Pages()
	collected := []any{}
	walk := pageWalk{number: 1}

	for page := 1; page <= maxPages; page++ {
		attempt := *request
		if walk.url != "" {
			attempt.URL, attempt.Path, attempt.Query = walk.url, "", nil
		} else if err := applyPageParams(&attempt, strategy, walk, op.IsGraphQL()); err != nil {
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
		if err := graphQLError(api.Pack, op, body, what); err != nil {
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

		done, err := advance(strategy, response, unwrapped, list, &walk)
		if err != nil {
			return errorf(ClassCommand, "%s: %s", what, err.Error())
		}
		if done {
			result.Result = collected
			return nil
		}
	}
	result.Result, result.TruncatedPages = collected, true
	return nil
}

// applyPageParams sets the page parameters of the request for one page. The
// link_header style needs none: the next URL carries them.
func applyPageParams(request *Request, strategy *packs.Pagination, walk pageWalk, graphql bool) error {
	switch strategy.Style {
	case packs.PagePage:
		if err := setPageParam(request, strategy.In, graphql, strategy.Param, walk.number); err != nil {
			return err
		}
		if strategy.SizeParam != "" && strategy.Size > 0 {
			return setPageParam(request, strategy.In, graphql, strategy.SizeParam, strategy.Size)
		}
	case packs.PageOffset:
		if err := setPageParam(request, strategy.In, graphql, strategy.Param, walk.offset); err != nil {
			return err
		}
		if strategy.LimitParam != "" && strategy.Size > 0 {
			return setPageParam(request, strategy.In, graphql, strategy.LimitParam, strategy.Size)
		}
	case packs.PageCursor:
		// The first page is the operation's own request: there is no cursor
		// to send until a page has named one.
		if walk.cursor != "" {
			return setPageParam(request, strategy.In, graphql, strategy.Param, walk.cursor)
		}
	}
	return nil
}

// setPageParam puts one pagination parameter where the pack asks for it: in
// the query string, or in the request body beside the arguments.
func setPageParam(request *Request, in packs.ParamIn, graphql bool, name string, value any) error {
	if in != packs.InBody {
		query := make(map[string]string, len(request.Query)+1)
		for key, existing := range request.Query {
			query[key] = existing
		}
		query[name] = paramText(value)
		request.Query = query
		return nil
	}

	headers := make(map[string]string, len(request.Headers)+1)
	for key, existing := range request.Headers {
		headers[key] = existing
	}
	if strings.HasPrefix(headers["Content-Type"], "application/x-www-form-urlencoded") {
		form, err := url.ParseQuery(string(request.Body))
		if err != nil {
			return fmt.Errorf("pagination.in: the form body cannot be read: %s", err)
		}
		form.Set(name, paramText(value))
		request.Body, request.Headers = []byte(form.Encode()), headers
		return nil
	}

	body := map[string]any{}
	if len(request.Body) > 0 {
		if err := json.Unmarshal(request.Body, &body); err != nil {
			return fmt.Errorf("pagination.in: body needs a JSON object body: %s", err)
		}
	}
	// A GraphQL request carries its arguments in variables, and so do its
	// page parameters.
	if graphql {
		graphQLVariables(body)[name] = value
	} else {
		body[name] = value
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("pagination.in: the page parameters are not JSON: %s", err)
	}
	headers["Content-Type"] = "application/json"
	request.Body, request.Headers = encoded, headers
	return nil
}

// paramText renders a pagination parameter for a query string or a form.
func paramText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

// advance decides whether the walk is over and, when it is not, where the
// following page comes from.
func advance(strategy *packs.Pagination, response *Response, body any, page []any, walk *pageWalk) (bool, error) {
	walk.url = ""
	switch strategy.Style {
	case packs.PageLinkHeader:
		walk.url = linkNext(response.Headers.Get("Link"))
		return walk.url == "", nil
	case packs.PagePage:
		if shortPage(strategy, page) {
			return true, nil
		}
		walk.number++
		return false, nil
	case packs.PageOffset:
		if shortPage(strategy, page) {
			return true, nil
		}
		step := strategy.Size
		if step <= 0 {
			step = len(page)
		}
		walk.offset += step
		total, known, err := strategy.PageTotal(body)
		if err != nil {
			return false, err
		}
		return known && walk.offset >= total, nil
	case packs.PageCursor:
		cursor, err := strategy.NextCursor(body)
		if err != nil {
			return false, err
		}
		text := cursorText(cursor)
		switch {
		case text == "":
			return true, nil
		case strings.HasPrefix(text, "http://"), strings.HasPrefix(text, "https://"):
			walk.url = text
		default:
			walk.cursor = text
		}
		return false, nil
	default:
		return true, nil
	}
}

// shortPage reports a page that ends the walk: an empty one, or one shorter
// than the size that was asked for.
func shortPage(strategy *packs.Pagination, page []any) bool {
	return len(page) == 0 || (strategy.Size > 0 && len(page) < strategy.Size)
}

// cursorText is the cursor as the service sent it: a string, or a number a
// service uses as an opaque token. Anything else means no next page.
func cursorText(cursor any) string {
	switch typed := cursor.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return ""
	default:
		return fmt.Sprint(typed)
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
