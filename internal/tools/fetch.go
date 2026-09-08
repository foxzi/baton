package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/foxzi/baton/internal/agent"
	"github.com/foxzi/baton/internal/gateway"
)

// FetchOptions is what the runner hands over for one step's fetch tool.
type FetchOptions struct {
	// Policy is the step's tool policy; Policy.Fetch holds the allowed
	// hosts and the limits (spec sections 7.3 and 7.6).
	Policy agent.Policy

	// Client performs the requests. A nil client gets one built here, with
	// no cookie jar and with redirects held to the allowed hosts.
	Client *http.Client
}

// maxFetchRedirects caps a redirect chain. Every hop is checked against the
// allow list, so this only stops a loop between two allowed hosts.
const maxFetchRedirects = 5

// fetchTimeout is the deadline of one request. The step's own timeout still
// applies; this keeps a single silent host from spending all of it.
const fetchTimeout = 30 * time.Second

// Fetch is the fetch tool of one step.
type Fetch struct {
	// allow lists the hosts the step may reach, lowercased and without a
	// port. An empty list allows any host: an allow list is what turns the
	// tool on, and validation only warns about leaving it out (spec section
	// 4, check 14).
	allow []string

	maxBytes int64
	maxCalls int
	client   *http.Client
	enabled  bool
}

// NewFetch prepares the fetch tool of a step. A step whose policy has no
// fetch block gets no tool, not an error: only the research profile and an
// explicit fetch block turn it on.
func NewFetch(opts FetchOptions) (*Fetch, error) {
	policy := opts.Policy.Fetch
	if policy == nil {
		return &Fetch{}, nil
	}

	set := &Fetch{
		maxBytes: policy.MaxBytes,
		maxCalls: policy.MaxCalls,
		client:   opts.Client,
		enabled:  true,
	}
	if set.maxBytes <= 0 {
		set.maxBytes = agent.DefaultFetchMaxBytes
	}
	if set.maxCalls <= 0 {
		set.maxCalls = agent.DefaultFetchMaxCalls
	}

	for _, host := range policy.Allow {
		normalized, err := normalizeHost(host)
		if err != nil {
			return nil, fmt.Errorf("fetch: allowed host %q: %w", host, err)
		}
		set.allow = append(set.allow, normalized)
	}

	if set.client == nil {
		set.client = &http.Client{
			Timeout:       fetchTimeout,
			CheckRedirect: set.checkRedirect,
		}
	}
	return set, nil
}

// Tools returns the fetch tool, or nothing when the step may not fetch.
func (f *Fetch) Tools() []gateway.Tool {
	if !f.enabled {
		return nil
	}
	return []gateway.Tool{{
		Name:        "fetch",
		Description: f.description(),
		InputSchema: fetchSchema(),
		MaxCalls:    f.maxCalls,
		Handler:     f.get,
	}}
}

// FetchResult is what the fetch tool returns. Text is the page as text: HTML
// arrives as markup no model needs to read.
type FetchResult struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Text        string `json:"text"`
	Bytes       int    `json:"bytes"`
	Truncated   bool   `json:"truncated"`
}

// fetchArgs are the arguments of a fetch call.
type fetchArgs struct {
	URL string `json:"url"`
}

func (f *Fetch) get(ctx context.Context, raw json.RawMessage) (any, error) {
	var args fetchArgs
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("fetch: arguments are not a JSON object: %w", err)
		}
	}
	if args.URL == "" {
		return nil, errors.New("fetch: url is required")
	}

	target, err := f.checkURL(args.URL)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("fetch: %s: %w", target, err)
	}
	// No cookies are sent and none are kept: the tool reads public pages,
	// and a session would make one call depend on another (section 7.6).
	request.Header.Set("Accept", "text/html, text/plain, application/json;q=0.9, */*;q=0.5")
	request.Header.Set("Accept-Encoding", "identity")

	response, err := f.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch: %s: %w", target, unwrapURLError(err))
	}
	defer response.Body.Close()

	// One byte over the limit is read on purpose: it is how a body that was
	// cut apart from one that just fits is told.
	body, err := io.ReadAll(io.LimitReader(response.Body, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fetch: %s: read the body: %w", target, err)
	}
	truncated := int64(len(body)) > f.maxBytes
	if truncated {
		body = body[:f.maxBytes]
	}

	contentType := response.Header.Get("Content-Type")
	return FetchResult{
		URL:         response.Request.URL.String(),
		Status:      response.StatusCode,
		ContentType: contentType,
		Text:        bodyText(contentType, body),
		Bytes:       len(body),
		Truncated:   truncated,
	}, nil
}

// checkURL holds a call to what the policy allows: a plain http or https GET
// of an allowed host.
func (f *Fetch) checkURL(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("fetch: %q is not a URL: %w", raw, err)
	}
	switch target.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("fetch: %s is not an http or https URL", raw)
	}
	if target.Host == "" {
		return nil, fmt.Errorf("fetch: %s has no host", raw)
	}
	if target.User != nil {
		// Credentials in a URL would be sent to the host and written to the
		// audit log; a public page does not need them.
		return nil, fmt.Errorf("fetch: %s must not carry credentials", target.Redacted())
	}
	if err := f.allowed(target.Host); err != nil {
		return nil, err
	}
	return target, nil
}

