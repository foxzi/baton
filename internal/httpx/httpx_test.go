package httpx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/foxzi/baton/internal/packs"
	"github.com/foxzi/baton/internal/values"
)

// mustPack parses a pack document or fails the test.
func mustPack(t *testing.T, yaml string) *packs.Pack {
	t.Helper()
	pack, err := packs.Parse([]byte(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("packs.Parse() error = %v", err)
	}
	return pack
}

// mustAPI builds an API against a test server or fails the test.
func mustAPI(t *testing.T, pack *packs.Pack, config map[string]string, secret values.Secret) *API {
	t.Helper()
	api, err := NewAPI("test", pack, config, secret)
	if err != nil {
		t.Fatalf("NewAPI() error = %v", err)
	}
	return api
}

const simpleOpsYAML = `ops:
  get_thing:
    get: /things/{id}
    readonly: true
    params:
      id: { pattern: '^\d+$' }
`

// ---------------------------------------------------------------------------
// NewAPI
// ---------------------------------------------------------------------------

func TestNewAPIDefaultsAndOverride(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: { default: "http://default.example" }
  token: { default: "deftok" }
`+simpleOpsYAML)

	api, err := NewAPI("demo", pack, map[string]string{"base_url": "http://override.example"}, values.Secret{})
	if err != nil {
		t.Fatalf("NewAPI() error = %v", err)
	}
	if api.Config["base_url"] != "http://override.example" {
		t.Errorf("Config[base_url] = %q, want override", api.Config["base_url"])
	}
	if api.Config["token"] != "deftok" {
		t.Errorf("Config[token] = %q, want default deftok", api.Config["token"])
	}
}

func TestNewAPIUnknownConfigField(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: { default: "http://default.example" }
`+simpleOpsYAML)

	_, err := NewAPI("demo", pack, map[string]string{"base_url": "http://x", "bogus": "1"}, values.Secret{})
	if err == nil {
		t.Fatalf("NewAPI() error = nil, want error")
	}
	if Class(err) != ClassConfig {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassConfig)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %q, want mention of bogus", err)
	}
}

func TestNewAPIRequiredConfigFieldMissing(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: { default: "http://default.example" }
  token: { required: true }
`+simpleOpsYAML)

	_, err := NewAPI("demo", pack, nil, values.Secret{})
	if err == nil {
		t.Fatalf("NewAPI() error = nil, want error")
	}
	if Class(err) != ClassConfig {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassConfig)
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("error = %q, want mention of token", err)
	}
}

func TestNewAPIMissingBaseURL(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
`+simpleOpsYAML)

	_, err := NewAPI("demo", pack, nil, values.Secret{})
	if err == nil {
		t.Fatalf("NewAPI() error = nil, want error")
	}
	if Class(err) != ClassConfig {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassConfig)
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Errorf("error = %q, want mention of base_url", err)
	}
}

func TestNewAPIAuthRequiredButMissing(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: { default: "http://default.example" }
auth:
  kind: bearer
`+simpleOpsYAML)

	_, err := NewAPI("demo", pack, nil, values.Secret{})
	if err == nil {
		t.Fatalf("NewAPI() error = nil, want error")
	}
	if Class(err) != ClassConfig {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassConfig)
	}
}

func TestNewAPINoAuthNeedsNoSecret(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: { default: "http://default.example" }
`+simpleOpsYAML)

	_, err := NewAPI("demo", pack, nil, values.Secret{})
	if err != nil {
		t.Fatalf("NewAPI() error = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Do: raw requests
// ---------------------------------------------------------------------------

func TestDoGETStatusHeadersBody(t *testing.T) {
	var gotMethod, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	resp, err := client.Do(context.Background(), api, &Request{Method: "GET", Path: "/things/1"})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if gotMethod != "GET" || gotPath != "/things/1" {
		t.Errorf("server saw %s %s, want GET /things/1", gotMethod, gotPath)
	}
	if resp.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.Status)
	}
	if resp.Headers.Get("X-Test") != "yes" {
		t.Errorf("Headers[X-Test] = %q, want yes", resp.Headers.Get("X-Test"))
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Errorf("Body = %q, want the JSON body", resp.Body)
	}
}

