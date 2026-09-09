# Быстрый старт

Десять минут пути от пустого каталога до сценария, который вызывает модель,
проверяет результат по схеме и сообщает, во что обошёлся прогон.

Каждая команда и каждый листинг ниже — реальный вывод версии из этого
репозитория, а не иллюстрация.

## 1. Сборка

Baton — единственный бинарник без зависимостей времени выполнения. Тегированного
релиза пока нет, поэтому собирайте из исходников; нужен Go 1.25.7 или новее.

```sh
git clone https://github.com/foxzi/baton.git
cd baton
make build          # создаёт ./baton с метаданными версии
./baton version
```

При желании поместите бинарник куда-нибудь в свой `PATH` — дальше на этой
странице команда предполагается просто как `baton`.

## 2. Запуск простейшего сценария

Сценарий — это YAML-файл. `examples/hello.yaml` — весь формат в семнадцати
строках: одна команда и одна проверка.

```yaml
version: 1
name: hello
description: Minimal example scenario that greets and checks the exit code.

inputs:
  who:
    type: string
    default: world
    pattern: "^[A-Za-z]+$"

steps:
  - id: greet
    run:
      argv: ["echo", "hello", "{{ .inputs.who }}"]
      readonly: true
      parse: text

  - id: check_exit
    needs: [greet]
    assert:
      condition: "steps.greet.exit_code == 0"
```

Здесь стоит назвать три вещи, потому что именно они формируют облик любого
сценария побольше:

- **Входы типизированы и ограничены.** `pattern` — не необязательное украшение:
  у string-входа, который попадает в `argv`, он обязателен, иначе валидация не
  пройдёт. Никакого shell в картине нет — `argv` передаётся процессу напрямую, —
  поэтому границей служит паттерн, а не экранирование.
- **Шаги — это DAG, а не список.** `needs:` объявляет зависимость; шаги без
  зависимости друг от друга могут выполняться параллельно.
- **Шаг может намеренно провалить прогон.** `assert` существует для того,
  чтобы сценарий мог заблокировать merge в CI отдельным кодом выхода.

Проверьте его, затем запустите:

```console
$ baton validate examples/hello.yaml
examples/hello.yaml: valid

$ baton run examples/hello.yaml -i who=Baton
[greet] step_started
[greet] step_finished: success (3.983783ms)
[check_exit] step_started
[check_exit] step_finished: success (264.371µs)
run 20260909-003107-0bfb: success in 6ms
run directory: examples/runs/20260909-003107-0bfb
```

Ограничение на вход проверяется прежде, чем что-либо выполнится:

```console
$ baton run examples/hello.yaml -i 'who=Baton 1'
baton: input "who": value does not match pattern "^[A-Za-z]+$"
```

`--dry-run` резолвит входы и секреты и печатает план, не выполняя ни одного
шага, — самый дешёвый способ увидеть, что сделал бы сценарий:

```console
$ baton run examples/hello.yaml -i who=Baton --dry-run
scenario: hello
inputs:
  who = Baton
plan:
  1. greet (run)
  2. check_exit (assert)
```

## 3. Что остаётся после прогона

Каждый прогон — это каталог. По умолчанию это `runs/` рядом со сценарием;
`--runs-dir` кладёт его в другое место.

```
runs/20260909-003107-0bfb/
├── run.json             # входы, статус, тайминги, разрешённый план
├── events.jsonl         # структурированный лог событий, секреты отредактированы
├── cost.json            # токены и доллары, по шагам и суммарно
└── steps/
    └── greet/
        ├── output.json  # результат шага, который интерполируют другие шаги
        └── stdout.log
```

`output.json` — контракт между шагами. Для шага `greet` выше:

```json
{
  "exit_code": 0,
  "result": "hello Baton\n",
  "status": "success"
}
```

Три команды читают прогоны обратно, так что открывать файлы руками почти не
приходится:

```console
$ baton runs list
20260909-003107-0bfb     success  6ms        hello

$ baton runs show 20260909-003107-0bfb
run:      20260909-003107-0bfb
scenario: hello
status:   success
started:  2026-09-09T00:31:07+03:00
duration: 6ms
steps:
  greet                success  4ms
  check_exit           success  0s

$ baton runs logs 20260909-003107-0bfb
=== greet stdout.log
hello world
```

## 4. Падение — это отдельный код выхода

Замените assert на `steps.greet.exit_code == 42`, и прогон упадёт именно там,
где проверка, а не где-то ниже по цепочке:

