# Docker

A container is not the primary way to run baton — the [quickstart](quickstart.md)
downloads a static binary and needs nothing else. Use the image when a host
lacks Go, or CI needs a pinned, reproducible environment for `run`/`validate`.

## What is in the image

Multi-stage build ([`Dockerfile`](../../Dockerfile)): `golang:1.25.7-bookworm`
compiles `./cmd/baton` with `CGO_ENABLED=0`, the result runs on
`debian:bookworm-slim` as a non-root user (`baton`, uid/gid `1000`).

Runtime packages, all `apt-get`, nothing else:

- `ca-certificates` — TLS for `http`/`llm` steps and pack fetches
- `git` — the `file` step's history/commit support
- `bash`, `jq`, `grep`, `ripgrep` (`rg`) — shell scenarios and ad hoc
  transforms outside baton's own gojq
- `curl` — debugging HTTP calls by hand
- `bzip2`, `zip`/`unzip`, `tar`, `gzip` — archive round-trips for artifacts

There is no Go toolchain, no Python, no Node, no Docker CLI and no coding
agent binaries (`claude`, `codex`) in the runtime image — the `agent` step
type needs those added in a derived image; this one is not it.

`ENTRYPOINT ["baton"]`, `CMD ["--help"]`: the image behaves like the `baton`
binary itself, so `docker run baton init ...` works without overriding the
entrypoint.

## Build and run

```sh
docker compose build
docker compose run --rm baton init myworkflow
docker compose run --rm baton validate myworkflow/hello.yaml
docker compose run --rm baton run myworkflow/hello.yaml -i who=Docker
```

[`compose.yaml`](../../compose.yaml) bind-mounts the current directory (the
repository checkout by default) at `/workspace` and runs commands as
`user: "${BATON_UID:-1000}:${BATON_GID:-1000}"`, so every scenario path is
relative to `/workspace` — `myworkflow/hello.yaml` above is
`./myworkflow/hello.yaml` on the host.

To run against a different directory, set `BATON_WORKSPACE`:

```sh
BATON_WORKSPACE=/path/to/project docker compose run --rm baton init myworkflow
```

## Host file ownership: `BATON_UID`/`BATON_GID`

The image's own user is uid/gid `1000:1000`. `compose.yaml` overrides the
container user per invocation instead of baking your uid into the image, so
files baton writes into the bind-mounted workspace (scenarios, `runs/`) are
owned by you on the host, not by root or a stray `1000:1000`:

```sh
BATON_UID=$(id -u) BATON_GID=$(id -g) docker compose run --rm baton run myworkflow/hello.yaml
```

Leaving both unset defaults to `1000:1000`, which matches your uid on most
single-user Linux desktops already.

## `HOME` for an arbitrary uid

Overriding the container user to an arbitrary host uid means that uid has no
entry in `/etc/passwd` and no writable home directory of its own. Baton's
config search and any provider SDK cache calls `os.UserConfigDir()` /
`os.UserCacheDir()`, which read `$HOME` — so `compose.yaml` points `HOME` at
a `tmpfs` mount, `/tmp/baton-home`, world-writable (`mode=1777`) and mounted
fresh for every `run`. Nothing baton needs to persist between runs lives
under `HOME`; run state lives under the bind-mounted workspace instead.

## Secrets

`compose.yaml` does not forward any `.env` file or ambient environment into
the container — a provider API key or other secret is explicit, per
invocation, with `-e`:

```sh
docker compose run --rm -e OPENROUTER_API_KEY baton run scenario.yaml
```

(`-e NAME` with no `=value` forwards the variable from the host shell's own
environment, same as plain `docker run -e`.)

## Optional read-only config mount

Baton reads its global configuration (spec section 12: `apis`, `secrets`,
`notify`, `providers`, `defaults`, `on_failure`, `pricing`, `mcp_servers`)
from `$XDG_CONFIG_HOME/baton/config.yaml` and `./baton.yaml`, merged, or from
the single file named by `$BATON_CONFIG` when that variable is set
(`internal/config/config.go`). To use a config file that lives outside the
bind-mounted workspace, mount it read-only and point `BATON_CONFIG` at the
in-container path:

```sh
docker compose run --rm \
  -v /path/to/config.yaml:/etc/baton/config.yaml:ro \
  -e BATON_CONFIG=/etc/baton/config.yaml \
  -e OPENROUTER_API_KEY \
  baton run scenario.yaml
```

## Smoke test

[`scripts/docker-smoke.sh`](../../scripts/docker-smoke.sh) runs inside the
container against the default bind-mounted repository checkout: confirms the
process is non-root, every packaged utility works (including a
gzip/bzip2/tar/zip round-trip), and `baton init`/`validate`/`run` succeed
against the bundled `hello` template, plus that an invalid input is rejected
with exit code 3.

```sh
docker compose run --rm --entrypoint sh baton /workspace/scripts/docker-smoke.sh
```

## Published image

CI builds and smoke-tests the image on every pull request and push to
`main`, without publishing anything. A `v*` tag additionally runs the test
suite and, once green, builds `linux/amd64` and `linux/arm64` and publishes
to `ghcr.io/foxzi/baton`, tagged with the semver parts of the tag (e.g.
`0.3.0` and `0.3`), plus `latest` for a stable (non-prerelease) tag.

Release `0.3.0` is being prepared as the first tag built against this
Dockerfile; `v0.2.0`, the latest existing tag, predates it and will not be
rebuilt or moved. No image has been published to `ghcr.io/foxzi/baton` yet —
publishing happens only once the `v0.3.0` tag is pushed and CI's publish job
runs.
