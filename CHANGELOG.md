# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
follows [semantic versioning](https://semver.org/spec/v2.0.0.html).

Russian version: [CHANGELOG.ru.md](CHANGELOG.ru.md).

## [Unreleased]

## [0.3.0] - 2026-09-09

### Added

- A Docker image (multi-stage build on `debian:bookworm-slim`) with
  `ca-certificates`, `git`, `bash`, `jq`, `grep`, `ripgrep`, `curl`,
  `bzip2`, `zip`/`unzip`, `tar` and `gzip`, running as a non-root user;
  `compose.yaml` runs it under an arbitrary host UID/GID and gives it a
  writable `HOME` for that UID.
- CI that builds and smoke-tests the image on every pull request and push
  to `main`, and publishes `linux/amd64`+`linux/arm64` images to
  `ghcr.io/foxzi/baton` on `v*` tags, tagged with the semver parts of the
  tag plus `latest` for a stable release.
- Docker docs in English and Russian (`docs/en/docker.md`,
  `docs/ru/docker.md`).

## [0.2.0] - 2026-09-09

### Added

- `baton init` with `hello` and `summarize` templates, so a new project
  starts from a runnable scenario instead of a blank file.
- A local `doctor` check that reports missing config, credentials and
  provider setup without making a network call.
- A run/resume summary (status, duration, cost, artifacts) and recovery
  hints pointing at the run's `runs show`/`runs logs` command.
- An offline JSON Schema for scenario YAML, emitted by `init` alongside a
  `# $schema` modeline, for editor validation without network access.
- `budget.tokens` to cap a scenario's total token spend, alongside the
  existing cost budget.
- The `codex` engine for `agent:` steps: the OpenAI Codex CLI is driven through
  `codex exec --json`, reaches the gateway as an MCP server in a `config.toml`
  of its own, and is held inside the step's policy by the CLI's sandbox, since
  its shell tool cannot be turned off. `skills` are not supported by it.
- `agent.inherit_auth` for the `codex` engine: the user's stored CLI login is
  copied into the run's own `CODEX_HOME`, so a ChatGPT subscription works in
  place of an API key.
- `create_issue` in the `jira` pack, so a scenario can open an issue through
  `tracker/v1` and not only read.
- A pack parameter can address a nested field of the request body by naming
  its wire path with dots (`params.<name>.name: fields.summary`).
- `baton validate` checks a scenario against the packs it loads: the operation
  must exist, its required parameters must be covered, and a `readonly: true`
  api may only be used with readonly operations.
- A `switch` step is checked for exhaustiveness against the enum of the
  schema its subject comes from.
- Live checks behind environment flags: `BATON_E2E_PROVIDERS=1` calls the real
  LLM providers, `BATON_E2E=1` runs an agent scenario through the installed
  `claude` CLI. `make e2e` runs both.
- CI validates every pack in `apis/` and every scenario in `examples/`
  (`make validate-apis`, `make validate-examples`).

### Changed

- Validation diagnostics include the filename, YAML line and step ID when
  available, plus accepted-value hints for unknown input types and secret sources.
- The CLI prints usage on `-h`/`--help`, suggests a correction for an
  unknown command, and quotes paths with spaces in its hint output.
- Checking a recorded example replays pagination: for an operation with
  `paginate: true` the example is the body of a single page, `pagination.items`
  is applied to it and the transform is fed that page's items. Examples of the
  paginated `jira-server` operations were re-recorded as single-page bodies.

### Fixed

- Template rendering confines file paths to the scenario directory, closing a
  path-traversal escape.
- A run ID is validated before use, closing a path-traversal escape through
  `--run-id`.
- The Codex gateway requires `default_tools_approval_mode = "approve"` when
  `approval_policy = "never"`, allowing gateway tool calls without an
  approval prompt instead of failing outright.
- The cache key of an http step includes the pack checksum, so editing a pack
  invalidates the entries that came from it.
- `ValidateJSON` decodes numbers as `float64`, which fixes false type errors
  on integer fields.
- `list_board_issues` in `jira-server` keeps the `description` field.
- A secret renders as `***` under the numeric formatting verbs too: `%d` used
  to fall through to the struct fields and print the plaintext.

## [0.1.0] - 2026-09-09

The first release: the scenario format, the engine, the API packs and the
agent gateway.

### Added

- **Scenario format.** YAML with a JSON Schema, typed inputs and secrets,
  expressions and templates checked before a run, static checking of step
  references. Steps: `run`, `assert`, `http`, `llm`, `foreach`, `until`,
  `switch`, `agent`, `notify`, `file`.
- **Engine.** A run directory with state and logs, a step result cache,
  retries that skip budget, policy and config failures, cost accounting for
  model calls, and `baton resume <id>` to continue a failed run.
- **Providers.** Anthropic, OpenAI and OpenRouter backends for `llm` steps,
  with schema-constrained output and a retry on a schema mismatch.
- **API packs.** Loading from a directory or a pinned git source with a
  checksum, envelopes, `offset`/`cursor`/link-header pagination, `form` and
  `graphql` operations, `exchange` authorisation, jq transforms refused at
  load if impure.
- **Interfaces.** The `forge/v1`, `tracker/v1` and `notify/v1` registry, an
  `implements` check against recorded examples, and `interface:` in a
  scenario so a pack can be swapped by input.
- **Shipped packs.** `gitlab`, `github`, `gitea` (forge/v1), `jira`,
  `jira-server` (tracker/v1), `telegram`, `slack` (notify/v1), each with a
  README in English and Russian.
- **Agent steps.** A `claude-code` adapter and an MCP gateway that serves the
  step its declared tools only: commands, git, files, fetch, state and
  readonly api operations, with third-party MCP servers proxied through.
  A tool that was not declared is refused as a policy violation.
- **CLI.** `run`, `resume`, `validate`, `runs list|show|logs`, `tools`,
  `schema`, `version`, and `apis import|validate|call`.
- **Docs.** README, quickstart, overview, specification and a generated
  schema reference, in English and Russian; example scenarios for review,
  reports and triage.
- **Release.** goreleaser builds static binaries for linux/amd64 and
  linux/arm64 with checksums, published by pushing a `v*` tag.

[Unreleased]: https://github.com/foxzi/baton/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/foxzi/baton/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/foxzi/baton/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/foxzi/baton/releases/tag/v0.1.0
