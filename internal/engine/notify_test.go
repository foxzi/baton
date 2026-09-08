package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/foxzi/baton/internal/cache"
	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/notify"
	"github.com/foxzi/baton/internal/runstore"
)

// TestNotify_StdoutChannelWritesRenderedMessage checks the sugar form of
// section 9.3 end to end on the built-in stdout channel.
func TestNotify_StdoutChannelWritesRenderedMessage(t *testing.T) {
	yamlText := `
version: 1
name: notify-stdout
inputs:
  who: { type: string, default: world }
steps:
  - id: say
    notify: out
    message: "hello {{ .inputs.who }}"
`
	var out strings.Builder
	eng, store, _ := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Inputs = map[string]any{"who": "world"}
		opts.Stdout = &out
		opts.Channels = map[string]notify.Channel{
			"out": {Name: "out", Kind: config.ChannelKindStdout},
		}
	})

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success", result.Status)
	}
	if got := out.String(); got != "hello world\n" {
		t.Errorf("stdout = %q, want %q", got, "hello world\n")
	}

	input := readFile(t, filepath.Join(store.Dir(), "steps", "say", "input.json"))
	if !strings.Contains(input, `"channel": "out"`) || !strings.Contains(input, "hello world") {
		t.Errorf("input.json = %s, want the channel and the message", input)
	}
}

// TestNotify_WebhookChannelPostsMessage checks the built-in webhook channel
// and that the url, being a secret, does not reach input.json.
func TestNotify_WebhookChannelPostsMessage(t *testing.T) {
	var (
		mu       sync.Mutex
		payloads []map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		payloads = append(payloads, payload)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	yamlText := `
version: 1
name: notify-webhook
steps:
  - id: ping
    notify: ops
    message: "deploy done"
`
	eng, store, _ := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Channels = map[string]notify.Channel{"ops": testWebhook(t, "ops", server.URL)}
	})

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success: %+v", result.Status, result.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 1 {
		t.Fatalf("received %d requests, want 1", len(payloads))
	}
	if payloads[0]["text"] != "deploy done" {
		t.Errorf("payload = %v, want text \"deploy done\"", payloads[0])
	}

	input := readFile(t, filepath.Join(store.Dir(), "steps", "ping", "input.json"))
	if strings.Contains(input, server.URL) {
		t.Errorf("input.json leaks the webhook url: %s", input)
	}
}

// TestNotify_UnknownChannelIsConfigError keeps a typo in a channel name from
// silently dropping the message.
func TestNotify_UnknownChannelIsConfigError(t *testing.T) {
	yamlText := `
version: 1
name: notify-unknown
steps:
  - id: say
    notify: nowhere
    message: "text"
`
	eng, _, _ := newTestEngine(t, yamlText, nil)
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassConfig {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassConfig)
	}
	if !strings.Contains(result.Error.Message, "nowhere") {
		t.Errorf("message = %q, want the channel name in it", result.Error.Message)
	}
}

// TestNotify_UnresolvedChannelKeepsItsReason reports why a configured
// channel could not be used, instead of claiming it is not configured.
func TestNotify_UnresolvedChannelKeepsItsReason(t *testing.T) {
	yamlText := `
version: 1
name: notify-unresolved
steps:
  - id: say
    notify: ops
    message: "text"
`
	eng, _, _ := newTestEngine(t, yamlText, func(opts *Options) {
		opts.ChannelErrors = map[string]error{"ops": fmt.Errorf("url: env TELEGRAM_WEBHOOK is not set")}
	})
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error == nil || result.Error.Class != ClassConfig {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassConfig)
	}
	if !strings.Contains(result.Error.Message, "TELEGRAM_WEBHOOK") {
		t.Errorf("message = %q, want the resolution error in it", result.Error.Message)
	}
}

// TestNotify_FailedDeliveryIsNotRetried covers section 9.4: sending a
// message is a side effect, so a transient failure is not repeated.
func TestNotify_FailedDeliveryIsNotRetried(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	yamlText := `
version: 1
name: notify-no-retry
steps:
  - id: ping
    notify: ops
    message: "text"
`
	eng, store, _ := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Channels = map[string]notify.Channel{"ops": testWebhook(t, "ops", server.URL)}
	})
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}
	if result.Error == nil || result.Error.Class != ClassTransient {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassTransient)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("webhook called %d times, want 1: a notify step must not be retried", calls)
	}

	state := readRunState(t, store.Dir())
	if step := state.Steps["ping"]; step == nil || step.Attempts != 1 {
		t.Errorf("step ping = %+v, want attempts 1", step)
	}
}

// TestNotify_OnFailureSendsToChannel is the shape section 9.3 documents: the
// on_failure block notifies a channel with the run context.
func TestNotify_OnFailureSendsToChannel(t *testing.T) {
	yamlText := `
version: 1
name: notify-on-failure
steps:
  - id: boom
    run:
      argv: ["false"]
on_failure:
  - id: tell
    notify: out
    message: "{{ .run.name }} failed: {{ .run.error.class }} at {{ .run.failed_step }}"
`
	var out strings.Builder
	eng, _, _ := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Stdout = &out
		opts.Channels = map[string]notify.Channel{
			"out": {Name: "out", Kind: config.ChannelKindStdout},
		}
	})
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusFailed {
		t.Fatalf("Status = %q, want failed", result.Status)
	}

	message := out.String()
	for _, want := range []string{"notify-on-failure", ClassCommand, "boom"} {
		if !strings.Contains(message, want) {
			t.Errorf("message = %q, want it to contain %q", message, want)
		}
	}
}

// TestNotify_MessageIsNotCached keeps a cached notify step from swallowing a
// second identical message.
func TestNotify_MessageIsNotCached(t *testing.T) {
	yamlText := `
version: 1
name: notify-cache
steps:
  - id: say
    cache: true
    notify: out
    message: "same text"
`
	var out strings.Builder
	cacheDir := t.TempDir()
	for i := 0; i < 2; i++ {
		eng, _, _ := newTestEngine(t, yamlText, func(opts *Options) {
			opts.Stdout = &out
			opts.Cache = cache.Open(cacheDir)
			opts.Channels = map[string]notify.Channel{
				"out": {Name: "out", Kind: config.ChannelKindStdout},
			}
		})
		if _, err := eng.Run(context.Background()); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}

	if got := strings.Count(out.String(), "same text"); got != 2 {
		t.Errorf("message delivered %d times, want 2: a notify step must not be served from the cache", got)
	}
}

// testWebhook resolves a webhook channel pointing at url.
func testWebhook(t *testing.T, name, url string) notify.Channel {
	t.Helper()
	t.Setenv("BATON_TEST_NOTIFY_URL", url)
	channel, err := notify.Resolve(name, config.Channel{
		Kind: config.ChannelKindWebhook,
		URL:  &config.SecretRef{From: "env", Key: "BATON_TEST_NOTIFY_URL"},
	}, "")
	if err != nil {
		t.Fatalf("notify.Resolve: %v", err)
	}
	return channel
}