func TestDoPathAgainstBaseURLTrailingSlash(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL + "/"}, values.Secret{})

	client := New(0)
	if _, err := client.Do(context.Background(), api, &Request{Path: "/things/1"}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if gotPath != "/things/1" {
		t.Errorf("server saw path %q, want /things/1 (no doubled slash)", gotPath)
	}
}

func TestDoQueryMerged(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	_, err := client.Do(context.Background(), api, &Request{
		Path:  "/things?existing=1",
		Query: map[string]string{"added": "2"},
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if !strings.Contains(gotQuery, "existing=1") || !strings.Contains(gotQuery, "added=2") {
		t.Errorf("query = %q, want both existing=1 and added=2", gotQuery)
	}
}

func TestDoHeadersAndBodySent(t *testing.T) {
	var gotHeader, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Custom")
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	_, err := client.Do(context.Background(), api, &Request{
		Method:  "POST",
		Path:    "/things",
		Headers: map[string]string{"X-Custom": "abc"},
		Body:    []byte("hello"),
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if gotHeader != "abc" {
		t.Errorf("X-Custom header = %q, want abc", gotHeader)
	}
	if gotBody != "hello" {
		t.Errorf("body = %q, want hello", gotBody)
	}
}

func TestDoExpectStatusSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	resp, err := client.Do(context.Background(), api, &Request{ExpectStatus: []int{200, 201}})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if resp.Status != http.StatusCreated {
		t.Errorf("Status = %d, want 201", resp.Status)
	}
}

func TestDoUnexpected404IsCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	_, err := client.Do(context.Background(), api, &Request{})
	if err == nil {
		t.Fatalf("Do() error = nil, want error")
	}
	if Class(err) != ClassCommand {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassCommand)
	}
	var typed *Error
	if !asError(err, &typed) {
		t.Fatalf("error is not *Error: %v", err)
	}
	if typed.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want 404", typed.Status)
	}
	if !strings.Contains(typed.BodyTail, "not found") {
		t.Errorf("BodyTail = %q, want mention of not found", typed.BodyTail)
	}
}

func TestDoUnexpected500And429AreTransient(t *testing.T) {
	for _, status := range []int{500, 429} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))

		pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
		api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

		client := New(0)
		_, err := client.Do(context.Background(), api, &Request{})
		server.Close()
		if err == nil {
			t.Fatalf("status %d: Do() error = nil, want error", status)
		}
		if Class(err) != ClassTransient {
			t.Errorf("status %d: Class(err) = %q, want %q", status, Class(err), ClassTransient)
		}
	}
}

func TestDoConnectionFailureIsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // closed: nothing listens on this address anymore

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	_, err := client.Do(context.Background(), api, &Request{})
	if err == nil {
		t.Fatalf("Do() error = nil, want error")
	}
	if Class(err) != ClassTransient {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassTransient)
	}
}

func TestDoMaxBytesCapsBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes100())
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	resp, err := client.Do(context.Background(), api, &Request{MaxBytes: 10})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if len(resp.Body) != 10 {
		t.Errorf("len(Body) = %d, want 10", len(resp.Body))
	}
	if !resp.Truncated {
		t.Errorf("Truncated = false, want true")
	}
}

func TestDoNeitherURLNorAPIIsConfigError(t *testing.T) {
	client := New(0)
	_, err := client.Do(context.Background(), nil, &Request{Path: "/x"})
	if err == nil {
		t.Fatalf("Do() error = nil, want error")
	}
	if Class(err) != ClassConfig {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassConfig)
	}
}

func TestDoAbsoluteURLWithNilAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(0)
	resp, err := client.Do(context.Background(), nil, &Request{URL: server.URL + "/ping"})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// Authorisation
// ---------------------------------------------------------------------------

func TestAuthHeaderScheme(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("PRIVATE-TOKEN")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
auth:
  kind: header
  name: PRIVATE-TOKEN
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Do(context.Background(), api, &Request{}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("PRIVATE-TOKEN = %q, want s3cr3t", got)
	}
}

