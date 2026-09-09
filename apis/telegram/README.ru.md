# telegram

API-пак, который учит baton работать с Telegram Bot API. Это эталонная
реализация интерфейса `notify/v1` (собственный комментарий в заголовке
pack.yaml): канал глобального конфига, либо шаг, вызывающий пак напрямую,
отправляет сообщение через `send(target, text, format)`, а сценарий не знает,
что говорит именно с Telegram.

| | |
|---|---|
| Пак | `telegram`, версия 1 |
| API | Telegram Bot API |
| Интерфейс | `notify/v1` — `send` |
| Дополнительные операции | `send_document`, `get_me` |
| Аутентификация | `path` — токен бота подставляется в `{auth}` внутри base URL |
| Base URL | по умолчанию `https://api.telegram.org/bot{auth}` |
| Пагинация | отсутствует — каждая операция это один вызов |

## Подключение в сценарии

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

Имя слева (`telegram`) — это то, как шаг обращается к паку: шаг выше
вызывает `telegram.send` и получает форму результата, которую обещает
интерфейс.

Поскольку пак реализует `notify/v1`, та же самая запись `apis` может вместо
явного шага `http` обслуживать канал `notify:` глобальной конфигурации —
именно для этого пака такой пример есть в разделе 12 спецификации и в
quickstart-руководстве:

```yaml
# baton.yaml (глобальный конфиг)
apis:
  telegram:
    pack: telegram
    from: ./apis/
    auth: { secret: tg_bot }

notify:
  chat:
    api: telegram                # пак, реализующий notify/v1
    target: "-1001234567890"     # id чата, или @channelname
```

```yaml
# сценарий
- id: report
  notify: chat
  message: "nightly-build failed: http on publish"
```

`notify: chat` разворачивается в вызов `telegram.send` с `target` канала и
отрендеренным `message`; канал авторизуется секретом своей же записи
`apis.telegram`, которую сценарий никогда не видит.

## Конфигурация

| Ключ | Обязателен | По умолчанию | Значение |
|---|---|---|---|
| `base_url` | нет | `https://api.telegram.org/bot{auth}` | Корень API; при `auth.kind: path` токен бота подставляется на место `{auth}` |
| `auth.secret` | да | — | Токен бота, выданный BotFather |

Токен читается из секрета только в момент вызова и подставляется в путь
запроса; в сам пак он никогда не записывается, а редактор секретов
маскирует его в логах так же, как и любое другое значение аутентификации
(`bot***/sendMessage`, а не настоящий токен).

## Операции

### `send` — `POST /sendMessage`

Не только чтение. Реализует `notify/v1.send`. Тело уходит как JSON
(`encode: json`).

| Аргумент | Имя на проводе | Где | Шаблон / ограничение | По умолчанию | Обязателен |
|---|---|---|---|---|---|
| `target` | `chat_id` | body | `^(-?\d+|@[\w]{5,32})$` | — | да |
| `text` | `text` | body | до 4096 символов | — | да |
| `format` | `parse_mode` | body | `^(MarkdownV2|HTML|Markdown)$`, одно из `MarkdownV2`, `HTML`, `Markdown` | — | нет |

`target` — числовой id чата (отрицательный для канала или супергруппы) или
`@username`; в `notify/v1` этот аргумент называется `target`, у Telegram —
`chat_id`. `format` несёт собственные имена режимов разметки Telegram,
поскольку словарь форматов `notify/v1` отдаёт на откуп паку: по умолчанию
используется обычный текст, а неэкранированное сообщение в `MarkdownV2`
API отклоняет целиком, а не рендерит частично.

```json
{ "id": 4821, "target": "-1001234567890" }
```

(`id` — это `message_id` из Telegram; `target` — id чата, приведённый к
строке.)

### `send_document` — `POST /sendDocument`

Не только чтение, без `implements`. Тело уходит как JSON (`encode: json`),
а не как multipart: `document` — это URL или `file_id` Telegram, уже
доступный API, а не сырые байты файла.

| Аргумент | Имя на проводе | Где | Шаблон / ограничение | По умолчанию | Обязателен |
|---|---|---|---|---|---|
| `target` | `chat_id` | body | `^(-?\d+|@[\w]{5,32})$` | — | да |
| `document` | `document` | body | `^\S+$` | — | да |
| `caption` | `caption` | body | до 1024 символов | — | нет |

```json
{ "id": 4821, "target": "-1001234567890" }
```

Тот же transform и та же форма результата, что у `send`.

### `get_me` — `GET /getMe`

Только чтение. Без `implements`; проверка того, какому боту принадлежит
настроенный токен.

Аргументов нет.

```json
{ "id": ..., "username": "...", "name": "..." }
```

(`id` и `username` берутся прямо из объекта пользователя Telegram, `name` —
это `.first_name`.)

## Ошибки и envelope

Telegram отвечает HTTP 200 с `{"ok": false, "description": "..."}` так же
охотно, как и статусом 4xx, и оборачивает каждый успех в
`{"ok": true, "result": ...}` (собственный комментарий в pack.yaml).
Envelope разворачивает тело и превращает `ok: false` в ошибку шага:

```yaml
envelope:
  unwrap: .result
  error_when: '.ok == false'
  error_message: .description
```

Без этого шаг, читающий `.message_id` из отклонённого сообщения, падал бы
внутри transform на несуществующем поле, вместо того чтобы завершиться с
ошибкой сразу, с тем самым описанием, которое дал Telegram.

## Заметки

- Пак оформлен как директория, потому что он поставляет записанный ответ,
  который читает проверка реализации `notify/v1` — `examples/send.json`.
- Токен бота находится в пути запроса, а не в заголовке, поэтому
  `auth.kind` — это `path`, а плейсхолдер принадлежит `base_url`, а не
  какой-то отдельной операции.
- Аргумент `target` операции `send` — это словарь `notify/v1`; на проводе
  пак переименовывает его в собственный `chat_id` Telegram, а `format` — в
  `parse_mode`.

## Проверка после правок

```sh
baton apis validate apis/telegram
```

Команда разбирает пак и прогоняет через transform записанный ответ из
`apis/telegram/examples/`, сверяя результат `send` со схемой интерфейса
`notify/v1`. То же самое делает набор тестов на каждом коммите, поэтому
пример нужно держать в согласии с transform.
