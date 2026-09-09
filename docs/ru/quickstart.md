# Быстрый старт

Десять минут пути от пустого каталога до сценария, который вызывает модель,
проверяет результат по схеме и сообщает, во что обошёлся прогон.

Каждая команда и каждый листинг ниже — реальный вывод версии из этого
репозитория, а не иллюстрация.

## 1. Установка

Baton — единственный бинарник без зависимостей времени выполнения. Возьмите его
из релиза или соберите из исходников.

На [странице релизов](https://github.com/foxzi/baton/releases) лежат архивы
`tar.gz` для linux/amd64 и linux/arm64 рядом с `checksums.txt`:

```sh
tag=v0.1.0
curl -fsSLO https://github.com/foxzi/baton/releases/download/$tag/baton_${tag#v}_linux_amd64.tar.gz
curl -fsSLO https://github.com/foxzi/baton/releases/download/$tag/checksums.txt
sha256sum --check --ignore-missing checksums.txt
tar -xzf baton_${tag#v}_linux_amd64.tar.gz baton
./baton version
```

Для сборки из исходников нужен Go 1.25.7 или новее:

```sh
git clone https://github.com/foxzi/baton.git
cd baton
make build          # создаёт ./baton с метаданными версии
./baton version
```

При желании поместите бинарник куда-нибудь в свой `PATH` — дальше на этой
странице команда предполагается просто как `baton`.

## 2. Первый запуск: `baton init`

`baton init` записывает рабочий сценарий в каталог по вашему выбору — клонировать
этот репозиторий для этого не нужно:

```console
$ baton init myworkflow
wrote hello template to myworkflow
next: baton run myworkflow/hello.yaml

$ baton run myworkflow/hello.yaml -i who=Baton
[greet] step_started
[greet] step_finished: success (3.983783ms)
[check_exit] step_started
[check_exit] step_finished: success (264.371µs)
run 20260909-003107-0bfb: success in 6ms
run directory: myworkflow/runs/20260909-003107-0bfb
details: baton runs show 20260909-003107-0bfb --runs-dir myworkflow/runs
logs:    baton runs logs 20260909-003107-0bfb --runs-dir myworkflow/runs
```

Шаблону `hello` (по умолчанию) не нужен ни ключ API, ни сеть. Команда проверяет
каждый целевой путь перед записью, так что повторный запуск в тот же каталог
безопасен, а не разрушителен:

```console
$ baton init myworkflow
baton: refusing to overwrite 2 existing files:
  myworkflow/hello.yaml
  myworkflow/scenario.schema.json
```

`init` также записывает `scenario.schema.json` рядом со сценарием — JSON-схему
формата из этой же сборки — и указывает на неё в `hello.yaml` комментарием
`yaml-language-server`, так что редакторы, которые его поддерживают,
проверяют и подсказывают поля офлайн. См. [настройку редактора](editor-setup.md)
о том, какие редакторы это покрывает и как указать схему для сценария,
который вы не создавали через `init`.

`baton init myworkflow --template summarize` записывает сценарий из двух шагов
— вызов `llm`, проверенный по JSON-схеме, затем шаг `file`, который сохраняет
результат — плюс `baton.yaml`, уже настроенный на провайдера — самый короткий
путь к настоящему вызову модели, разобранный в [разделе 6](#6-подключение-модели).
Текст для суммаризации и путь вывода — это входы сценария, их можно
переопределить через `-i text=... -i out=...` в любом прогоне. `baton help
init` (или `baton init -h`) документирует оба шаблона и все опции;
`-h`/`--help` никогда не пишут файлы — ни у этой команды, ни у любой другой.

## 3. Запуск простейшего сценария

`myworkflow/hello.yaml` — файл, который только что записал `baton init`, —
весь формат в семнадцати строках: одна команда и одна проверка. Это тот же
файл, что и `examples/hello.yaml` в этом репозитории, байт в байт, если вы его
всё же клонировали.


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
details: baton runs show 20260909-003107-0bfb --runs-dir examples/runs
logs:    baton runs logs 20260909-003107-0bfb --runs-dir examples/runs
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

## 4. Что остаётся после прогона

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

Итоговая строка, которую `run` и `resume` печатают в конце, при необходимости
обрастает ещё тремя строками: `cost:`, как только шаг реально оценил стоимость
токенов, `artifacts:` со списком всех путей, которые действительно записала
операция `write`/`append` шага `file`, и — только если прогон упал — готовая
к вставке команда `resume:`. Возьмём `greet` выше и добавим второй шаг,
`file: { write: report.txt, content: "{{ .steps.greet.result }}" }`:

```console
$ baton run scn.yaml --workspace "$(pwd)"
[greet] step_started
[greet] step_finished: success (4.083852ms)
[report] step_started
[report] step_finished: success (660.358µs)
run 20260909-175057-863f: success in 7ms
artifacts:
  /tmp/artifact-demo/report.txt
run directory: runs/20260909-175057-863f
details: baton runs show 20260909-175057-863f --runs-dir runs
logs:    baton runs logs 20260909-175057-863f --runs-dir runs
```

`details:`/`logs:` печатаются всегда, а `--runs-dir` расписан явно, потому что
собственное значение по умолчанию для этого флага в CLI зависит от текущего
каталога, который не обязан быть тем же, что использовал сам прогон.

## 5. Падение — это отдельный код выхода

Замените assert на `steps.greet.exit_code == 42`, и прогон упадёт именно там,
где проверка, а не где-то ниже по цепочке:

```console
$ baton run examples/hello.yaml
[check_exit] step_started
[check_exit] step_failed: assert: assert failed: steps.greet.exit_code == 42
run 20260909-003207-93f9: failed in 6ms
failed step check_exit: assert: assert failed: steps.greet.exit_code == 42
resume: baton resume 20260909-003207-93f9 --runs-dir examples/runs
run directory: examples/runs/20260909-003207-93f9
details: baton runs show 20260909-003207-93f9 --runs-dir examples/runs
logs:    baton runs logs 20260909-003207-93f9 --runs-dir examples/runs
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

## 6. Подключение модели

Сценарий называет модель как `<provider>/<model>`; где живёт этот провайдер и
каким ключом он пользуется — дело глобальной конфигурации, а не сценария.
Baton сначала читает `~/.config/baton/config.yaml` (или
`$XDG_CONFIG_HOME/baton/config.yaml`), затем `./baton.yaml` в текущем
каталоге, объединяя оба файла — значения из локального побеждают;
`--config FILE` (можно повторять, для `run`, `apis` и `tools`) или
`BATON_CONFIG` полностью заменяют этот поиск указанным файлом (или файлами).

Самый быстрый способ увидеть это в деле целиком — `baton init myworkflow
--template summarize`: команда сразу пишет `baton.yaml` ниже, уже заполненный
для одного провайдера, плюс сценарий и схему, так что остаётся задать только
ключ. Перед тем как его задать, `baton doctor` проверяет всё, что не требует
сети, — сам сценарий, какой файл конфигурации он нашёл, и на месте ли
провайдер и переменная его ключа:

```console
$ baton init myworkflow --template summarize --provider openrouter
wrote summarize template to myworkflow
next: export OPENROUTER_API_KEY=... then cd myworkflow && baton run summarize.yaml

$ cd myworkflow && baton doctor summarize.yaml
scenario: summarize.yaml
  OK load
  OK validate
config:
  MISSING /home/you/.config/baton/config.yaml
  FOUND baton.yaml
step summarize (llm):
  OK model: openrouter/openai/gpt-4.1-nano (from defaults.model)
  FAIL provider openrouter: kind openrouter, OPENROUTER_API_KEY is not set
  OK system: inline template (no file at You summarize text. Answer with JSON only.)
  OK prompt: file prompts/summarize.md
  OK schema: schemas/summarize.json

not checked: api key validity, network reachability of any provider or pack, notify channel delivery.
$ echo $?
3

$ export OPENROUTER_API_KEY=...
$ baton run summarize.yaml
```

`doctor` никогда не делает сетевой вызов, поэтому чистый отчёт — не
доказательство, что ключ валиден или что провайдер доступен, это подтверждает
только `baton run`. Строка `MISSING .../config.yaml` называет
`os.UserConfigDir()` на машине, где это выполнялось; здесь это `/home/you/...`,
чтобы не печатать настоящее имя пользователя, — у вас будет другое.

Прогоните как есть, и собственный текст шаблона по умолчанию суммируется в
`summary.json` в текущем каталоге — это делает шаг `file` шаблона; передайте
свой текст и другой путь вывода через `-i`:

```console
$ baton run summarize.yaml -i text="The migration finished ahead of schedule." -i out=notes.json
$ cat notes.json
{"keywords":["migration","schedule"],"summary":"The migration finished early."}
```

`baton.yaml` подхватывается только из текущего каталога (см. выше), а не
относительно файла сценария, поэтому сначала `cd myworkflow`; запуск
`baton run myworkflow/summarize.yaml` из родительского каталога его не
найдёт и упадёт с ошибкой "unknown provider".

Чтобы собрать ту же конфигурацию вручную, создайте `baton.yaml` рядом со
сценарием:

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

## 7. Куда дальше

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
| [`jira`](../../apis/jira/README.ru.md) | `tracker/v1` | `get_issue`, `search`, `create_issue`, `comment` |
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
- [Настройка редактора](editor-setup.md) — проверка и автодополнение файлов
  сценария в VS Code, IntelliJ и любом другом клиенте yaml-language-server
- [Обзор проекта](overview.md) — для чего нужен Baton и почему он устроен
  именно так
- [Техническое задание на v1](spec.md) — авторитетный технический документ
