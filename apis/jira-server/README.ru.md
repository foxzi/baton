# jira-server

API-пак, который учит baton работать с Jira Server / Data Center REST API,
не внося название Jira в сам бинарник. Это отдельный пак от `jira`, который
говорит с Jira Cloud: они различаются тремя вещами, которые не может
покрыть один пак — аутентификация, эндпоинт поиска и то, что Agile-доска
сообщает о себе сама. Пак реализует одну операцию интерфейса `tracker/v1` и
добавляет сверх неё несколько операций только для чтения, для отчётов.

Всё в этом паке проверено на живом сайте Jira Server 9.17.5, а записанные
примеры — это собственные ответы этого сайта с обезличенными значениями.

| | |
|---|---|
| Пак | `jira-server`, версия 1 |
| API | Jira Server / Data Center REST API |
| Интерфейс | `tracker/v1` — `get_issue` |
| Дополнительные операции | `search`, `search_all`, `search_summary`, `list_boards`, `list_board_issues` |
| Аутентификация | `bearer`, personal access token |
| Base URL | нет значения по умолчанию, `base_url` обязателен |
| Пагинация | offset `startAt`, размер страницы задаётся в каждой операции, до 50 страниц |

Этот пак только для чтения: ни одна операция не пишет в Jira.

## Подключение в сценарии

```yaml
secrets:
  jira: { from: env, key: JIRA_TOKEN }

apis:
  tracker:
    pack: jira-server
    from: ../apis/
    config: { base_url: "{{ .inputs.jira_url }}" }
    auth: { secret: jira }
    # Широкий JQL по большому проекту проходит десятки страниц; таймаут
    # одного запроса должен оставлять запас на самую медленную из них.
    timeout: 60s

steps:
  - id: issues
    http:
      op: tracker.search_summary
      args:
        query: "{{ .inputs.jql }}"
```

Имя слева (`tracker`) — это то, как шаги обращаются к паку, поэтому один и
тот же сценарий вызывает `tracker.search_summary` независимо от того, с
каким трекером он работает. Сценарий может также объявить
`interface: tracker/v1` — тогда baton откажется загружать пак, который
покрывает интерфейс не полностью.

Второй сценарий, `examples/jira-quality.yaml`, обходит одну доску через
`list_board_issues` и просит модель оценить описание каждой задачи:

```yaml
apis:
  tracker:
    pack: jira-server
    from: ../apis/
    config: { base_url: "{{ .inputs.jira_url }}" }
    auth: { secret: jira }
    timeout: 60s

steps:
  - id: issues
    cache: true
    http:
      op: tracker.list_board_issues
      args:
        board: "{{ .inputs.board }}"
        # description — это то, что оценивается, поэтому его нужно запросить.
        fields: summary,status,description
        # Пак называет этот аргумент query; jql — его имя на проводе.
        query: "{{ .inputs.jql }}"
```

## Конфигурация

| Ключ | Обязателен | По умолчанию | Значение |
|---|---|---|---|
| `base_url` | да | — | Корень сайта, без REST-префикса; пути ниже несут свои префиксы `/rest/api/2/...` и `/rest/agile/1.0/...` |
| `auth.secret` | да | — | Personal access token (Profile -> Personal Access Tokens), уходит как `Authorization: Bearer` |

У personal access token Jira Server нет пользовательской половины, в
отличие от схемы Basic в паке для Cloud.

## Операции

### `get_issue` — `GET /rest/api/2/issue/{key}`

Только чтение. Реализует `tracker/v1.get_issue`.

| Аргумент | Где | Имя на проводе | Шаблон | По умолчанию | Обязателен |
|---|---|---|---|---|---|
| `key` | path | `key` | `^[A-Z][A-Z0-9_]*-\d+$` | — | да |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status,description,reporter` | да (есть значение по умолчанию) |

```json
{ "key": "DEMO-8661", "title": "Search misses returned orders",
  "url": "https://jira.example.com/browse/DEMO-8661",
  "status": "In review", "body": "Steps:\n\n...", "author": "Jane Doe" }
