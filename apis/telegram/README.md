# telegram

An API pack that teaches baton the Telegram Bot API. It is the reference
implementation of the `notify/v1` interface (pack.yaml's own header comment):
a channel of the global config, or a step calling the pack directly, sends
through `send(target, text, format)` without the scenario knowing it is
talking to Telegram.

| | |
|---|---|
| Pack | `telegram`, version 1 |
| API | Telegram Bot API |
| Interface | `notify/v1` — `send` |
| Extra operations | `send_document`, `get_me` |
| Auth | `path` — the bot token is substituted into `{auth}` in the base URL |
| Base URL | `https://api.telegram.org/bot{auth}` by default |
| Pagination | none — every operation is a single call |

## Wiring it into a scenario

```yaml
secrets:
  tg_bot: { from: env, key: TELEGRAM_BOT_TOKEN }

apis:
  telegram:
    pack: telegram
    from: ../apis/
    auth: { secret: tg_bot }

steps:
  - id: alert
    http:
      op: telegram.send
      args:
        target: "-1001234567890"
        text: "nightly-build failed: http on publish"
```

The name on the left (`telegram`) is how a step addresses the pack: the step
above calls `telegram.send` and gets back the shape the interface promises.

Because the pack implements `notify/v1`, the same `apis` entry can also back
a `notify:` channel of the global configuration instead of an explicit `http`
step (spec section 12 and the quickstart guide document this for this exact
pack):

```yaml
# baton.yaml (global config)
apis:
  telegram:
    pack: telegram
    from: ./apis/
    auth: { secret: tg_bot }

notify:
  chat:
    api: telegram                # pack implementing notify/v1
    target: "-1001234567890"     # a chat id, or @channelname
```

```yaml
# scenario
- id: report
  notify: chat
  message: "nightly-build failed: http on publish"
```

`notify: chat` expands into a call of `telegram.send` with the channel's
`target` and the rendered `message`; the channel authorises with the
`apis.telegram` entry's own secret, which the scenario never sees.

## Configuration

| Key | Required | Default | Meaning |
|---|---|---|---|
| `base_url` | no | `https://api.telegram.org/bot{auth}` | API root; `auth.kind: path` substitutes the bot token at the `{auth}` placeholder |
| `auth.secret` | yes | — | The bot token issued by BotFather |

The token is only ever read from the secret and substituted into the request
path at call time; it is never written into the pack itself, and the secret
redactor masks it in logs the same way it masks any other auth value
(`bot***/sendMessage` rather than the real token).

## Operations

### `send` — `POST /sendMessage`

Not read-only. Implements `notify/v1.send`. Body is sent as JSON
(`encode: json`).

| Argument | Wire name | In | Pattern / Limit | Default | Required |
|---|---|---|---|---|---|
| `target` | `chat_id` | body | `^(-?\d+|@[\w]{5,32})$` | — | yes |
| `text` | `text` | body | max 4096 chars | — | yes |
| `format` | `parse_mode` | body | `^(MarkdownV2|HTML|Markdown)$`, one of `MarkdownV2`, `HTML`, `Markdown` | — | no |

`target` is a numeric chat id (negative for a channel or supergroup) or an
`@username`; `notify/v1` calls this argument `target`, Telegram calls it
`chat_id`. `format` carries Telegram's own parse-mode names, since
`notify/v1` leaves the format vocabulary to the pack: plain text is the
default, and an unescaped `MarkdownV2` message is rejected by the API rather
than partially rendered.

```json
{ "id": 4821, "target": "-1001234567890" }
```

(`id` is Telegram's `message_id`; `target` is the chat id turned into a
string.)

### `send_document` — `POST /sendDocument`

Not read-only, no `implements`. Body is sent as JSON (`encode: json`), not
multipart: `document` is a URL or a Telegram `file_id` already reachable by
the API, not raw file bytes.

| Argument | Wire name | In | Pattern / Limit | Default | Required |
|---|---|---|---|---|---|
| `target` | `chat_id` | body | `^(-?\d+|@[\w]{5,32})$` | — | yes |
| `document` | `document` | body | `^\S+$` | — | yes |
| `caption` | `caption` | body | max 1024 chars | — | no |

```json
{ "id": 4821, "target": "-1001234567890" }
```

Same transform and result shape as `send`.

### `get_me` — `GET /getMe`

Read-only. No `implements`; a self-check of which bot the configured token
belongs to.

No arguments.

```json
{ "id": ..., "username": "...", "name": "..." }
```

(`id` and `username` come straight from the Telegram user object, `name` is
`.first_name`.)

## Errors and the envelope

Telegram answers HTTP 200 with `{"ok": false, "description": "..."}` exactly
as readily as it answers a 4xx status, and wraps every success in
`{"ok": true, "result": ...}` (pack.yaml's own comment). The envelope
unwraps the payload and turns a false `ok` into the step's error:

```yaml
envelope:
  unwrap: .result
  error_when: '.ok == false'
  error_message: .description
```

Without it, a step reading `.message_id` off a rejected message would fail
inside the transform on a field that never existed, instead of failing the
step upfront with Telegram's own description of what went wrong.

## Notes

- The pack is laid out as a directory because it ships the recorded response
  the `notify/v1` implementation check reads, `examples/send.json`.
- The bot token lives in the request path rather than a header, so
  `auth.kind` is `path` and the placeholder belongs to `base_url` rather than
  to any single operation.
- `send`'s `target` argument is `notify/v1`'s vocabulary; the pack renames it
  to Telegram's own `chat_id` on the wire, and `format` to `parse_mode`.

## Validating a change

```sh
baton apis validate apis/telegram
```

This parses the pack and replays the recorded response in
`apis/telegram/examples/` through the transforms, checking the result of
`send` against the `notify/v1` interface schema. The test suite does the
same on every commit, so the example needs to be kept in step with the
transform.
