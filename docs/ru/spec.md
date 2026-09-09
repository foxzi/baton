# Baton v1 — техническое задание

Документ для одного разработчика. Решения, перечисленные здесь, уже приняты; их не нужно пересматривать без веской причины. Спорные места вынесены в раздел «Открытые вопросы». Объём рассчитан на 9–11 недель работы одного человека.

## 1. Цель и границы v1

Цель: рабочий бинарник, которым можно (а) запустить код-ревью MR из CI на GitLab и GitHub и (б) собрать еженедельный отчёт по нескольким репозиториям через cron. Всё остальное — во вторую версию.

### В объёме v1

- CLI `baton` с командами `run`, `validate`, `resume`, `runs`, `tools`, `schema`
- Шаги: `run`, `http`, `llm`, `agent`, `foreach`, `until`, `assert`
- Выражения `when:`, `switch:`, шаблоны строк
- Типизированные значения, тип `secret`, провайдеры секретов `env` и `file`
- API-паки (раздел 7.4): загрузчик с пином версии и чексуммой, схемы авторизации включая `exchange`, конверты ответов, четыре стратегии пагинации, jq-трансформы (gojq), операции `graphql`; интерфейсы `forge/v1`, `tracker/v1`, `notify/v1`
- Шлюз как MCP-сервер, инструменты из паков, `commands`, `fetch`, `state`, `submit_result`
- Команда `baton apis import` — генерация заготовки пака из OpenAPI
- Отдельный репозиторий `baton-apis` со стартовыми паками: `gitlab`, `github`, `gitea` (`forge/v1`), `jira` (`tracker/v1`), `telegram`, `slack` (`notify/v1`)
- Движки: `claude-code` для `agent:`; провайдеры `anthropic`, `openai`, `openrouter` и любой OpenAI-совместимый endpoint для `llm:`; `fake` для тестов
- Классы ошибок, retry, `on_error`, `fallback`, `on_failure`, коды выхода
- Каталог прогона, кеш, `resume`, аудит-лог инструментов, редактирование секретов
- Бюджеты на шаг и прогон
- Каналы уведомлений как операции паков с интерфейсом `notify/v1`, плюс встроенные `webhook` и `stdout`

### Не в объёме v1 (сознательно)

- Docker-песочница и режим `exec.mode: shell`
- `baton serve`, вебхуки, встроенное расписание
- Шаг `gate` с ожиданием человека
- Движок `codex` и другие CLI-агенты
- Провайдеры секретов `vault`, `sops`, `1password`
- Параллельное выполнение независимых ветвей DAG (параллелизм только внутри `foreach`)
- Паки для Outline, Confluence, MediaWiki и прочих — формат их поддерживает (раздел 7.4.6), сами паки пишутся по мере надобности вне плана v1
- Составные операции в паках (несколько вызовов в одной операции) — цикл делает `foreach` в сценарии
- Multipart-загрузка файлов через паки
- Кросс-платформенность: v1 работает на Linux, macOS не тестируется

## 2. Архитектура

```
cmd/baton              CLI (cobra или stdlib flag)
internal/scenario      парсинг YAML, валидация, JSON Schema сценария
internal/expr          обёртка над expr-lang, контекст выражений
internal/tmpl          text/template + функции, защита типа secret
internal/values        система типов значений (string, number, bool, list, map, secret, file)
internal/secrets       провайдеры, резолвинг, редактор для логов
internal/engine        интерфейс движка; claudecode/, anthropic/, fake/
internal/steps         run, http, llm, agent, foreach, until, assert
internal/tools         каталог инструментов, генерация схем, исполнение
internal/packs         загрузка и валидация API-паков, реестр интерфейсов, чексуммы
internal/httpx         универсальный HTTP-клиент: auth-схемы, exchange, пагинация, конверт, jq
internal/gateway       MCP-сервер шлюза (streamable HTTP на localhost)
internal/runstate      каталог runs/, состояние, кеш, resume
internal/errors        классы ошибок
internal/notify        каналы уведомлений
internal/executor      обход DAG, retry, on_error, бюджеты, отмена
```

Правило зависимостей: `steps` зависит от `engine`, `tools`, `runstate`; `executor` зависит от `steps`; ничто не зависит от `cmd`. Секреты доступны только `secrets`, `httpx`, `gateway`, `notify` и `engine` (для ключа провайдера) — остальные пакеты получают значения уже как `values.Secret` без доступа к содержимому.

### Последовательность прогона

1. Загрузить сценарий, провести полную валидацию (раздел 4), при ошибке — код выхода 3
2. Создать `runs/<id>/`, записать `run.json` со статусом `running`
3. Зарезолвить все секреты, включая нужные для `on_failure`. Не удалось — прогон не стартует, код 3
4. Подготовить workspace (раздел 7.1)
5. Поднять шлюз, если хотя бы один `agent`-шаг его требует
6. Выполнять шаги по порядку объявления с учётом `when`, `needs`, кеша
7. При падении — отменить параллельные элементы `foreach`, выполнить `on_failure`, записать состояние
8. Записать `cost.json`, финальный статус, завершиться с кодом по разделу 9.5

## 3. Формат сценария

### 3.1 Верхний уровень

```yaml
version: 1
name: code-review
description: Ревью MR с классификацией по риску

inputs:
  project: { type: string, required: true }
  mr:      { type: int,    required: true }
  base:    { type: string, default: origin/main }

defaults:
  engine: claude-code
  model: claude-sonnet-4-6
  timeout: 10m
  budget_usd: 3

budget:
  usd: 8               # на весь прогон
  time: 40m

secrets: { ... }       # раздел 6
apis:    { ... }       # раздел 7.4
commands:              # раздел 7.5
  test: { argv: ["go", "test", "./..."] }

steps: [ ... ]

on_failure: [ ... ]
```

Правила:

- `inputs` типизированы: `string | int | number | bool | list | map`; `pattern` обязателен для string-входов, которые попадают в argv команд
- `defaults` применяются к шагам, у которых поле не задано
- Файлы промптов, схем, шаблонов и навыков указываются путями относительно файла сценария

### 3.2 Общие поля шага

```yaml
- id: review                # уникальный, ^[a-z][a-z0-9_]*$
  when: <expr>              # опционально
  needs: [diff, lint]       # опционально, по умолчанию — все предыдущие шаги
  timeout: 15m
  retry: { on: [transient], attempts: 2, backoff: 10s }
  on_error: fail            # fail | continue | fallback
  fallback: { <тело шага> } # обязательно при on_error: fallback
  cache: true               # см. 8.3
  dedupe_key: <tmpl>        # для шагов с побочными эффектами
```

Ровно одно из полей `run`, `http`, `llm`, `agent`, `foreach`, `until`, `assert` должно присутствовать.

### 3.3 `run`

```yaml
- id: diff
  run:
    argv: ["git", "diff", "{{ .inputs.base }}...HEAD"]
    cwd: workspace          # по умолчанию
    env: { GOFLAGS: "-mod=readonly", TOKEN: { secret: gitlab_ro } }
    stdin: "{{ .steps.prev.stdout }}"
    parse: json             # text | json | lines
    allow_exit_codes: [0]
    max_output_bytes: 1m
```