```

`fields` запрашивается явно, чтобы в ответе оказалось только то, что читает
transform: набор полей этого эндпоинта по умолчанию — это все поля задачи,
десятки килобайт на активном проекте. Поле, которое не было запрошено, или
то, что задача оставляет пустым, приходит как null или отсутствует, а
`tracker/v1` типизирует `status`, `body` и `author` как строки, поэтому
каждое из них добавляется в результат только когда у него есть значение.
`url` — это браузерная ссылка, собранная из REST-адреса задачи, поскольку
Jira не сообщает никакой другой: `self` — это
`.../rest/api/2/issue/86908`, а читателю нужен `.../browse/DEMO-8661`.
Трансформы этого пака собирают её как
`((.self | split("/rest/"))[0]) + "/browse/" + .key`.

### `search` — `GET /rest/api/2/search`

Только чтение, одна страница. Сверх `tracker/v1`.

| Аргумент | Где | Имя на проводе | Шаблон | По умолчанию | Обязателен |
|---|---|---|---|---|---|
| `query` | query | `jql` | `^[\s\S]+$`, максимум 4000 символов | — | да |
| `limit` | query | `maxResults` | `^\d+$` | — | нет |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status` | да (есть значение по умолчанию) |

```json
[ { "key": "DEMO-8661", "title": "Search misses returned orders",
    "status": "In review", "url": "https://jira.example.com/browse/DEMO-8661" } ]
```

Операция сознательно остаётся однострадничной: `tracker/v1.search`
принимает `limit`, а операция с пагинацией перезаписала бы его размером
страницы. `search_all` ниже обходит все страницы вместо этого.

### `search_all` — `GET /rest/api/2/search`

Только чтение, с пагинацией. Сверх `tracker/v1`.

| Аргумент | Где | Имя на проводе | Шаблон | По умолчанию | Обязателен |
|---|---|---|---|---|---|
| `query` | query | `jql` | `^[\s\S]+$`, максимум 4000 символов | — | да |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status` | да (есть значение по умолчанию) |

Пагинация: `style: offset`, `param: startAt`, `limit_param: maxResults`, в
query, размер страницы 100, до 50 страниц. Результаты пагинации доходят до
transform уже как склейка items со всех страниц, поэтому входные данные
transform — это сам массив задач.

```json
[ { "key": "DEMO-8661", "title": "Search misses returned orders",
    "status": "In review", "url": "https://jira.example.com/browse/DEMO-8661" } ]
```

### `search_summary` — `GET /rest/api/2/search`

Только чтение, с пагинацией. Сверх `tracker/v1`. `search_all`,
переформатированный для отчёта: тот же обход, но transform также считает
задачи по статусу, исполнителю и типу. Подсчёт делается именно в transform,
потому что jq — единственное место в сценарии, которое умеет группировать
список; шаблон умеет перебирать, но не агрегировать.

| Аргумент | Где | Имя на проводе | Шаблон | По умолчанию | Обязателен |
|---|---|---|---|---|---|
| `query` | query | `jql` | `^[\s\S]+$`, максимум 4000 символов | — | да |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status,assignee,priority,issuetype,created,updated` | да (есть значение по умолчанию) |

Пагинация: `style: offset`, `param: startAt`, `limit_param: maxResults`, в
query, размер страницы 100, до 50 страниц.

```json
{
  "total": 4,
  "by_status": [ { "name": "Open", "category": "To Do", "count": 2 } ],
  "by_assignee": [ { "name": "Unassigned", "count": 1 } ],
  "by_type": [ { "name": "Bug", "count": 2 } ],
  "issues": [ { "key": "DEMO-8663", "title": "Add retry logic to the payment webhook consumer",
    "status": "Open", "category": "To Do", "assignee": "Unassigned", "priority": "Critical",
    "type": "Story", "created": "2025-08-16", "updated": "2025-08-16",
    "url": "https://jira.example.com/browse/DEMO-8663" } ]
}
```

