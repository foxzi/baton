# Baton

> Working title. `baton` is already taken by several projects in the agentic-automation niche; the final name is still open (`segue` is the leading alternative).

A headless runner for YAML scenarios that interleave **deterministic** steps (scripts, HTTP calls) with **non-deterministic** ones (LLM calls, coding agents). One binary, one run per invocation, no UI and no server. Think n8n for teams that would rather keep their workflows as YAML in git and trigger them from CI or cron.

## Why

Tasks shaped like *collect data with a script → hand it to a model for interpretation → destructure the result and publish it* are usually solved one of two bad ways. Either a bash wrapper around `claude -p` in every repository, which controls neither budget nor security, or a heavyweight platform with a server, a database and a web UI, which is overkill for a one-shot run in CI.

Baton fills the gap: a scenario is a file in the repository, a run is a single command, and all run state is a directory on disk.

## Principles

**The runner is deterministic; agency is a step.** Baton is not an agent. It executes a DAG of steps in a predictable order, and all agency is encapsulated in an `agent:` step (spawning a CLI agent) or an `llm:` step with tools (the runner drives the tool loop itself). That makes runs reproducible and budgets enforceable.

**Integrations live outside the binary.** Baton knows protocols — HTTP, JSON, auth schemes, pagination — but not services. GitLab, GitHub, Jira, Telegram and friends are described by *API packs*: YAML files in a separate repository, pinned by version and checksum. Packs implement small interfaces (`forge/v1`, `tracker/v1`, `notify/v1`), so one review scenario runs against GitLab, GitHub and Gitea by changing a single input. A new integration is a new file, not a new release.

**The model never sees credentials.** Every authorised call to an external API goes through a built-in gateway: the agent gets narrow tools like `gitlab.get_mr_changes(iid)` while the runner injects the tokens. Publishing results is a separate deterministic step with its own permissions. Secrets are their own type in the value system, and trying to interpolate one into a prompt is rejected at validation time.

**Capability instead of access.** The agent gets no `curl`, no `bash`, no network. Instead the scenario declares parameterised commands (`test`, `lint`) which become tools with validated arguments, executed without a shell.

**A structural contract between steps.** Every LLM step returns JSON matching a schema. Branching is only allowed on validated fields, ideally on an `enum`. Invalid model output is its own error class with its own retry strategy.

**Fail-fast by default, always notify.** Any failing step fails the run unless stated otherwise. The `on_failure` section runs for every error class, budget exhaustion included.

**Triggers are external.** Baton knows nothing about schedules or webhooks. Use cron, a systemd timer, or your CI's schedule. That is what separates a CLI tool from a platform.

## What a scenario looks like

```yaml
version: 1
name: code-review
description: Review a merge request, depth chosen by risk

inputs:
  project: { type: string, required: true, pattern: '^[\w.-]+/[\w.-]+$' }
  mr:      { type: int,    required: true }
  base:    { type: string, default: origin/main }

defaults:
  engine: claude-code
  model: anthropic/claude-sonnet-4-6
  timeout: 10m

budget:
  usd: 8
  time: 40m

steps:
  - id: diff
    run:
      argv: ["git", "diff", "{{ .inputs.base }}...HEAD"]

  - id: classify
    llm:
      model: anthropic/claude-haiku-4-5
      prompt: prompts/classify.md
      with: { diff: "{{ .steps.diff.stdout }}" }
      schema: schemas/classify.json

  - id: review
    when: 'steps.classify.result.risk != "low"'
    agent:
      prompt: prompts/review.md
      with: { project: "{{ .inputs.project }}", id: "{{ .inputs.mr }}" }
      profile: review
      max_turns: 25
      budget_usd: 2
      result: schemas/findings.json

  - id: publish
    http:
      op: forge.post_comment
      auth: forge_rw
      args:
        project: "{{ .inputs.project }}"
        id: "{{ .inputs.mr }}"
        body: "{{ render \"templates/review.md.tmpl\" .steps.review }}"
    dedupe_key: "review-{{ .inputs.project }}-{{ .inputs.mr }}"

  - id: gate
    assert: 'len(filter(steps.review.result.findings, .severity == "blocker")) == 0'

on_failure:
  - notify: telegram
    message: "{{ .run.name }} failed: {{ .run.error.class }} at {{ .run.failed_step }}"
```

The agent in the `review` step can read the repository and call read-only forge operations. It cannot post the comment — that is what the `publish` step is for, with a write credential the model never sees.

