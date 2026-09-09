# gitlab

API-пак, который учит baton работать с GitLab REST API v4, не внося название
GitLab в сам бинарник. Пак реализует четыре операции из пяти в интерфейсе
`forge/v1`, поэтому сценарий, написанный под `forge/v1`, запускается на
GitLab, GitHub или Gitea — меняется только подключаемый пак.

| | |
|---|---|
| Пак | `gitlab`, версия 1 |
| API | GitLab REST API v4 |
| Интерфейс | `forge/v1` — `get_change`, `list_files`, `get_file`, `post_comment` |
| Дополнительные операции | `list_merge_requests` |
| Аутентификация | `header`, `PRIVATE-TOKEN` |
| Base URL | по умолчанию `https://gitlab.com/api/v4` |
| Пагинация | заголовок `Link`, до 20 страниц |

## Подключение в сценарии

```yaml
secrets:
  gitlab_rw: { from: env, key: GITLAB_TOKEN }

apis:
  forge:
    pack: gitlab
    from: ../apis/
    # Для gitlab.com блок config можно не писать вообще; для self-managed
    # инстанса GitLab CI отдаёт адрес в переменной CI_API_V4_URL.
    config: { base_url: "{{ .inputs.api_url }}" }
    auth: { secret: gitlab_rw }
    timeout: 20s

steps:
  - id: changes
    http:
      op: forge.get_change
      args: { project: "{{ .inputs.project }}", id: "{{ .inputs.mr }}" }
```

Имя слева (`forge`) — это то, как шаги обращаются к паку, поэтому один и тот
же сценарий вызывает `forge.get_change` независимо от того, с каким форджем
он работает. Сценарий может также объявить `interface: forge/v1` — тогда
baton откажется загружать пак, который покрывает интерфейс не полностью.

## Конфигурация

| Ключ | Обязателен | По умолчанию | Значение |
|---|---|---|---|
| `base_url` | нет | `https://gitlab.com/api/v4` | Корень API, вместе с `/api/v4` |
| `auth.secret` | да | — | Personal, project или group access token, уходит в `PRIVATE-TOKEN` |

Токену достаточно `read_api` для операций только на чтение и нужен `api`,
чтобы оставить комментарий.

## Операции

### `get_change` — `GET /projects/{project}/merge_requests/{id}/changes`

Только чтение. Реализует `forge/v1.get_change`.

| Аргумент | Где | Шаблон | Примечания |
|---|---|---|---|
| `project` | path | `^[\w./-]+$` | Числовой id или `namespace/path`; кодируется как один сегмент пути |
| `id` | path | `^\d+$` | `iid` merge request — номер, который виден в его URL |

```json
{ "id": 42, "title": "...", "description": "...", "author": "user",
  "base": "sha", "head": "sha",
  "files": [ { "path": "a.go", "diff": "@@ ...", "deleted": false } ] }
```

Тело ответа ограничено 500k.

### `list_files` — `GET /projects/{project}/merge_requests/{id}/diffs`

Только чтение, с пагинацией. Реализует `forge/v1.list_files`. Аргументы те
же, что у `get_change`, а результат — один массив `files`:

```json
[ { "path": "a.go", "diff": "@@ ...", "deleted": false } ]
```

Страницы склеиваются до вызова transform, поэтому шаг видит один плоский
массив со всеми файлами. Ограничение 500k на страницу.

### `get_file` — `GET /projects/{project}/repository/files/{path}`

Только чтение. Реализует `forge/v1.get_file`.

| Аргумент | Где | Шаблон | Примечания |
|---|---|---|---|
| `project` | path | `^[\w./-]+$` | Кодируется как один сегмент пути |
| `path` | path | `^[^\s?#]+$` | Для GitLab весь путь — один сегмент, вместе со слешами |
| `ref` | query | `^[\w./-]+$` | По умолчанию `HEAD`; в `forge/v1` ref необязателен, а GitLab требует его всегда |

```json
{ "content": "package main\n...", "path": "cmd/main.go", "ref": "HEAD" }
```

GitLab отдаёт содержимое в base64 независимо от типа файла; transform его
декодирует. Ограничение 500k.

### `list_merge_requests` — `GET /projects/{project}/merge_requests`

Только чтение, с пагинацией. Сверх `forge/v1`: нужна для отчётов по
влитой работе.

| Аргумент | Где | Шаблон | Примечания |
|---|---|---|---|
| `project` | path | `^[\w./-]+$` | |
| `state` | query | `^[a-z]+$` | Одно из `opened`, `closed`, `merged`, `locked`, `all`; по умолчанию `merged` |
| `updated_after` | query | `^[\d:.TZ+-]+$` | Необязательная дата или метка времени в ISO 8601 |

```json
[ { "id": 42, "title": "...", "author": "user", "url": "https://...",
    "merged_at": "2024-05-01T10:00:00Z", "updated_at": "2024-05-01T10:00:00Z" } ]
```

### `post_comment` — `POST /projects/{project}/merge_requests/{id}/notes`

Запись. Реализует `forge/v1.post_comment`. Тело уходит как JSON.

| Аргумент | Где | Ограничение | Примечания |
|---|---|---|---|
| `project` | path | | |
| `id` | path | | |
| `body` | body | 65000 символов | Текст комментария, Markdown |

```json
{ "id": 1, "url": "https://gitlab.example/.../notes/1" }
```

Операция не помечена `readonly`, поэтому её результат никогда не берётся из
кеша, а `--dry-run` её не отправляет.

## Ошибки и лимиты

GitLab сообщает об ошибке кодом ответа и полем `message`, и это сообщение
становится ошибкой шага:

```yaml
envelope:
  error_when: 'type == "object" and .message != null'
  error_message: .message
```

Проверка типа стоит первой, потому что списочный эндпоинт отвечает массивом,
у которого полей нет вообще. Троттлинг читается из `RateLimit-Remaining` и
`Retry-After`, так что шаг с повторами ждёт столько, сколько просит GitLab.

## Чего здесь нет и почему

`post_review` не реализован сознательно. GitLab создаёт по одной дискуссии на
каждый комментарий и не имеет пакетного вызова ревью, поэтому сценарий
вызывает `post_comment` в `foreach`. Поскольку у операции нет `implements`,
сценарию, объявившему `interface: forge/v1`, об этом сообщат на валидации, а
не во время выполнения.

## Проверка после правок

```sh
baton apis validate apis/gitlab
```

Команда разбирает пак и прогоняет через transform каждый записанный ответ из
`apis/gitlab/examples/`, сверяя результат со схемой интерфейса для каждой
операции с `implements`. То же самое делает набор тестов на каждом коммите,
поэтому пример для операции с `implements` нужно держать в согласии с её
transform.