`total` здесь — это количество реально обойдённых задач, которое меньше
собственного total сайта, если обход остановился на `max_pages`. Набор
полей по умолчанию шире, чем у `search`, потому что отчёт называет
исполнителя, приоритет и тип, и сортирует по дате обновления.

### `list_boards` — `GET /rest/agile/1.0/board`

Только чтение, с пагинацией. Сверх `tracker/v1`.

| Аргумент | Где | Имя на проводе | Шаблон | По умолчанию | Обязателен |
|---|---|---|---|---|---|
| `project` | query | `projectKeyOrId` | `^[\w-]+$` | — | нет |
| `type` | query | `type` | `^(scrum\|kanban\|simple)$`, enum `scrum`, `kanban`, `simple` | — | нет |
| `name` | query | `name` | `^[\s\S]+$`, максимум 200 символов | — | нет |

Пагинация: `style: offset`, `param: startAt`, `limit_param: maxResults`, в
query, размер страницы 50, до 50 страниц. Agile API жёстко ограничивает этот
эндпоинт 50, что бы ни было запрошено: запрос на 100 возвращается с
`maxResults` 50. Размер страницы должен совпадать с этим лимитом, иначе
первая страница выглядит короткой и обход останавливается после одной
страницы.

```json
[ { "id": 91, "name": "(AT) scrum", "type": "scrum",
    "url": "https://jira.example.com/secure/RapidBoard.jspa?rapidView=91" } ]
```

Доска в Server не несёт своей локации, в отличие от Cloud, поэтому здесь
нет ключа проекта, который можно было бы отдать; `list_board_issues`
получает задачи доски, и их ключи называют проект.

### `list_board_issues` — `GET /rest/agile/1.0/board/{board}/issue`

Только чтение, с пагинацией. Сверх `tracker/v1`.

| Аргумент | Где | Имя на проводе | Шаблон | По умолчанию | Обязателен |
|---|---|---|---|---|---|
| `board` | path | `board` | `^\d+$` | — | да |
| `query` | query | `jql` | `^[\s\S]+$`, максимум 4000 символов | — | нет |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status` | да (есть значение по умолчанию) |

Пагинация: `style: offset`, `param: startAt`, `limit_param: maxResults`, в
query, размер страницы 100, до 50 страниц. Собственный фильтр доски уже
задаёт границы результата; `query` сужает его дальше, например, до одного
спринта или одного статуса.

```json
[ { "key": "DEMO-8438", "title": "Move the catalogue page to Inertia",
    "status": "In review", "url": "https://jira.example.com/browse/DEMO-8438" } ]
```

`description` попадает в результат только когда `fields` его запросил,
поэтому ключ остаётся отсутствующим вместо того, чтобы превратиться в null
для полей по умолчанию.

## Чего здесь нет и почему

И `comment` (`POST /rest/api/2/issue/{key}/comment`), и `create_issue`
(`POST /rest/api/2/issue`, через точечные имена в теле) теперь выразимы, и
пак `jira` несёт обе, но personal access token, на котором проверялся этот
пак, предназначен только для чтения, поэтому ни одного запроса на запись к
нему на живом сайте так и не было сделано. Они появятся здесь, как только
кто-то сможет их проверить; до тех пор пак поставляет только проверенное.
Сценарий, которому нужно что-то записать в задачу Server, отправляет это
сырым http-шагом через этот api.

## Заметка о записанных примерах

`baton apis validate` прогоняет transform по файлу примера без обхода
пагинации, поэтому пример операции с пагинацией (`search_all.json`,
`search_summary.json`, `list_boards.json`, `list_board_issues.json`) хранится
как уже склеенный по страницам массив items — та же форма, что transform
получает во время реального выполнения, — а не как один сырой ответ одной
страницы.

## Проверка после правок

```sh
baton apis validate apis/jira-server
```

Команда разбирает пак и прогоняет через transform каждый записанный ответ из
`apis/jira-server/examples/`, сверяя результат со схемой интерфейса для
каждой операции с `implements`. То же самое делает набор тестов на каждом
коммите, поэтому пример для операции с `implements` нужно держать в
согласии с её transform.