Никакого `sh -c`. Если пользователь передал `run: "строка"`, валидация принимает это только если строка не содержит метасимволов shell, и разбивает по пробелам с предупреждением. Результат: `stdout`, `stderr`, `exit_code`, `result` (при `parse: json`).

### 3.4 `http`

Две формы. Основная — вызов операции пака:

```yaml
- id: publish
  http:
    op: forge.post_comment                 # <api>.<op> из apis
    auth: forge_rw                         # переопределение секрета, объявленного в apis
    args:
      project: "{{ .inputs.project }}"
      iid: "{{ .inputs.mr }}"
      body: "{{ render \"templates/review.md.tmpl\" .steps.review }}"
  dedupe_key: "review-{{ .inputs.project }}-{{ .inputs.mr }}-{{ .run.id }}"
```

Аргументы валидируются по `params` операции, пагинация, конверт и трансформ применяются автоматически, результат — уже нормализованный `result`. Это единственный способ вызвать операцию с побочным эффектом из пака: агенту такие операции недоступны.

Сырая форма — для разовых запросов, когда пака нет и писать его не стоит:

```yaml
- id: ping
  http:
    api: gitlab                      # base_url и auth из apis, или url: полный адрес
    method: POST
    path: /projects/{{ .inputs.project }}/merge_requests/{{ .inputs.mr }}/notes
    headers: { Content-Type: application/json }
    body: "{{ toJSON (dict \"body\" \"ok\") }}"
    expect_status: [200, 201]
    parse: json
```

`POST/PUT/PATCH/DELETE` (или операция без `readonly: true`) без `dedupe_key` — предупреждение при валидации и запрет автоматического retry (раздел 9.4). Результат: `status`, `headers`, `body`, `result`.

### 3.5 `llm`

```yaml
- id: classify
  llm:
    model: anthropic/claude-haiku-4-5       # <provider>/<model>, провайдер из конфига
    fallback_models:                        # опционально, при transient/budget провайдера
      - openrouter/google/gemini-2.5-flash
      - openai/gpt-4.1-mini
    system: prompts/classify.system.md
    prompt: prompts/classify.md            # файл или inline
    with: { diff: "{{ .steps.diff.stdout }}" }
    schema: schemas/classify.json           # обязательно
    tools: [apis.jira.get_issue]            # опционально, раннер ведёт tool-loop
    max_tokens: 2000
    temperature: 0
```

Модель задаётся строкой `<provider>/<model>`, где `provider` — имя из секции `providers` глобального конфига или сценария (раздел 8.3). Для OpenRouter имя модели само содержит слэш (`openrouter/google/gemini-2.5-flash`): разделитель — первый слэш. `fallback_models` — цепочка на случай ошибок класса `transient` у провайдера (недоступность, 429, 5xx); при ошибке класса `schema` цепочка не используется, работает обычный `schema`-retry на той же модели. Смена модели фиксируется в `output.json` как `model_used`.

`schema` обязательна. Результат `result` — распарсенный и провалидированный JSON. Невалидный вывод → ошибка класса `schema`; при retry в контекст добавляется сообщение с текстом ошибки валидации. Файл промпта — Markdown с `{{ }}`-шаблонами, переменные из `with`.

### 3.6 `agent`

```yaml
- id: review
  agent:
    engine: claude-code
    model: claude-sonnet-4-6
    prompt: prompts/review.md
    with: { project: "{{ .inputs.project }}", iid: "{{ .inputs.mr }}" }
    skills: [./skills/go-review]
    profile: review                        # review | fix | research
    tools: { ... }                         # переопределения поверх профиля, раздел 7
    max_turns: 25
    budget_usd: 2
    result: schemas/findings.json          # схема для submit_result
```

Шаг завершается успешно только вызовом `submit_result` с валидным JSON. Агент, завершившийся без вызова, — ошибка класса `schema` с сообщением «результат не сдан».

### 3.7 `foreach`

```yaml
- id: per_repo
  foreach:
    items: "{{ .inputs.repos }}"           # список
    as: repo
    max_parallel: 3
    on_item_error: fail                    # fail | continue
    min_success: 1.0                       # доля; при continue
    step: { llm: { ... } }                 # тело — любой шаг без id
```

Результат `items` — список `{ status, result, error }` в порядке входа. При `on_item_error: fail` первая ошибка отменяет остальные через context cancellation и роняет шаг.

### 3.8 `until`

```yaml
- id: fix
  until:
    condition: "iter.test.exit_code == 0"  # в контексте доступен iter — результат прошлой итерации
    max_iterations: 3                      # обязательно
    step: { agent: { ... } }
```

Каждая итерация получает `iter` в `with`. Результат — результат последней итерации плюс `iterations`.

### 3.9 `assert`

```yaml
- id: gate
  assert:
    condition: "len(filter(steps.review.result.findings, .severity == 'blocker')) == 0"
    message: "Найдены блокирующие замечания"
```

Ложное условие → прогон завершается с кодом 2, `on_failure` **не** выполняется (это не ошибка), но `always`-уведомления, если будут добавлены в v2, — выполняются.

### 3.10 `switch`

Сахар над `when`, раскрывается при загрузке:

```yaml
- switch: steps.classify.result.risk
  cases:
    high:   { id: deep_review, agent: { ... } }
    medium: { id: deep_review, agent: { ... } }
    low:    { id: light_review, llm: { ... } }
  default: { id: skip_note, run: { argv: ["echo", "skipped"] } }
```

Если `steps.classify` — `llm`-шаг со схемой и поле — `enum`, валидация требует покрытия всех значений либо `default`.

## 4. Валидация сценария

Выполняется целиком до запуска. Ошибки — код выхода 3, все ошибки выводятся списком, а не первая.

Обязательные проверки:

1. Соответствие JSON Schema сценария (`baton schema` печатает её)
2. Уникальность `id`, отсутствие циклов в `needs`
3. Компиляция всех `when:`, `condition:` в expr-lang с типизацией контекста
4. Разбор всех шаблонов `{{ }}`, существование файлов промптов, схем, навыков
5. Все ссылки `steps.<id>` указывают на объявленные шаги, объявленные **раньше**
6. Ссылка на результат шага с `when:` из шага без `when:` и без `coalesce`/`default` — ошибка
7. Значения типа `secret` не могут попасть в `llm.prompt`, `llm.system`, `llm.with`, `agent.prompt`, `agent.with`, `run.argv`, `run.stdin`, `assert.message`, `on_failure.*.message`. Разрешены только в `http.auth`, `apis.*.auth`, `run.env.*.secret`, `notify.*.secret`
8. `pattern` обязателен у каждого аргумента команды и у каждого string-входа, используемого в argv
9. `max_iterations` обязателен у `until`
10. `switch` покрывает `enum` или имеет `default`
11. `on_error: fallback` требует `fallback`
12. Все паки из `apis` загружаются, пин версии и чексумма сходятся, пак проходит собственную валидацию (раздел 7.4.5); `interface:` — выбранный пак реализует все операции интерфейса; если `pack` задан шаблоном, проверяются все паки-кандидаты из `from`
13. `http.op` ссылается на существующую операцию; `args` покрывают обязательные `params`; `tools.apis` содержат только операции с `readonly: true`
14. Предупреждения (не ошибки): `http` с изменяющим методом или операцией без `readonly` без `dedupe_key`; `agent` с `tools.fetch` без аллоулиста; `foreach.max_parallel > 5`; операция пака без `description`, выданная агенту