## Step types

| Step | Purpose |
|---|---|
| `run` | Execute a command via argv without a shell, capturing stdout/stderr and the exit code |
| `http` | Call an API pack operation (`forge.post_comment`) or make a raw request, credentials from the secret store |
| `llm` | A single model call through Anthropic, OpenAI, OpenRouter or any OpenAI-compatible API, with a mandatory result schema, a fallback model chain and optional tools |
| `agent` | Spawn a CLI agent (Claude Code in v1) with skills, tools and MCP servers |
| `foreach` | Process a list in parallel, with a parallelism cap and partial-success control |
| `until` | A bounded loop with an exit condition, e.g. "keep fixing until the tests pass" |
| `assert` | Fail the run deliberately with a distinct exit code, to block a merge in CI |

## Feature overview

**Flow control.** `when:` conditions in expr-lang (typed, side-effect free); `switch:`/`cases:` with exhaustiveness checking against a schema `enum`; `needs:` for explicit dependencies; static reference checking, so referencing the result of a possibly-skipped step without handling `null` is a validation error.

**Agent tools.** Filesystem reads always, writes only inside the workspace, path deny-lists. Git history commands and local commits, no network operations. Declared argv commands with argument validation, timeouts and call limits. Read-only pack operations from an allowlist, exposed as narrow tools authorised by the runner. `fetch` for public URLs on a domain allowlist. Third-party MCP servers from an allowlist, spawned by the runner, with tools taking arbitrary URLs flagged `unsafe`. `submit_result` as the schema-validated result channel. Cross-run `state` scoped to the scenario. Ready-made permission profiles: `review`, `fix`, `research`.

**Error handling.** Classified errors — `transient`, `schema`, `command`, `timeout`, `budget`, `policy`, `config` — with class-bound retry; a `schema` failure is retried with the validation error in context. `on_error: fail | continue | fallback`. No automatic retry of side-effecting steps without a `dedupe_key`. Partial success in `foreach` behind a `min_success` threshold. Run-level `on_failure` with notification channels from the global config. Distinct exit codes for success, execution failure, a tripped assert, bad configuration, exhausted budget and interruption.

**Runs, caching, resume.** Every run is a `runs/<id>/` directory holding state, artifacts, tool-call logs and cost. Step results are cached by a hash of the step definition and its inputs, so a repeat run does not pay for tokens again. `baton resume <id>` continues from the failed step. Every agent tool call is written to an audit log.

**Budgets and observability.** Per-step and per-run limits on tokens, dollars, agent turns and wall time. Run cost recorded in state and included in notifications. Structured JSONL event log with secret redaction across every output.

## Use cases

- **Code review in CI.** A job calls `baton run review.yaml -i mr=$CI_MERGE_REQUEST_IID`. The scenario classifies the change, picks a review depth, runs a read-only agent, publishes findings in a separate step and blocks the merge if there are blockers. One scenario covers every forge; only the invocation is forge-specific.
- **Periodic reports.** Cron runs a scenario that walks repositories or systems, gathers data with scripts, has a model summarise it and sends the report to Telegram, Slack or email. One failing source does not sink the report but is recorded in it.
- **Triage and classification.** Route incoming tickets or incidents: classify against an `enum`, dispatch with `switch`, create tickets through `http` steps holding write credentials the model never sees.
- **Automated fixes with verification.** The `fix` profile lets an agent edit the workspace and run declared test commands inside an iteration-bounded `until` loop; the result is a diff artifact and applying it is a separate step.
- **Compliance checks.** Deterministic steps collect facts about dependencies, licences or configuration, an LLM step interprets them, and `assert` halts the pipeline on a violation.

## What Baton is not

- **Not a platform.** No web UI, no server, no database.
- **Not a scheduler.** No built-in cron or webhooks (possibly later, as `baton serve`).
- **Not an agent.** It does not decide which APIs to call; the scenario does.
- **Not a CI replacement.** Deterministic builds and tests stay in CI. Baton adds the steps that need interpretation.

## Compared to the alternatives

| | Baton | gh-aw | Kestra | Dagu | Dagger |
|---|---|---|---|---|---|
| Scenarios in YAML | yes | md + frontmatter | yes | yes | no, code |
| Serverless | yes | yes | no | yes | yes |
| Forge-agnostic | yes | GitHub only | yes | yes | yes |
| LLM steps | yes | yes | yes | no | yes |
| Credentials isolated from the model | by design | safe-outputs | no | no | containers |
| Per-run budget | yes | yes | no | no | no |
| Ops burden | one binary | none | JVM + DB | one binary | daemon |

