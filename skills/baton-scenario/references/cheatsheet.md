# Baton scenario cheatsheet

Sources: docs/en/schema.md (generated from the JSON Schema, authoritative for field names), docs/en/spec.md, examples/, apis/, internal/. Citations: `(spec §N)`.
Binary: `make build` -> `./baton` (Makefile: `go build ... -o baton ./cmd/baton`).

## 1. Scenario skeleton

Unknown top-level keys are rejected ("No other fields are allowed" applies to every object).

```yaml
version: 1                 # required, must be 1
name: my-scenario          # optional
description: ...           # optional
inputs:   { <name>: { type, required, default, pattern } }
defaults: { engine, model, timeout, budget_usd }   # applied to steps that do not set the field (spec §3.1)
budget:   { usd, time, tokens }                    # whole run
secrets:  { <name>: { from: env|file, key, path, trim } }
env:      { NAME: "template" | { secret: name } }  # env of EVERY process: run, agent, commands; step env of same name wins
apis:     { <name>: { interface, pack, from, sha256, config, auth: {secret}, timeout } }
commands: { <name>: { argv, description, timeout, args, parse, readonly, max_calls, env } }
steps:      [ ... ]        # required, >= 1
on_failure: [ ... ]        # optional
```

| Key | Required | Notes |
|---|---|---|
| `version` | yes | only `1` |
| `steps` | yes | at least 1 item |
| `apis.<n>.pack`, `apis.<n>.from` | yes (inside an api) | `pack` may be a template; `from` is a local dir or git source with a version pin |
| `secrets.<n>.from` | yes (inside a secret) | `env` or `file` |

Input fields (schema.md `input`): `type` = `string|int|number|bool|list|map`, `required`, `default`, `pattern` (regex).
`pattern` is required for every string input that ends up in argv (spec §3.1, §4 check 8).
`duration` = Go duration string (`10m`) or number of seconds. `size` = bytes, optional `k|m|g` suffix (powers of 1024), or integer.
Paths to prompts, schemas, templates, skills are relative to the scenario file (spec §3.1); paths of `file` steps are relative to the workspace (schema.md `template`).
`baton init <dir>` makes a self-contained example; `baton schema` prints the JSON Schema; editor setup: examples start with `# $schema: ./scenario.schema.json` (hello.yaml).

## 2. Templating & expressions

### 2.1 Templates `{{ }}` (text/template; spec §5.2)
- Escapes nothing: pipe through `html` where output is HTML. A missing key renders empty, not an error: guard with `default` or an `assert` (schema.md `template`).
- Functions (exact list, spec §5.2): `render(path, data)`, `toJSON`, `fromJSON`, `coalesce`, `default`, `trunc(n)`, `indent(n)`, `join`, `dict`, `quote`, `md2html`, `md2text`. (Standard text/template builtins such as `index`, `range`, `len`, `html` are also used in examples: `index .steps.x.result 0`.)
- `render "templates/x.html" .` : path resolved against the scenario file's dir, confined to it (absolute path, `..` segment or escaping symlink = render error) (spec §5.2, schema.md).
- Printing a secret value is a render error (spec §5.2). Secrets are not in `.`.
- Data:

| Reference | Meaning |
|---|---|
| `.inputs.<name>` | scenario input |
| `.steps.<id>.result` | parsed result (llm/agent/http/run with parse; file read with parse) |
| `.steps.<id>.stdout` / `.stderr` / `.exit_code` | run step |
| `.steps.<id>.items` | foreach / until items |
| `.<as>` | the foreach item, under the name given by `foreach.as` (e.g. `.issue`, `.ticket`; jira-quality.yaml, spec §3.7) |
| `.args.<name>` | argument of a `commands` entry (inside its argv) (spec §7.5) |
| `.run.id`, `.run.name`, `.run.started_at` | run info |
| `.run.failed_step`, `.run.error.class`, `.run.error.message`, `.run.error.stderr_tail`, `.run.duration`, `.run.cost_usd`, `.run.dir` | only in `on_failure` (spec §9.3) |
| `.iter.*` | `until` body receives `iter` in `with` (spec §3.8) |

### 2.2 Expressions (`when:`, `assert.condition`, `until.condition`, `switch:`; expr-lang; spec §5.1)
No `{{ }}`. Context:

