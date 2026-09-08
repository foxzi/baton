// Package notify delivers a message to a channel of the global
// configuration (docs/ru/spec.md, sections 9.3 and 12).
//
// Two channel kinds are built in: webhook, which posts {"text": …} to a URL
// read from a secret, and stdout, which writes the message to a writer the
// caller supplies. A channel declared as a pack operation (notify/v1) is
// recognised but not delivered yet; Resolve reports it as a configuration
// error so a scenario does not silently lose its notification.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/foxzi/baton/internal/config"
	"github.com/foxzi/baton/internal/httpx"
	"github.com/foxzi/baton/internal/secrets"
	"github.com/foxzi/baton/internal/values"
)

// Channel is a channel of the configuration with its secrets already read.
// The URL is kept as a values.Secret so that it reaches the redactor and
// never a log or an input.json.
type Channel struct {
	Name string
	Kind string
	URL  values.Secret
}

// Resolve reads the secrets of one configured channel. Relative file paths
// are resolved against baseDir, as everywhere else.
func Resolve(name string, ch config.Channel, baseDir string) (Channel, error) {
	switch ch.Kind {
	case config.ChannelKindStdout:
		return Channel{Name: name, Kind: ch.Kind}, nil
	case config.ChannelKindWebhook:
		if ch.URL == nil {
			return Channel{}, fmt.Errorf("notify.%s: kind webhook requires url", name)
		}
		url, err := secrets.ResolveOne("notify."+name+".url", ch.URL.Secret(), baseDir)
		if err != nil {
			return Channel{}, fmt.Errorf("notify.%s.url: %w", name, err)
		}
		return Channel{Name: name, Kind: ch.Kind, URL: url}, nil
	default:
		return Channel{}, fmt.Errorf("notify.%s: channels backed by an api pack are not implemented yet", name)
	}
}

// ResolveAll reads the secrets of every configured channel. A channel that
// cannot be resolved is left out with its error returned, so that a run only
// fails when it actually sends to that channel.
func ResolveAll(channels map[string]config.Channel, baseDir string) (map[string]Channel, map[string]error) {
	if len(channels) == 0 {
		return nil, nil
	}
	resolved := make(map[string]Channel, len(channels))
	problems := make(map[string]error)
	for name, ch := range channels {
		channel, err := Resolve(name, ch, baseDir)
		if err != nil {
			problems[name] = err
			continue
		}
		resolved[name] = channel
	}
	if len(problems) == 0 {
		problems = nil
	}
	return resolved, problems
}

// Sender delivers messages. Both fields are optional: HTTP defaults to a
// client without its own timeout, Out to io.Discard.
type Sender struct {
	HTTP *httpx.Client
	Out  io.Writer
}

// Send delivers one message. The returned error is a *httpx.Error for a
// webhook, so the caller can classify a delivery failure the same way it
// classifies an http step (section 9.1).
func (s Sender) Send(ctx context.Context, ch Channel, text string) error {
	switch ch.Kind {
	case config.ChannelKindStdout:
		if s.Out == nil {
			return nil
		}
		_, err := fmt.Fprintln(s.Out, text)
		return err
	case config.ChannelKindWebhook:
		return s.sendWebhook(ctx, ch, text)
	default:
		return fmt.Errorf("notify.%s: unknown kind %q", ch.Name, ch.Kind)
	}
}

// webhookPayload is the body of a built-in webhook. A single text field is
// what the usual incoming-webhook endpoints expect (Slack, Mattermost).
type webhookPayload struct {
	Text string `json:"text"`
}

func (s Sender) sendWebhook(ctx context.Context, ch Channel, text string) error {
	body, err := json.Marshal(webhookPayload{Text: text})
	if err != nil {
		return fmt.Errorf("notify.%s: %w", ch.Name, err)
	}
	client := s.HTTP
	if client == nil {
		client = httpx.New(0)
	}
	_, err = client.Do(ctx, nil, &httpx.Request{
		Method:  http.MethodPost,
		URL:     ch.URL.Reveal(),
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	})
	return err
}
