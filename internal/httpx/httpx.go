// Package httpx executes the HTTP requests of http steps: raw requests and
// pack operations (spec sections 3.4 and 7.4).
//
// The package knows the protocol and nothing about any service: base URLs,
// authorisation schemes, pagination, envelopes and result transforms all come
// from the pack. Authorisation values are values.Secret and are only ever
// revealed into an outgoing request, never into a result or a message; the
// secret redactor of the run masks them in everything the runner writes,
// including the {auth} segment of a URL.
//
// The exchange authorisation scheme trades a configured secret for a token
// through an operation of the pack itself; the token and, when the pack
// keeps a session, its cookies live only in memory for the run and are
// injected into the requests that follow, never written to disk.
//
// The rate_limit section of a pack is parsed but not acted upon yet;
// respecting the remaining quota belongs to the resilience milestone.
package httpx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/foxzi/baton/internal/packs"
	"github.com/foxzi/baton/internal/values"
)

// DefaultMaxBytes caps how much of a response body is read when neither the
// step nor the operation says otherwise.
const DefaultMaxBytes = 8 << 20

// bodyTailBytes is how much of a failed response is quoted in the error.
const bodyTailBytes = 512

// Client executes requests.
type Client struct {
	http *http.Client
}

// New returns a client. The zero timeout leaves the request deadline to the
// context, which the engine derives from the step timeout.
func New(timeout time.Duration) *Client {
	return &Client{http: &http.Client{Timeout: timeout}}
}

// API is a pack bound to the configuration of one apis entry.
type API struct {
	// Name is the name the scenario gave the API.
	Name string

	Pack   *packs.Pack
	Config map[string]string
	Auth   values.Secret

	// Timeout overrides the client timeout for this API.
	Timeout time.Duration

	// exchange is the state the exchange scheme keeps across the calls of a
	// run: nil when the pack authorises some other way.
	exchange *exchangeState
}

// NewAPI merges the configuration of an apis entry with the pack defaults and
// checks that the pack has everything it needs.
func NewAPI(name string, pack *packs.Pack, config map[string]string, auth values.Secret) (*API, error) {
	merged := map[string]string{}
	for field, declared := range pack.Config {
		if declared.Default != "" {
			merged[field] = declared.Default
		}
	}
	var problems []error
	for field, value := range config {
		if _, ok := pack.Config[field]; !ok {
			problems = append(problems, fmt.Errorf("apis.%s.config.%s: pack %s has no such setting", name, field, pack.Pack))
			continue
		}
		merged[field] = value
	}
	for field, declared := range pack.Config {
		if declared.Required && merged[field] == "" {
			problems = append(problems, fmt.Errorf("apis.%s.config.%s: required by pack %s", name, field, pack.Pack))
		}
	}
	if merged["base_url"] == "" {
		problems = append(problems, fmt.Errorf("apis.%s.config.base_url: required", name))
	}
	if pack.Auth != nil && auth.IsZero() {
		problems = append(problems, fmt.Errorf("apis.%s.auth: pack %s authorises with %s, set auth.secret", name, pack.Pack, pack.Auth.Kind))
	}
	if err := errors.Join(problems...); err != nil {
		return nil, &Error{Class: ClassConfig, Msg: err.Error()}
	}
	api := &API{Name: name, Pack: pack, Config: merged, Auth: auth}
	if pack.Auth.IsExchange() {
		api.exchange = &exchangeState{}
		if pack.Auth.KeepsCookies() {
			jar, err := cookiejar.New(nil)
			if err != nil {
				return nil, &Error{Class: ClassConfig, Msg: fmt.Sprintf("apis.%s.auth.session: cannot build a cookie jar: %s", name, err)}
			}
			api.exchange.jar = jar
		}
	}
	return api, nil
}

// Request is a raw HTTP request, the second form of an http step. When API is
// given, Path is resolved against its base URL; otherwise URL is absolute.
type Request struct {
	Method  string
	URL     string
	Path    string
	Headers map[string]string
	Query   map[string]string
	Body    []byte

	// ExpectStatus lists the statuses that count as success. Empty means any
	// 2xx status.
	ExpectStatus []int

	// MaxBytes caps how much of the response body is read.
	MaxBytes int64
}

// Response is a raw HTTP response.
type Response struct {
	Status  int
	Headers http.Header
	Body    []byte

	// Truncated reports that the body hit the byte cap and is incomplete.
	Truncated bool
}

// Do performs a raw request. API may be nil when the request carries an
// absolute URL and needs no authorisation.
func (c *Client) Do(ctx context.Context, api *API, req *Request) (*Response, error) {
	req, err := c.authorize(ctx, api, req)
	if err != nil {
		return nil, err
	}
	built, err := c.build(ctx, api, req)
	if err != nil {
		return nil, api.redactError(err)
	}
	response, err := c.send(api, built, req.MaxBytes)
	if err != nil {
		return nil, api.redactError(err)
	}
	if !statusExpected(response.Status, req.ExpectStatus) {
		return response, api.redactError(&Error{
			Class:    classForStatus(response.Status),
			Msg:      fmt.Sprintf("%s %s returned %d", built.Method, built.URL.Redacted(), response.Status),
			Status:   response.Status,
			BodyTail: tail(response.Body),
		})
	}
	return response, nil
}

