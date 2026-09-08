package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/httpx"
	"github.com/foxzi/baton/internal/scenario"
)

func TestResolveStdout(t *testing.T) {
	channel, err := Resolve("ops", config.Channel{Kind: config.ChannelKindStdout}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if channel.Name != "ops" || channel.Kind != config.ChannelKindStdout {
		t.Errorf("channel = %+v, want name ops kind stdout", channel)
	}
	if !channel.URL.IsZero() {
		t.Errorf("URL is set for a stdout channel")
	}
}

func TestResolveWebhookReadsURLFromEnv(t *testing.T) {
	t.Setenv("HOOK_URL", "https://example.test/hook")

	channel, err := Resolve("ops", config.Channel{
		Kind: config.ChannelKindWebhook,
		URL:  &config.SecretRef{From: scenario.SecretFromEnv, Key: "HOOK_URL"},
	}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := channel.URL.Reveal(); got != "https://example.test/hook" {
		t.Errorf("URL = %q, want the value of HOOK_URL", got)
	}
}

func TestResolveWebhookReadsURLFromFileRelativeToBaseDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hook.txt"), []byte("https://example.test/from-file\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	channel, err := Resolve("ops", config.Channel{
		Kind: config.ChannelKindWebhook,
		URL:  &config.SecretRef{From: scenario.SecretFromFile, Path: "hook.txt", Trim: true},
	}, dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := channel.URL.Reveal(); got != "https://example.test/from-file" {
		t.Errorf("URL = %q, want the trimmed file contents", got)
	}
}

func TestResolveWebhookMissingSecretFails(t *testing.T) {
	_, err := Resolve("ops", config.Channel{
		Kind: config.ChannelKindWebhook,
		URL:  &config.SecretRef{From: scenario.SecretFromEnv, Key: "BATON_ABSENT_HOOK"},
	}, "")
	if err == nil {
		t.Fatal("Resolve accepted a missing url secret")
	}
}

func TestResolvePackChannelIsNotImplemented(t *testing.T) {
	_, err := Resolve("telegram", config.Channel{API: "telegram", Target: "-100"}, "")
	if err == nil {
		t.Fatal("Resolve accepted a pack channel")
	}
}

func TestResolveAllKeepsGoodChannelsAndReportsBadOnes(t *testing.T) {
	t.Setenv("HOOK_URL", "https://example.test/hook")

	channels, problems := ResolveAll(map[string]config.Channel{
		"out": {Kind: config.ChannelKindStdout},
		"ops": {Kind: config.ChannelKindWebhook, URL: &config.SecretRef{From: scenario.SecretFromEnv, Key: "HOOK_URL"}},
		"bad": {Kind: config.ChannelKindWebhook, URL: &config.SecretRef{From: scenario.SecretFromEnv, Key: "BATON_ABSENT_HOOK"}},
	}, "")

	if len(channels) != 2 {
		t.Errorf("resolved %d channels, want 2: %v", len(channels), channels)
	}
	if _, ok := channels["bad"]; ok {
		t.Error("the unresolvable channel was returned as resolved")
	}
	if problems["bad"] == nil {
		t.Error("no error reported for the unresolvable channel")
	}
	if len(problems) != 1 {
		t.Errorf("problems = %v, want only bad", problems)
	}
}

func TestResolveAllEmpty(t *testing.T) {
	channels, problems := ResolveAll(nil, "")
	if channels != nil || problems != nil {
		t.Errorf("ResolveAll(nil) = %v, %v, want nil, nil", channels, problems)
	}
}

func TestSendStdoutWritesLine(t *testing.T) {
	var out bytes.Buffer
	sender := Sender{Out: &out}
	if err := sender.Send(context.Background(), Channel{Name: "out", Kind: config.ChannelKindStdout}, "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := out.String(); got != "hello\n" {
		t.Errorf("output = %q, want %q", got, "hello\n")
	}
}

func TestSendStdoutWithoutWriterIsDropped(t *testing.T) {
	sender := Sender{}
	if err := sender.Send(context.Background(), Channel{Name: "out", Kind: config.ChannelKindStdout}, "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestSendWebhookPostsTextAsJSON(t *testing.T) {
	type received struct {
		method      string
		contentType string
		body        []byte
	}
	var got received
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.contentType = r.Header.Get("Content-Type")
		got.body, _ = readAll(r)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := Sender{HTTP: httpx.New(0)}
	channel := webhookChannel(t, server.URL)
	if err := sender.Send(context.Background(), channel, "run failed"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got.contentType)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.body, &payload); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, got.body)
	}
	if payload["text"] != "run failed" {
		t.Errorf("payload = %v, want text \"run failed\"", payload)
	}
}

func TestSendWebhookServerErrorIsTransient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	sender := Sender{HTTP: httpx.New(0)}
	err := sender.Send(context.Background(), webhookChannel(t, server.URL), "text")
	if err == nil {
		t.Fatal("Send accepted a 502 response")
	}
	var typed *httpx.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not a *httpx.Error", err)
	}
	if typed.Class != httpx.ClassTransient {
		t.Errorf("class = %q, want %q", typed.Class, httpx.ClassTransient)
	}
}

func TestSendWebhookClientErrorIsCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	sender := Sender{HTTP: httpx.New(0)}
	err := sender.Send(context.Background(), webhookChannel(t, server.URL), "text")
	var typed *httpx.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not a *httpx.Error", err)
	}
	if typed.Class != httpx.ClassCommand {
		t.Errorf("class = %q, want %q", typed.Class, httpx.ClassCommand)
	}
}

func TestSendUnknownKindFails(t *testing.T) {
	sender := Sender{}
	if err := sender.Send(context.Background(), Channel{Name: "x", Kind: "carrier-pigeon"}, "text"); err == nil {
		t.Fatal("Send accepted an unknown channel kind")
	}
}

// webhookChannel is a channel pointing at url.
func webhookChannel(t *testing.T, url string) Channel {
	t.Helper()
	t.Setenv("BATON_TEST_HOOK", url)
	channel, err := Resolve("ops", config.Channel{
		Kind: config.ChannelKindWebhook,
		URL:  &config.SecretRef{From: scenario.SecretFromEnv, Key: "BATON_TEST_HOOK"},
	}, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return channel
}

// readAll drains a request body.
func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}
