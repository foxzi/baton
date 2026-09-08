# Baton — project overview

> The name is settled: `baton`. Several other projects in the agentic-automation niche use the word too; the binary, the `~/.config/baton` config directory and the `github.com/foxzi/baton` module path stay as they are.

## In one sentence

Baton is a headless runner for YAML scenarios that interleave deterministic steps (scripts, HTTP requests) with non-deterministic steps (LLM calls, running coding agents). One binary, one run per invocation, no UI and no server. Think n8n for teams that would rather keep their workflows as YAML in git and trigger them from CI or cron.

## Why

Tasks shaped like "collect data with a script -> hand it to a model for interpretation -> destructure the result and publish it" are today solved one of two bad ways: either a bash wrapper around `claude -p` in every repository, or a heavyweight platform with a server, a database and a web UI. The first does not scale and controls neither budget nor security. The second is overkill for one-off runs in CI.

Baton fills the gap: a scenario is a file in the repository, a run is a single command, and all run state is a directory on disk.

## Key principles

**No mandatory state between runs.** Baton does not require carrying `state/` from one run to the next. Work results can be saved as files when needed; an ephemeral CI filesystem is not an architectural limitation.

**The runner is deterministic, the agent is a step.** Baton is not an agent. It executes a DAG of steps in a predictable order, and "agency" is encapsulated in the `agent:` step (spawning a CLI agent) or in an `llm:` step with tools (the runner itself drives the tool loop through the API). This makes runs reproducible and lets the budget be tightly bounded.

**Integrations live outside the binary.** Baton knows the protocol (HTTP, JSON, auth schemes, pagination) but not the services. GitLab, GitHub, Jira, Telegram, Confluence, Outline are described by API packs — YAML files in a separate repository, pinned by version and checksum. Packs implement small interfaces (`forge/v1`, `tracker/v1`, `notify/v1`), so one review scenario works against GitLab, GitHub and Gitea by changing a single input. A new integration is a new file, not a new release.

`baton apis import` bootstraps a new pack from an OpenAPI 3 document: it turns the chosen operations' paths, methods and parameters into pack ops, deriving patterns from the schema types (enum, integer, boolean) and carrying over the document's authorisation scheme when it is one the pack format can express (`apiKey`, bearer, basic). The rest — transforms, descriptions, readonly and pagination — is left as TODO markers for the author to fill in, since the document alone cannot tell which fields matter or how a paginated listing should be walked. `baton apis validate` closes the loop: it parses a pack and replays each recorded response in `examples/<op>.json` through the envelope and the operation transform, so a broken jq expression is caught in CI rather than on the next real call. An operation that declares `implements` is checked against the interface registry: the argument names must be the ones the interface names, an operation must not require an argument the interface calls optional, and the transformed example must match the interface's result schema. A scenario's `apis` entry may declare the same contract with `interface: forge/v1`, and then the pack it selects is checked for the interface's operations when it loads, so swapping GitLab for Gitea by changing one input fails early and clearly rather than on the first call. `baton apis call <scenario.yaml> <api>.<op> -a k=v` goes the other way and calls the real service through the scenario's `apis` entry, with the same base URL, secret and timeout a run would use; values that are not strings are passed as `-a k:=<json>`.

**The model never sees credentials.** Every authorised call to an external API goes through a built-in gateway: the agent gets narrow tools like `gitlab.get_mr_changes(iid)`, and the runner supplies the tokens. Publishing results is a separate deterministic step with its own permissions. Secrets are a distinct type in the value system, and any attempt to interpolate them into a prompt is rejected at scenario validation time.

**Capability instead of access.** The agent is not given `curl`, `bash` or network access. Instead the scenario declares parameterised commands (`test`, `lint`) that become tools with validated arguments and run without a shell.

**A structural contract between steps.** Every LLM step returns JSON matching a schema. Branching is only allowed on validated fields, ideally on an `enum`. Invalid model output is a distinct error class with its own retry strategy.

**Fail-fast by default, always notify.** Any failing step fails the run unless stated otherwise. The `on_failure` section runs for any error class, including budget exhaustion.

**Triggers are external.** Baton knows nothing about schedules or webhooks. Scheduling is cron, a systemd timer, or a schedule in CI. That is what separates a CLI tool from a platform.

## Capabilities

### Step types

| Step | Purpose |
|---|---|
| `run` | Execute a command or script without a shell (argv), capturing stdout/stderr and the exit code |
| `http` | Call an API pack operation (`forge.post_comment`) or a raw HTTP request, credentials from the secret store |
| `llm` | A single model call through Anthropic, OpenAI, OpenRouter or any OpenAI-compatible API, with a mandatory result JSON schema, a fallback model chain and optional tools |
| `agent` | Spawn a CLI agent (Claude Code in v1) with skills, tools and MCP servers |
| `foreach` | Parallel processing of a list with control over the degree of parallelism and partial success |
| `until` | A bounded loop with an exit condition (e.g. "keep fixing until the tests pass") |
| `assert` | Deliberately fail the run with a distinct exit code, to block a merge in CI |

### Flow control

- `when:` — a condition on a step, using the expr-lang expression language, typed and free of side effects
- `switch:` / `cases:` — multi-way branching, expanded at load into one `when:`-guarded step per case
- Static reference checking: referencing the result of a step that may be skipped without handling `null` is a validation error
- `needs:` — explicit dependencies, for when declaration order is not enough