func TestAuthBearerScheme(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
auth:
  kind: bearer
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Do(context.Background(), api, &Request{}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got != "Bearer s3cr3t" {
		t.Errorf("Authorization = %q, want Bearer s3cr3t", got)
	}
}

func TestAuthBasicSchemeDefaultUserField(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
  user: {}
auth:
  kind: basic
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL, "user": "alice"}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Do(context.Background(), api, &Request{}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cr3t"))
	if got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

func TestAuthBasicSchemeCustomUserField(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
  login: {}
auth:
  kind: basic
  user: login
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL, "login": "bob"}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Do(context.Background(), api, &Request{}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("bob:s3cr3t"))
	if got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
}

func TestAuthQueryScheme(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("access_token")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
auth:
  kind: query
  name: access_token
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Do(context.Background(), api, &Request{}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("access_token = %q, want s3cr3t", got)
	}
}

func TestAuthPathScheme(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: { default: "`+server.URL+`/bot{auth}" }
auth:
  kind: path
`+simpleOpsYAML)
	api := mustAPI(t, pack, nil, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Do(context.Background(), api, &Request{Path: "/sendMessage"}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if !strings.Contains(gotPath, "s3cr3t") {
		t.Errorf("path = %q, want it to contain the token", gotPath)
	}
}

func TestAuthPathPlaceholderWithoutPathSchemeIsConfigError(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: { default: "http://x.example/bot{auth}" }
auth:
  kind: bearer
`+simpleOpsYAML)
	api := mustAPI(t, pack, nil, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	_, err := client.Do(context.Background(), api, &Request{Path: "/sendMessage"})
	if err == nil {
		t.Fatalf("Do() error = nil, want error")
	}
	if Class(err) != ClassConfig {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassConfig)
	}
}

func TestAuthPathSchemeWithoutPlaceholderIsConfigError(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: { default: "http://x.example" }
auth:
  kind: path
`+simpleOpsYAML)
	api := mustAPI(t, pack, nil, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	_, err := client.Do(context.Background(), api, &Request{Path: "/sendMessage"})
	if err == nil {
		t.Fatalf("Do() error = nil, want error")
	}
	if Class(err) != ClassConfig {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassConfig)
	}
}

// ---------------------------------------------------------------------------
// Auth: exchange scheme
// ---------------------------------------------------------------------------

// exchangeOpsYAML declares a login operation that trades a bearer secret for
// a token, and a protected operation the token authorises.
const exchangeOpsYAML = `ops:
  login:
    post: /login
  get_thing:
    get: /things/{id}
    params:
      id: { pattern: '^\d+$' }
`

func exchangePack(t *testing.T, auth string) *packs.Pack {
	t.Helper()
	return mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
auth:
  kind: exchange
`+auth+exchangeOpsYAML)
}

