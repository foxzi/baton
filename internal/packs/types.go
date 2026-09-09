// Package packs loads and validates API packs (spec section 7.4).
//
// A pack is a YAML file that teaches baton about one service: its base URL,
// authorisation scheme, response envelope, pagination and operations. The
// binary knows the protocol, the pack knows the service. Packs never contain
// secret values, only the names of the authorisation parameters.
//
// v1 covers local directory and pinned git sources, all six authorisation
// schemes, GraphQL operations and all four pagination styles. The exchange
// scheme does not yet chain: a pack that sets auth.depends_on is rejected,
// because the format of a chain of exchanges is not settled. Interface
// conformance checks are rejected with a clear error until the milestone
// that implements them.
package packs

import (
	"regexp"
	"strings"

	"github.com/foxzi/baton/internal/units"
	"github.com/itchyny/gojq"
)

// Pack is a parsed pack file.
type Pack struct {
	Pack        string                 `yaml:"pack"`
	Version     int                    `yaml:"version"`
	Description string                 `yaml:"description"`
	Config      map[string]ConfigField `yaml:"config"`
	Auth        *Auth                  `yaml:"auth"`
	RateLimit   *RateLimit             `yaml:"rate_limit"`
	Envelope    *Envelope              `yaml:"envelope"`
	Pagination  *Pagination            `yaml:"pagination"`
	Ops         map[string]*Op         `yaml:"ops"`

	// Path is the file the pack was read from, for diagnostics.
	Path string `yaml:"-"`

	// Digest is the checksum of the content the pack was loaded from. It
	// goes into the cache key of a step, so that editing a pack does not
	// leave the steps that call it answering from the cache. It is empty
	// for a pack parsed straight from bytes.
	Digest string `yaml:"-"`
}

// ConfigField declares a setting the scenario may supply in api.config.
type ConfigField struct {
	Default  string `yaml:"default"`
	Required bool   `yaml:"required"`
}

// AuthKind is an authorisation scheme (spec section 7.4.3).
type AuthKind string

// Authorisation schemes named by the pack format.
const (
	AuthHeader   AuthKind = "header"
	AuthBearer   AuthKind = "bearer"
	AuthBasic    AuthKind = "basic"
	AuthQuery    AuthKind = "query"
	AuthPath     AuthKind = "path"
	AuthExchange AuthKind = "exchange"
)

// Auth says where the authorisation value goes.
type Auth struct {
	Kind AuthKind `yaml:"kind"`

	// Name is the header or query parameter name, for the header and query
	// schemes.
	Name string `yaml:"name"`

	// User names the config field holding the user half of basic
	// authorisation; it defaults to user.
	User string `yaml:"user"`

	// Op names the operation of this pack that trades the configured
	// secret for a token, for the exchange scheme.
	Op string `yaml:"op"`

	// Base authorises the exchange call itself.
	Base *Auth `yaml:"base"`

	// Extract pulls the token out of the response of the exchange
	// operation.
	Extract string `yaml:"extract"`

	// Inject says where the token goes in the calls that follow.
	Inject *Inject `yaml:"inject"`

	// TTL is how long a token is reused; zero keeps it for the whole run.
	TTL units.Duration `yaml:"ttl"`

	// Session keeps cookies between the calls of a run when set to
	// cookies.
	Session Session `yaml:"session"`

	// DependsOn names a preceding exchange chain. The format of a chain is
	// not settled yet, so the loader rejects the field.
	DependsOn string `yaml:"depends_on"`

	extract *gojq.Code
}

// Inject says where a token the exchange scheme obtained goes in the calls
// that follow.
type Inject struct {
	In   ParamIn `yaml:"in"`
	Name string  `yaml:"name"`
}

// Session is how a run keeps state between the calls an exchange scheme
// authorises.
type Session string

// SessionCookies keeps the cookies a server sets across the calls of a run.
const SessionCookies Session = "cookies"

// IsExchange reports whether the scheme trades a secret for a token before
// every call, rather than sending the secret itself.
func (a *Auth) IsExchange() bool {
	return a != nil && a.Kind == AuthExchange
}

// KeepsCookies reports whether the scheme keeps a cookie jar across the
// calls of a run.
func (a *Auth) KeepsCookies() bool {
	return a != nil && a.Session == SessionCookies
}

// RateLimit names the response headers that report the remaining quota.
type RateLimit struct {
	RemainingHeader  string `yaml:"remaining_header"`
	RetryAfterHeader string `yaml:"retry_after_header"`
}

// Envelope unwraps a response body and recognises errors reported with a 200
// status.
type Envelope struct {
	Unwrap       string `yaml:"unwrap"`
	ErrorWhen    string `yaml:"error_when"`
	ErrorMessage string `yaml:"error_message"`

	unwrap       *gojq.Code
	errorWhen    *gojq.Code
	errorMessage *gojq.Code
}

// PageStyle is a pagination strategy (spec section 7.4.4).
type PageStyle string

// Pagination styles named by the pack format.
const (
	PageLinkHeader PageStyle = "link_header"
	PagePage       PageStyle = "page"
	PageOffset     PageStyle = "offset"
	PageCursor     PageStyle = "cursor"
)

// DefaultMaxPages caps pagination when the pack does not.
const DefaultMaxPages = 20