```
inputs.<name>
steps.<id>.status          # success | failed | skipped
steps.<id>.result  .stdout  .stderr  .exit_code  .items
steps.<id>.http_status  .headers  .body     # http steps (NOT "status", which is the step status)
run.id, run.name, run.started_at
iter.*                      # only inside until.condition: flat fields of the previous iteration's result:
                            #   status, result, stdout, stderr, exit_code, items (spec §3.8)
```
- Secrets are absent from the expression context (spec §5.1). Runtime evaluation error (nil deref) = `config` error.
- Functions seen in docs/examples: `len(...)`, `filter(list, .field == 'x')` (spec §3.9). The full expr-lang function list is not in the docs: unclear.
- Reference to a step's result from a step without `when` when the source has `when` and no `coalesce`/`default` = validation error (spec §4 check 6). `steps.X` must refer to a step declared EARLIER (check 5).

## 3. Steps

### 3.1 Common fields (spec §3.2; schema.md `step-body`)

| Field | Notes |
|---|---|
| `id` | unique; validator enforces `^[a-z][a-z0-9_]*$` (validate.go `idPattern`; the JSON Schema pattern is looser, `^[a-zA-Z_][a-zA-Z0-9_]*$`: write lowercase). A `switch` carries no id of its own; a foreach `step:` body has none either. |
| `when` | expr-lang boolean; false -> step status `skipped` |
| `needs` | list of EARLIER step ids; default = all preceding steps. Self or later id = error |
| `timeout` | duration |
| `retry` | `{ on: [class...], attempts: N (>=1), backoff: dur }`; classes: `transient schema command timeout budget policy config assert`, but `budget`/`policy`/`config` are rejected as "never retried" |
| `on_error` | `fail` (default) \| `continue` \| `fallback` (needs `fallback:` body, a step body without id) |
| `cache` | bool; default true for `llm` and `run` with `readonly: true`; false for mutating `http`, `agent` with `fs.write` (spec §10.3) |
| `dedupe_key` | template; marks a side effect done in `effects.json` (spec §9.4) |
| `message` + `notify` | `notify: <channel>` + `message:` step = send to a channel of the global config (spec §9.3) |

Exactly ONE body per step: `run http llm agent foreach until file assert gate` (validate.go: "must declare exactly one body"). Note `file` and `gate` exist in the validator/schema though spec §3.2's list omits them.

### 3.2 Kinds at a glance

| Kind | Required fields | Optional fields | Result fields |
|---|---|---|---|
| `run` | `argv` (list, no shell) | `cwd` (default workspace), `env`, `stdin`, `parse` (`text\|json\|lines`), `allow_exit_codes`, `max_output_bytes`, `readonly` | `stdout stderr exit_code result` |
| `http` (pack op) | `op: <api>.<op>` | `args`, `auth` (secret name) | normalized `result` |
| `http` (raw) | `api` or `url`; `method`, `path` | `headers query body expect_status parse max_bytes auth` | `http_status headers body result` |
| `llm` | `schema` | `model` (`<provider>/<model>`, first slash splits), `fallback_models`, `system`, `prompt`, `with`, `tools`, `max_tokens`, `temperature`, `structured_mode` (`native\|tool\|prompt`) | `result` (validated JSON) |
| `agent` | `engine` (`claude-code\|codex\|fake`), `prompt`, `result` (schema file) | `model system with skills profile tools limits max_turns budget_usd allow_unsafe env inherit_auth script workspace` | `result` via `submit_result` |
| `foreach` | `items` | `as max_parallel on_item_error min_success` + `step` OR `steps` | `items` = list of `{status,result,error}` |
| `until` | `condition`, `max_iterations` (>=1), `step` | | last iteration result + `iterations` |
| `assert` | `condition` | `message` | false -> exit 2, no `on_failure` |
| `file` | exactly one of `read write append glob` | `content` (write/append only), `parse`, `max_bytes` (read only, default 1MiB) | read: `result` |
| `gate` | `message` | `notify` (channel) | `result.decision` = approved\|rejected, `result.reason` |
| `switch` + `cases` | `switch` (expr), `cases` (>=1) | `default` | sugar over `when`; expanded at load |

