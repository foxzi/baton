package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foxzi/baton/internal/cache"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/values"
)

// pathAuthPack puts the credential in the URL itself, the way Telegram's
// bot API does (section 7.4.3).
const pathAuthPack = `pack: demo
version: 1
config:
  base_url: {}
auth:
  kind: path
ops:
  send:
    get: /sendMessage
    readonly: true
`

// 1. A path-scheme token lives in the request URL, which the error message
// of a failed call quotes. Neither the message nor any file of the run
// directory may hold it (spec section 13).
func TestRun_HTTPOp_PathAuthTokenNeverOnDisk(t *testing.T) {
	const plaintext = "unlikely-path-token-4242"
	t.Setenv("BATON_PATH_TOKEN", plaintext)

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"description":"boom"}`))
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: path-auth
secrets:
  tok:
    from: env
    key: BATON_PATH_TOKEN
apis:
  svc:
    pack: demo
    from: ./apis/
    config:
      base_url: %q
    auth:
      secret: tok
steps:
  - id: one
    retry: { max_attempts: 1 }
    http:
      op: svc.send
`, server.URL+"/bot{auth}")

	var logged []string
	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		s, err := secrets.Resolve(o.Scenario.Secrets, filepath.Dir(o.Scenario.Path))
		if err != nil {
			t.Fatalf("secrets.Resolve: %v", err)
		}
		o.Secrets = s
		// The observer is what the CLI prints to the log, and it never sees
		// the secrets itself, so the engine has to hand it redacted text.
		o.Observer = func(event Event) {
			line := event.Type + " " + event.Message
			for _, value := range event.Fields {
				line += fmt.Sprintf(" %v", value)
			}
			logged = append(logged, line)
		}
	})
	writePack(t, dir, "demo", pathAuthPack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if !strings.Contains(gotPath, plaintext) {
		t.Fatalf("server saw path %q, want the token in the URL", gotPath)
	}
	if result.Error == nil {
		t.Fatal("Error = nil, want the failed call reported")
	}
	if strings.Contains(result.Error.Message, plaintext) {
		t.Errorf("the error message quotes the token: %q", result.Error.Message)
	}
	if !strings.Contains(result.Error.Message, values.Redacted) {
		t.Errorf("the error message = %q, want the token replaced with %q", result.Error.Message, values.Redacted)
	}

	for _, line := range logged {
		if strings.Contains(line, plaintext) {
			t.Errorf("an event handed to the observer quotes the token: %q", line)
		}
	}

	walkForPlaintext(t, store.Dir(), plaintext)
}

// exchangeAuthPack trades the configured secret for a token and injects the
// token as a query parameter, the placement most likely to end up quoted in
// a URL somewhere (sections 7.4.3 and 13).
const exchangeAuthPack = `pack: demo
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
    in: query
    name: access_token
ops:
  login:
    post: /login
  get_thing:
    get: /thing
    readonly: true
`

// 2. The token an exchange trades for is not a secret the scenario declared,
// so the run redactor knows nothing about it. It must still stay out of the
// run directory and out of the step cache, both on the call that succeeds
// and on the one that fails (spec section 13).
func TestRun_HTTPOp_ExchangeTokenNeverOnDiskOrInCache(t *testing.T) {
	const base = "unlikely-exchange-base-31"
	const token = "unlikely-exchange-token-99"
	t.Setenv("BATON_EXCHANGE_SECRET", base)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login":
			json.NewEncoder(w).Encode(map[string]string{"access_token": token})
		default:
			if r.URL.Query().Get("access_token") != token {
				t.Errorf("access_token = %q, want the exchanged token", r.URL.Query().Get("access_token"))
			}
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: exchange-auth
secrets:
  tok:
    from: env
    key: BATON_EXCHANGE_SECRET
apis:
  svc:
    pack: demo
    from: ./apis/
    config:
      base_url: %q
    auth:
      secret: tok
steps:
  - id: one
    cache: true
    http:
      op: svc.get_thing
`, server.URL)

	cacheDir := t.TempDir()
	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		s, err := secrets.Resolve(o.Scenario.Secrets, filepath.Dir(o.Scenario.Path))
		if err != nil {
			t.Fatalf("secrets.Resolve: %v", err)
		}
		o.Secrets = s
		o.Cache = cache.Open(cacheDir)
	})
	writePack(t, dir, "demo", exchangeAuthPack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}

	walkForPlaintext(t, store.Dir(), token)
	walkForPlaintext(t, store.Dir(), base)
	walkForPlaintext(t, cacheDir, token)
	walkForPlaintext(t, cacheDir, base)
}

// 3. And when the authorised call fails, the message about it does not carry
// the exchanged token either (spec section 13).
func TestRun_HTTPOp_ExchangeTokenRedactedInFailure(t *testing.T) {
	const base = "unlikely-exchange-base-32"
	const token = "unlikely-exchange-token-98"
	t.Setenv("BATON_EXCHANGE_SECRET", base)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/login" {
			json.NewEncoder(w).Encode(map[string]string{"access_token": token})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"access_token %s is not welcome here"}`, token)
	}))
	defer server.Close()

	yamlText := fmt.Sprintf(`
version: 1
name: exchange-auth-failure
secrets:
  tok:
    from: env
    key: BATON_EXCHANGE_SECRET
apis:
  svc:
    pack: demo
    from: ./apis/
    config:
      base_url: %q
    auth:
      secret: tok
steps:
  - id: one
    retry: { max_attempts: 1 }
    http:
      op: svc.get_thing
`, server.URL)

	eng, store, dir := newTestEngine(t, yamlText, func(o *Options) {
		s, err := secrets.Resolve(o.Scenario.Secrets, filepath.Dir(o.Scenario.Path))
		if err != nil {
			t.Fatalf("secrets.Resolve: %v", err)
		}
		o.Secrets = s
	})
	writePack(t, dir, "demo", exchangeAuthPack)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error != nil && strings.Contains(result.Error.Message, token) {
		t.Errorf("the error message quotes the exchanged token: %q", result.Error.Message)
	}

	walkForPlaintext(t, store.Dir(), token)
}
