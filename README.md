# Baton

A headless runner for YAML scenarios that interleave **deterministic** steps (scripts, HTTP calls) with **non-deterministic** ones (LLM calls, coding agents). One binary, one run per invocation, no UI and no server. Think n8n for teams that would rather keep their workflows as YAML in git and trigger them from CI or cron.

## Why

Tasks shaped like *collect data with a script → hand it to a model for interpretation → destructure the result and publish it* are usually solved one of two bad ways: a bash wrapper around `claude -p` in every repository, which controls neither budget nor security, or a heavyweight platform with a server, a database and a web UI, which is overkill for a one-shot run in CI. Baton fills the gap — a scenario is a file in the repository, a run is a single command, and all run state is a directory on disk.

## Key features

- **Scenarios are YAML in git.** Typed inputs with defaults and patterns, steps with explicit dependencies, `when:` conditions in expr-lang, static reference checking — `baton validate` reports every problem before anything runs, and `--dry-run` prints the plan.
- **Nine step types.** `run`, `http`, `llm`, `agent`, `foreach`, `until`, `file`, `switch`, `assert` — see [the table below](#step-types).
- **Model calls with a contract.** Anthropic, OpenAI and any OpenAI-compatible endpoint (OpenRouter included). Every `llm` step returns JSON validated against a schema, with a fallback model chain and the structured-output mode negotiated per provider.
- **Coding agents as one step.** Claude Code in v1, driven through a built-in MCP gateway: the runner prepares the workspace, serves exactly the tools the step's profile allows and takes the answer from `submit_result`.
- **Capability instead of access.** No shell, no `curl`, no ambient network. An agent gets declared argv commands with validated arguments, workspace-scoped file writes with path deny-lists, git history and local commits, `fetch` on a domain allowlist, read-only pack operations, cross-run `state` and proxied third-party MCP servers. The `review`, `fix` and `research` profiles pick the set.
- **API packs instead of built-in integrations.** The HTTP layer knows auth schemes (bearer, header, query, basic, token exchange), pagination and gojq transforms; services are YAML packs loaded from a directory or from git, pinned by version and checksum. Packs implement `forge/v1`, `tracker/v1` and `notify/v1`, so swapping GitLab for GitHub is an input rather than a rewrite.
- **Packs are debuggable.** `baton apis import` bootstraps a pack from an OpenAPI 3 document, `apis validate` replays every recorded example through the envelope and the transform, `apis call` runs a single operation against the real service.
- **Secrets the model never sees.** A secret is its own value type, interpolating one into a prompt fails validation, the runner injects tokens into API calls itself, and every output, the run directory and the cache are redacted.
- **Budgets and cost accounting.** Dollar, token, agent-turn and wall-time limits per step and per run; the real cost of every model call lands in the run state and in notifications.
- **Classified errors.** `transient`, `schema`, `command`, `timeout`, `budget`, `policy` and `config`, each with its own retry policy — a schema failure is retried with the validation error in context. `on_error: fail | continue | fallback`, `min_success` for `foreach`, `dedupe_key` guarding side effects, a run-level `on_failure` that always notifies, and distinct exit codes for CI.
- **Runs on disk, cache and resume.** Every run is a `runs/<id>/` directory with state, step outputs, a JSONL event log and an audit log of every agent tool call. Step results are cached by hash, so a repeat run pays no tokens again, and `baton resume <id>` continues from the failed step.

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
| `file` | Reading, writing and listing files in the workspace |
| `switch` | Branch on the value of an expression: one step per case, sugar over `when:` |
| `assert` | Fail the run deliberately with a distinct exit code, to block a merge in CI |

## Use cases

- **Code review in CI.** Classify the change, pick a review depth, run a read-only agent, publish findings in a separate step, block the merge on blockers. One scenario covers every forge.
- **Periodic reports.** Cron walks repositories or systems, scripts gather the data, a model summarises it, the report goes to a channel. One failing source does not sink the report but is recorded in it.
- **Triage and classification.** Classify incoming tickets against an `enum`, dispatch with `switch`, create tickets through `http` steps holding write credentials the model never sees.
- **Automated fixes with verification.** The `fix` profile lets an agent edit the workspace and run declared test commands inside a bounded `until` loop; the result is a diff artifact and applying it is a separate step.
- **Compliance checks.** Deterministic steps collect facts, an LLM step interprets them, `assert` halts the pipeline on a violation.

## Principles

- **The runner is deterministic; agency is a step.** Baton executes a DAG in a predictable order; all agency is encapsulated in an `agent:` step or an `llm:` step with tools. That makes runs reproducible and budgets enforceable.
- **A structural contract between steps.** Every LLM step returns JSON matching a schema, and branching is only allowed on validated fields, ideally on an `enum`.
- **Fail-fast by default, always notify.** Any failing step fails the run unless stated otherwise, and `on_failure` runs for every error class, budget exhaustion included.
- **Triggers are external.** No built-in scheduler and no webhooks: use cron, a systemd timer or your CI's schedule. That is what separates a CLI tool from a platform.
- **Not a platform, not an agent, not a CI replacement.** No web UI, server or database. Baton does not decide which APIs to call — the scenario does. Deterministic builds and tests stay in CI; Baton adds the steps that need interpretation.

## Status

**Runnable.** The `run`, `assert`, `http`, `llm`, `foreach`, `notify`, `agent`, `until`, `file` and `switch` steps execute end to end, together with the cache, `resume`, budgets, `fallback`, `dedupe_key`, `fetch`, `state`, signal handling, the MCP gateway with proxied third-party servers, packs from git with interface checks and the `baton apis` commands. The [weekly report example](examples/weekly-report.yaml) is the current acceptance scenario — a foreach over projects through the GitLab pack, a model digest against a JSON schema and a notification, cached so a repeated run of the same week spends no tokens.

**Outstanding.** The `baton-apis` repository itself: this repository ships only the `gitlab` pack, and `telegram` still notifies through the built-in channel rather than a `notify/v1` pack. The exhaustiveness of a `switch` over a schema `enum` is only a missing-`default` warning rather than a check against the values. Release builds through goreleaser are not done yet.

Roadmap, per [the specification](docs/ru/spec.md) (section 15):

| Milestone | Scope | State |
|---|---|---|
| M1 | Core: parsing, validation, secrets, expressions, templates, `run`, `assert`, run directory, HTTP layer with auth schemes and pagination, local packs | done |
| M2 | Providers (Anthropic, OpenAI, OpenAI-compatible/OpenRouter), `llm` steps, structured output, `foreach`, cache, resume, `on_failure`, reports | done |
| M3 | `fake` engine, Claude Code adapter, MCP gateway, `submit_result`, `commands`, profiles, audit log | done |
| M3.5 | Packs from git with pinning and checksums, the `forge/v1`/`tracker/v1`/`notify/v1` interface registry, `baton apis` commands | in progress |
| M4 | `until`, `fallback`, `dedupe_key`, `switch`, `fetch`, `state`, third-party MCP, signal handling | done |
| M5 | Documentation, example scenarios, goreleaser builds for linux/amd64 and linux/arm64 | in progress |

## Building

Go, a single static binary, no external services and no database. Requires Go 1.25.7 or newer.

```sh
make build      # builds ./baton with version metadata
make test       # go test ./...
make race       # go test -race ./...
make all        # fmt, vet, test, build
make docs       # regenerates docs/{en,ru}/schema.md from the scenario schema
```

## Running

Starting from scratch? The [quickstart](docs/en/quickstart.md) walks through a
first run, the files it leaves behind and the first model call.

```sh
baton validate examples/weekly-report.yaml
baton run examples/weekly-report.yaml \
    -i 'projects=["acme/web", "acme/api"]' -i since=2026-01-01
```

`examples/` holds the scenarios the tests run: `hello.yaml`, `mr-comment.yaml`, `review.yaml`, `weekly-report.yaml`, `triage.yaml` and `llm-smoke.yaml`. The last one needs no forge and no repository token, only a provider key, so it is the shortest way to see that a key, a model name, the structured output mode and the cost accounting all work against the real service:

```sh
export OPENROUTER_API_KEY=...
baton run examples/llm-smoke.yaml
```

The provider, the notification channels and the pricing table live in the global configuration, `~/.config/baton/config.yaml` or `./baton.yaml`, or wherever `--config` points. Secrets are read from the environment or from files at the moment a step needs them.

Every run writes `runs/<id>/`, read back with `baton runs list`, `runs show <id>` and `runs logs <id>`; `baton resume <id>` continues a failed run. `--dry-run` prints the plan, `--json` prints events as JSONL, `--no-cache` ignores cached results, and `baton tools <scenario.yaml> --step ID` prints the tools an agent step would be given without running anything. `baton apis import|validate|call` generates a pack from an OpenAPI 3 document, replays its recorded examples, and calls one operation through a scenario's `apis` entry.

## Documentation

- [Quickstart](docs/en/quickstart.md) — build, first scenario, run artifacts, first model call
- [Project overview](docs/en/overview.md) — the source this README is based on
- [Specification for v1](docs/en/spec.md) — the authoritative technical document
- [Scenario schema reference](docs/en/schema.md) — every field of the format, generated from the JSON Schema
- [Specification review](docs/en/spec-review.md) — inconsistencies and gaps found while reading the spec
- Russian originals: [быстрый старт](docs/ru/quickstart.md), [обзор](docs/ru/overview.md), [ТЗ](docs/ru/spec.md), [справочник схемы](docs/ru/schema.md), [вычитка](docs/ru/spec-review.md), [README](docs/ru/README.md)

## License

[MIT](LICENSE)