func TestExchangeFetchesTokenAndInjectsHeader(t *testing.T) {
	var logins int
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			logins++
			if r.Header.Get("Authorization") != "Bearer s3cr3t" {
				t.Errorf("login Authorization = %q, want Bearer s3cr3t", r.Header.Get("Authorization"))
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
		default:
			gotToken = r.Header.Get("X-Token")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	pack := exchangePack(t, `  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: header
    name: X-Token
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"}); err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if logins != 1 {
		t.Errorf("logins = %d, want 1", logins)
	}
	if gotToken != "tok-1" {
		t.Errorf("X-Token = %q, want tok-1", gotToken)
	}
}

func TestExchangeReusesTokenWithinTTL(t *testing.T) {
	var logins int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			logins++
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := exchangePack(t, `  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: header
    name: X-Token
  ttl: 1m
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	for i := 0; i < 3; i++ {
		if _, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"}); err != nil {
			t.Fatalf("Op() error = %v", err)
		}
	}
	if logins != 1 {
		t.Errorf("logins = %d, want 1", logins)
	}
}

func TestExchangeRefetchesAfterTTLExpires(t *testing.T) {
	var logins int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			logins++
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := exchangePack(t, `  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: header
    name: X-Token
  ttl: 10ms
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"}); err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"}); err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if logins != 2 {
		t.Errorf("logins = %d, want 2", logins)
	}
}

func TestExchangeSessionCookiesPersist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
		default:
			cookie, err := r.Cookie("session")
			if err != nil || cookie.Value != "abc" {
				t.Errorf("session cookie missing or wrong: %v", err)
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	pack := exchangePack(t, `  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: header
    name: X-Token
  session: cookies
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"}); err != nil {
		t.Fatalf("Op() error = %v", err)
	}
}

func TestExchangeInjectQuery(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
			return
		}
		gotToken = r.URL.Query().Get("token")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := exchangePack(t, `  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: query
    name: token
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"}); err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if gotToken != "tok-1" {
		t.Errorf("token query param = %q, want tok-1", gotToken)
	}
}

func TestExchangeTokenRedactedInError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "s3cr3t-tok"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("no such thing, token was s3cr3t-tok"))
	}))
	defer server.Close()

	pack := exchangePack(t, `  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: query
    name: token
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	_, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"})
	if err == nil {
		t.Fatalf("Op() error = nil, want error")
	}
	if strings.Contains(err.Error(), "s3cr3t-tok") {
		t.Errorf("error = %q, token leaked", err)
	}
	if !strings.Contains(err.Error(), values.Redacted) {
		t.Errorf("error = %q, want mention of %s", err, values.Redacted)
	}
}

func TestExchangeInjectsTokenIntoForm(t *testing.T) {
	var gotContentType string
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
			return
		}
		gotContentType = r.Header.Get("Content-Type")
		r.ParseForm()
		gotForm = r.Form
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
auth:
  kind: exchange
  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: form
    name: token
`+exchangeOpsYAML+`  edit_page:
    post: /edit
    encode: form
    params:
      title: { in: form }
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Op(context.Background(), api, "edit_page", map[string]any{"title": "Home"}); err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotContentType)
	}
	if gotForm.Get("title") != "Home" {
		t.Errorf("form title = %q, want Home", gotForm.Get("title"))
	}
	if gotForm.Get("token") != "tok-1" {
		t.Errorf("form token = %q, want tok-1", gotForm.Get("token"))
	}
}

func TestExchangeInjectsTokenIntoBody(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok-1"})
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
auth:
  kind: exchange
  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: body
    name: token
`+exchangeOpsYAML+`  create_thing:
    post: /things
    encode: json
    params:
      name: { in: body }
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	if _, err := client.Op(context.Background(), api, "create_thing", map[string]any{"name": "widget"}); err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if gotBody["name"] != "widget" {
		t.Errorf("body name = %v, want widget", gotBody["name"])
	}
	if gotBody["token"] != "tok-1" {
		t.Errorf("body token = %v, want tok-1", gotBody["token"])
	}
}

func TestExchangeWithoutTokenFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			json.NewEncoder(w).Encode(map[string]string{"other": "value"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := exchangePack(t, `  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: header
    name: X-Token
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	_, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"})
	if err == nil {
		t.Fatalf("Op() error = nil, want error")
	}
	if Class(err) != ClassCommand {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassCommand)
	}
	if !strings.Contains(err.Error(), "test") {
		t.Errorf("error = %q, want mention of the API name", err)
	}
}

// TestExchangeCallFailureIsReported is a regression test: exchangeToken
// holds api.exchange.mu across the exchange call, and redactError reads
// api.exchange.token for the very same exchange while handling the
// failure. If either side ever needs the mutex to read the token, this
// deadlocks; the token is read atomically instead so it does not.
func TestExchangeCallFailureIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := exchangePack(t, `  op: login
  base:
    kind: bearer
  extract: .access_token
  inject:
    in: header
    name: X-Token
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.NewSecret("tok", "s3cr3t"))

	client := New(0)
	_, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"})
	if err == nil {
		t.Fatalf("Op() error = nil, want error")
	}
	if Class(err) != ClassCommand {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassCommand)
	}
}

func TestOpSimpleGETPathArgEncodeAndTransform(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":1,"name":"widget"}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  get_thing:
    get: /things/{path}
    readonly: true
    params:
      path: { pattern: '.+', encode: path }
    transform: '{ id, name }'
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "get_thing", map[string]any{"path": "a/b"})
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if !strings.Contains(gotPath, "%2F") {
		t.Errorf("server saw path %q, want an escaped slash (%%2F)", gotPath)
	}
	want := map[string]any{"id": float64(1), "name": "widget"}
	gotMap, ok := result.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result = %T, want map[string]any", result.Result)
	}
	if gotMap["id"] != want["id"] || gotMap["name"] != want["name"] {
		t.Errorf("Result = %v, want %v", gotMap, want)
	}
	if result.Pages != 1 {
		t.Errorf("Pages = %d, want 1", result.Pages)
	}
	if result.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", result.Status)
	}
}