## 5. Выражения, шаблоны, значения

### 5.1 Выражения

Движок: `github.com/expr-lang/expr`. Контекст:

```
inputs.<name>
steps.<id>.status          # success | failed | skipped
steps.<id>.result          # для llm, agent, http(parse), run(parse)
steps.<id>.stdout / stderr / exit_code
steps.<id>.items           # для foreach
run.id, run.name, run.started_at
iter.*                     # внутри until
```

Секреты в контексте выражений отсутствуют как класс. Ошибка вычисления в рантайме (nil-разыменование, которое не поймала валидация) — класс `config`.

### 5.2 Шаблоны

`text/template` с функциями: `render(path, data)`, `toJSON`, `fromJSON`, `coalesce`, `default`, `trunc(n)`, `indent(n)`, `join`, `dict`, `quote`, `md2html`, `md2text`. Последние две — для API, которые принимают HTML (Confluence storage format) или чистый текст; форматы конкретных вендоров (ADF и подобные) в бинарник не добавляются. Шаблонизатор получает значения через обёртку: при попытке вывести `values.Secret` возвращает ошибку рендера, а не строку.

### 5.3 Типы значений

`string`, `number`, `bool`, `list`, `map`, `secret`, `file`. `file` — ссылка на артефакт в каталоге прогона (`runs/<id>/steps/<step>/artifacts/...`), рендерится в путь. `secret` — непрозрачная обёртка; единственный способ получить содержимое — метод, доступный пакетам из белого списка (проверяется линтером или внутренним пакетом `internal/secrets/unwrap` с ограничением импорта через `go vet` правило или простую проверку в CI).

## 6. Секреты

```yaml
secrets:
  gitlab_ro: { from: env,  key: GITLAB_READ_TOKEN }
  gitlab_rw: { from: env,  key: GITLAB_WRITE_TOKEN }
  jira:      { from: file, path: /run/secrets/jira, trim: true }
  tg:        { from: env,  key: TELEGRAM_BOT_TOKEN }
```

Требования:

- Резолвинг всех секретов при старте, до первого шага; отсутствие любого — код 3
- Редактор: все значения секретов (и их base64/URL-encoded формы) заменяются на `***` в stdout/stderr шагов, логах, `run.json`, уведомлениях, аудит-логе инструментов
- Секрет провайдера модели (`ANTHROPIC_API_KEY` или OAuth-токен Claude Code) — единственный секрет, который попадает в окружение процесса агента. Документировать: использовать отдельный ключ с лимитом расходов
- Глобальный конфиг `~/.config/baton/config.yaml` и `./baton.yaml` могут объявлять секреты и каналы уведомлений; сценарий их переопределяет

## 7. Инструменты агента и шлюз

### 7.1 Подготовка workspace

Перед первым `agent`-шагом раннер:

- Определяет корень репозитория (или `--workspace` из CLI)
- Переписывает `.git/config`: убирает креды из URL всех remote, удаляет `extraheader` с авторизацией
- Не удаляет файлы; deny-списки применяются на уровне инструментов

### 7.2 Профили

| Профиль | fs.read | fs.write | git | exec | apis | fetch | mcp | state |
|---|---|---|---|---|---|---|---|---|
| `review` | да | нет | read | none | по списку | нет | нет | read |
| `fix` | да | workspace | read + commit | commands | по списку | нет | нет | read |
| `research` | да | нет | read | none | по списку | аллоулист | по списку | read-write |

Блок `tools:` на шаге переопределяет отдельные поля профиля.

### 7.3 Полная форма `tools`

```yaml
tools:
  fs:
    read: true
    write: none | workspace
    deny: [".git/**", ".env*", "**/*.pem", "**/id_*"]   # дефолт, дополняется
  git:
    read: true
    commit: false
  exec:
    mode: none | commands
    commands: [test, lint]         # подмножество из commands:, или все
  apis: [forge.get_change, forge.get_file, jira.get_issue]   # только readonly-операции паков
  fetch:
    allow: ["pkg.go.dev"]
    max_bytes: 300k
    max_calls: 10
  mcp: [context7]
  state: none | read | read-write
limits:
  max_tool_calls: 60
  max_result_bytes: 64k
```

### 7.4 API-паки

Принцип: бинарник знает протокол (HTTP, JSON, схемы авторизации, пагинация), но не сервисы. Всё, что знает имя сервиса, живёт в паке — YAML-файле вне бинарника. Тест для любого предложения «добавить X в бинарник»: понадобится ли X пятому, не связанному сервису? `md2html` — да; `md2adf` — нет, это Atlassian.

#### 7.4.1 Подключение в сценарии

```yaml
apis:
  forge:                                   # имя, под которым операции видны в сценарии
    interface: forge/v1                    # опционально: гарантирует набор операций и форму ответов
    pack: "{{ .inputs.forge }}"            # gitlab | github | gitea — имя пака
    from: github.com/org/baton-apis@v1.3.0 # источник паков; пин версии обязателен
    sha256: "…"                            # обязателен для источников не из локальной ФС
    config: { base_url: https://gitlab.company.com/api/v4 }
    auth: { secret: forge_ro }             # значение для схемы авторизации пака
    timeout: 20s
  jira:
    pack: jira
    from: ./apis/                          # локальный каталог, чексумма не нужна
    auth: { secret: jira }
  telegram:
    pack: telegram
    from: github.com/org/baton-apis@v1.3.0
    sha256: "…"
    auth: { secret: tg_bot }
```

`from` — локальный каталог или git-источник с тегом. Раннер кеширует загруженные паки в `~/.cache/baton/packs/<источник>@<версия>/` и сверяет чексумму при каждой загрузке. Пак без пина или с расхождением чексуммы — ошибка класса `config`. В паке не может быть секретов, только имена параметров авторизации.

Операции доступны как `<api>.<op>`: в `http`-шагах — любые, в инструментах агента — только с `readonly: true` и только перечисленные в `tools.apis`.

#### 7.4.2 Формат пака