// checkRedirect follows a redirect only while it stays on an allowed host: a
// host outside the list is the same policy question as the first call.
func (f *Fetch) checkRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= maxFetchRedirects {
		return fmt.Errorf("fetch: more than %d redirects", maxFetchRedirects)
	}
	switch request.URL.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("fetch: redirect to %s is not http or https", request.URL)
	}
	return f.allowed(request.URL.Host)
}

// allowed answers whether a host is on the step's list. An entry covers its
// subdomains: an allow list of one site should not have to name every host
// the site's pages live on.
func (f *Fetch) allowed(host string) error {
	if len(f.allow) == 0 {
		return nil
	}

	name, err := normalizeHost(host)
	if err != nil {
		return fmt.Errorf("fetch: host %q: %w", host, err)
	}
	for _, entry := range f.allow {
		if name == entry || strings.HasSuffix(name, "."+entry) {
			return nil
		}
	}
	return fmt.Errorf("fetch: %s is not in the allow list (%s)", name, strings.Join(f.allow, ", "))
}

// description tells the agent which hosts it can reach, so that it does not
// have to learn the list by being refused.
func (f *Fetch) description() string {
	if len(f.allow) == 0 {
		return "Fetch a page over HTTP GET and return it as text."
	}
	return "Fetch a page over HTTP GET and return it as text. Allowed hosts: " +
		strings.Join(f.allow, ", ") + " (subdomains included)."
}

// normalizeHost drops the port and lowercases the name, so that the list and
// the URL are compared in one form.
func normalizeHost(host string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(host))
	if name == "" {
		return "", errors.New("empty host")
	}
	if bare, _, err := net.SplitHostPort(name); err == nil {
		name = bare
	}
	name = strings.Trim(strings.TrimPrefix(name, "*."), ".")
	if name == "" {
		return "", errors.New("empty host")
	}
	if strings.ContainsAny(name, "/?#") {
		return "", errors.New("not a host name")
	}
	return name, nil
}

// unwrapURLError trims the transport's wrapper, which repeats the URL the
// caller already has.
func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

// bodyText turns a body into what a model should read: HTML becomes text,
// anything else is passed through as it arrived.
func bodyText(contentType string, body []byte) string {
	mediaType := contentType
	if index := strings.IndexByte(mediaType, ';'); index >= 0 {
		mediaType = mediaType[:index]
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	text := string(body)
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		text = htmlToText(text)
	}
	return text
}

var (
	// htmlDropped are the elements whose content is markup for the browser,
	// not text for a reader. One pattern per element, since the regexp
	// package has no backreference to pair an end tag with its start.
	htmlDropped = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script\b[^>]*>.*?</\s*script\s*>`),
		regexp.MustCompile(`(?is)<style\b[^>]*>.*?</\s*style\s*>`),
		regexp.MustCompile(`(?is)<template\b[^>]*>.*?</\s*template\s*>`),
		regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</\s*noscript\s*>`),
	}

	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

	// htmlBreak are the tags that end a line of text.
	htmlBreak = regexp.MustCompile(`(?i)<\s*/?\s*(br|p|div|section|article|header|footer|li|tr|h[1-6]|table|ul|ol|dl|dd|dt|pre|blockquote|form)\b[^>]*>`)

	htmlTag = regexp.MustCompile(`(?s)<[^>]*>`)

	// htmlBlankLines collapses the empty lines the tags above leave behind.
	htmlBlankLines = regexp.MustCompile(`\n{3,}`)

	htmlSpaces = regexp.MustCompile(`[ \t\f\v\x{00a0}]+`)
)

// htmlToText extracts the text of a page. This is a reader's view, not a
// parse: the tool exists so that an agent can read a page, and a full DOM
// would only give it more markup to skip.
func htmlToText(page string) string {
	page = htmlComment.ReplaceAllString(page, "")
	for _, dropped := range htmlDropped {
		page = dropped.ReplaceAllString(page, "\n")
	}
	page = htmlBreak.ReplaceAllString(page, "\n")
	page = htmlTag.ReplaceAllString(page, "")
	page = html.UnescapeString(page)

	page = strings.ReplaceAll(page, "\r\n", "\n")
	page = strings.ReplaceAll(page, "\r", "\n")
	page = htmlSpaces.ReplaceAllString(page, " ")

	lines := strings.Split(page, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	page = strings.Join(lines, "\n")
	page = htmlBlankLines.ReplaceAllString(page, "\n\n")
	return strings.TrimSpace(page)
}

// fetchSchema describes the arguments of a fetch call.
func fetchSchema() json.RawMessage {
	schema, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute http or https URL of a page on an allowed host.",
			},
		},
		"required":             []string{"url"},
		"additionalProperties": false,
	})
	if err != nil {
		// The map above is a literal: it cannot fail to encode.
		panic(err)
	}
	return schema
}