func TestOpPostJSONEncode(t *testing.T) {
	var gotContentType, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  create_thing:
    post: /things
    encode: json
    params:
      name: { in: body }
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	if _, err := client.Op(context.Background(), api, "create_thing", map[string]any{"name": "widget"}); err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(gotBody), &decoded); err != nil {
		t.Fatalf("body %q is not JSON: %v", gotBody, err)
	}
	if decoded["name"] != "widget" {
		t.Errorf("body name = %v, want widget", decoded["name"])
	}
}

func TestOpPostFormEncode(t *testing.T) {
	var gotContentType, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  create_thing:
    post: /things
    encode: form
    params:
      name: { in: form }
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	if _, err := client.Op(context.Background(), api, "create_thing", map[string]any{"name": "widget"}); err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotContentType)
	}
	if gotBody != "name=widget" {
		t.Errorf("body = %q, want name=widget", gotBody)
	}
}

func TestOpUnknownNameIsConfigError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	_, err := client.Op(context.Background(), api, "no_such_op", nil)
	if err == nil {
		t.Fatalf("Op() error = nil, want error")
	}
	if Class(err) != ClassConfig {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassConfig)
	}
}

func TestOpPatternViolationIsPolicy(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": "http://x.example"}, values.Secret{})

	client := New(0)
	_, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "not-a-number"})
	if err == nil {
		t.Fatalf("Op() error = nil, want error")
	}
	if Class(err) != ClassPolicy {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassPolicy)
	}
}

func TestOpMissingRequiredArgumentIsConfig(t *testing.T) {
	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": "http://x.example"}, values.Secret{})

	client := New(0)
	_, err := client.Op(context.Background(), api, "get_thing", nil)
	if err == nil {
		t.Fatalf("Op() error = nil, want error")
	}
	if Class(err) != ClassConfig {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassConfig)
	}
}

func TestOpEnvelopeErrorWhen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result":null,"error":"boom"}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
envelope:
  unwrap: .result
  error_when: '.error != null'
  error_message: .error
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	_, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"})
	if err == nil {
		t.Fatalf("Op() error = nil, want error")
	}
	if Class(err) != ClassCommand {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassCommand)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %q, want mention of boom (error_message)", err)
	}
}

func TestOpMaxBytesCutsListResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`["aaaaaaaaaa","bbbbbbbbbb","cccccccccc","dddddddddd"]`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    max_bytes: 20b
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if !result.Truncated {
		t.Errorf("Truncated = false, want true")
	}
	list, ok := result.Result.([]any)
	if !ok {
		t.Fatalf("Result = %T, want []any", result.Result)
	}
	if len(list) >= 4 {
		t.Errorf("len(Result) = %d, want fewer than 4 items after the cap", len(list))
	}
}

func TestOpSmallResultUntouched(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`["a","b"]`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if result.Truncated {
		t.Errorf("Truncated = true, want false")
	}
	list, ok := result.Result.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("Result = %v, want [a b]", result.Result)
	}
}

func TestOpNonJSONResponseIsCommandError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	_, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"})
	if err == nil {
		t.Fatalf("Op() error = nil, want error")
	}
	if Class(err) != ClassCommand {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassCommand)
	}
}

func TestOpEmptyResponseBodyDecodesToNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
`+simpleOpsYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "get_thing", map[string]any{"id": "1"})
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if result.Result != nil {
		t.Errorf("Result = %v, want nil", result.Result)
	}
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

func TestPaginationPageStyleFullThenShortPage(t *testing.T) {
	pages := [][]string{{"a", "b"}, {"c"}}
	var requests []struct{ page, size string }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		size := r.URL.Query().Get("per_page")
		requests = append(requests, struct{ page, size string }{page, size})
		idx := len(requests) - 1
		if idx >= len(pages) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
			return
		}
		encoded, _ := json.Marshal(pages[idx])
		w.WriteHeader(http.StatusOK)
		w.Write(encoded)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: page
      param: page
      size_param: per_page
      size: 2
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	list, ok := result.Result.([]any)
	if !ok {
		t.Fatalf("Result = %T, want []any", result.Result)
	}
	if len(list) != 3 {
		t.Errorf("len(Result) = %d, want 3 (a, b, c)", len(list))
	}
	if result.Pages != 2 {
		t.Errorf("Pages = %d, want 2", result.Pages)
	}
	if result.TruncatedPages {
		t.Errorf("TruncatedPages = true, want false")
	}
	if len(requests) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(requests))
	}
	if requests[0].page != "1" || requests[0].size != "2" {
		t.Errorf("first request page=%q size=%q, want page=1 size=2", requests[0].page, requests[0].size)
	}
	if requests[1].page != "2" || requests[1].size != "2" {
		t.Errorf("second request page=%q size=%q, want page=2 size=2", requests[1].page, requests[1].size)
	}
}

func TestPaginationPageStyleStopsOnEmptyPage(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: page
      param: page
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if calls != 1 {
		t.Errorf("server saw %d calls, want 1", calls)
	}
	list, ok := result.Result.([]any)
	if !ok || len(list) != 0 {
		t.Errorf("Result = %v, want an empty list", result.Result)
	}
}

func TestPaginationMaxPagesStopsWithTruncatedPages(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`["x","y"]`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: page
      param: page
      max_pages: 3
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if !result.TruncatedPages {
		t.Errorf("TruncatedPages = false, want true")
	}
	if calls != 3 {
		t.Errorf("server saw %d calls, want 3 (max_pages)", calls)
	}
}

func TestPaginationLinkHeaderStyle(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/things", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") != "2" {
			w.Header().Set("Link", `<`+server.URL+`/things?page=2>; rel="next"`)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`["a"]`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`["b"]`))
	})

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: link_header
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	list, ok := result.Result.([]any)
	if !ok {
		t.Fatalf("Result = %T, want []any", result.Result)
	}
	if len(list) != 2 {
		t.Errorf("len(Result) = %d, want 2 (a, b)", len(list))
	}
	if result.Pages != 2 {
		t.Errorf("Pages = %d, want 2", result.Pages)
	}
	if calls != 2 {
		t.Errorf("server saw %d calls, want 2", calls)
	}
}

func TestPaginationItemsOnObjectPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"items":["a","b"]}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: link_header
      items: .items
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	list, ok := result.Result.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("Result = %v, want [a b]", result.Result)
	}
}

func TestPaginationItemsPageNotAListIsCommandError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"items":"not-a-list"}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: link_header
      items: .items
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	_, err := client.Op(context.Background(), api, "list_things", nil)
	if err == nil {
		t.Fatalf("Op() error = nil, want error")
	}
	if Class(err) != ClassCommand {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassCommand)
	}
}

func TestPaginationDefaultMaxPagesTwenty(t *testing.T) {
	// A pack without max_pages relies on Pagination.Pages()'s default of 20.
	// The server returns a short page immediately so the walk stops on the
	// first request regardless of the cap; this only proves the default is
	// exercised without paying for 20 requests.
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`["a"]`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: page
      param: page
      size_param: per_page
      size: 2
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	client := New(0)
	result, err := client.Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if calls != 1 {
		t.Errorf("server saw %d calls, want 1 (page shorter than size stops the walk)", calls)
	}
	if result.TruncatedPages {
		t.Errorf("TruncatedPages = true, want false")
	}
}

func TestPaginationOffsetStyle(t *testing.T) {
	pages := [][]string{{"a", "b"}, {"c"}}
	var requests []struct{ offset, limit string }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, struct{ offset, limit string }{
			r.URL.Query().Get("offset"), r.URL.Query().Get("limit"),
		})
		idx := len(requests) - 1
		if idx >= len(pages) {
			w.Write([]byte(`[]`))
			return
		}
		encoded, _ := json.Marshal(pages[idx])
		w.Write(encoded)
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: offset
      param: offset
      limit_param: limit
      size: 2
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	result, err := New(0).Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	list, ok := result.Result.([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("Result = %v, want three items", result.Result)
	}
	if len(requests) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(requests))
	}
	if requests[0].offset != "0" || requests[0].limit != "2" {
		t.Errorf("first request offset=%q limit=%q, want offset=0 limit=2", requests[0].offset, requests[0].limit)
	}
	if requests[1].offset != "2" || requests[1].limit != "2" {
		t.Errorf("second request offset=%q limit=%q, want offset=2 limit=2", requests[1].offset, requests[1].limit)
	}
}

func TestPaginationOffsetStyleStopsOnTotal(t *testing.T) {
	// The page is as long as the limit, so only the total tells the walk it
	// has seen everything.
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"items":["a","b"],"total":2}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: offset
      param: offset
      limit_param: limit
      size: 2
      total: .total
      items: .items
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	result, err := New(0).Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if calls != 1 {
		t.Errorf("server saw %d calls, want 1: the total says there is nothing after this page", calls)
	}
	if result.TruncatedPages {
		t.Errorf("TruncatedPages = true, want false")
	}
}

func TestPaginationOffsetStyleInBody(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode the request body: %v", err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.Write([]byte(`["a","b"]`))
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  search_things:
    post: /search
    readonly: true
    paginate: true
    params:
      query: { in: body, required: true }
    pagination:
      style: offset
      param: from
      limit_param: size
      size: 2
      in: body
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	_, err := New(0).Op(context.Background(), api, "search_things", map[string]any{"query": "cat"})
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(bodies))
	}
	if bodies[0]["query"] != "cat" {
		t.Errorf("first body = %v, want the argument kept beside the page parameters", bodies[0])
	}
	if bodies[0]["from"] != float64(0) || bodies[0]["size"] != float64(2) {
		t.Errorf("first body = %v, want from=0 size=2", bodies[0])
	}
	if bodies[1]["from"] != float64(2) {
		t.Errorf("second body = %v, want from=2", bodies[1])
	}
}

func TestPaginationCursorStyle(t *testing.T) {
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		if len(cursors) == 1 {
			w.Write([]byte(`{"items":["a","b"],"next":"c2"}`))
			return
		}
		w.Write([]byte(`{"items":["c"],"next":null}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: cursor
      param: cursor
      next: .next
      items: .items
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	result, err := New(0).Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	list, ok := result.Result.([]any)
	if !ok || len(list) != 3 {
		t.Fatalf("Result = %v, want three items", result.Result)
	}
	if len(cursors) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(cursors))
	}
	if cursors[0] != "" {
		t.Errorf("first request cursor = %q, want none: no page has named one yet", cursors[0])
	}
	if cursors[1] != "c2" {
		t.Errorf("second request cursor = %q, want c2", cursors[1])
	}
}