```yaml
pack: gitlab
version: 1
description: GitLab REST API v4
config:
  base_url: { default: https://gitlab.com/api/v4 }

auth:
  kind: header                 # header | bearer | basic | query | path | exchange
  name: PRIVATE-TOKEN

rate_limit:
  remaining_header: RateLimit-Remaining
  retry_after_header: Retry-After

envelope:                      # опционально, конверт ответа
  unwrap: .
  error_when: '.message != null and .error != null'
  error_message: .message

pagination:                    # стратегия по умолчанию для операций с paginate: true
  style: link_header           # link_header | page | offset | cursor
  max_pages: 20

ops:
  get_change:
    get: /projects/{project}/merge_requests/{iid}/changes
    description: Изменения merge request
    readonly: true
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
      iid:     { pattern: '^\d+$' }
    transform: |
      { id: .iid, title, description, author: .author.username,
        base: .diff_refs.base_sha, head: .diff_refs.head_sha,
        files: [ .changes[] | { path: .new_path, diff, deleted: .deleted_file } ] }
    implements: forge/v1.get_change
    max_bytes: 500k

  post_comment:
    post: /projects/{project}/merge_requests/{iid}/notes
    description: Комментарий к merge request
    encode: json               # json | form | multipart(v2)
    params:
      project: { pattern: '^[\w./-]+$', encode: path }
      iid:     { pattern: '^\d+$' }
      body:    { max_len: 65000, in: body }
    transform: '{ id, url: .web_url }'
    implements: forge/v1.post_comment

  list_pipelines:
    get: /projects/{project}/pipelines
    readonly: true
    paginate: true
    params: { project: { pattern: '^[\w./-]+$', encode: path } }
    transform: '[ .[] | { id, status, ref, url: .web_url } ]'

  search_code:
    kind: graphql
    query: queries/search_code.graphql
    readonly: true
    params: { q: { max_len: 500 } }
    transform: '.data.search.nodes'
```

Поля операции:

- `get | post | put | patch | delete: <path>` или `kind: graphql` с `query` (файл или inline); путь содержит `{param}`-плейсхолдеры
- `readonly` — определяет, доступна ли операция агенту. Не выводится из HTTP-метода: у RPC-стиля (Outline) чтение идёт через `POST`
- `params.<name>`: `pattern` (обязателен для всего, что попадает в путь или query), `max_len`, `enum`, `in: path | query | body | form` (по умолчанию: плейсхолдер пути → `path`, `GET` → `query`, иначе `body`), `encode: path` для сегментов с `/`, `required` (по умолчанию true), `default`
- `params.<name>.name` — имя, под которым аргумент уходит на провод, если оно отличается от имени в сценарии; для аргумента тела имя с точками вкладывает значение: `name: fields.project.key` отправляет `{ "fields": { "project": { "key": … } } }`
- `encode` — кодирование тела: `json` (по умолчанию), `form`
- `paginate: true` — применить стратегию пака или свою в `pagination` операции; результат до трансформа — конкатенированный массив страниц
- `transform` — jq-выражение (gojq), применяется после конверта и пагинации; трансформ обязан быть чистым, без `input`/`env`/`$__loc__`
- `pick: [поля]` — сокращение для тривиального трансформа
- `max_bytes` — обрезка результата после трансформа с пометкой `truncated`
- `implements: <interface>/<version>.<op>` — заявка на реализацию операции интерфейса

#### 7.4.3 Схемы авторизации

| `kind` | Куда подставляется | Пример |
|---|---|---|
| `header` | заголовок `name` | GitLab `PRIVATE-TOKEN` |
| `bearer` | `Authorization: Bearer …` | GitHub, Outline, Confluence DC |
| `basic` | `Authorization: Basic base64(user:secret)`, `user` из `config` | Confluence Cloud, Jira Cloud |
| `query` | параметр запроса `name` | старые API |
| `path` | сегмент пути `{auth}` в `base_url` или пути операции | Telegram `https://api.telegram.org/bot{auth}/` |
| `exchange` | токен получается операцией пака и кешируется | MediaWiki CSRF, OAuth2 client credentials |

`exchange`:

```yaml
auth:
  kind: exchange
  op: get_csrf_token            # операция этого пака, вызывается с базовой авторизацией
  base: { kind: header, name: Authorization }   # чем авторизуется сам обмен
  extract: .query.tokens.csrftoken
  inject: { in: form, name: token }
  ttl: 10m
  session: cookies              # хранить cookie между вызовами в рамках прогона
  depends_on: login             # предшествующая exchange-цепочка (bot password → сессия)
```

Токены обмена живут только в памяти прогона, не пишутся в кеш и в `runs/`. Cookie jar — на прогон, без сохранения на диск. Глубина цепочки `depends_on` ограничена 3.

Значения авторизации любого вида проходят через редактор секретов, включая подстановку в путь: URL с `bot123:ABC/sendMessage` в логах должен выглядеть как `bot***/sendMessage`.

#### 7.4.4 Пагинация

| `style` | Как работает | Параметры |
|---|---|---|
| `link_header` | следующая страница из заголовка `Link: <…>; rel="next"` | — |
| `page` | параметр номера страницы, стоп при пустой странице или `size` меньше запрошенного | `param`, `size_param`, `size`, `in` |
| `offset` | смещение и лимит | `param`, `limit_param`, `size`, `in: query | body`, `total: <jq>` опционально |
| `cursor` | курсор следующей страницы из тела ответа | `next: <jq>`, `param`, `in`; если `next` — полный URL, используется как есть |

Общие: `max_pages` (по умолчанию 20), `items: <jq>` — где в теле страницы список (по умолчанию `.` для массивов, обязателен для объектов). Пагинация останавливается по `max_pages` с пометкой `truncated_pages: true` в результате, не падает.

#### 7.4.5 Интерфейсы

Интерфейс — контракт из нескольких операций с фиксированной формой аргументов и результата (JSON Schema). Реестр интерфейсов встроен в бинарник и версионируется; в v1 три:

`forge/v1`: `get_change(project, id)`, `list_files(project, id)`, `get_file(project, path, ref)`, `post_comment(project, id, body)`, `post_review(project, id, summary, comments[])`.

`tracker/v1`: `get_issue(key)`, `search(query, limit)`, `create_issue(project, title, body, type)`, `comment(key, body)`.

`notify/v1`: `send(target, text, format)`.

Правила:

- Пак, объявивший `implements` для операции, при загрузке проверяется: `params` покрывают аргументы интерфейса, а трансформ на тестовом ответе из пака (`examples/<op>.json`, обязателен для `implements`) даёт результат, валидный по схеме интерфейса. Иначе — `config`
- Проверка примера эмулирует пагинацию: для операции с `paginate: true` пример — сырое тело одной страницы, из него берётся `pagination.items`, и трансформу подаётся массив элементов этой страницы. Так пример проверяет и `pagination.items`, и трансформ
- Сценарий, объявивший `interface:`, при валидации проверяет, что выбранный пак реализует все операции интерфейса; иначе — `config` с перечислением недостающих
- Операции интерфейса — одиночные вызовы. Если API форжа не умеет батч (GitLab: одно обсуждение на комментарий), пак не реализует `post_review`, а сценарий использует `post_comment` в `foreach`. Валидация это покажет заранее
- Помимо операций интерфейса пак может содержать любые свои; они доступны как `forge.list_pipelines` только когда `pack` задан статически

Каналы уведомлений (раздел 12) — это `notify/v1`: `notify: telegram` в `on_failure` разворачивается в `http: { op: telegram.send, args: { target: <chat_id из конфига канала>, text: <message> } }`.

#### 7.4.6 Границы формата

Формат покрывает REST- и RPC-стиль поверх HTTP с JSON или form-encoded телами, токенные схемы авторизации и обмен токенов. Проверено на бумаге для GitLab, GitHub, Gitea, Jira (v2 для текста комментариев, без ADF), Confluence Cloud и DC (storage format через `md2html`), Outline, MediaWiki (form + exchange для CSRF и bot password), Telegram Bot API (`path`-авторизация, конверт `{ok, result}`).

