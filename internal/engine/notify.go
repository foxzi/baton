package engine

import (
	"context"
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
	e.writeStepJSON(path, "input.json", map[string]any{
		"channel": channel.Name,
		"kind":    channel.Kind,
		"message": text,
	})

	sender := notify.Sender{HTTP: e.httpClient(), Out: e.opts.Stdout}
	if err := sender.Send(ctx, channel, text); err != nil {
		return expr.Step{}, e.httpFailure(ctx, step, path, err)
	}
	e.writeStepJSON(path, "output.json", map[string]any{
		"status":  expr.StatusSuccess,
		"channel": channel.Name,
	})
	return expr.Step{Result: text}, nil
}

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