## Status

**Runnable.** Scenarios built from `run`, `assert`, `http`, `llm`, `foreach` and `notify` steps execute end to end: `baton run`, `resume`, `runs`, `validate`, `schema`. The [weekly report example](examples/weekly-report.yaml) is the current acceptance scenario — a foreach over projects through the GitLab pack, a model digest against a JSON schema, and a notification, cached so that a repeated run of the same week spends no tokens. `agent` steps run too, with the `fake` engine and the Claude Code adapter: the runner prepares the workspace, serves the step's `commands` as tools through the MCP gateway and takes the result from `submit_result`. The gateway also serves the file, api, git, fetch and state tools of the step's profile; an engine that brings its own file tools, as Claude Code does, is not served the gateway's.

Roadmap, per [the specification](docs/ru/spec.md) (section 15):

| Milestone | Scope | State |
|---|---|---|
| M1 | Core: parsing, validation, secrets, expressions, templates, `run`, `assert`, run directory, HTTP layer with auth schemes and pagination, local packs | done |
| M2 | Providers (Anthropic, OpenAI, OpenAI-compatible/OpenRouter), `llm` steps, structured output, `foreach`, cache, resume, `on_failure`, reports | done |
| M3 | `fake` engine, Claude Code adapter, MCP gateway, `submit_result`, `commands`, profiles, audit log | in progress |
| M3.5 | Packs from git with pinning and checksums, the `forge/v1`/`tracker/v1`/`notify/v1` interface registry, `baton apis` commands | in progress |
| M4 | `until`, `fallback`, `dedupe_key`, `switch`, `fetch`, `state`, third-party MCP, signal handling | not started |
| M5 | Documentation, example scenarios, goreleaser builds for linux/amd64 and linux/arm64 | not started |

## Building

Requires Go 1.25.7 or newer.

```sh
make build      # builds ./baton with version metadata
make test       # go test ./...
make race       # go test -race ./...
make all        # fmt, vet, test, build
```

## Running

```sh
baton validate examples/weekly-report.yaml
baton run examples/weekly-report.yaml \
    -i 'projects=["acme/web", "acme/api"]' -i since=2026-01-01
```

The provider, the notification channels and the pricing table live in the global configuration, `~/.config/baton/config.yaml` or `./baton.yaml`, or wherever `--config` points. Secrets are read from the environment or from files at the moment a step needs them; they never reach the run directory, the cache or a model prompt.

Every run writes `runs/<id>/` with `run.json`, `events.jsonl` and the outputs of each step. `baton runs list`, `baton runs show <id>` and `baton runs logs <id>` read it back, `baton resume <id>` continues a failed run from the step that failed. `--dry-run` prints the plan, `--json` prints events as JSONL, `--no-cache` ignores cached step results. `baton tools <scenario.yaml> --step ID` prints the tools an agent step would be given, without running anything.

`baton apis import` generates the skeleton of an API pack from an OpenAPI 3 document, e.g. `baton apis import --openapi openapi.yaml --ops listMergeRequests > gitlab.yaml`. `baton apis validate <pack.yaml|pack-dir>...` loads a pack and replays every `examples/<op>.json` through its envelope and transform, which is the part of a pack that breaks silently without a live API to call. `baton apis call <scenario.yaml> <api>.<op> -a k=v` calls a single operation through the scenario's `apis` entry, for trying a pack against the real service before a step depends on it.

## Technology

Go, a single static binary. Dependencies: a YAML parser, expr-lang for expressions, gojq for pack transforms, a JSON Schema validator, the Go MCP SDK, and model provider SDKs (Anthropic, OpenAI, and OpenAI-compatible endpoints including OpenRouter). No external services, no database. Service integrations live in a separate packs repository.

## Documentation

- [Project overview](docs/en/overview.md) — the source this README is based on
- [Specification for v1](docs/en/spec.md) — the authoritative technical document
- [Specification review](docs/en/spec-review.md) — inconsistencies and gaps found while reading the spec
- Russian originals: [обзор](docs/ru/overview.md), [ТЗ](docs/ru/spec.md), [вычитка](docs/ru/spec-review.md), [README](docs/ru/README.md)

## License

[MIT](LICENSE)