Не покрывает и не будет: интерактивный OAuth, long polling и websockets, SOAP/XML, gRPC, составные операции с логикой. Для этого — MCP-сервер вендора через `tools.mcp` или `run`-шаг со скриптом.

#### 7.4.7 `baton apis import`

`baton apis import --openapi <spec> --ops <id,…> [--interface forge/v1] > pack.yaml` — генерирует заготовку: пути, методы, `params` с типами из спеки, пустые `transform` и `description`. Спека в рантайме не используется. Автор пака дописывает трансформы, пагинацию и `readonly`.

#### 7.4.8 Генерация инструментов

Из операций с `readonly: true`, перечисленных в `tools.apis` шага, раннер генерирует MCP-инструменты `<api>.<op>` с JSON-схемой аргументов из `params` и описанием из `description`. Операции без `description` — предупреждение при валидации: агент выбирает инструмент по описанию.

### 7.5 `commands`

```yaml
commands:
  test:
    argv: ["go", "test", "./..."]
    description: Запустить все тесты
    timeout: 5m
    readonly: true
  lint:
    argv: ["golangci-lint", "run", "--out-format", "json", "{{ .args.path }}"]
    description: Линтер по пути (по умолчанию весь модуль)
    args: { path: { pattern: '^[\w/.-]+$', default: "./..." } }
    parse: json
    readonly: true
    max_calls: 5
```

Исполнение — раннером (не процессом агента), `exec` без shell, `cwd` = workspace, env = минимальный аллоулист (`PATH`, `HOME`, `GOCACHE`, `GOPATH`, `GOFLAGS`) плюс объявленный. Аргумент, начинающийся с `/` или содержащий `..`, отклоняется до проверки `pattern`. Ответ инструмента: `{ exit_code, stdout, stderr, truncated, duration_ms, result? }`. Ненулевой exit code — данные, не ошибка. Суммарный лимит времени команд в шаге — половина `timeout` шага.

### 7.6 Прочие инструменты шлюза

- `fetch(url)`: GET по аллоулисту доменов, без cookies, без следования редиректам за пределы аллоулиста, извлечение текста из HTML, `max_bytes`
- `state.get(key)`, `state.set(key, value)`: файл `state/<scenario-name>.json` рядом с `runs/`, значения ≤ 64 КБ
- `submit_result(result)`: валидация по `result`-схеме шага; при ошибке возвращает агенту текст ошибки и не завершает шаг; при успехе — шаг помечается завершённым, процесс агента получает сигнал на завершение
- MCP сторонние: раннер запускает процесс сервера со своим env (секреты из `secrets`), проксирует его инструменты через шлюз; инструменты, чья схема содержит параметр с именем/форматом `url`/`uri`/`headers`, помечаются `unsafe` и доступны только при `allow_unsafe: true` на шаге

### 7.7 Шлюз

MCP-сервер (streamable HTTP) на `127.0.0.1:<эфемерный порт>` с одноразовым bearer-токеном на прогон. Один экземпляр на прогон, набор инструментов — на шаг (агент получает только инструменты своего шага). Каждый вызов пишется в `runs/<id>/steps/<step>/tool-calls.jsonl`: время, имя, аргументы (после редактора секретов), размер ответа, длительность, статус. Лимиты `max_tool_calls`, `max_calls` проверяются здесь.

## 8. Движки

### 8.1 Интерфейс

```go
type Engine interface {
    Run(ctx context.Context, req AgentRequest) (AgentResult, error)
}
type AgentRequest struct {
    Prompt, System string
    Model          string
    Workspace      string
    Skills         []string      // пути к каталогам с SKILL.md
    Gateway        GatewayInfo   // URL + токен
    BuiltinTools   ToolPolicy    // что разрешить из встроенных
    MaxTurns       int
    BudgetUSD      float64
    Env            map[string]string
}
type AgentResult struct {
    Submitted   bool            // был ли вызван submit_result
    Result      json.RawMessage
    Usage       Usage           // токены, стоимость
    Turns       int
    Transcript  string          // путь к файлу
}
```

### 8.2 `claude-code`

- Запуск `claude -p` в неинтерактивном режиме, `--output-format json` для получения usage и стоимости, `--max-turns`
- MCP-конфиг шлюза передаётся через временный файл `--mcp-config`
- Встроенные инструменты: разрешать `Read`, `Glob`, `Grep` всегда; `Edit`, `Write`, `MultiEdit` — при `fs.write: workspace`; **запрещать** `Bash`, `WebFetch`, `WebSearch`, `Task` всегда. Механизм: `--allowedTools` / `--disallowedTools` плюс `--permission-mode` без интерактивных запросов. Точные флаги сверять с `claude --help` установленной версии; адаптер проверяет версию при старте и отказывается работать с неизвестной мажорной
- Deny-списки путей: временный файл настроек с PreToolUse-хуком, который отклоняет `Read`/`Edit`/`Write` по путям из `fs.deny` и вне workspace
- Навыки: копирование или симлинк каталогов из `skills` в `<workspace>/.claude/skills/` на время шага, с удалением после
- Окружение процесса: пустое, плюс `PATH`, `HOME` (временный каталог), ключ провайдера, объявленный `env`
- Завершение: по `submit_result` — SIGTERM, через 10 с SIGKILL; по таймауту — то же; по отмене контекста — то же
- Стоимость берётся из JSON-вывода; при отсутствии — оценка по токенам и таблице цен из конфига

### 8.3 Провайдеры для `llm:`

Шаг `llm` не привязан к одному API. Внутри — единый интерфейс, под ним три реализации:

```go
type Provider interface {
    Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
    Capabilities() Caps   // structured_output, tools, vision, max_context
}
```

`CompletionRequest` — нормализованные сообщения (system, user, assistant, tool_call, tool_result), список инструментов в JSON Schema, желаемая схема результата, `max_tokens`, `temperature`. `CompletionResponse` — текст или tool-calls, usage (input/output/cached tokens), стоимость если провайдер её вернул, `finish_reason`.

| Провайдер | Транспорт | Structured output | Стоимость |
|---|---|---|---|
| `anthropic` | Messages API, официальный Go SDK | инструмент-обёртка с `tool_choice` на неё; нативный JSON-режим SDK, если доступен | таблица `pricing` |
| `openai` | Chat Completions (или Responses API, если SDK стабилен), официальный Go SDK | `response_format: json_schema` со `strict: true`; при отказе модели — инструмент-обёртка | таблица `pricing` |
| `openrouter` | OpenAI-совместимый Chat Completions, `base_url: https://openrouter.ai/api/v1` | `response_format: json_schema` там, где нижележащая модель поддерживает (проверка по `/models`); иначе инструмент-обёртка; иначе промпт + валидация | из поля `usage.cost` ответа (запрашивать `usage: { include: true }`) |
| `openai_compatible` | тот же клиент, что `openrouter`, произвольный `base_url` | по флагу `capabilities` в конфиге | таблица `pricing` |