// Pagination configures how pages are walked.
type Pagination struct {
	Style    PageStyle `yaml:"style"`
	MaxPages int       `yaml:"max_pages"`

	// Param is the page number, offset or cursor parameter.
	Param string `yaml:"param"`

	// SizeParam and LimitParam name the page size parameter of the page and
	// offset styles; Size is the value sent.
	SizeParam  string `yaml:"size_param"`
	LimitParam string `yaml:"limit_param"`
	Size       int    `yaml:"size"`

	// In is where the parameters go: query or body.
	In ParamIn `yaml:"in"`

	// Next extracts the next cursor, Total the total count and Items the
	// page items when the page is an object rather than an array.
	Next  string `yaml:"next"`
	Total string `yaml:"total"`
	Items string `yaml:"items"`

	items *gojq.Code
	next  *gojq.Code
	total *gojq.Code
}

// Pages returns the page cap in effect.
func (p *Pagination) Pages() int {
	if p == nil || p.MaxPages <= 0 {
		return DefaultMaxPages
	}
	return p.MaxPages
}

// ParamIn is where an argument is sent.
type ParamIn string

// Argument placements.
const (
	InUnset ParamIn = ""
	InPath  ParamIn = "path"
	InQuery ParamIn = "query"
	InBody  ParamIn = "body"
	InForm  ParamIn = "form"

	// InHeader places a value in a request header. Operation params never
	// use it: they keep query, body, form or path; only the token an
	// exchange scheme injects can go into a header.
	InHeader ParamIn = "header"
)

// Encoding is the request body encoding.
type Encoding string

// Body encodings supported in v1.
const (
	EncodeUnset Encoding = ""
	EncodeJSON  Encoding = "json"
	EncodeForm  Encoding = "form"

	// EncodePath marks a parameter whose value must be percent-encoded as a
	// whole path segment, even when it contains slashes.
	EncodePath Encoding = "path"
)

// KindGraphQL is the only non-REST operation kind: the operation POSTs a
// GraphQL document and sends its arguments as variables.
const KindGraphQL = "graphql"

// Op is one operation of a pack.
type Op struct {
	Get    string `yaml:"get"`
	Post   string `yaml:"post"`
	Put    string `yaml:"put"`
	Patch  string `yaml:"patch"`
	Delete string `yaml:"delete"`

	// Kind marks a non-REST operation; only graphql is defined.
	Kind string `yaml:"kind"`

	// Query is the GraphQL document of a graphql operation, either inline
	// or a *.graphql file beside the pack.
	Query string `yaml:"query"`

	Description string            `yaml:"description"`
	Readonly    bool              `yaml:"readonly"`
	Encode      Encoding          `yaml:"encode"`
	Params      map[string]*Param `yaml:"params"`
	Transform   string            `yaml:"transform"`
	Pick        []string          `yaml:"pick"`
	Paginate    bool              `yaml:"paginate"`
	Pagination  *Pagination       `yaml:"pagination"`
	MaxBytes    units.ByteSize    `yaml:"max_bytes"`
	Implements  string            `yaml:"implements"`

	name      string
	method    string
	path      string
	query     string
	transform *gojq.Code
}

// Name returns the operation name as written in ops.
func (o *Op) Name() string { return o.name }

// Method returns the HTTP method of the operation.
func (o *Op) Method() string { return o.method }

// Path returns the operation path, with {param} placeholders unresolved.
func (o *Op) Path() string { return o.path }

// IsGraphQL reports whether the operation sends a GraphQL document.
func (o *Op) IsGraphQL() bool { return o.Kind == KindGraphQL }

// Document returns the GraphQL document of the operation, read from the file
// named by query when the pack points at one.
func (o *Op) Document() string { return o.query }

// Param declares one argument of an operation.
type Param struct {
	// Name is what the service calls the argument, when that differs from
	// the name a scenario writes. It is what lets a pack implement an
	// interface whose argument names are not the service's own: notify/v1
	// says target, the Telegram API says chat_id. Path arguments cannot
	// rename, since the path placeholder already names them.
	Name string `yaml:"name"`

	Pattern  string   `yaml:"pattern"`
	MaxLen   int      `yaml:"max_len"`
	Enum     []string `yaml:"enum"`
	In       ParamIn  `yaml:"in"`
	Encode   Encoding `yaml:"encode"`
	Required *bool    `yaml:"required"`
	Default  any      `yaml:"default"`

	pattern *regexp.Regexp
}

// IsRequired reports whether the argument must be supplied. Arguments are
// required unless the pack says otherwise or supplies a default.
func (p *Param) IsRequired() bool {
	if p.Required != nil {
		return *p.Required
	}
	return p.Default == nil
}

// Wire returns the name the argument is sent under, which is the name the
// scenario writes unless the pack renames it.
func (p *Param) Wire(name string) string {
	if p.Name != "" {
		return p.Name
	}
	return name
}

// WirePath is the wire name of a body argument split into object keys: a
// dotted name nests the value, so name: fields.summary is sent as
// { "fields": { "summary": … } }. Only the body is nested; elsewhere the
// wire name is one key and dots in it are literal.
func (p *Param) WirePath(name string) []string {
	wire := p.Wire(name)
	if p.In != InBody {
		return []string{wire}
	}
	return strings.Split(wire, ".")
}
