package httpx

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/foxzi/baton/internal/packs"
	"github.com/foxzi/baton/internal/values"
)

// exchangeState holds what an exchange scheme accumulates over a run: the
// token it traded the secret for and the cookies of its session. Both live
// only in memory, they are never written to the run directory or a cache
// (spec section 7.4.3).
type exchangeState struct {
	// mu serialises the exchange itself, so that concurrent callers trade
	// the secret once rather than once each.
	mu sync.Mutex

	// token is stored atomically because redactError reads it while the
	// lock is held: an exchange call that fails is reported through the
	// same path as any other request.
	token atomic.Pointer[string]

	// expires is only touched under mu, beside the exchange it belongs to.
	expires time.Time

	// jar keeps the cookies of the session when the pack asks for one.
	jar http.CookieJar
}

// authorize returns req unchanged unless the pack authorises with the
// exchange scheme, in which case it returns a copy of req carrying the
// token in the place the pack asks for.
func (c *Client) authorize(ctx context.Context, api *API, req *Request) (*Request, error) {
	if api == nil || api.Pack == nil || !api.Pack.Auth.IsExchange() {
		return req, nil
	}
	token, err := c.exchangeToken(ctx, api)
	if err != nil {
		return nil, err
	}
	return injectToken(api, req, token)
}

// exchangeToken returns the token of the exchange scheme, calling the
// exchange operation when none is cached or the cached one has expired. The
// lock spans the whole operation so that concurrent callers make one
// exchange rather than several.
func (c *Client) exchangeToken(ctx context.Context, api *API) (string, error) {
	if api.exchange == nil {
		return "", errorf(ClassConfig, "apis.%s: the pack does not authorise with the exchange scheme", api.Name)
	}
	state := api.exchange
	state.mu.Lock()
	defer state.mu.Unlock()

	if cached := state.token.Load(); cached != nil && (state.expires.IsZero() || time.Now().Before(state.expires)) {
		return *cached, nil
	}

	result, err := c.Op(ctx, api.exchangeAPI(), api.Pack.Auth.Op, nil)
	if err != nil {
		return "", err
	}
	token, err := api.Pack.Auth.ExchangeToken(result.Body)
	if err != nil {
		return "", errorf(ClassCommand, "apis.%s: %s", api.Name, err)
	}

	state.token.Store(&token)
	if ttl := api.Pack.Auth.TTL.Duration(); ttl > 0 {
		state.expires = time.Now().Add(ttl)
	} else {
		state.expires = time.Time{}
	}
	return token, nil
}

// exchangeAPI builds the API the exchange call itself is made through: the
// same pack, config and secret, but authorised with the base scheme rather
// than the exchange scheme, and sharing the cookie jar of the calls the
// exchange authorises.
func (a *API) exchangeAPI() *API {
	pack := *a.Pack
	pack.Auth = a.Pack.Auth.Base
	return &API{
		Name:     a.Name,
		Pack:     &pack,
		Config:   a.Config,
		Auth:     a.Auth,
		Timeout:  a.Timeout,
		exchange: a.exchange,
	}
}

// injectToken returns a copy of req with the token placed where the pack's
// auth.inject says it goes.
func injectToken(api *API, req *Request, token string) (*Request, error) {
	inject := api.Pack.Auth.Inject
	built := *req
	switch inject.In {
	case packs.InHeader:
		headers := make(map[string]string, len(req.Headers)+1)
		for key, value := range req.Headers {
			headers[key] = value
		}
		headers[inject.Name] = token
		built.Headers = headers
	case packs.InQuery:
		query := make(map[string]string, len(req.Query)+1)
		for key, value := range req.Query {
			query[key] = value
		}
		query[inject.Name] = token
		built.Query = query
	case packs.InForm:
		if err := setFormField(&built, inject.Name, token); err != nil {
			return nil, errorf(ClassConfig, "apis.%s: injecting the token failed: %s", api.Name, err)
		}
	case packs.InBody:
		if err := setJSONField(&built, inject.Name, token, false); err != nil {
			return nil, errorf(ClassConfig, "apis.%s: injecting the token failed: %s", api.Name, err)
		}
	default:
		// Validation already rejects any other placement; this is the
		// belt-and-braces branch for a pack that was built some other way.
		return nil, errorf(ClassConfig, "apis.%s: %q is not a placement for a token", api.Name, inject.In)
	}
	return &built, nil
}

// doer returns the HTTP client a request against api should use: the
// client's own unless the pack keeps a session, in which case the shared
// cookie jar of the exchange is attached. Copying the client is cheap, the
// transport underneath is shared, and the jar is safe for concurrent use
// once built.
func (c *Client) doer(api *API) *http.Client {
	if api == nil || api.exchange == nil || api.exchange.jar == nil {
		return c.http
	}
	client := *c.http
	client.Jar = api.exchange.jar
	return &client
}

// redactError replaces a live exchange token with values.Redacted in the
// message and body tail of a failed request. The run redactor only knows
// the secrets the scenario configured, not a token an exchange traded for
// one, so a token that ends up in a URL query or a quoted body tail is
// redacted here instead.
func (a *API) redactError(err error) error {
	if a == nil || a.exchange == nil || err == nil {
		return err
	}
	typed, ok := err.(*Error)
	if !ok {
		return err
	}
	token := a.exchange.token.Load()
	if token == nil || *token == "" {
		return err
	}
	redacted := *typed
	redacted.Msg = strings.ReplaceAll(typed.Msg, *token, values.Redacted)
	redacted.BodyTail = strings.ReplaceAll(typed.BodyTail, *token, values.Redacted)
	return &redacted
}
