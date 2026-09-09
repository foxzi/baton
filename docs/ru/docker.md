# Docker

Контейнер — не основной способ запускать baton: [быстрый старт](quickstart.md)
скачивает статический бинарник, и больше ничего не нужно. Образ полезен,
когда на хосте нет Go или CI нужна зафиксированная воспроизводимая среда для
`run`/`validate`.

## Что внутри образа

Многоэтапная сборка ([`Dockerfile`](../../Dockerfile)): `golang:1.25.7-bookworm`
собирает `./cmd/baton` с `CGO_ENABLED=0`, результат исполняется на
`debian:bookworm-slim` от имени непривилегированного пользователя (`baton`,
uid/gid `1000`).

Пакеты в рантайме, всё через `apt-get`, больше ничего:

- `ca-certificates` — TLS для шагов `http`/`llm` и загрузки паков
- `git` — история/локальные коммиты шага `file`
- `bash`, `jq`, `grep`, `ripgrep` (`rg`) — шаги-скрипты и разовые
  преобразования вне встроенного gojq
- `curl` — ручная отладка HTTP-вызовов
- `bzip2`, `zip`/`unzip`, `tar`, `gzip` — упаковка/распаковка артефактов

В образе рантайма нет тулчейна Go, нет Python, нет Node, нет Docker CLI и
нет бинарников кодинг-агентов (`claude`, `codex`) — для шага типа `agent` их
нужно добавлять в производном образе; этот образ — не для этого.

`ENTRYPOINT ["baton"]`, `CMD ["--help"]`: образ ведёт себя как сам бинарник
`baton`, поэтому `docker run baton init ...` работает без переопределения
entrypoint.

## Сборка и запуск

```sh
docker compose build
docker compose run --rm baton init myworkflow
docker compose run --rm baton validate myworkflow/hello.yaml
docker compose run --rm baton run myworkflow/hello.yaml -i who=Docker
```

[`compose.yaml`](../../compose.yaml) монтирует текущую директорию (по
умолчанию — сам чекаут репозитория) в `/workspace` и запускает команды от
`user: "${BATON_UID:-1000}:${BATON_GID:-1000}"`, поэтому пути сценариев
указываются относительно `/workspace` — `myworkflow/hello.yaml` выше — это
`./myworkflow/hello.yaml` на хосте.

Чтобы работать с другой директорией, задайте `BATON_WORKSPACE`:

```sh
BATON_WORKSPACE=/path/to/project docker compose run --rm baton init myworkflow
```

## Владелец файлов на хосте: `BATON_UID`/`BATON_GID`

Собственный пользователь образа — uid/gid `1000:1000`. `compose.yaml`
переопределяет пользователя контейнера на каждый вызов вместо того, чтобы
зашивать ваш uid в образ, поэтому файлы, которые baton пишет в
смонтированную директорию (сценарии, `runs/`), на хосте принадлежат вам, а
не root или произвольному `1000:1000`:

```sh
BATON_UID=$(id -u) BATON_GID=$(id -g) docker compose run --rm baton run myworkflow/hello.yaml
```

Если обе переменные не заданы, используется `1000:1000` — на большинстве
однопользовательских Linux-десктопов это и так совпадает с вашим uid.

## `HOME` для произвольного uid

Если пользователь контейнера переопределён на произвольный uid хоста, у
этого uid нет записи в `/etc/passwd` и своей домашней директории для
записи. Поиск конфигурации baton и кеш-вызовы SDK провайдеров используют
`os.UserConfigDir()` / `os.UserCacheDir()`, которые читают `$HOME` —
поэтому `compose.yaml` указывает `HOME` на `tmpfs`-точку монтирования,
`/tmp/baton-home`, доступную на запись всем (`mode=1777`) и монтируемую
заново на каждый `run`. Ничего, что baton должен сохранять между
запусками, под `HOME` не хранится; состояние прогона лежит в смонтированной
рабочей директории.

## Секреты

`compose.yaml` не пробрасывает в контейнер ни `.env`-файл, ни окружение
хоста целиком — ключ API провайдера или другой секрет передаётся явно, на
каждый вызов, через `-e`:

```sh
docker compose run --rm -e OPENROUTER_API_KEY baton run scenario.yaml
```

(`-e NAME` без `=значение` пробрасывает переменную из окружения хоста,
как и у обычного `docker run -e`.)

## Опциональное монтирование конфигурации только для чтения

Глобальную конфигурацию baton (раздел 12 ТЗ: `apis`, `secrets`, `notify`,
`providers`, `defaults`, `on_failure`, `pricing`, `mcp_servers`) baton читает
из `$XDG_CONFIG_HOME/baton/config.yaml` и `./baton.yaml`, объединяя их, либо
из единственного файла, указанного в `$BATON_CONFIG`, если эта переменная
задана (`internal/config/config.go`). Чтобы использовать файл конфигурации
вне смонтированной рабочей директории, смонтируйте его только для чтения и
укажите `BATON_CONFIG` на путь внутри контейнера:

```sh
docker compose run --rm \
  -v /path/to/config.yaml:/etc/baton/config.yaml:ro \
  -e BATON_CONFIG=/etc/baton/config.yaml \
  -e OPENROUTER_API_KEY \
  baton run scenario.yaml
```

## Дымовой тест

[`scripts/docker-smoke.sh`](../../scripts/docker-smoke.sh) выполняется
внутри контейнера при смонтированном по умолчанию чекауте репозитория:
проверяет, что процесс не root, что все упакованные утилиты работают
(включая round-trip через gzip/bzip2/tar/zip) и что `baton
init`/`validate`/`run` успешно проходят на встроенном шаблоне `hello`, а
также что невалидный ввод отклоняется с кодом выхода 3.

```sh
docker compose run --rm --entrypoint sh baton /workspace/scripts/docker-smoke.sh
```

## Публикуемый образ

CI собирает и прогоняет дымовой тест образа на каждый pull request и push в
`main`, ничего не публикуя. Тег `v*` дополнительно прогоняет набор тестов
и, если они прошли, собирает `linux/amd64` и `linux/arm64` и публикует в
`ghcr.io/foxzi/baton` с тегами по частям semver из тега (например, `0.3.0`
и `0.3`), плюс `latest` для стабильного (не pre-release) тега.

Релиз `0.3.0` готовится как первый тег, собранный по этому Dockerfile;
`v0.2.0`, последний существующий тег, старше его и не будет пересобран или
сдвинут. В `ghcr.io/foxzi/baton` пока не опубликовано ни одного образа —
публикация произойдёт только после того, как будет запушен тег `v0.3.0` и
отработает публикующая задача CI.
