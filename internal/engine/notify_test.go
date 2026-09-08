package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/foxzi/baton/internal/cache"
	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/notify"
	"github.com/foxzi/baton/internal/runstore"
	"github.com/foxzi/baton/internal/scenario"
	"github.com/foxzi/baton/internal/values"
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

// notifyPack is a telegram-shaped pack implementing notify/v1: it renames
// the interface's target argument to the one the service expects, so the
// test also covers the wire-name form of a param (section 7.4.2).
const notifyPack = `pack: notifier
version: 1
config:
  base_url: {}
auth:
  kind: bearer
ops:
  send:
    post: /sendMessage
    encode: json
    params:
      target: { name: chat_id, pattern: '^-?\d+$', in: body }
      text: { max_len: 4096, in: body }
      format: { name: parse_mode, pattern: '^(HTML|Markdown)$', required: false, in: body }
    transform: '{ id: .message_id, target: (.chat.id | tostring) }'
    implements: notify/v1.send
`

// writeNotifyPack writes notifyPack as a pack directory: a pack that
// declares implements also ships the recorded response the interface check
// replays (section 7.4.5).
func writeNotifyPack(t *testing.T, dir string) {
	t.Helper()
	packDir := filepath.Join(dir, "apis", "notifier")
	if err := os.MkdirAll(filepath.Join(packDir, "examples"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", packDir, err)
	}
	files := map[string]string{
		filepath.Join(packDir, "pack.yaml"):             notifyPack,
		filepath.Join(packDir, "examples", "send.json"): `{"message_id":4821,"chat":{"id":-1001234567890}}`,
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// notifyPackNoInterface is the same pack with the implements marker taken
// off, which is how a pack that a channel must not use looks.
const notifyPackNoInterface = `pack: notifier
version: 1
config:
  base_url: {}
ops:
  send:
    post: /sendMessage
    encode: json
    params:
      target: { name: chat_id, pattern: '^-?\d+$', in: body }
      text: { max_len: 4096, in: body }
`

// TestNotify_PackChannelSendsThroughOp covers the third channel form of
// sections 9.3 and 12: the channel names a global apis entry implementing
// notify/v1, and notify: <channel> becomes a call to its send operation with
// the channel's target. The entry authorises with a secret of the
// configuration, which the scenario cannot name and must not see.
func TestNotify_PackChannelSendsThroughOp(t *testing.T) {
	var (
		mu       sync.Mutex
		gotPath  string
		gotAuth  string
		gotBody  string
		requests int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotAuth, gotBody = r.URL.Path, r.Header.Get("Authorization"), string(body)
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message_id":4821,"chat":{"id":-1001234567890}}`))
	}))
	defer server.Close()

	yamlText := `
version: 1
name: notify-pack
steps:
  - id: say
    notify: chat
    message: "release published"
`
	eng, store, dir := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Config = &config.Config{APIs: map[string]scenario.API{
			"telegram": {
				Pack:   "notifier",
				From:   "./apis/",
				Config: map[string]string{"base_url": server.URL},
				Auth:   scenario.APIAuth{Secret: "telegram_token"},
			},
		}}
		opts.APISecrets = map[string]values.Secret{
			"telegram_token": values.NewSecret("telegram_token", "bot-token-42"),
		}
		opts.Channels = map[string]notify.Channel{
			"chat": {Name: "chat", Kind: notify.KindPack, API: "telegram", Target: "-1001234567890", Auth: "telegram_token"},
		}
	})
	writeNotifyPack(t, dir)

	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != runstore.StatusSuccess {
		t.Fatalf("Status = %q, want success (error=%+v)", result.Status, result.Error)
	}

	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("pack called %d times, want 1", requests)
	}
	if gotPath != "/sendMessage" {
		t.Errorf("path = %q, want /sendMessage", gotPath)
	}
	if gotAuth != "Bearer bot-token-42" {
		t.Errorf("Authorization = %q, want the entry's secret", gotAuth)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("request body %q: %v", gotBody, err)
	}
	if sent["chat_id"] != "-1001234567890" {
		t.Errorf("body chat_id = %#v, want the channel target under its wire name", sent["chat_id"])
	}
	if sent["text"] != "release published" {
		t.Errorf("body text = %#v, want the rendered message", sent["text"])
	}

	input := readFile(t, filepath.Join(store.Dir(), "steps", "say", "input.json"))
	if strings.Contains(input, "bot-token-42") {
		t.Errorf("input.json leaks the api secret: %s", input)
	}
}

// TestNotify_PackChannelRequiresTheInterface keeps a channel from sending
// through an entry whose pack does not implement notify/v1: the argument
// names would be anyone's guess (section 7.4.5).
func TestNotify_PackChannelRequiresTheInterface(t *testing.T) {
	yamlText := `
version: 1
name: notify-pack-mismatch
apis:
  telegram:
    pack: notifier
    from: ./apis/
    config:
      base_url: "http://127.0.0.1:1"
steps:
  - id: say
    notify: chat
    message: "text"
`
	eng, _, dir := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Channels = map[string]notify.Channel{
			"chat": {Name: "chat", Kind: notify.KindPack, API: "telegram", Target: "-100"},
		}
	})
	writePack(t, dir, "notifier", notifyPackNoInterface)

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
	if !strings.Contains(result.Error.Message, "notify/v1") {
		t.Errorf("message = %q, want the interface name in it", result.Error.Message)
	}
}

// TestNotify_PackChannelUnknownAPIIsConfigError reports a channel pointing
// at an apis entry that no longer exists, rather than dropping the message.
func TestNotify_PackChannelUnknownAPIIsConfigError(t *testing.T) {
	yamlText := `
version: 1
name: notify-pack-unknown-api
steps:
  - id: say
    notify: chat
    message: "text"
`
	eng, _, _ := newTestEngine(t, yamlText, func(opts *Options) {
		opts.Channels = map[string]notify.Channel{
			"chat": {Name: "chat", Kind: notify.KindPack, API: "telegram", Target: "-100"},
		}
	})
	result, err := eng.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Error == nil || result.Error.Class != ClassConfig {
		t.Fatalf("Error = %+v, want class %q", result.Error, ClassConfig)
	}
	if !strings.Contains(result.Error.Message, "telegram") {
		t.Errorf("message = %q, want the api name in it", result.Error.Message)
	}
}