### 3.3 `run` (spec §3.3)
```yaml
- id: diff
  run:
    argv: ["git", "diff", "{{ .inputs.base }}...HEAD"]   # input needs a `pattern` (see §9, E3)
    env: { TOKEN: { secret: gitlab_ro } }                # or "template string"
    parse: json         # text (default) | json | lines; also for http.response and agent.commands
    readonly: true      # allows retry + cache by default
```
- No `sh -c`. `run: "a string"` is accepted only without shell metacharacters, split on whitespace, with a warning.
- Non-zero exit not in `allow_exit_codes` (default `[0]`) = `command` error.
- Passing values between steps is template-only: `env: { VERSION: "{{ index .steps.version.result 0 }}" }` after a `parse: lines` step. Child-process env changes never propagate (spec §3.3).
- Retry without `readonly: true` and without `dedupe_key` = warning (validate.go).

### 3.4 `http` (spec §3.4)
```yaml
- id: publish
  http:
    op: forge.post_comment            # <api>.<op>
    args: { project: "{{ .inputs.project }}", id: "{{ .inputs.mr }}", body: "..." }
  dedupe_key: "review-{{ .run.id }}"
```
- Pack form and raw form (`api`/`url`, `method`, `path`) cannot mix: "op cannot be combined with api or url; use one form".
- Args are checked against the op's `params` (required, pattern, max_len, enum, path-dot rule; §9).
- POST/PUT/PATCH/DELETE or non-readonly op without `dedupe_key` = warning and no auto-retry.
- The response status is `http_status`, not `status` (that is the step status).
- Pack ops with side effects are never given to agents; only `http` steps can call them (spec §3.4).

### 3.5 `llm` and `agent` (spec §3.5, §3.6)
- `llm.prompt`/`system` may be a file path (relative to the scenario) or inline text. A path-looking value that is not a file is sent as the prompt itself and produces a WARNING (see §9, W1).
- `llm.schema`, `agent.result`, `agent.skills[]/SKILL.md`: files must exist or validation fails (E: `file X not found`).
- Invalid output = `schema` error; retried once by default with the error text appended. Agent that finishes without `submit_result` = `schema` error "result not submitted".
- Agent profiles (spec §7.2): `review` (read-only fs, git read, apis by list, state read), `fix` (fs.write workspace, git read+commit, exec commands), `research` (fetch by allowlist, mcp by list, state read-write). `tools:` overrides individual fields.
- `llm.tools` entries and `agent.tools.apis` are `<api>.<op>`; only `readonly: true` ops are allowed.

### 3.6 `foreach` / `until` / `switch` / `gate`
```yaml
- id: per_ticket
  foreach:
    items: "{{ .steps.tickets.result }}"
    as: ticket
    max_parallel: 3
    on_item_error: continue      # fail (default) | continue
    min_success: 0.8             # 0..1, with continue
    steps:                       # OR `step:` (not both)
      - { id: classify, llm: { ... } }
- id: fix
  until: { condition: "iter.exit_code == 0", max_iterations: 3, step: { agent: { ... } } }
- switch: steps.classify.result.risk
  cases: { high: { id: deep, agent: { ... } }, low: { id: light, llm: { ... } } }
  default: { id: skip_note, run: { argv: ["echo", "skipped"] } }
- id: approval
  gate: { message: "Apply for {{ .inputs.project }}?", notify: ops }
- id: apply
  needs: [approval]
  when: 'steps.approval.result.decision == "approved"'
  run: { ... }
```
- foreach `steps:` body: every step needs an id, ids must not repeat any top-level id, body steps are invisible after the foreach, a gate and a switch are not allowed inside (spec §3.7). Dirs: `steps/<foreach>/<index>/<body id>/`.
- `switch`: when the subject is an `llm` field with an `enum`, cases must cover it or a `default` is required.
- `gate`: top-level only (not in foreach/until/fallback), no `retry`/`cache`/`dedupe_key`. Exit 5, status `waiting`; continue with `baton resume <id> --approve|--reject [--reason T]`. Rejection is not a failure: branch with `when`, add `assert` to fail (spec §3.11).
- `assert` false: exit 2, `on_failure` NOT run (spec §3.9).
- `on_failure:` list (scenario-level): runs on any failed run (incl. budget and runtime config error), not on exit 2; no retry, 60 s timeout each, context `.run.failed_step .run.error.class .run.error.message .run.error.stderr_tail .run.duration .run.cost_usd .run.dir` (spec §9.3).

