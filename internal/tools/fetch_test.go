package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/agent"
)

// callFetch calls the fetch tool of the set and type-asserts the result.
func callFetch(t *testing.T, f *Fetch, args string) (FetchResult, error) {
	t.Helper()
	tools := f.Tools()
	if len(tools) != 1 {
		t.Fatalf("Tools() = %d tools, want 1", len(tools))
	}
	out, err := tools[0].Handler(context.Background(), json.RawMessage(args))
	if err != nil {
		return FetchResult{}, err
	}
	result, ok := out.(FetchResult)
	if !ok {
		t.Fatalf("call returned %T, want FetchResult", out)
	}
	return result, nil
}

// 1. A step whose policy has no fetch block gets no tool.
func TestNewFetchNoPolicyNoTools(t *testing.T) {
	f, err := NewFetch(FetchOptions{})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	if tools := f.Tools(); len(tools) != 0 {
		t.Fatalf("Tools() = %v, want none for a step without a fetch policy", tools)
	}
}

// 2. A fetch policy gives exactly one tool named "fetch", carrying the
// policy's MaxCalls and naming the allowed hosts in its description.
func TestNewFetchWithPolicyGivesOneTool(t *testing.T) {
	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{
		Allow:    []string{"example.com", "docs.example.org"},
		MaxCalls: 7,
	}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	tools := f.Tools()
	if len(tools) != 1 || tools[0].Name != "fetch" {
		t.Fatalf("Tools() = %v, want [fetch]", tools)
	}
	tool := tools[0]
	if tool.MaxCalls != 7 {
		t.Errorf("MaxCalls = %d, want 7", tool.MaxCalls)
	}
	if !strings.Contains(tool.Description, "example.com") || !strings.Contains(tool.Description, "docs.example.org") {
		t.Errorf("Description = %q, want the allowed hosts named", tool.Description)
	}
}

// 3. A successful GET of a plain text page returns the status, the text
// unchanged, the byte count, Truncated=false and the final URL.
func TestFetchGetPlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello there")
	}))
	defer server.Close()

	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{
		Allow: []string{serverHost(t, server.URL)},
	}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}

	result, err := callFetch(t, f, `{"url":"`+server.URL+`/page"}`)
	if err != nil {
		t.Fatalf("call error = %v", err)
	}
	if result.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", result.Status)
	}
	if result.Text != "hello there" {
		t.Errorf("Text = %q, want %q", result.Text, "hello there")
	}
	if result.Bytes != len("hello there") {
		t.Errorf("Bytes = %d, want %d", result.Bytes, len("hello there"))
	}
	if result.Truncated {
		t.Error("Truncated = true, want false")
	}
	if result.URL != server.URL+"/page" {
		t.Errorf("URL = %q, want %q", result.URL, server.URL+"/page")
	}
}