func TestPaginationCursorStyleFullURL(t *testing.T) {
	var paths []string
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()
	mux.HandleFunc("/things", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Write([]byte(`{"items":["a"],"next":"` + server.URL + `/more?token=x"}`))
	})
	mux.HandleFunc("/more", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		w.Write([]byte(`{"items":["b"],"next":""}`))
	})

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: cursor
      param: cursor
      next: .next
      items: .items
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	result, err := New(0).Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	list, ok := result.Result.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("Result = %v, want two items", result.Result)
	}
	want := []string{"/things", "/more?token=x"}
	if strings.Join(paths, " ") != strings.Join(want, " ") {
		t.Errorf("requests = %v, want %v: a cursor that is a URL is followed as it is", paths, want)
	}
}

func TestPaginationCursorStyleMaxPages(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"items":["a"],"next":"more"}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_things:
    get: /things
    readonly: true
    paginate: true
    pagination:
      style: cursor
      param: cursor
      next: .next
      items: .items
      max_pages: 3
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	result, err := New(0).Op(context.Background(), api, "list_things", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if calls != 3 {
		t.Errorf("server saw %d calls, want 3 (max_pages)", calls)
	}
	if !result.TruncatedPages {
		t.Errorf("TruncatedPages = false, want true: the service still had pages")
	}
}

// ---------------------------------------------------------------------------
// Op: graphql operations
// ---------------------------------------------------------------------------

const graphQLPackYAML = `pack: demo
version: 1
config:
  base_url: {}
ops:
  search_code:
    kind: graphql
    post: /graphql
    readonly: true
    query: 'query($q: String!) { search(query: $q) { nodes { path } } }'
    params:
      q: { max_len: 500 }
    transform: '.data.search.nodes'
`