`openrouter` — это `openai_compatible` с преднастроенным `base_url`, обязательными заголовками `HTTP-Referer`/`X-Title` и парсером стоимости. Реализация одна, различия — в конфиге и в трёх точках расширения. `openai_compatible` бесплатно даёт Ollama, vLLM, LM Studio и корпоративные прокси.

Стратегия structured output, общая для всех: (1) нативный JSON Schema режим, если провайдер и модель его заявляют; (2) один инструмент `submit` со схемой шага и принудительным его вызовом; (3) инструкция в промпте плюс валидация ответа. Уровень фиксируется в `output.json` как `structured_mode`. Понижение уровня — автоматическое по `Capabilities()`, ручное переопределение — `structured_mode:` на шаге.

Tool-loop для `llm.tools` ведёт раннер, одинаково для всех провайдеров: инструменты те же, что в шлюзе, вызываются напрямую без HTTP; лимиты `max_tool_calls` те же. Различия форматов tool-calls между Anthropic и OpenAI скрыты за нормализацией сообщений.

Учёт стоимости: если провайдер вернул стоимость — она; иначе `usage × pricing[model]`; если модели нет в `pricing` — предупреждение при валидации и `cost_usd: null` в отчёте, бюджет по долларам для такого шага не проверяется, проверяется только по токенам (`budget_tokens`).

Конфиг провайдеров (глобальный или в сценарии, слияние по имени):

```yaml
providers:
  anthropic:
    kind: anthropic
    api_key: { from: env, key: ANTHROPIC_API_KEY }
  openai:
    kind: openai
    api_key: { from: env, key: OPENAI_API_KEY }
    base_url: https://api.openai.com/v1          # опционально, для Azure/прокси
  openrouter:
    kind: openrouter
    api_key: { from: env, key: OPENROUTER_API_KEY }
    app_name: baton
  local:
    kind: openai_compatible
    base_url: http://ollama:11434/v1
    api_key: { from: env, key: OLLAMA_KEY, optional: true }
    capabilities: { structured_output: false, tools: true }
```

Ключи провайдеров — обычные `secret`, живут в процессе раннера. В процесс агента (`claude-code`) попадает только ключ его собственного провайдера. Таймауты и retry на уровне HTTP — общие: 3 попытки на 429/5xx с учётом `Retry-After`, это часть класса `transient`.

### 8.4 `fake`

Обязателен для тестов. Читает сценарий поведения из файла: последовательность вызовов инструментов с аргументами и финальный `submit_result`. Позволяет прогнать весь исполнитель, шлюз, retry и `on_failure` без затрат на модель. Все интеграционные тесты пишутся на нём.

## 9. Ошибки, повторы, коды выхода

### 9.1 Классы

| Класс | Источник | Retry по умолчанию |
|---|---|---|
| `transient` | сетевые ошибки, HTTP 408/429/5xx, таймауты провайдера | да, `attempts: 2`, backoff экспоненциальный от 5 с |
| `schema` | результат не прошёл JSON Schema, `submit_result` не вызван | да, `attempts: 1`, с текстом ошибки в контексте |
| `command` | `run` с недопустимым exit code, `http` с неожиданным статусом 4xx | нет |
| `timeout` | шаг превысил `timeout` | нет |
| `budget` | превышен `budget_usd`, `max_turns`, `max_tool_calls`, бюджет прогона | **никогда** |
| `policy` | `publish`-валидация, вызов `unsafe`-инструмента, аргумент вне `pattern` | никогда, событие в аудит |
| `config` | ошибка валидации, ошибка вычисления выражения в рантайме | никогда |

### 9.2 Семантика `on_error`

- `fail`: шаг `failed`, прогон останавливается, параллельные элементы `foreach` отменяются, выполняется `on_failure`
- `continue`: шаг `failed`, `result = null`, прогон продолжается; последующие шаги видят `steps.<id>.status == "failed"`
- `fallback`: выполняется тело `fallback` как шаг с тем же `id`; статус `success` с флагом `fallback_used: true`; если упал и fallback — как `fail`

### 9.3 `on_failure`

- Выполняется при статусе прогона `failed` любого класса, включая `budget` и рантайм-`config`
- Не выполняется при коде 2 (`assert`)
- Шаги без retry, с собственным таймаутом 60 с, вне бюджета прогона
- Ошибки внутри `on_failure` логируются, не порождают повторного `on_failure`
- Контекст: `run.failed_step`, `run.error.class`, `run.error.message`, `run.error.stderr_tail` (2 КБ), `run.duration`, `run.cost_usd`, `run.dir`
- Форма `notify: <канал>` с `message:` — сахар над `http` с конфигом канала из глобального конфига

### 9.4 Побочные эффекты и retry

Шаг с побочным эффектом — любой `http` с методом кроме `GET/HEAD`, `run` без `readonly: true`, `agent` с `fs.write`. Такие шаги не ретраятся автоматически, если у них нет `dedupe_key`. При наличии `dedupe_key` раннер перед выполнением проверяет `runs/<id>/effects.json`; если ключ уже помечен выполненным — шаг пропускается как выполненный. Ключ помечается сразу после успешного ответа.

### 9.5 Коды выхода

| Код | Значение |
|---|---|
| 0 | прогон успешен |
| 1 | ошибка выполнения (`transient`, `schema`, `command`, `timeout`, `policy`) |
| 2 | сработал `assert` |
| 3 | ошибка конфигурации / валидации / секретов |
| 4 | исчерпан бюджет |
| 130 | прерван сигналом |

### 9.6 Отмена

Единый `context.Context` на прогон. SIGINT/SIGTERM → отмена, агентские процессы получают SIGTERM, состояние записывается, код 130. Падение элемента `foreach` при `on_item_error: fail` → отмена остальных элементов.

## 10. Каталог прогона, кеш, resume

### 10.1 Расположение

`runs/` — рядом со сценарием по умолчанию, переопределяется `--runs-dir` или `BATON_RUNS_DIR`. `run-id` — `YYYYMMDD-HHMMSS-<4 hex>`, переопределяется `--run-id`.

```
runs/<id>/
  run.json              статус, входы (без секретов), времена, стоимость, failed_step
  events.jsonl          все события прогона
  effects.json          выполненные dedupe_key
  cost.json             по шагам: токены, usd, длительность
  steps/<step-id>/
    input.json          отрендеренные входы шага (без секретов)
    output.json         status, result, exit_code, usage
    stdout.log, stderr.log
    tool-calls.jsonl    для agent/llm с инструментами
    transcript.jsonl    для agent, если движок отдаёт
    artifacts/          файлы, произведённые шагом; diff.patch для fix
  steps/<foreach-id>/<n>/...   элементы foreach
```

### 10.2 Состояние

`run.json` обновляется после каждого шага атомарной записью (temp + rename). Формат — JSON с версией схемы.

### 10.3 Кеш

Ключ: `sha256(нормализованное определение шага без id и when + отрендеренные входы + хеши файлов промптов/схем/навыков + контрольная сумма пака, который вызывает http-шаг + engine + model + версия baton)`. Кеш — каталог `cache/` рядом с `runs/`, содержимое — `output.json` и артефакты.

