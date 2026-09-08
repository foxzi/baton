package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/foxzi/baton/internal/expr"
	"github.com/foxzi/baton/internal/notify"
	"github.com/foxzi/baton/internal/scenario"
)

// execNotify sends a message to a channel of the global configuration. The
// step is the sugar form of section 9.3: notify: <channel> with message:.
func (e *Engine) execNotify(ctx context.Context, step *scenario.Step, path string) (expr.Step, *Error) {
	channel, stepErr := e.channel(step.Notify)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	text, stepErr := e.render(fmt.Sprintf("%s.message", step.ID), step.Message)
	if stepErr != nil {
		return expr.Step{}, stepErr
	}
	// A template cannot read secrets, but it can quote a failure message or a
	// step result that echoed one back, and a notification leaves the machine
	// (section 13).
	text = e.redact(text)
	e.writeStepJSON(path, "input.json", map[string]any{
		"channel": channel.Name,
		"kind":    channel.Kind,
		"message": text,
	})

	sender := notify.Sender{HTTP: e.httpClient(), Out: e.opts.Stdout, Pack: e.sendViaPack}
	if err := sender.Send(ctx, channel, text); err != nil {
		return expr.Step{}, e.notifyFailure(ctx, step, path, err)
	}
	e.writeStepJSON(path, "output.json", map[string]any{
		"status":  expr.StatusSuccess,
		"channel": channel.Name,
	})
	return expr.Step{Result: text}, nil
}

// notifyFailure classifies a failed delivery. A pack channel resolves an
// apis entry first, so the failure may already be a config error of the
// engine's own, which keeps its class and its reason; anything else is
// classified the way an http step is (section 9.1).
func (e *Engine) notifyFailure(ctx context.Context, step *scenario.Step, path string, err error) *Error {
	var stepErr *Error
	if !errors.As(err, &stepErr) {
		return e.httpFailure(ctx, step, path, err)
	}
	e.writeStepJSON(path, "output.json", map[string]any{
		"status": expr.StatusFailed,
		"error":  map[string]any{"class": stepErr.Class, "message": stepErr.Msg},
	})
	return stepErr
}

// sendViaPack delivers a notification through a channel backed by an api
// pack: notify: <channel> is sugar for the send operation of notify/v1, with
// the target from the channel and the text from the step (section 7.4.5).
//
// The pack is checked to implement notify/v1 here rather than at load time,
// because the apis entry a channel names is an ordinary one that other steps
// may call for anything else.
func (e *Engine) sendViaPack(ctx context.Context, ch notify.Channel, text string) error {
	api, stepErr := e.api(ch.API, ch.Auth)
	if stepErr != nil {
		return stepErr
	}
	if err := api.Pack.CheckInterface(notifyInterface); err != nil {
		return errorf(ClassConfig, "notify.%s.api: %v", ch.Name, err)
	}
	callCtx, cancel := apiContext(ctx, api)
	defer cancel()
	_, err := e.httpClient().Op(callCtx, api, notifySendOp, map[string]any{
		"target": ch.Target,
		"text":   text,
	})
	return err
}

// The interface a notify channel's pack must implement, and the operation a
// notification is sent with (spec section 7.4.5).
const (
	notifyInterface = "notify/v1"
	notifySendOp    = "send"
)

// channel looks up a resolved channel by the name the step used. A channel
// that is configured but whose secrets could not be read fails here, with
// the reason kept from resolution time.
func (e *Engine) channel(name string) (notify.Channel, *Error) {
	if channel, ok := e.opts.Channels[name]; ok {
		return channel, nil
	}
	if err, ok := e.opts.ChannelErrors[name]; ok {
		return notify.Channel{}, errorf(ClassConfig, "notify channel %q: %v", name, err)
	}
	return notify.Channel{}, errorf(ClassConfig, "notify channel %q is not configured", name)
}