// build turns a request into an *http.Request with the base URL, query and
// authorisation of the API applied.
func (c *Client) build(ctx context.Context, api *API, req *Request) (*http.Request, error) {
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	raw := req.URL
	if raw == "" {
		if api == nil {
			return nil, errorf(ClassConfig, "http: neither url nor api is set")
		}
		raw = strings.TrimSuffix(api.Config["base_url"], "/") + req.Path
	}
	query := map[string]string{}
	for name, value := range req.Query {
		query[name] = value
	}
	if api != nil {
		var err error
		if raw, err = applyPathAuth(raw, api); err != nil {
			return nil, err
		}
	}
	target, err := url.Parse(raw)
	if err != nil {
		return nil, wrapf(ClassConfig, err, "http: %q is not a URL", raw)
	}
	if len(query) > 0 {
		params := target.Query()
		for name, value := range query {
			params.Set(name, value)
		}
		target.RawQuery = params.Encode()
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	built, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, wrapf(ClassConfig, err, "http: cannot build the request")
	}
	for name, value := range req.Headers {
		built.Header.Set(name, value)
	}
	if api != nil {
		if err := applyAuth(built, api); err != nil {
			return nil, err
		}
	}
	return built, nil
}

// send performs a built request and reads its body up to the cap. It uses
// the API's own cookie jar when the pack keeps a session across an exchange.
func (c *Client) send(api *API, req *http.Request, maxBytes int64) (*Response, error) {
	response, err := c.doer(api).Do(req)
	if err != nil {
		class := ClassTransient
		if errors.Is(err, context.DeadlineExceeded) {
			class = ClassTimeout
		}
		// The URL can carry an authorisation segment, so the error names the
		// request through url.URL.Redacted and leaves the rest to the
		// secret redactor.
		return nil, wrapf(class, err, "%s %s failed", req.Method, req.URL.Redacted())
	}
	defer response.Body.Close()

	limit := maxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, wrapf(ClassTransient, err, "%s %s: reading the response failed", req.Method, req.URL.Redacted())
	}
	truncated := int64(len(body)) > limit
	if truncated {
		body = body[:limit]
	}
	return &Response{
		Status:    response.StatusCode,
		Headers:   response.Header,
		Body:      body,
		Truncated: truncated,
	}, nil
}

// applyAuth injects the authorisation value into the request, following the
// scheme the pack declares.
func applyAuth(req *http.Request, api *API) error {
	scheme := api.Pack.Auth
	if scheme == nil || api.Auth.IsZero() {
		return nil
	}
	token := api.Auth.Reveal()
	switch scheme.Kind {
	case packs.AuthHeader:
		req.Header.Set(scheme.Name, token)
	case packs.AuthBearer:
		req.Header.Set("Authorization", "Bearer "+token)
	case packs.AuthBasic:
		field := scheme.User
		if field == "" {
			field = "user"
		}
		pair := api.Config[field] + ":" + token
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(pair)))
	case packs.AuthQuery:
		query := req.URL.Query()
		query.Set(scheme.Name, token)
		req.URL.RawQuery = query.Encode()
	case packs.AuthPath:
		// Already substituted into the URL by applyPathAuth.
	case packs.AuthExchange:
		// The token was already injected into the request by authorize;
		// the configured secret here only authorises the exchange call
		// itself, which builds its own request through applyAuth with the
		// base scheme.
	default:
		return errorf(ClassConfig, "apis.%s: authorisation scheme %q is not supported", api.Name, scheme.Kind)
	}
	return nil
}

// authPlaceholder is the segment a path scheme replaces with the token.
const authPlaceholder = "{auth}"

// applyPathAuth substitutes the authorisation value into a URL that carries
// the {auth} placeholder.
func applyPathAuth(raw string, api *API) (string, error) {
	if api.Pack.Auth == nil || api.Pack.Auth.Kind != packs.AuthPath {
		if strings.Contains(raw, authPlaceholder) {
			return "", errorf(ClassConfig, "apis.%s: the URL uses {auth} but the pack does not authorise through the path", api.Name)
		}
		return raw, nil
	}
	if !strings.Contains(raw, authPlaceholder) {
		return "", errorf(ClassConfig, "apis.%s: pack %s authorises through the path but neither base_url nor the operation path has {auth}", api.Name, api.Pack.Pack)
	}
	return strings.ReplaceAll(raw, authPlaceholder, url.PathEscape(api.Auth.Reveal())), nil
}

// statusExpected reports whether a status counts as success.
func statusExpected(status int, expected []int) bool {
	if len(expected) > 0 {
		return slices.Contains(expected, status)
	}
	return status >= 200 && status < 300
}

// tail quotes the beginning of a response body for an error message.
func tail(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	if len(text) > bodyTailBytes {
		text = text[:bodyTailBytes] + "…"
	}
	return strconv.Quote(text)
}

// decodeJSON decodes a response body into a JSON value.
func decodeJSON(body []byte, what string) (any, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, wrapf(ClassCommand, err, "%s: the response is not JSON", what)
	}
	return value, nil
}