По умолчанию `cache: true` для `llm` и `run` с `readonly: true`; `false` для `http` с изменяющими методами и `agent` с `fs.write`. `--no-cache` отключает чтение из кеша, не запись.

### 10.4 Resume

`baton resume <id>`: читает `run.json`, шаги со статусом `success` пропускает (берёт их output), начинает с упавшего. Секреты резолвятся заново. Новый прогон получает тот же `id` с суффиксом `-r1`, `-r2`, ссылка на исходный в `run.json`.

## 11. CLI

```
baton run <scenario.yaml> [-i key=val]... [--input-file f.json] [--run-id ID]
          [--runs-dir DIR] [--workspace DIR] [--no-cache] [--dry-run] [--json] [-v]
baton validate <scenario.yaml>            # только валидация, код 0/3
baton resume <run-id> [--runs-dir DIR]
baton runs list [--runs-dir DIR] [-n 20]
baton runs show <run-id>                  # сводка, стоимость, статусы шагов
baton runs logs <run-id> [--step ID]      # stdout/stderr шага
baton tools <scenario.yaml> --step ID     # что увидит агент: имена, схемы, описания
baton schema                              # JSON Schema сценария на stdout
baton apis import --openapi f --ops a,b   # заготовка пака из OpenAPI (раздел 7.4.7)
baton apis validate <pack.yaml>           # проверка пака и его examples/ против интерфейсов
baton apis call <scenario> <api>.<op> -a k=v   # вызвать операцию вручную, для отладки паков
```

`--dry-run`: валидация, резолвинг секретов (проверка наличия), рендер входов первого шага, вывод плана без выполнения. `--json`: события на stdout в JSONL вместо человекочитаемого лога; человекочитаемый — на stderr.

Человекочитаемый лог: одна строка на событие, префикс `[step-id]`, длительность и стоимость по завершении шага, итог по прогону.

## 12. Глобальный конфиг

`~/.config/baton/config.yaml`, затем `./baton.yaml` (слияние, локальный побеждает):

```yaml
apis:                            # глобальные паки, доступны всем сценариям
  telegram:
    pack: telegram
    from: github.com/org/baton-apis@v1.3.0
    sha256: "…"
    auth: { secret: tg_bot }
secrets:
  tg_bot: { from: env, key: TELEGRAM_BOT_TOKEN }
notify:                          # каналы = операция notify/v1 + адресат
  telegram:
    api: telegram                # пак с интерфейсом notify/v1
    target: "-100123"
  ops:
    kind: webhook                # встроенный: POST JSON на URL
    url: { from: env, key: SLACK_OPS_WEBHOOK }
providers:                       # раздел 8.3
  anthropic:  { kind: anthropic,  api_key: { from: env, key: ANTHROPIC_API_KEY } }
  openai:     { kind: openai,     api_key: { from: env, key: OPENAI_API_KEY } }
  openrouter: { kind: openrouter, api_key: { from: env, key: OPENROUTER_API_KEY } }
defaults:
  engine: claude-code
  model: anthropic/claude-sonnet-4-6
  budget_usd: 3
on_failure:                      # применяется ко всем сценариям без своего on_failure
  - notify: telegram
    message: "{{ .run.name }} упал: {{ .run.error.class }} на {{ .run.failed_step }}"
pricing:                         # для оценки стоимости, если провайдер не отдал
  anthropic/claude-sonnet-4-6: { input_per_mtok: 3,    output_per_mtok: 15 }
  anthropic/claude-haiku-4-5:  { input_per_mtok: 0.8,  output_per_mtok: 4 }
  openai/gpt-4.1-mini:         { input_per_mtok: 0.4,  output_per_mtok: 1.6 }
mcp_servers:
  context7: { command: ["npx", "-y", "@upstash/context7-mcp"], env: {} }
```

## 13. Требования безопасности (чеклист приёмки)

- [x] Ни один секрет не появляется в `runs/`, логах, уведомлениях, аудит-логе (тест: секрет со случайным значением, grep по всему каталогу после прогона)
- [x] Шаблон с `{{ .secrets.x }}` в промпте не проходит валидацию
- [x] Процесс агента не имеет в окружении ничего, кроме аллоулиста (тест: fake-движок пишет `os.Environ()` в файл)
- [x] Агент не может вызвать инструмент, не объявленный для шага (тест через fake: вызов чужого инструмента → `policy`)
- [x] `commands` не интерпретируют метасимволы shell (тест: аргумент `; echo pwned` отклоняется `pattern`, аргумент `$(id)` при разрешающем pattern передаётся буквально)
- [x] `fetch` на домен вне аллоулиста → `policy`; редирект на домен вне аллоулиста → `policy`
- [x] Путь `../x` и `/etc/passwd` в аргументах команд и `fs` → `policy`
- [x] `.git/config` после подготовки workspace не содержит токенов
- [x] `http` с `POST` без `dedupe_key` не ретраится при `transient`
- [x] `budget` не ретраится ни при каких настройках `retry`
- [x] Пак с расхождением `sha256` или без пина версии не загружается
- [x] Токен `path`-авторизации отсутствует в URL внутри логов, `events.jsonl`, `tool-calls.jsonl` и сообщениях об ошибках HTTP
- [x] Токены `exchange` не попадают в кеш шагов и в `runs/`
- [x] Агент не может вызвать операцию пака без `readonly: true`, даже если она перечислена в `tools.apis` (валидация) и даже при прямом обращении к шлюзу (рантайм → `policy`)
- [x] jq-трансформ с `env`, `input`, `$__loc__` отклоняется при загрузке пака

## 14. Тестирование

- Unit: `expr` контекст и типизация, шаблоны и запрет `secret`, редактор секретов, валидатор сценария (golden-тесты: каталог YAML-файлов и ожидаемых списков ошибок), кеш-ключ (стабильность), argv-подстановка
- Интеграция на `fake`-движке: полный прогон ревью-сценария; retry по `schema`; `fallback`; `on_failure` при `budget`; `foreach` с `continue` и `min_success`; `resume` после падения; `until` с `max_iterations`; `dedupe_key`
- Провайдеры: контрактный тест на `httptest`-сервере для каждого `kind` — нормализация сообщений и tool-calls в обе стороны, три уровня structured output, разбор usage и стоимости (включая `usage.cost` OpenRouter), обработка 429 с `Retry-After`, переключение по `fallback_models`
- Паки (`internal/httpx`, `internal/packs`): `httptest`-сервер на каждую схему авторизации, включая `exchange` с цепочкой и cookie; каждую стратегию пагинации, включая остановку по `max_pages`; конверт с ошибкой; `form` и `graphql`; редактирование токена в URL
- Репозиторий `baton-apis`: контрактные тесты — `examples/<op>.json` с записанными ответами каждого API прогоняются через трансформы и проверяются по схемам интерфейсов в CI репозитория; `baton apis validate` на каждый пак
- Один сквозной тест с реальным `claude-code` за флагом `BATON_E2E=1` и по одному реальному запросу на провайдера за флагом `BATON_E2E_PROVIDERS=1`, запускаются вручную перед релизом
- Линтер, `go vet`, `-race` в CI проекта