## 4. Secrets and env (spec §6, §3.1)
```yaml
secrets:
  gitlab_ro: { from: env,  key: GITLAB_READ_TOKEN }
  jira:      { from: file, path: /run/secrets/jira, trim: true }
```
- All secrets resolve at startup; a missing one = exit 3. Values are redacted to `***` (also base64 / URL-encoded / JSON-escaped forms) in logs, `run.json`, cache entries.
- Allowed only in `http.auth`, `apis.*.auth.secret`, `run.env.<N>.secret` / `agent.env.<N>.secret` / top-level `env`, `notify` secrets (spec §4 check 7). NOT in `llm.prompt|system|with`, `agent.prompt|with`, `run.argv|stdin`, `assert.message`, `on_failure.*.message`.
- Templates cannot see secrets: `templates cannot reference secrets` (tmpl.go); rendering a secret value is a render error.
- `env` entry forms: `"template"` or `{ secret: name }`; empty secret name = parse error `env entry needs a secret name`; undeclared name = `undeclared secret "x"`.
- The model-provider key is the only secret that reaches an agent process (spec §6). codex: name the credential in the step `env`, or `inherit_auth: true` (copies the user's `auth.json`; codex only) (spec §8.2).
- Scenario `env:` is merged under each step's/command's own `env`; MCP servers do NOT receive it.

## 5. Global config, providers, engines (spec §8, §12)
Files: `~/.config/baton/config.yaml`, then `./baton.yaml` (local wins); `--config FILE` is repeatable. Keys: `apis secrets notify providers defaults on_failure pricing mcp_servers`.

| Provider `kind` | Notes |
|---|---|
| `anthropic`, `openai` | `api_key: { from: env, key: ... }`; `openai` accepts `base_url` |
| `openrouter` | model name has a slash: `openrouter/google/gemini-2.5-flash` (first slash separates provider) |
| `openai_compatible` | arbitrary `base_url`, `capabilities: { structured_output, tools }` (Ollama, vLLM...) |

- `llm.model` / `fallback_models[]` must be `<provider>/<model>`; the provider name is a key in `providers`.
- Structured output order: native JSON schema -> forced `submit` tool -> prompt + validation; recorded as `structured_mode` in `output.json`; override with `structured_mode:` (spec §8.3).
- `pricing: { "<provider>/<model>": { input_per_mtok, output_per_mtok } }` estimates cost when the provider returns none. Missing model = WARNING (W2), `cost_usd: null`, dollar budget not checked for that step (tokens still are via `budget.tokens`). The check resolves `llm.model`, then scenario `defaults.model`, then config `defaults.model`, and also covers `fallback_models`.
- Agent engines: `claude-code` (runs `claude -p`; always allows Read/Glob/Grep, forbids Bash/WebFetch/WebSearch/Task), `codex` (`codex exec --json`; sandbox-based; deny-lists inside workspace and `skills` NOT supported; no `max_turns`/`budget_usd` flags, bounded by `limits.max_tool_calls` + timeout; `cost_usd` stays null), `fake` (tests; behaviour file via `agent.script`) (spec §8.2, §8.4).
- Notification channels (`notify:` in config): `{ api: <pack with notify/v1>, target }`, `{ kind: webhook, url }` (url may be a `{ secret: }`), or `{ kind: stdout }` (prints the message; handy for local testing).

## 6. Budgets, errors, exit codes, cache, resume (spec §9, §10)

Budgets: `defaults.budget_usd` (per step default), `budget: { usd, time, tokens }` (whole run), `agent.budget_usd`, `agent.max_turns`, `agent.limits.{max_tool_calls,max_result_bytes}`.

| Error class | Source | Default retry |
|---|---|---|
| `transient` | network, HTTP 408/429/5xx, provider timeouts | yes, 2 attempts, backoff from 5 s |
| `schema` | result fails JSON Schema; `submit_result` not called | yes, 1 attempt, error text added to context |
| `command` | `run` exit code not allowed; `http` unexpected 4xx | no |
| `timeout` | step over `timeout` | no |
| `budget` | any budget/turn/tool-call limit | never |
| `policy` | `unsafe` tool call; arg outside `pattern` | never |
| `config` | validation error; runtime expression error | never |

| Exit | Meaning |
|---|---|
| 0 | success |
| 1 | execution error (transient, schema, command, timeout, policy) |
| 2 | an `assert` fired |
| 3 | config / validation / secrets error |
| 4 | budget exhausted |
| 5 | stopped at a `gate`, waiting for `resume --approve/--reject` |
| 130 | interrupted (SIGINT/SIGTERM) |

- `on_error: continue` -> step `failed`, `result` null, later steps see `steps.X.status == "failed"`. `fallback` -> body runs under the same id, `fallback_used: true`.
- Side-effect steps (non-GET/HEAD `http`, `run` without `readonly`, `agent` with `fs.write`) are not auto-retried without `dedupe_key`.
- Run dir `runs/<id>/` (next to the scenario; `--runs-dir`, `BATON_RUNS_DIR`): `run.json events.jsonl effects.json cost.json steps/<id>/{input.json,output.json,stdout.log,stderr.log,tool-calls.jsonl,artifacts/}`. Run id `YYYYMMDD-HHMMSS-<4hex>`.
- Cache: `cache/` next to `runs/`; key covers definition, rendered inputs, env digest, prompt/schema/skill file hashes, pack checksum, engine, model, baton version. `--no-cache` disables reads only.
- `baton resume <id>`: replays `success` steps whose definition hash still matches (edited steps re-run, event `step_changed`); new run id gets `-r1`, `-r2`. Secrets re-resolve.

## 7. Packs and interfaces (spec §7.4; apis/*/pack.yaml; internal/ifaces/data/*.json)

Wire into a scenario:
```yaml
apis:
  forge:
    interface: forge/v1                  # optional contract check
    pack: gitlab                         # may be a template, e.g. "{{ .inputs.forge }}" (then ops are only checked at run time: warning)
    from: ./apis/                        # local dir (no sha256) or git source @tag + sha256
    config: { base_url: https://gitlab.example.com/api/v4 }
    auth: { secret: forge_ro }
    timeout: 20s
```
Ops are `<api>.<op>`. Agents get only `readonly: true` ops listed in `tools.apis`. Pack `auth.kind`: `header | bearer | basic | query | path | exchange`. Pagination styles: `link_header | page | offset | cursor` (`max_pages` default 20).

Shipped packs (op: R = readonly / W = mutating; `*` = declares `implements`):

| Pack | Auth / config | Ops |
|---|---|---|
| `gitlab` | header `PRIVATE-TOKEN`; `base_url` (default gitlab.com/api/v4) | R `get_change*` `list_files*` `get_file*` `list_merge_requests`; W `post_comment*` `post_discussion` |
| `github` | bearer; `base_url` default api.github.com | R `get_change` (NOT implements: no file list; pair with `list_files`) `list_files*` `get_file*`; W `post_comment*` `post_review*` |
| `gitea` | bearer; `base_url` required | R `get_change` (not implements) `list_files*` `get_file*`; W `post_comment*` `post_review*` |
| `jira` (Cloud) | basic; `base_url`, `user` required | R `get_issue*` `search*`; W `comment*` `create_issue*` |
| `jira-server` | bearer; `base_url` required | R `get_issue*` `search*` `search_all` `search_summary` `list_boards` `list_board_issues` |
| `slack` | bearer; `base_url` default slack.com/api | W `send*`; R `auth_test` |
| `telegram` | path (`bot{auth}`) | W `send*` `send_document`; R `get_me` |

Interfaces (built in; `ifaces.Names()` lists them): argument names are exact.

| Interface.op | Args (req = required) | Result fields (req) |
|---|---|---|
| `forge/v1.get_change` | project, id | id, title, files (req); description, author, base, head |
| `forge/v1.list_files` | project, id | array |
| `forge/v1.get_file` | project, path, ref (opt) | content (req); path, ref |
| `forge/v1.post_comment` | project, id, body | id (req); url |
| `forge/v1.post_review` | project, id, summary (opt), comments[{path, line, body}] (req) | id (req); url |
| `tracker/v1.get_issue` | key | key, title (req); status, body, author, url |
| `tracker/v1.search` | query, limit (opt) | array |
| `tracker/v1.create_issue` | project, title, body (opt), type (opt) | key (req); url |
| `tracker/v1.comment` | key, body | id (req); url |
| `notify/v1.send` | target, text, format (opt) | id (req); target, url |

- Optionality above is from `internal/ifaces/data/*.json` (`required` flags); `post_review.summary` is optional.
- A scenario with `interface:` fails validation if the chosen pack does not implement ALL ops of it. Use the interface only if every op you need is declared `implements` (e.g. gitlab has no `post_review`: use `post_comment` in a `foreach`).
- Pack-only ops take their own params (e.g. gitlab `post_discussion`: project, id, body, path, line, base_sha, head_sha, start_sha, optional position_type). Check `apis/<pack>/pack.yaml` and `baton tools`/`baton apis call` before guessing.

## 8. CLI and build (spec §11; Makefile)

| Command | Purpose |
|---|---|
| `baton validate <scn> [--json]` | validation only (one scenario per call); exit 0 ok / 3 errors. Prints `<scn>: valid` or ALL errors as `scn:LINE: path: message (step id)`; warnings separately. Also checks packs (`apis` ops/args) and warns on unpriced models |
| `baton doctor <scn>` | scenario + config + providers + files, runs no step |
| `baton run <scn> [-i k=v]... [--input-file f.json] [--run-id ID] [--runs-dir D] [--workspace D] [--config F]... [--cache-dir D] [--no-cache] [--dry-run] [--json] [-v]` | `--dry-run` = validate, resolve secrets, print inputs + step plan; runs nothing (fails with `...api_key: environment variable X is not set` if a loaded config names an unset key). Defaults: runs dir `runs/` next to the scenario (or `$BATON_RUNS_DIR`), workspace = scenario dir. Exit: 0 ok, 1 step failure, 2 assert, 3 config, 4 budget, 5 waiting at a gate, 130 interrupted |
| `baton resume <id> [--runs-dir D] [--json] [-v] [--approve \| --reject] [--reason T]` | continue a failed or `waiting` run; a waiting run needs `--approve` or `--reject` |
| `baton runs list [-n N \| --limit N] \| show <id> \| logs <id> [--step ID] \| prune [--keep N] [--older-than 30d] [--dry-run]` | all take `--runs-dir D` (default `$BATON_RUNS_DIR` or `./runs` in the CWD, NOT next to the scenario: pass it when they differ); `list`/`show` take `--json`. `prune` needs `--keep` or `--older-than`; `running`/`waiting` runs are kept unless older than `--older-than` |
| `baton tools <scn> --step ID [-i k=v]... [--input-file f] [--config F]... [--workspace D] [--json]` | what an agent step will see (tool names, schemas); `--step` required |
| `baton schema [--markdown en\|ru]` | scenario JSON Schema on stdout; `--markdown` prints the reference (docs/*/schema.md are generated from it) |
| `baton init <dir> [--template hello\|summarize] [--provider P] [--model M]` | self-contained example scenario (`--provider`/`--model` only for `summarize`) |
| `baton apis validate <pack.yaml\|dir>...` / `import --openapi f --ops a,b [--interface I] [--name N]` / `call <scn> <api>.<op> [-a k=v \| -a k:=json]... [-i k=v] [--input-file f] [--config F]` | pack contract check (replays `examples/<op>.json`) / stub generator to stdout / manual call |
| `baton version` | version, commit, build date, Go version |
| `baton help [cmd]` | usage |

Makefile (`BINARY := baton`, `CMD := ./cmd/baton`):

| Target | Does |
|---|---|
| `make build` | `go build -ldflags '-s -w -X ...version.{Version,Commit,Date}' -o baton ./cmd/baton` |
| `make test` / `race` / `vet` / `fmt` / `lint` | `go test ./...` / `-race` / `go vet` / `gofmt -l -w .` / `golangci-lint run` |
| `make validate-examples` | builds, then `./baton validate` on each `examples/*.yaml` (copy a new scenario there to check it) |
| `make validate-apis` | builds, then `./baton apis validate apis/*` |
| `make docs` / `docs-check` | regenerate / verify docs/{en,ru}/schema.md and examples/scenario.schema.json; edit the schema, never the generated files |
| `make check` | `fmt-check vet lint docs-check race validate-apis validate-examples` (what CI runs; needs golangci-lint) |
| `make e2e` | live provider/claude-code tests, cost money; sets `BATON_E2E=1` itself |

Shell loop to validate while iterating: `./baton validate path/to/scenario.yaml; echo $?`.

## 9. Verbatim validation messages: cause and fix

Format printed by `baton validate`: `error: <scn>:<line>: steps[<i>].<kind>.<field>: <message> (step <id>)` (verified by running it); the table lists the message part. `%q` shows as a quoted string. E = error (exit 3), W = warning (run still allowed). Sources: internal/scenario/{validate,apicheck,refs,parse}.go, internal/packs/params.go, cmd/baton/pricing.go.

| # | Message | Cause | Fix |
|---|---|---|---|
| E1 | `must declare exactly one body, got run, llm` (also `must declare one of run, http, llm, agent, foreach, until, file, assert, gate`; the text omits `switch` and `notify`, which are valid bodies too) | two or zero body keys on a step | keep one body key; split into two steps |
| E2 | `"Review" must match ^[a-z][a-z0-9_]*$` / `duplicate step id "x"` | bad or repeated step id | lowercase, digits, `_`, starts with a letter, unique (also vs. foreach body ids: `step id "x" is already used by a step of the scenario`) |
| E3 | `input "who" reaches a command argument, so it needs a pattern` | string input referenced in `run.argv` (or a command argv) with no `pattern` | add `pattern: "^[A-Za-z]+$"` on the input; non-string inputs are exempt |
| E4 | `args.<name>: a path argument cannot climb with . or ..` (raised when the op's args are bound, packs/params.go; at `baton run` time, not by `validate`) | an arg that lands in the URL path (`in: path`, no `encode: path`) has a `.` or `..` segment (split on `/`) | pass a clean value; for a value that is one whole segment containing `/` (GitLab `project`), the pack param must declare `encode: path` (it is then percent-escaped and the dot check is skipped) |
| E5 | `args.<name>: does not match <regex>` / `exceeds max_len` / `must be one of ...` | arg violates the op param `pattern`/`max_len`/`enum` | pass a conforming value; fix `pattern` in the pack if it is wrong |
| E6 | `file prompts/x.md not found` | `llm.schema`, `agent.result`, `agent.skills[]` (`<dir>/SKILL.md`) path missing; paths are relative to the SCENARIO file (templated paths are skipped) | create the file or correct the relative path |
| W1 | `file prompts/x.md not found, so the value is sent as the prompt itself` | `prompt`/`system` has no spaces/`{{`, ends in `.md .txt .tmpl .prompt`, and no such file | create the file or fix the path; otherwise the literal path text is the prompt |
| W2 | `model <provider>/<model> is not in pricing: cost_usd stays null and the dollar budget is not checked for it` | model has no `pricing` entry in the config | add `pricing."<provider>/<model>": { input_per_mtok, output_per_mtok }` |
| E7 | `references step "x", which is not declared before this step` / `references undeclared step "x"` | template/expr uses `steps.x` declared later or never | reorder steps; fix the id |
| E8 | `reads result of step "x", which runs under a when:; guard it with coalesce/default or give this step a when: too` (field name appears instead of `result`) | step without `when` reads an optional step's output | `{{ coalesce .steps.x.result "..." }}` / `default`, or copy the `when:`; `.status` is always safe |
| E9 | `unknown or later step "x"` / `step "x" cannot need itself` | bad `needs` | `needs` may name earlier steps only |
| E10 | `templates cannot reference secrets` / `undeclared secret "x"` | `.secrets` in a template, or `{secret: x}` / `auth` / `api.auth.secret` not in `secrets:` | pass secrets only via `env: {N: {secret: x}}` or `auth`; declare the secret |
| E11 | `on_error: fallback requires a fallback body` | `on_error: fallback` without `fallback:` | add a step-body `fallback:` |
| E12 | `max_iterations is required` / `must be at least 1` | `until` without a bound | set `max_iterations >= 1` |
| E13 | `does not cover <values> of the subject enum; add the missing cases or a default` (W: `no default case; every value of the subject must be covered`) | `switch` on an enum field missing values | add cases or `default:` |
| E14 | `op cannot be combined with api or url; use one form` / `must set op, api or url` / `"x" must be <api>.<op>` / `unknown api "x"` | mixed or incomplete `http` | use either `op: api.op` or raw `api`/`url` + `method` + `path` |
| E15 | `operation <api>.<op> has no such argument` (path `...args.<name>`) / `operation <api>.<op> requires the argument <name>` | `http.args` differs from the pack op `params` | match names in `apis/<pack>/pack.yaml` (the pack must load: `from` + pin) |
| E16 | `operation <api>.<op> is not readonly and cannot be given to a model` | a mutating op listed in `llm.tools` / `agent.tools.apis` | remove it; do the write in an `http` step |
| W3 | `<METHOD> without dedupe_key cannot be retried safely` / `operation x is not readonly, so without dedupe_key it cannot be retried safely` / `retry on a step without readonly: true or dedupe_key may repeat side effects` | side-effect step with `retry` or no key | add `dedupe_key` (or `readonly: true` for `run`) |
| E17 | `"x" must be <provider>/<model>` / `unknown engine "x", want claude-code, codex or fake` / `unknown profile "x", want review, fix or research` | malformed model/engine/profile | use `anthropic/claude-sonnet-4-6` style; valid engines/profiles as listed |
| E18 | `declare either step or steps, not both` / `must declare a body: step or steps` / `must be between 0 and 1` (`min_success`) / `unknown value "x", want fail or continue` (`on_item_error`) | bad `foreach` | pick one body form; `min_success` is a fraction |
| E19 | `class "budget" is never retried` (also `config`, `policy`) / `unknown error class "x"` / `.attempts: must be at least 1` | bad `retry` (note `retry` without `attempts` fails) | `retry: { on: [transient, schema], attempts: 2 }` |
| E20 | `must set exactly one of read, write, append, glob` / `write requires content` / `content belongs to write and append, not to read` | bad `file` step | one op key; `content` only with write/append; `parse`/`max_bytes` only with read |
| E21 | `must declare from: env or from: file` / `unknown source "x", want env or file` / `env entry needs a secret name` | bad `secrets` / `env` entry | `{ from: env, key: X }`, `{ from: file, path: P }`; `env` value is a string or `{ secret: name }` |
| E22 | `must be 1, got N` (`version`) / `must declare at least one step` / `unknown type "x", want one of ...` (input) | scaffolding errors | `version: 1`; at least one step; types `string int number bool list map` |
| W4 | `pack name is a template, so its operations are only checked at run time` | `apis.<n>.pack` contains `{{ }}` | expected; validate each candidate pack separately |

Verified live: an input without `pattern` in `run.argv`, a missing `schema` file = errors (exit 3); a missing `prompt: prompts/nope.md` and an unpriced model = warnings only, listed before errors.

Unclear from sources: the exact text of expr-lang compile errors for bad `when:` (comes from `expr.CompileBool`, not in baton's code) and template parse errors (`parse <path>: ...` from text/template).

## 10. Which example to copy (examples/*.yaml; `make validate-examples` checks all of them)

- `hello.yaml` - `run` + `assert`; no packs, no secrets, no network, no model: the smallest runnable shape and the `pattern` rule for argv inputs.
- `mr-comment.yaml` - `http` x2 (`gitlab`: get_change, post_comment) + `run` (argv with env) + `assert`; needs env `GITLAB_TOKEN` and a GitLab; no model.
- `jira-report.yaml` - `http` (`jira-server` search_summary) + `file` write + `assert`; needs `JIRA_TOKEN` and a Jira Server; no model.
- `jira-quality.yaml` - `http` (`jira-server` list_board_issues) + `foreach` over an `llm` step + `file` + `assert`; `cache: true`; needs `JIRA_TOKEN` and an OpenRouter key.
- `llm-smoke.yaml` - `llm` x2 (plain + JSON schema, `fallback_models`) + `notify` + `on_failure`; no packs; needs one provider key (OpenRouter), prices the model.
- `weekly-report.yaml` - `foreach` of `http` (`gitlab` merged MRs) + `llm` digest + `notify` + `on_failure`; cron-style, cached; secret `gitlab_ro` (env `GITLAB_TOKEN`), a provider key and a `report` channel in the global config.
- `triage.yaml` - `http` x3 (`gitlab`, raw requests with `dedupe_key`) + `llm` classify + `switch` with cases (`notify` on critical, `assert` default) + `on_failure`; needs GitLab token, provider key, `alerts` channel.
- `review.yaml` - `http` (`gitlab`) + `agent` (`engine: claude-code`, `profile: review`, `tools.apis`, `result` schema) + `http` post + `assert` + `on_failure` comment; needs `GITLAB_TOKEN`, `ANTHROPIC_API_KEY`, the `claude` binary (default of `internal/agent/claudecode`) and a checkout.
- `review-deep.yaml` - `http` + `llm` split + `foreach` of `agent` with `on_error: fallback` to an `llm` + inline-comment `foreach` over `gitlab` post_discussion + `assert` exit 2 on blocking; same needs as `review.yaml`, bigger budget.

None of the examples uses `engine: fake`: it needs a behaviour script (internal/engine/agent.go), so use `hello.yaml` for offline checks.