func TestGraphQLOpSendsQueryAndVariables(t *testing.T) {
	var body map[string]any
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode the request body: %v", err)
		}
		w.Write([]byte(`{"data":{"search":{"nodes":[{"path":"a.go"}]}}}`))
	}))
	defer server.Close()

	pack := mustPack(t, graphQLPackYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	result, err := New(0).Op(context.Background(), api, "search_code", map[string]any{"q": "cat"})
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if method != "POST" || path != "/graphql" {
		t.Errorf("request = %s %s, want POST /graphql", method, path)
	}
	if _, ok := body["query"].(string); !ok {
		t.Errorf("body = %v, want the document in query", body)
	}
	variables, ok := body["variables"].(map[string]any)
	if !ok || variables["q"] != "cat" {
		t.Errorf("body = %v, want the argument in variables", body)
	}
	nodes, ok := result.Result.([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("Result = %#v, want one transformed node", result.Result)
	}
}

func TestGraphQLOpErrorsFailTheCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":null,"errors":[{"message":"field q is required"}]}`))
	}))
	defer server.Close()

	pack := mustPack(t, graphQLPackYAML)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	_, err := New(0).Op(context.Background(), api, "search_code", map[string]any{"q": "cat"})
	if err == nil {
		t.Fatalf("Op() error = nil, want the graphql errors reported")
	}
	if Class(err) != ClassCommand {
		t.Errorf("Class(err) = %q, want %q", Class(err), ClassCommand)
	}
	if !strings.Contains(err.Error(), "field q is required") {
		t.Errorf("error = %q, want the graphql message", err)
	}
}

func TestGraphQLOpEnvelopeKeepsControlOfErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"search":{"nodes":[]}},"errors":[{"message":"deprecated field"}]}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
envelope:
  error_when: '.errors != null and (.data == null)'
  error_message: '.errors[0].message'
ops:
  search_code:
    kind: graphql
    query: '{ search { nodes { path } } }'
    readonly: true
    transform: '.data.search.nodes'
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	result, err := New(0).Op(context.Background(), api, "search_code", nil)
	if err != nil {
		t.Fatalf("Op() error = %v, want the pack envelope to allow a partial response", err)
	}
	if nodes, ok := result.Result.([]any); !ok || len(nodes) != 0 {
		t.Errorf("Result = %#v, want an empty node list", result.Result)
	}
}

func TestGraphQLOpPaginatesInVariables(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode the request body: %v", err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.Write([]byte(`{"data":{"nodes":["a","b"],"page":{"end":"c2"}}}`))
			return
		}
		w.Write([]byte(`{"data":{"nodes":["c"],"page":{"end":null}}}`))
	}))
	defer server.Close()

	pack := mustPack(t, `pack: demo
version: 1
config:
  base_url: {}
ops:
  list_nodes:
    kind: graphql
    query: 'query($after: String) { nodes }'
    readonly: true
    paginate: true
    pagination:
      style: cursor
      param: after
      in: body
      items: .data.nodes
      next: .data.page.end
`)
	api := mustAPI(t, pack, map[string]string{"base_url": server.URL}, values.Secret{})

	result, err := New(0).Op(context.Background(), api, "list_nodes", nil)
	if err != nil {
		t.Fatalf("Op() error = %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(bodies))
	}
	if _, ok := bodies[0]["variables"]; ok {
		t.Errorf("first body = %v, want no cursor before the service names one", bodies[0])
	}
	variables, ok := bodies[1]["variables"].(map[string]any)
	if !ok || variables["after"] != "c2" {
		t.Errorf("second body = %v, want the cursor in variables", bodies[1])
	}
	if items, ok := result.Result.([]any); !ok || len(items) != 3 {
		t.Errorf("Result = %#v, want the three nodes of both pages", result.Result)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func bytes100() []byte {
	buf := make([]byte, 100)
	for i := range buf {
		buf[i] = 'x'
	}
	return buf
}

// asError is a small errors.As wrapper local to this file to avoid importing
// errors solely for one call site.
func asError(err error, target **Error) bool {
	for err != nil {
		if typed, ok := err.(*Error); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