```console
$ baton run examples/hello.yaml
[check_exit] step_started
[check_exit] step_failed: assert: assert failed: steps.greet.exit_code == 42
run 20260909-003207-93f9: failed in 6ms
failed step check_exit: assert: assert failed: steps.greet.exit_code == 42
run directory: examples/runs/20260909-003207-93f9
$ echo $?
2
```

CI различает случаи без разбора логов:

| Код | Значение |
|---|---|
| 0 | успех |
| 1 | шаг упал |
| 2 | сработал `assert` |
| 3 | плохая конфигурация — незаданный секрет, вход, не прошедший паттерн, невалидный сценарий |
| 4 | закончился бюджет |
| 130 | прервано |

`baton resume <run-id>` продолжает упавший прогон с того шага, на котором он
упал, переиспользуя результаты уже успешных шагов.

## 5. Подключение модели

Сценарий называет модель как `<provider>/<model>`; где живёт этот провайдер и
каким ключом он пользуется — дело глобальной конфигурации, а не сценария.
Baton читает `~/.config/baton/config.yaml`, затем `./baton.yaml`, более
поздние значения побеждают; `--config FILE` и `BATON_CONFIG` переопределяют
поиск.

Создайте `baton.yaml` рядом со сценарием:

```yaml
providers:
  openrouter:
    kind: openrouter
    api_key: { from: env, key: OPENROUTER_API_KEY }
    app_name: baton

notify:
  report:
    kind: stdout        # второй встроенный вид — webhook

pricing:
  # OpenRouter сообщает реальную стоимость в ответе; эта таблица —
  # запасной вариант для провайдера, который этого не делает.
  openrouter/openai/gpt-4.1-nano:
    input_per_mtok: 0.1
    output_per_mtok: 0.4
```

Ключи никогда не попадают ни в сценарий, ни в каталог прогона, ни в кеш, а
попытка интерполировать ключ в промпт — это ошибка валидации, а не утечка.
Они читаются из окружения или из файла в момент, когда они нужны шагу.
Провайдеры Anthropic и OpenAI настраиваются так же, через `kind: anthropic` /
`kind: openai`; `kind: openai_compatible` с `base_url` покрывает Ollama, vLLM
и корпоративные прокси.

У канала есть третья форма помимо двух встроенных видов: он называет запись
`apis`, пак которой реализует `notify/v1`, и адрес получателя. В репозитории
лежит пак `telegram` (`apis/telegram/`), так что уведомление в чат — это
запись пака и указывающий на неё канал:

```yaml
apis:
  telegram:
    pack: telegram
    from: ./apis/
    auth: { secret: tg_bot }

secrets:
  tg_bot: { from: env, key: TELEGRAM_BOT_TOKEN }

notify:
  chat:
    api: telegram
    target: "-1001234567890"   # id чата или @имя_канала
```

Запись авторизуется секретом конфигурации, который сценарий не может назвать
и никогда не видит; `notify: chat` в шаге вызывает операцию `send` пака с
адресом канала и отрендеренным сообщением.

`examples/llm-smoke.yaml` — самый короткий сценарий, который прогоняет
провайдера от начала до конца: однословный ответ, JSON-ответ, провалидированный
по схеме, и уведомление. Обратите внимание: `schema:` **обязателен** на шаге
`llm` — результат модели, по которому могут ветвиться другие шаги, обязан быть
провалидированной структурой, а не свободным текстом.

```console
$ export OPENROUTER_API_KEY=...
$ baton run examples/llm-smoke.yaml
[ping] step_started
[ping] step_finished: success (1.475260672s)
[extract] step_started
[extract] step_finished: success (1.477451602s)
[report] step_started
llm-smoke: Paris
language: en
sentiment: neutral
keywords: deploy pipeline, rollback, waking up at night

[report] step_finished: success (1.511917ms)
run 20260909-003146-f99c: success in 2.958s
run directory: examples/runs/20260909-003146-f99c
```

`steps/extract/output.json` фиксирует, что провайдер сделал на самом деле, —
это то, что проверяют, когда модель или ключ ведут себя не так:

```json
{
  "cost_usd": 0.0000256,
  "mode": "native",
  "model_used": "openrouter/openai/gpt-4.1-nano",
  "result": {
    "keywords": ["deploy pipeline", "rollback", "waking up at night"],
    "language": "en",
    "sentiment": "neutral"
  },
  "status": "success",
  "usage": { "input_tokens": 156, "output_tokens": 25 }
}
```

