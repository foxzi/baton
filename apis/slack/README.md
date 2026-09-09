# slack

An API pack that teaches baton the Slack Web API. It is the second
implementation of `notify/v1` besides `telegram` (pack.yaml's own header
comment): a scenario sends through `send(target, text, format)` without
knowing which chat service is on the other end.

| | |
|---|---|
| Pack | `slack`, version 1 |
| API | Slack Web API |
| Interface | `notify/v1` — `send` |
| Extra operations | `auth_test` |
| Auth | `bearer` — bot token as `Authorization: Bearer ...` |
| Base URL | `https://slack.com/api` by default |
| Pagination | none — every operation is a single call |

## Wiring it into a scenario

No shipped scenario in this repository calls `slack` yet, so the snippet
below follows only from the pack's own `config`, `auth` and `params`, the
same way the `telegram` pack is wired in (spec section 7.4.1):

```yaml
secrets:
  slack_bot: { from: env, key: SLACK_BOT_TOKEN }

apis:
  slack:
    pack: slack
    from: ../apis/
    auth: { secret: slack_bot }

steps:
  - id: alert
    http:
      op: slack.send
      args:
        target: "#deploys"
        text: "nightly-build failed: http on publish"
```

The name on the left (`slack`) is how a step addresses the pack: the step
above calls `slack.send` and gets back the shape the interface promises.

Because the pack implements `notify/v1`, the same `apis` entry can back a
`notify:` channel of the global configuration instead of an explicit `http`
step, the same generic way any `notify/v1` pack does (spec section 12):

```yaml
# baton.yaml (global config)
apis:
  slack:
    pack: slack
    from: ./apis/
    auth: { secret: slack_bot }

notify:
  chat:
    api: slack                   # pack implementing notify/v1
    target: "#deploys"
```

```yaml
# scenario
- id: report
  notify: chat
  message: "nightly-build failed: http on publish"
```

## Configuration

| Key | Required | Default | Meaning |
|---|---|---|---|
| `base_url` | no | `https://slack.com/api` | API root |
| `auth.secret` | yes | — | Bot token (`xoxb-...`), sent as `Authorization: Bearer <token>` |

The token is only ever read from the secret at call time; it is never
written into the pack itself, and the secret redactor masks it in logs the
same way it masks any other auth value.

## Operations

### `send` — `POST /chat.postMessage`

Not read-only. Implements `notify/v1.send`. Body is sent as JSON
(`encode: json`).

| Argument | Wire name | In | Pattern / Limit | Default | Required |
|---|---|---|---|---|---|
| `target` | `channel` | body | `^([CDG][A-Z0-9]{4,}|[@#][\w.-]+)$` | — | yes |
| `text` | `text` | body | max 40000 chars | — | yes |
| `format` | `parse` | body | `^(none|full)$`, one of `none`, `full` | — | no |

`target` is `notify/v1`'s vocabulary; Slack calls the same argument
`channel`, and accepts either a channel/group/DM id or a `#name`. Slack has
no format vocabulary of its own beyond how it parses a message's text for
links and mentions, which is what `notify/v1`'s `format` maps onto here:
`none` leaves the text exactly as sent, `full` parses it the way a user's
own typed message would be parsed.

```json
{ "id": "1717000000.123456", "target": "C0123456789" }
```

(`id` is Slack's message timestamp `.ts`, which doubles as the message's
id; `target` is `.channel` unchanged.)

### `auth_test` — `POST /auth.test`

Read-only. No `implements`; checks that the configured bot token is valid
without sending a message.

No arguments.

```json
{ "id": ..., "team": ..., "user": ... }
```

(`id` is `.user_id`; `team` and `user` are Slack's own top-level fields of
the same name.)

## Errors and the envelope

Slack answers HTTP 200 with `{"ok": false, "error": "channel_not_found"}`
exactly as readily as it answers a genuine success, and never wraps the
fields of a successful call in anything — they sit at the top level of the
body (pack.yaml's own comment). The envelope has no `unwrap` for that
reason, only an error check:

```yaml
envelope:
  error_when: '.ok == false'
  error_message: .error
```

Without it, a rejected call's `.ts` would be read as `null` by the transform
instead of failing the step with Slack's own error code.

## Notes

- The pack.yaml header comment describes `slack` as the second
  implementation of `notify/v1` besides `telegram`, so a scenario written
  against `notify/v1` can address either without changing its steps.
- The bot token is a Bearer token, so `auth.kind` is `bearer` and no `name`
  or path placeholder is needed, unlike `telegram`'s `path` auth.
- `send`'s `target` and `format` arguments are `notify/v1`'s vocabulary; the
  pack renames them to Slack's own `channel` and `parse` on the wire.

## Validating a change

```sh
baton apis validate apis/slack
```

This parses the pack and replays the recorded response in
`apis/slack/examples/` through the transforms, checking the result of
`send` against the `notify/v1` interface schema. The test suite does the
same on every commit, so the example needs to be kept in step with the
transform.
