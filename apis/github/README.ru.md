# github

API-пак, который учит baton работать с GitHub REST API, не внося название
GitHub в сам бинарник. Пак реализует четыре операции из пяти в интерфейсе
`forge/v1`, поэтому сценарий, написанный под `forge/v1`, запускается на
GitHub, GitLab или Gitea — меняется только подключаемый пак.

| | |
|---|---|
| Пак | `github`, версия 1 |
| API | GitHub REST API |
| Интерфейс | `forge/v1` — `list_files`, `get_file`, `post_comment`, `post_review` |
| Дополнительные операции | `get_change` (без `implements`; см. «Чего здесь нет и почему») |
| Аутентификация | `bearer`, уходит как `Authorization: Bearer <token>` |
| Base URL | по умолчанию `https://api.github.com` |
| Пагинация | заголовок `Link`, до 20 страниц |

## Подключение в сценарии

```yaml
secrets:
  github_rw: { from: env, key: GITHUB_TOKEN }

apis:
  forge:
    pack: github
    from: ../apis/
    # Для api.github.com блок config можно не писать вообще; для GitHub
    # Enterprise Server понадобится config.base_url с адресом своего API.
    auth: { secret: github_rw }
    timeout: 20s

steps:
  - id: files
    http:
      op: forge.list_files
      args: { project: "{{ .inputs.project }}", id: "{{ .inputs.pr }}" }
```

Имя слева (`forge`) — это то, как шаги обращаются к паку, поэтому один и тот
же сценарий вызывает `forge.list_files` независимо от того, с каким форджем
он работает. Сценарий может также объявить `interface: forge/v1` — тогда
baton откажется загружать пак, который покрывает интерфейс не полностью.

## Конфигурация

| Ключ | Обязателен | По умолчанию | Значение |
|---|---|---|---|
| `base_url` | нет | `https://api.github.com` | Корень API |
| `auth.secret` | да | — | Токен, уходит как `Authorization: Bearer <token>` |

## Операции

### `get_change` — `GET /repos/{project}/pulls/{id}`

Только чтение. Без `implements`: см. «Чего здесь нет и почему».

| Аргумент | Где | Шаблон | Примечания |
|---|---|---|---|
| `project` | path | `^[\w.-]+/[\w.-]+$` | `owner/repo`; настоящий слеш в пути, поэтому не кодируется как один сегмент |
| `id` | path | `^\d+$` | Номер pull request |

```json
{ "id": 42, "title": "...", "description": "...", "author": "user",
  "base": "sha", "head": "sha", "url": "https://github.com/..." }
```

### `list_files` — `GET /repos/{project}/pulls/{id}/files`

Только чтение, с пагинацией. Реализует `forge/v1.list_files`. Аргументы те
же, что у `get_change`:

```json
[ { "path": "internal/httpx/paginate.go", "diff": "@@ -18,7 +18,7 @@\n..." },
  { "path": "internal/httpx/testdata/link_header.txt", "deleted": true } ]
```

GitHub называет удалённый файл "removed", а не "deleted"; transform
переводит это в флаг `deleted`, который ожидает `forge/v1`. Бинарный файл
приходит без `.patch`, и поле `diff` добавляется, только когда `.patch`
есть: `forge/v1` типизирует `diff` как текст, и отсутствующий ключ лучше,
чем null. Ограничение 500k на страницу.

### `get_file` — `GET /repos/{project}/contents/{path}`

Только чтение. Реализует `forge/v1.get_file`.

| Аргумент | Где | Шаблон | Примечания |
|---|---|---|---|
| `project` | path | `^[\w.-]+/[\w.-]+$` | |
| `path` | path | `^[^\s?#]+$` | Весь путь — один сегмент, вместе со слешами; GitHub держит настоящие сегменты пути в URL, в отличие от GitLab, поэтому `encode: path` здесь не нужен |
| `ref` | query | `^[\w./-]+$` | Необязателен |

```json
{ "content": "package main\n...", "path": "internal/httpx/paginate.go",
  "sha": "9a1c0f5d6e7b8a9c0d1e2f3a4b5c6d7e8f901234" }
```

GitHub заворачивает base64-содержимое по 60 символов в строке; transform
убирает переносы строк перед декодированием. Ответ не отдаёт ref, только
sha блоба, который так и сообщается. Ограничение 500k.

### `post_comment` — `POST /repos/{project}/issues/{id}/comments`

Запись. Реализует `forge/v1.post_comment`. Тело уходит как JSON. Pull
request для этого API — тот же issue, поэтому путь идёт через
`/issues/{id}`.

| Аргумент | Где | Ограничение | Примечания |
|---|---|---|---|
| `project` | path | | |
| `id` | path | | |
| `body` | body | 65000 символов | Текст комментария, Markdown |

```json
{ "id": 123456789, "url": "https://github.com/example/project/pull/42#issuecomment-123456789" }
```

Операция не помечена `readonly`, поэтому её результат никогда не берётся из
кеша, а `--dry-run` её не отправляет.

### `post_review` — `POST /repos/{project}/pulls/{id}/reviews`

Запись. Реализует `forge/v1.post_review`. Ревью pull request с общим
комментарием и построчными комментариями одним батчем.

| Аргумент | Где | Ограничение | Примечания |
|---|---|---|---|
| `project` | path | | |
| `id` | path | | |
| `summary` | body | 65000 символов, необязателен | Уходит на проводе как `body` |
| `comments` | body | | Каждый элемент — `{ path, line, body }` |
| `event` | body | | Одно из `COMMENT`, `APPROVE`, `REQUEST_CHANGES`; по умолчанию `COMMENT` |

```json
{ "id": 987654321, "url": "https://github.com/example/project/pull/42#pullrequestreview-987654321" }
```

Операция не помечена `readonly`, поэтому её результат никогда не берётся из
кеша, а `--dry-run` её не отправляет.

## Ошибки и лимиты

Пак не задаёт `envelope`, поэтому неудачный запрос сообщается обычным HTTP
статусом, а не полем с текстом ошибки от сервиса. Троттлинг читается из
`X-RateLimit-Remaining` и `Retry-After`, так что шаг с повторами ждёт
столько, сколько просит GitHub.

## Чего здесь нет и почему

У `get_change` нет `implements`. GitHub разносит pull request по двум
эндпоинтам: сам pull request не содержит список файлов, а эндпоинт с
файлами не содержит заголовок, поэтому `forge/v1.get_change` — который
просит id, title и files одним вызовом — не может быть отвечен одним
запросом. Сценарию, которому нужно и то и другое, приходится сочетать
`get_change` с `list_files`, который несёт диффы. Поскольку у операции нет
`implements`, сценарию, объявившему `interface: forge/v1`, об этом сообщат
на валидации, а не во время выполнения.

## Проверка после правок

```sh
baton apis validate apis/github
```

Команда разбирает пак и прогоняет через transform каждый записанный ответ из
`apis/github/examples/`, сверяя результат со схемой интерфейса для каждой
операции с `implements`. То же самое делает набор тестов на каждом коммите,
поэтому пример для операции с `implements` нужно держать в согласии с её
transform.