`mode` — это стратегия структурированного вывода, которую принял провайдер:
нативная JSON-схема, принудительный вызов инструмента или промпт-инструкция с
валидацией. `model_used` отличается от запрошенной модели, когда в дело
вступила запись из `fallback_models`. Деньги учитываются по шагам:

```console
$ baton runs show 20260909-003146-f99c
...
cost:     $0.0000
cost by step:
  ping                 openrouter/openai/gpt-4.1-nano in=60 out=6 $0.0000
  extract              openrouter/openai/gpt-4.1-nano in=156 out=25 $0.0000
```

точные цифры — в `cost.json` (`total_usd: 0.000034` для прогона выше). Секция
`budget:` сценария ограничивает доллары, токены и время; при исчерпании
прогон останавливается с кодом выхода 4, но `on_failure` всё равно
выполняется.

## 6. Куда дальше

Остальные примеры в `examples/` — это рабочие сценарии, которые прогоняет
тестовый набор, примерно в порядке усложнения:

| Сценарий | Что добавляет |
|---|---|
| `hello.yaml` | команда и assert |
| `llm-smoke.yaml` | два вызова модели, схема, уведомление |
| `mr-comment.yaml` | чтение merge request по HTTP и ответ комментарием |
| `weekly-report.yaml` | параллельный `foreach` по проектам, дайджест и отправка в канал |
| `triage.yaml` | классификация по `enum`, метки, комментарий, вызов дежурного только при критичности |
| `review.yaml` | шаг `agent`, работающий над чекаутом |
| `jira-report.yaml` | пагинация и агрегация внутри пака, рендер HTML-отчёта в файл |
| `jira-quality.yaml` | `foreach`, в котором модель по схеме оценивает описание каждой задачи доски, и HTML-отчёт с оценками |

Паки, которые лежат в репозитории, находятся в `apis/` и подключаются через
`from: ./apis/`. Каждый объявляет интерфейс, который реализует, поэтому
сценарий с `interface: forge/v1` переносится между ними сменой входа:

| Пак | Интерфейс | Операции |
|---|---|---|
| [`gitlab`](../../apis/gitlab/README.ru.md) | `forge/v1` | `get_change`, `list_files`, `get_file`, `post_comment`, `list_merge_requests` |
| [`github`](../../apis/github/README.ru.md) | `forge/v1` | `list_files`, `get_file`, `post_comment`, `post_review` и `get_change` без списка файлов |
| [`gitea`](../../apis/gitea/README.ru.md) | `forge/v1` | то же, что у GitHub, только `list_files` не отдаёт текст диффа |
| [`jira`](../../apis/jira/README.ru.md) | `tracker/v1` | `get_issue`, `search`, `comment` |
| [`jira-server`](../../apis/jira-server/README.ru.md) | `tracker/v1` | `get_issue`, `search`, `search_all`, `search_summary`, `list_boards`, `list_board_issues`; только чтение |
| [`telegram`](../../apis/telegram/README.ru.md) | `notify/v1` | `send`, `send_document`, `get_me` |
| [`slack`](../../apis/slack/README.ru.md) | `notify/v1` | `send`, `auth_test` |

`baton apis validate apis/*` разбирает их все и прогоняет записанные ответы из
`apis/<pack>/examples/` через трансформы — это же делает тестовый набор на
каждом коммите.

Полезные команды при написании сценария:

```sh
baton validate scenario.yaml           # все проблемы разом, а не только первая
baton run scenario.yaml --dry-run      # план, без выполнения
baton tools scenario.yaml --step ID    # инструменты, которые получит шаг agent
baton schema                           # JSON Schema формата
baton run scenario.yaml --json         # события как JSONL, для сборщика в CI
```

В CI прогон — это одна команда и её код выхода:

```yaml
- run: baton run review.yaml -i project=$CI_PROJECT_PATH -i mr=$CI_MERGE_REQUEST_IID
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
```

У Baton намеренно нет ни планировщика, ни вебхуков: триггером служит cron,
systemd timer или расписание вашего CI.

Дальше по теме:

- [Справочник схемы сценария](schema.md) — все поля формата, сгенерированные
  из JSON Schema
- [Обзор проекта](overview.md) — для чего нужен Baton и почему он устроен
  именно так
- [Техническое задание на v1](spec.md) — авторитетный технический документ