// 4. An HTML page becomes plain text: no tags remain, script/style content
// is dropped, entities are unescaped and block tags become line breaks.
func TestFetchHTMLBecomesText(t *testing.T) {
	page := `<html><head><style>body{color:red}</style></head>` +
		`<body><h1>Title</h1><p>Hello &amp; welcome</p>` +
		`<script>alert(1)</script></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, page)
	}))
	defer server.Close()

	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{
		Allow: []string{serverHost(t, server.URL)},
	}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}

	result, err := callFetch(t, f, `{"url":"`+server.URL+`"}`)
	if err != nil {
		t.Fatalf("call error = %v", err)
	}
	if strings.ContainsAny(result.Text, "<>") {
		t.Errorf("Text = %q, want no markup left", result.Text)
	}
	if strings.Contains(result.Text, "color:red") || strings.Contains(result.Text, "alert(1)") {
		t.Errorf("Text = %q, want script/style content dropped", result.Text)
	}
	if !strings.Contains(result.Text, "Hello & welcome") {
		t.Errorf("Text = %q, want the entity unescaped", result.Text)
	}
	if !strings.Contains(result.Text, "Title\n") && !strings.Contains(result.Text, "Title\n\n") {
		t.Errorf("Text = %q, want the block tag to have become a line break", result.Text)
	}
}

// 5. A host outside the allow list is rejected before any request is made.
func TestFetchRejectsHostOutsideAllowList(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer server.Close()

	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{
		Allow: []string{"example.com"},
	}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}

	_, err = callFetch(t, f, `{"url":"`+server.URL+`"}`)
	if err == nil {
		t.Fatal("call error = nil, want a host outside the allow list to be rejected")
	}
	if !strings.Contains(err.Error(), "allow list") {
		t.Errorf("error = %v, want it to mention the allow list", err)
	}
	if !isPolicyRefusal(err) {
		t.Errorf("error = %v, want a policy refusal", err)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want the server never reached", calls)
	}
}

// 6. A redirect to a host outside the allow list is not followed. The two
// servers listen on different loopback addresses (127.0.0.1 and 127.0.0.2)
// so that the allow list, which is host-only and ignores the port, can
// actually tell them apart.
func TestFetchRedirectOutsideAllowListIsRejected(t *testing.T) {
	var targetCalls int
	target := newServerOn(t, "127.0.0.2", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls++
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	defer origin.Close()

	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{
		Allow: []string{serverHost(t, origin.URL)},
	}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}

	_, err = callFetch(t, f, `{"url":"`+origin.URL+`"}`)
	if err == nil {
		t.Fatal("call error = nil, want a redirect off the allow list to be rejected")
	}
	if !isPolicyRefusal(err) {
		t.Errorf("error = %v, want a policy refusal", err)
	}
	if targetCalls != 0 {
		t.Errorf("target calls = %d, want the redirect target never reached", targetCalls)
	}
}

// 7. A redirect that stays on an allowed host is followed, and the result's
// URL is the final one.
func TestFetchRedirectWithinAllowListIsFollowed(t *testing.T) {
	var mux http.ServeMux
	var server *httptest.Server
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "landed")
	})
	server = httptest.NewServer(&mux)
	defer server.Close()

	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{
		Allow: []string{serverHost(t, server.URL)},
	}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}

	result, err := callFetch(t, f, `{"url":"`+server.URL+`/start"}`)
	if err != nil {
		t.Fatalf("call error = %v", err)
	}
	if result.Text != "landed" {
		t.Errorf("Text = %q, want %q", result.Text, "landed")
	}
	if result.URL != server.URL+"/final" {
		t.Errorf("URL = %q, want the final URL %q", result.URL, server.URL+"/final")
	}
}

// 8. A body larger than max_bytes is truncated, and Text is capped at
// max_bytes.
func TestFetchTruncatesOverTheLimit(t *testing.T) {
	const limit = 16
	body := strings.Repeat("x", limit*4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, body)
	}))
	defer server.Close()

	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{
		Allow:    []string{serverHost(t, server.URL)},
		MaxBytes: limit,
	}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}

	result, err := callFetch(t, f, `{"url":"`+server.URL+`"}`)
	if err != nil {
		t.Fatalf("call error = %v", err)
	}
	if !result.Truncated {
		t.Error("Truncated = false, want true for a body over max_bytes")
	}
	if len(result.Text) != limit {
		t.Errorf("len(Text) = %d, want %d", len(result.Text), limit)
	}
}

// 9. A non-http(s) scheme is rejected.
func TestFetchRejectsNonHTTPScheme(t *testing.T) {
	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	_, err = callFetch(t, f, `{"url":"file:///etc/passwd"}`)
	if err == nil {
		t.Fatal("call error = nil, want a non-http scheme to be rejected")
	}
}

// 10. A URL carrying credentials is rejected, and the password never shows
// up in the error text.
func TestFetchRejectsURLWithCredentials(t *testing.T) {
	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	_, err = callFetch(t, f, `{"url":"http://user:hunter2@example.com/"}`)
	if err == nil {
		t.Fatal("call error = nil, want credentials in the URL to be rejected")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error = %v, want the password not to appear in it", err)
	}
}

// 11. A missing or empty url argument is an error.
func TestFetchRejectsMissingURL(t *testing.T) {
	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	for _, args := range []string{`{}`, `{"url":""}`} {
		if _, err := callFetch(t, f, args); err == nil {
			t.Errorf("call(%s) error = nil, want a missing url to be rejected", args)
		}
	}
}

// 12. An empty allow list lets any host through: an allow list is what
// turns the check on.
func TestFetchEmptyAllowListReachesAnyHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "open")
	}))
	defer server.Close()

	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}

	result, err := callFetch(t, f, `{"url":"`+server.URL+`"}`)
	if err != nil {
		t.Fatalf("call error = %v, want an empty allow list to reach any host", err)
	}
	if result.Text != "open" {
		t.Errorf("Text = %q, want %q", result.Text, "open")
	}
}

// 13. An allowed host entry also covers its subdomains.
func TestFetchAllowedHostCoversSubdomain(t *testing.T) {
	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{
		Allow: []string{"example.com"},
	}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}
	if err := f.allowed("docs.example.com"); err != nil {
		t.Errorf("allowed(docs.example.com) = %v, want a subdomain of an allowed host to pass", err)
	}
	if err := f.allowed("example.com"); err != nil {
		t.Errorf("allowed(example.com) = %v, want the exact host to pass", err)
	}
	if err := f.allowed("notexample.com"); err == nil {
		t.Error("allowed(notexample.com) = nil, want a host that merely shares a suffix to be rejected")
	}
}

// 14. No cookie jar: a Set-Cookie from one response is not sent back on the
// next call to the same server.
func TestFetchSendsNoCookies(t *testing.T) {
	var sawCookie bool
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
		} else if _, err := r.Cookie("session"); err == nil {
			sawCookie = true
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	f, err := NewFetch(FetchOptions{Policy: agent.Policy{Fetch: &agent.FetchPolicy{
		Allow: []string{serverHost(t, server.URL)},
	}}})
	if err != nil {
		t.Fatalf("NewFetch() error = %v", err)
	}

	if _, err := callFetch(t, f, `{"url":"`+server.URL+`"}`); err != nil {
		t.Fatalf("first call error = %v", err)
	}
	if _, err := callFetch(t, f, `{"url":"`+server.URL+`"}`); err != nil {
		t.Fatalf("second call error = %v", err)
	}
	if sawCookie {
		t.Error("the second call sent the cookie from the first response, want no cookie jar")
	}
}

// newServerOn starts an httptest server bound to a chosen loopback address,
// so that two servers in one test can be told apart by host, not just by
// port: the allow list is host-only (section 13's subdomain rule needs a
// host to match against, not a port).
func newServerOn(t *testing.T, ip string, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp", ip+":0")
	if err != nil {
		t.Skipf("cannot listen on %s: %v", ip, err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	return server
}

// serverHost strips the scheme off an httptest server's URL, leaving the
// host:port the allow list is compared against.
func serverHost(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", rawURL, err)
	}
	return parsed.Host
}
