# gitea

API-пак, который учит baton работать с Gitea REST API v1, не внося название
Gitea в сам бинарник. Пак реализует четыре операции из пяти в интерфейсе
`forge/v1`, поэтому сценарий, написанный под `forge/v1`, запускается на
Gitea, GitHub или GitLab — меняется только подключаемый пак. API Gitea во
многом повторяет GitHub, поэтому этот пак похож на пак `github`.

| | |
|---|---|
| Пак | `gitea`, версия 1 |
| API | Gitea REST API v1 |
| Интерфейс | `forge/v1` — `list_files`, `get_file`, `post_comment`, `post_review` |
| Дополнительные операции | `get_change` (без `implements`; см. «Чего здесь нет и почему») |
| Аутентификация | `bearer`, уходит как `Authorization: Bearer <token>` |
| Base URL | обязателен, значения по умолчанию нет; инстанс Gitea self-hosted |
| Пагинация | заголовок `Link`, до 20 страниц |

## Подключение в сценарии

```yaml
secrets:
  gitea_rw: { from: env, key: GITEA_TOKEN }

apis:
  forge:
    pack: gitea
    from: ../apis/
    config: { base_url: "https://{{ .inputs.host }}/api/v1" }
    auth: { secret: gitea_rw }
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
| `base_url` | да | — | `https://<host>/api/v1`; значения по умолчанию нет, потому что инстанс Gitea self-hosted |
| `auth.secret` | да | — | Токен доступа Gitea, уходит как `Authorization: Bearer <token>` |

Gitea также принимает токен как `Authorization: token <t>`; пак использует
схему bearer, которую baton уже умеет, вместо того чтобы добавлять
отдельный вид аутентификации ради одного сервиса.

## Операции

### `get_change` — `GET /repos/{project}/pulls/{id}`

Только чтение. Без `implements`: см. «Чего здесь нет и почему».

| Аргумент | Где | Шаблон | Примечания |
|---|---|---|---|
| `project` | path | `^[\w.-]+/[\w.-]+$` | `owner/repo` |
| `id` | path | `^\d+$` | Номер pull request |

```json
{ "id": 42, "title": "...", "description": "...", "author": "user",
  "base": "sha", "head": "sha", "url": "https://gitea.example.com/..." }
```

### `list_files` — `GET /repos/{project}/pulls/{id}/files`

Только чтение, с пагинацией. Реализует `forge/v1.list_files`. Аргументы те
же, что у `get_change`:

```json
[ { "path": "internal/httpx/paginate.go" },
  { "path": "internal/httpx/testdata/link_header.txt", "deleted": true },
  { "path": "internal/httpx/paginate_v2.go" } ]
```

Этот эндпоинт Gitea не отдаёт текст диффа, только имена файлов и счётчики
изменений, поэтому сценарию, которому нужен именно дифф, приходится читать
файл через `get_file`. Gitea называет удалённый файл "deleted" (GitHub
называет его "removed"); transform переводит это в флаг `deleted`, который
ожидает `forge/v1`. Ограничение 500k на страницу.

### `get_file` — `GET /repos/{project}/contents/{path}`

Только чтение. Реализует `forge/v1.get_file`.

| Аргумент | Где | Шаблон | Примечания |
|---|---|---|---|
| `project` | path | `^[\w.-]+/[\w.-]+$` | |
| `path` | path | `^[^\s?#]+$` | Весь путь — один сегмент, вместе со слешами |
| `ref` | query | `^[\w./-]+$` | Необязателен |

```json
{ "content": "package main\n...", "path": "internal/httpx/paginate.go",
  "sha": "9a1c0f5d6e7b8a9c0d1e2f3a4b5c6d7e8f901234" }
```

В отличие от GitHub, Gitea не заворачивает base64-содержимое по строкам,
поэтому transform декодирует его напрямую. Ответ не отдаёт ref, только sha
блоба, который так и сообщается. Ограничение 500k.

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
{ "id": 456789, "url": "https://gitea.example.com/example/project/pulls/42#issuecomment-456789" }
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
| `comments` | body | | Каждый элемент — `{ path, body, new_position }` |
| `event` | body | | Одно из `COMMENT`, `APPROVED`, `REQUEST_CHANGES`; по умолчанию `COMMENT` |

```json
{ "id": 321, "url": "https://gitea.example.com/example/project/pulls/42#pullrequestreview-321" }
```

Операция не помечена `readonly`, поэтому её результат никогда не берётся из
кеша, а `--dry-run` её не отправляет.

## Ошибки и лимиты

Пак не задаёт ни `envelope`, ни `rate_limit`, поэтому неудачный запрос
сообщается обычным HTTP статусом, а шаг с повторами использует свой
собственный backoff, а не заголовок от Gitea.

## Чего здесь нет и почему

У `get_change` нет `implements`. Gitea разносит pull request так же, как
GitHub: сам pull request не содержит список файлов, поэтому
`forge/v1.get_change` — который просит id, title и files одним вызовом — не
может быть отвечен одним запросом. Сценарию, которому нужно и то и другое,
приходится сочетать `get_change` с `list_files`. Поскольку `list_files` не
отдаёт текст диффа, только имена и счётчики изменений, сценарию, которому
нужен ещё и сам дифф, приходится читать его через `get_file`. Поскольку у
`get_change` нет `implements`, сценарию, объявившему `interface: forge/v1`,
об этом сообщат на валидации, а не во время выполнения.

## Проверка после правок

```sh
baton apis validate apis/gitea
```

Команда разбирает пак и прогоняет через transform каждый записанный ответ из
`apis/gitea/examples/`, сверяя результат со схемой интерфейса для каждой
операции с `implements`. То же самое делает набор тестов на каждом коммите,
поэтому пример для операции с `implements` нужно держать в согласии с её
transform.