## 15. Этапы и критерии готовности

### M1 — ядро (2 недели)

Парсинг, валидация, типы, `secret`, выражения, шаблоны, `run`, `assert`, каталог прогона, коды выхода, CLI `run`/`validate`/`schema`. `internal/httpx`: схемы авторизации `header`/`bearer`/`basic`/`query`/`path`, пагинация `link_header`/`page`, конверт, gojq, редактирование токенов в URL. `internal/packs`: загрузка из локального каталога, валидация, `http.op`. Первый пак — `gitlab` с `get_change` и `post_comment`, локально.
Готово: сценарий из `run` → `http: { op: gitlab.post_comment }` → `assert` работает из GitLab CI; секреты не утекают в `runs/`.

### M2 — LLM и отчёты (2 недели)

Интерфейс `Provider`, реализации `anthropic`, `openai`, `openai_compatible` (+ `openrouter` как его конфигурация), нормализация сообщений и tool-calls, три уровня structured output, `fallback_models`, учёт стоимости; `llm`-шаг, схемы, `schema`-retry, `foreach`, кеш, `resume`, `on_failure`, каналы уведомлений, глобальный конфиг, `runs list/show/logs`.
Готово: еженедельный отчёт по 5 репозиториям из cron с уведомлением в Telegram; один и тот же сценарий проходит на `anthropic/…`, `openai/…` и `openrouter/…` без правок кроме строки `model`; повторный запуск не тратит токены.

Порядок внутри этапа: сначала `openai_compatible` (самый простой транспорт и покрывает OpenRouter), потом `anthropic`, потом `openai` со `strict` JSON Schema.

### M3 — агент (2 недели)

`fake`-движок, `claude-code`-адаптер, шлюз MCP, `submit_result`, `commands`, инструменты из readonly-операций паков, профили, подготовка workspace, аудит-лог, бюджеты `agent`.
Готово: сценарий код-ревью с профилем `review` работает на реальном MR; агент не имеет доступа к токенам; `baton tools` показывает ровно объявленные инструменты.

### M3.5 — паки и интерфейсы (1.5 недели)

Загрузка паков из git-источника с пином и чексуммой, кеш паков, реестр интерфейсов `forge/v1`, `tracker/v1`, `notify/v1` с проверкой `implements` по `examples/`, `interface:` в сценарии с выбором пака по входу, `exchange`-авторизация с цепочкой и cookie, пагинация `offset`/`cursor`, `form`, `graphql`, `baton apis import/validate/call`, `md2html`/`md2text`. Репозиторий `baton-apis`: `gitlab`, `github`, `gitea` под `forge/v1`; `jira` под `tracker/v1`; `telegram`, `slack` под `notify/v1`; контрактные тесты в его CI. Канал `telegram` переезжает на `notify/v1`.
Готово: один сценарий ревью проходит на GitLab, GitHub и Gitea со сменой только `-i forge=`; `baton apis validate` зелёный на всех паках; Telegram-уведомления идут через пак.

### M4 — устойчивость (1 неделя)

`until`, `fallback`, `dedupe_key`, `switch`, статическая проверка ссылок на пропущенные шаги, `fetch`, `state`, сторонние MCP с `unsafe`-маркировкой, отмена по сигналам.
Готово: профиль `fix` правит код и гоняет тесты в цикле; чеклист раздела 13 пройден полностью.

### M5 — полировка (1 неделя)

Документация: README, справочник схемы (генерируется из JSON Schema), три примерных сценария (ревью, отчёт, триаж). Релизная сборка через goreleaser, бинарники для linux/amd64 и linux/arm64.

## 16. Открытые вопросы

Пункты с пометкой «Решено» закрыты; они оставлены здесь вместе с принятым решением, чтобы не терять контекст.

1. **Имя. Решено:** имя остаётся `baton`, хотя слово в нише занято несколькими проектами. Бинарник, каталог конфига `~/.config/baton` и путь модуля `github.com/foxzi/baton` закреплены, вариант `segue` отброшен
2. **Deny-списки для встроенных инструментов Claude Code. Решено:** хук `PreToolUse` не используется. Встроенные `Read`/`Glob`/`Grep`/`Write` остаются за CLI (шлюз движку `claude-code` инструменты `fs.*` не отдаёт), а политика шага выражается запретами: в `settings.json` пишется `permissions.deny` с парой правил `Read(<pattern>)`/`Edit(<pattern>)` на каждый элемент deny-списка, а имена инструментов (`Bash`, `BashOutput`, `KillShell`, `WebFetch`, `WebSearch`, `Task`, плюс `Edit`/`Write`/`NotebookEdit`, когда `fs.write` не `workspace`) уходят и в настройки, и в `--disallowedTools`. Реализация: `internal/agent/claudecode/setup.go`, `internal/engine/agent.go`
3. **Structured output через OpenRouter. Решено:** `/models` не опрашивается. `openrouter` объявляет `structured_output: true` и `tools: true`, произвольный `openai_compatible` — `structured_output: false`, пока конфиг не скажет иное через `capabilities`. Дальше режим выбирает `provider.ResolveMode` по объявленным возможностям, а неточную метаинформацию перебивает явный `structured_mode: tool|prompt` в шаге `llm`. Автоматического понижения режима после отказа модели нет: отказ приходит как ошибка класса `schema`. Реализация: `internal/provider/compat.go`, `internal/provider/structured.go`
3a. **Responses API у OpenAI. Решено:** в v1 — Chat Completions (`internal/provider/openai.go`), он стабилен. Responses API новее и лучше для tools; пересмотреть, если Go SDK сделает его основным
4. **Состояние между запусками. Решено:** Baton не требует сохранения `state/` между запусками. При необходимости сценарий сохраняет результаты работы в файлах. Эфемерная файловая система CI — штатная среда запуска, а не ограничение архитектуры; обязательное внешнее хранилище состояния не требуется.
5. **Формат `commands.from`. Решено:** `commands.from` в v1 не реализуется. Команды и их аргументы объявляются явно внутри `commands:` самого сценария (раздел 7.5, `Commands map[string]Command` в `internal/scenario/types.go`); внешнего набора команд, отдельного файла на стек и автоподключения по наличию `go.mod`/`package.json` нет. Если один и тот же набор команд нужен в нескольких сценариях, блок копируется или генерируется из общего источника вне Baton.
6. **Объём интерфейсов. Решено (с триггером пересмотра):** `forge/v1` из пяти операций может оказаться мал (нужны ли `get_pipeline_status`, `list_changes`?). Правило: интерфейс расширяется только когда операция понадобилась двум сценариям на двух форжах; до этого — прямые операции пака. Пересмотреть после первого месяца эксплуатации
7. **jq в паках как логика вне бинарника. Решено:** компромисс принят осознанно: трансформы могут ошибаться и хуже тестируются, чем Go. Компенсация — обязательные `examples/` для `implements` и контрактные тесты в `baton-apis`. Если трансформы начнут расти в сторону условной логики — это сигнал вынести операцию в интерфейс с несколькими простыми паками, а не усложнять jq