### Agent tools

- Filesystem: `fs.read`, `fs.glob`, `fs.grep`, `fs.write` for an engine without file tools of its own; reads always allowed, writes only inside the workspace, path deny-lists
- The `claude-code` engine uses its own built-in file tools: the gateway serves it no `fs.*`, and the deny-lists together with the ban on `Bash`/`WebFetch`/`Task` go into the CLI's own `settings.json` and `--disallowedTools`
- Git: `status`, `log`, `diff`, `blame`, `show`, local `commit`; network operations unavailable
- Commands: declared argv commands with argument validation, timeouts and call limits
- API: read-only pack operations from an allowlist only, exposed as narrow tools authorised by the runner
- `fetch`: reading public URLs from a domain allowlist with a size limit
- MCP: third-party servers from an allowlist, spawned by the runner; tools with arbitrary URLs are flagged `unsafe`
- `submit_result`: the result-submission channel, schema-validated inside the agent loop
- `state`: storage shared between runs, scoped to the scenario
- Profiles: `review`, `fix`, `research` as ready-made permission sets

### Error handling

- Classification: `transient`, `schema`, `command`, `timeout`, `budget`, `policy`, `config`
- Retry bound to the class; `schema` is retried with the error text included in context
- `on_error`: `fail` (default), `continue`, `fallback` to a backup step
- Automatic retry of steps with side effects is forbidden without a `dedupe_key`
- Partial success in `foreach` with a `min_success` threshold
- Run-level `on_failure` with notification channels from the global config
- Distinct exit codes: success, execution error, a tripped assert, configuration error, budget exhausted

### Runs, cache, resume

- Every run is a `runs/<id>/` directory with state, artifacts, tool logs and cost
- Step results are cached by a hash of the step definition and its inputs: a repeat run does not pay for tokens again
- `baton resume <id>` — continue from the failed step
- An audit log of every tool call made by the agent
- `baton tools <scenario.yaml> --step ID` — print the tools an agent step would be given, without running it

### Budgets and observability

- Per-step and per-run limits: tokens, dollars, number of agent turns, time
- Run cost recorded in the state and in notifications
- A structured event log (JSONL), with secret redaction across every output

## Use cases

**Code review in CI.** A job in GitLab CI, GitHub Actions or Gitea calls `baton run review.yaml -i mr=$CI_MERGE_REQUEST_IID`. The scenario classifies the change, picks a review depth, runs an agent with read-only access, publishes findings as a separate step, and blocks the merge if there are blockers. One scenario works for every forge; only the invocation is forge-specific.

**Periodic reports.** Cron runs a scenario that walks repositories or systems, collects data with scripts, hands it to a model for summarisation and sends the report to Telegram, Slack or email. One failing source does not sink the report but is recorded in it.

**Triage and classification.** Parsing incoming tickets, incidents, issues: classification against an `enum`, routing via `switch`, creating tickets through `http` steps with write credentials the model never sees.

**Automated fixes with verification.** The `fix` profile: an agent fixes code in the workspace, runs declared test commands, the `until` loop is bounded by a number of iterations, the result is a diff artifact, and applying it is a separate step.

**Compliance checks.** Auditing dependencies, licenses, configurations: deterministic steps gather facts, an LLM step interprets them and produces a summary, `assert` stops the pipeline on violations.

## What Baton is not

- Not a UI and not a platform: no web interface, no server, no database
- Not a scheduler: no built-in cron or webhooks (possibly in the future, as `baton serve`)
- Not an agent: it does not decide which APIs to call; that is the scenario's job
- Not a CI replacement: deterministic builds and tests stay in CI, Baton adds the steps that need interpretation

## Positioning versus alternatives

| | Baton | gh-aw | Kestra | Dagu | Dagger |
|---|---|---|---|---|---|
| Scenarios in YAML | yes | md + frontmatter | yes | yes | no, code |
| Serverless | yes | yes | no | yes | yes |
| Forge-agnostic | yes | no, GitHub only | yes | yes | yes |
| LLM steps | yes | yes | yes | no | yes |
| Credentials isolated from the model | by design | safe-outputs | no | no | containers |
| Per-run budget | yes | yes | no | no | no |
| Ops burden | one binary | zero | JVM + DB | one binary | daemon |

## Technology

Go, a single static binary. Dependencies: a YAML parser, expr-lang for expressions, gojq for transforms in packs, a JSON Schema validator, the Go SDK for MCP, model provider SDKs (Anthropic, OpenAI, OpenAI-compatible including OpenRouter), a Markdown renderer for `md2html`/`md2text` and an OpenAPI parser for `baton apis import`. No external services and no databases. Integrations with services live in a separate packs repository.

## Status

The technical specification for the first version is a separate document ([spec.md](spec.md)), and the implementation follows its milestones. Working: the core, the HTTP layer with packs, the model providers and `llm` steps, `foreach`, the cache, `resume`, notifications, `agent` steps with the MCP gateway, the tool profiles and proxied third-party MCP servers, the interface registry with the `baton apis` commands, and the `until` and `switch` steps. Outstanding: the separate `baton-apis` packs repository and the release builds. The [roadmap in the README](../../README.md#status) tracks the state milestone by milestone.
