# Baton v1 — Technical Specification

A document for a single developer. The decisions listed here have already been made; they should not be revisited without a good reason. Contentious points are collected in the "Open Questions" section. The scope is sized for 9-11 weeks of work by one person.

## 1. Goal and Scope of v1

Goal: a working binary that can (a) run MR code review from CI on GitLab and GitHub, and (b) assemble a weekly report across several repositories via cron. Everything else is for the second version.

### In scope for v1

- CLI `baton` with commands `run`, `validate`, `resume`, `runs`, `tools`, `schema`
- Steps: `run`, `http`, `llm`, `agent`, `foreach`, `until`, `assert`
- Expressions `when:`, `switch:`, string templates
- Typed values, the `secret` type, secret providers `env` and `file`
- API packs (section 7.4): a loader with version pinning and checksum, auth schemes including `exchange`, response envelopes, four pagination strategies, jq transforms (gojq), `graphql` operations; interfaces `forge/v1`, `tracker/v1`, `notify/v1`
- The gateway as an MCP server, tools from packs, `commands`, `fetch`, `state`, `submit_result`
- The `baton apis import` command — generating a pack stub from OpenAPI
- A separate `baton-apis` repository with starter packs: `gitlab`, `github`, `gitea` (`forge/v1`), `jira` (`tracker/v1`), `telegram`, `slack` (`notify/v1`)
- Engines: `claude-code` and `codex` for `agent:`; providers `anthropic`, `openai`, `openrouter`, and any OpenAI-compatible endpoint for `llm:`; `fake` for tests
- Error classes, retry, `on_error`, `fallback`, `on_failure`, exit codes
- Run directory, cache, `resume`, tool audit log, secret redaction
- Per-step and per-run budgets
- Notification channels as pack operations with the `notify/v1` interface, plus built-in `webhook` and `stdout`

### Out of scope for v1 (deliberately)

- Docker sandbox and `exec.mode: shell` mode
- `baton serve`, webhooks, built-in scheduling
- The `gate` step waiting for a human
- CLI agents other than `claude-code` and `codex`
- Secret providers `vault`, `sops`, `1password`
- Parallel execution of independent DAG branches (parallelism only inside `foreach`)
- Packs for Outline, Confluence, MediaWiki and others — the format supports them (section 7.4.6), the packs themselves are written as needed outside the v1 plan
- Composite operations in packs (multiple calls in one operation) — a loop is done with `foreach` in the scenario
- Multipart file upload through packs
- Cross-platform support: v1 runs on Linux, macOS is not tested

## 2. Architecture

```
cmd/baton              CLI (cobra or stdlib flag)
internal/scenario      YAML parsing, validation, scenario JSON Schema
internal/expr          wrapper around expr-lang, expression context
internal/tmpl          text/template + functions, secret type protection
internal/values        value type system (string, number, bool, list, map, secret, file)
internal/secrets       providers, resolution, redactor for logs
internal/engine        engine interface; claudecode/, anthropic/, fake/
internal/steps         run, http, llm, agent, foreach, until, assert
internal/tools         tool catalog, schema generation, execution
internal/packs         API pack loading and validation, interface registry, checksums
internal/httpx         generic HTTP client: auth schemes, exchange, pagination, envelope, jq
internal/gateway       gateway MCP server (streamable HTTP on localhost)
internal/runstate      runs/ directory, state, cache, resume
internal/errors        error classes
internal/notify        notification channels
internal/executor      DAG traversal, retry, on_error, budgets, cancellation
```

Dependency rule: `steps` depends on `engine`, `tools`, `runstate`; `executor` depends on `steps`; nothing depends on `cmd`. Secrets are accessible only to `secrets`, `httpx`, `gateway`, `notify` and `engine` (for the provider key) — other packages receive values already wrapped as `values.Secret` without access to the content.

### Run sequence

1. Load the scenario, run full validation (section 4); on error — exit code 3
2. Create `runs/<id>/`, write `run.json` with status `running`
3. Resolve all secrets, including those needed for `on_failure`. On failure — the run does not start, code 3
4. Prepare the workspace (section 7.1)
5. Bring up the gateway if at least one `agent` step requires it
6. Execute steps in declaration order, honoring `when`, `needs`, cache
7. On failure — cancel parallel `foreach` elements, run `on_failure`, record state
8. Write `cost.json`, the final status, exit with the code per section 9.5

## 3. Scenario Format

### 3.1 Top level

```yaml
version: 1
name: code-review
description: MR review with risk classification

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
  usd: 8               # for the entire run
  time: 40m

secrets: { ... }       # section 6
apis:    { ... }       # section 7.4
commands:              # section 7.5
  test: { argv: ["go", "test", "./..."] }

steps: [ ... ]

on_failure: [ ... ]
```

Rules:

- `inputs` are typed: `string | int | number | bool | list | map`; `pattern` is required for string inputs that end up in command argv
- `defaults` apply to steps whose field is not set
- Paths to prompt files, schemas, templates and skills are given relative to the scenario file

### 3.2 Common step fields

```yaml
- id: review                # unique, ^[a-z][a-z0-9_]*$
  when: <expr>              # optional
  needs: [diff, lint]       # optional, defaults to all preceding steps
  timeout: 15m
  retry: { on: [transient], attempts: 2, backoff: 10s }
  on_error: fail            # fail | continue | fallback
  fallback: { <step body> } # required if on_error: fallback
  cache: true               # see 8.3
  dedupe_key: <tmpl>        # for steps with side effects
```

Exactly one of the fields `run`, `http`, `llm`, `agent`, `foreach`, `until`, `assert` must be present.

### 3.3 `run`

```yaml
- id: diff
  run:
    argv: ["git", "diff", "{{ .inputs.base }}...HEAD"]
    cwd: workspace          # default
    env: { GOFLAGS: "-mod=readonly", TOKEN: { secret: gitlab_ro } }
    stdin: "{{ .steps.prev.stdout }}"
    parse: json             # text | json | lines
    allow_exit_codes: [0]
    max_output_bytes: 1m
```

No `sh -c`. If the user passes `run: "a string"`, validation accepts it only if the string contains no shell metacharacters, and splits it on whitespace with a warning. Result: `stdout`, `stderr`, `exit_code`, `result` (with `parse: json`).

### 3.4 `http`

Two forms. The primary one — calling a pack operation:

```yaml
- id: publish
  http:
    op: forge.post_comment                 # <api>.<op> from apis
    auth: forge_rw                         # override of the secret declared in apis
    args:
      project: "{{ .inputs.project }}"
      iid: "{{ .inputs.mr }}"
      body: "{{ render \"templates/review.md.tmpl\" .steps.review }}"
  dedupe_key: "review-{{ .inputs.project }}-{{ .inputs.mr }}-{{ .run.id }}"
```

Arguments are validated against the operation's `params`, pagination, envelope and transform are applied automatically, the result is an already-normalized `result`. This is the only way to call an operation with a side effect from a pack: such operations are not available to the agent.

The raw form — for one-off requests, when there is no pack and writing one is not worth it:

```yaml
- id: ping
  http:
    api: gitlab                      # base_url and auth from apis, or url: full address
    method: POST
    path: /projects/{{ .inputs.project }}/merge_requests/{{ .inputs.mr }}/notes
    headers: { Content-Type: application/json }
    body: "{{ toJSON (dict \"body\" \"ok\") }}"
    expect_status: [200, 201]
    parse: json
```

`POST/PUT/PATCH/DELETE` (or an operation without `readonly: true`) without `dedupe_key` — a validation warning and automatic retry is disallowed (section 9.4). Result: `status`, `headers`, `body`, `result`.

### 3.5 `llm`

```yaml
- id: classify
  llm:
    model: anthropic/claude-haiku-4-5       # <provider>/<model>, provider from config
    fallback_models:                        # optional, on provider transient/budget errors
      - openrouter/google/gemini-2.5-flash
      - openai/gpt-4.1-mini
    system: prompts/classify.system.md
    prompt: prompts/classify.md            # file or inline
    with: { diff: "{{ .steps.diff.stdout }}" }
    schema: schemas/classify.json           # required
    tools: [apis.jira.get_issue]            # optional, the runner drives the tool loop
    max_tokens: 2000
    temperature: 0
```

The model is given as the string `<provider>/<model>`, where `provider` is a name from the `providers` section of the global config or the scenario (section 8.3). For OpenRouter the model name itself contains a slash (`openrouter/google/gemini-2.5-flash`): the separator is the first slash. `fallback_models` is a chain used on `transient`-class provider errors (unavailability, 429, 5xx); on a `schema`-class error the chain is not used, the usual `schema` retry runs on the same model instead. A model switch is recorded in `output.json` as `model_used`.

`schema` is required. The `result` is the parsed and validated JSON. Invalid output → a `schema`-class error; on retry a message with the validation error text is added to the context. The prompt file is Markdown with `{{ }}` templates, variables come from `with`.

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
    tools: { ... }                         # overrides on top of the profile, section 7
    max_turns: 25
    budget_usd: 2
    result: schemas/findings.json          # schema for submit_result
```

The step succeeds only through a call to `submit_result` with valid JSON. An agent that finishes without calling it is a `schema`-class error with the message "result not submitted".

### 3.7 `foreach`

```yaml
- id: per_repo
  foreach:
    items: "{{ .inputs.repos }}"           # a list
    as: repo
    max_parallel: 3
    on_item_error: fail                    # fail | continue
    min_success: 1.0                       # fraction; used with continue
    step: { llm: { ... } }                 # body — any step without an id
```

The `items` result — a list of `{ status, result, error }` in input order. With `on_item_error: fail` the first error cancels the rest via context cancellation and fails the step.

### 3.8 `until`

```yaml
- id: fix
  until:
    condition: "iter.test.exit_code == 0"  # iter — the result of the previous iteration — is available in context
    max_iterations: 3                      # required
    step: { agent: { ... } }
```

Each iteration receives `iter` in `with`. The result is the result of the last iteration plus `iterations`.

### 3.9 `assert`

```yaml
- id: gate
  assert:
    condition: "len(filter(steps.review.result.findings, .severity == 'blocker')) == 0"
    message: "Blocking findings found"
```

A false condition → the run finishes with exit code 2, `on_failure` is **not** executed (this is not an error), but `always` notifications, if added in v2, are executed.

### 3.10 `switch`

Sugar over `when`, expanded at load time:

```yaml
- switch: steps.classify.result.risk
  cases:
    high:   { id: deep_review, agent: { ... } }
    medium: { id: deep_review, agent: { ... } }
    low:    { id: light_review, llm: { ... } }
  default: { id: skip_note, run: { argv: ["echo", "skipped"] } }
```

If `steps.classify` is an `llm` step with a schema and the field is an `enum`, validation requires coverage of all values or a `default`.

## 4. Scenario Validation

Runs in full before execution. Errors — exit code 3, all errors are printed as a list, not just the first one.

Required checks:

1. Conformance to the scenario JSON Schema (`baton schema` prints it)
2. Uniqueness of `id`, absence of cycles in `needs`
3. Compilation of all `when:`, `condition:` in expr-lang with context typing
4. Parsing of all `{{ }}` templates, existence of prompt, schema, skill files
5. All `steps.<id>` references point to declared steps, declared **earlier**
6. A reference to the result of a step with `when:` from a step without `when:` and without `coalesce`/`default` — an error
7. Values of type `secret` may not end up in `llm.prompt`, `llm.system`, `llm.with`, `agent.prompt`, `agent.with`, `run.argv`, `run.stdin`, `assert.message`, `on_failure.*.message`. Allowed only in `http.auth`, `apis.*.auth`, `run.env.*.secret`, `notify.*.secret`
8. `pattern` is required for every command argument and every string input used in argv
9. `max_iterations` is required for `until`
10. `switch` covers the `enum` or has a `default`
11. `on_error: fallback` requires `fallback`
12. All packs from `apis` load, version pin and checksum match, the pack passes its own validation (section 7.4.5); for `interface:` — the chosen pack implements all operations of the interface; if `pack` is given as a template, all candidate packs from `from` are checked
13. `http.op` refers to an existing operation; `args` cover the required `params`; `tools.apis` contains only operations with `readonly: true`
14. Warnings (not errors): `http` with a mutating method or an operation without `readonly` and without `dedupe_key`; `agent` with `tools.fetch` and no allowlist; `foreach.max_parallel > 5`; a pack operation without `description` handed to the agent

## 5. Expressions, Templates, Values

### 5.1 Expressions

Engine: `github.com/expr-lang/expr`. Context:

```
inputs.<name>
steps.<id>.status          # success | failed | skipped
steps.<id>.result          # for llm, agent, http(parse), run(parse)
steps.<id>.stdout / stderr / exit_code
steps.<id>.items           # for foreach
run.id, run.name, run.started_at
iter.*                     # inside until
```

Secrets are absent from the expression context as a class. An evaluation error at runtime (a nil dereference not caught by validation) is a `config`-class error.

### 5.2 Templates

`text/template` with functions: `render(path, data)`, `toJSON`, `fromJSON`, `coalesce`, `default`, `trunc(n)`, `indent(n)`, `join`, `dict`, `quote`, `md2html`, `md2text`. The last two are for APIs that accept HTML (Confluence storage format) or plain text; vendor-specific formats (ADF and similar) are not added to the binary. The template engine receives values through a wrapper: attempting to output a `values.Secret` returns a render error, not a string.

### 5.3 Value types

`string`, `number`, `bool`, `list`, `map`, `secret`, `file`. `file` is a reference to an artifact in the run directory (`runs/<id>/steps/<step>/artifacts/...`), rendered as a path. `secret` is an opaque wrapper; the only way to get the content is a method available to packages on an allowlist (checked by a linter or the internal `internal/secrets/unwrap` package, restricting the import via a `go vet` rule or a simple CI check).

## 6. Secrets

```yaml
secrets:
  gitlab_ro: { from: env,  key: GITLAB_READ_TOKEN }
  gitlab_rw: { from: env,  key: GITLAB_WRITE_TOKEN }
  jira:      { from: file, path: /run/secrets/jira, trim: true }
  tg:        { from: env,  key: TELEGRAM_BOT_TOKEN }
```

Requirements:

- Resolution of all secrets at startup, before the first step; absence of any — code 3
- Redactor: all secret values (and their base64/URL-encoded forms) are replaced with `***` in step stdout/stderr, logs, `run.json`, notifications, the tool audit log
- The model provider secret (`ANTHROPIC_API_KEY` or a Claude Code OAuth token) is the only secret that ends up in the agent process environment. Documented recommendation: use a separate key with a spending limit
- The global config `~/.config/baton/config.yaml` and `./baton.yaml` may declare secrets and notification channels; the scenario overrides them

## 7. Agent Tools and the Gateway

### 7.1 Workspace preparation

Before the first `agent` step the runner:

- Determines the repository root (or `--workspace` from the CLI)
- Rewrites `.git/config`: strips credentials from all remote URLs, removes the `extraheader` with authorization
- Does not delete files; deny-lists are enforced at the tool level

### 7.2 Profiles

| Profile | fs.read | fs.write | git | exec | apis | fetch | mcp | state |
|---|---|---|---|---|---|---|---|---|
| `review` | yes | no | read | none | by list | no | no | read |
| `fix` | yes | workspace | read + commit | commands | by list | no | no | read |
| `research` | yes | no | read | none | by list | allowlist | by list | read-write |

The `tools:` block on a step overrides individual profile fields.

### 7.3 Full form of `tools`

```yaml
tools:
  fs:
    read: true
    write: none | workspace
    deny: [".git/**", ".env*", "**/*.pem", "**/id_*"]   # default, extended
  git:
    read: true
    commit: false
  exec:
    mode: none | commands
    commands: [test, lint]         # subset of commands:, or all
  apis: [forge.get_change, forge.get_file, jira.get_issue]   # only readonly pack operations
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

### 7.4 API Packs

Principle: the binary knows the protocol (HTTP, JSON, auth schemes, pagination), but not the services. Everything that knows a service's name lives in a pack — a YAML file outside the binary. The test for any proposal to "add X to the binary": would a fifth, unrelated service need X? `md2html` — yes; `md2adf` — no, that's Atlassian.

#### 7.4.1 Wiring into a scenario

```yaml
apis:
  forge:                                   # name under which operations are visible in the scenario
    interface: forge/v1                    # optional: guarantees a set of operations and response shapes
    pack: "{{ .inputs.forge }}"            # gitlab | github | gitea — pack name
    from: github.com/org/baton-apis@v1.3.0 # pack source; version pin required
    sha256: "…"                            # required for sources other than the local filesystem
    config: { base_url: https://gitlab.company.com/api/v4 }
    auth: { secret: forge_ro }             # value for the pack's auth scheme
    timeout: 20s
  jira:
    pack: jira
    from: ./apis/                          # local directory, no checksum needed
    auth: { secret: jira }
  telegram:
    pack: telegram
    from: github.com/org/baton-apis@v1.3.0
    sha256: "…"
    auth: { secret: tg_bot }
```

`from` is a local directory or a git source with a tag. The runner caches loaded packs in `~/.cache/baton/packs/<source>@<version>/` and verifies the checksum on every load. A pack without a pin or with a checksum mismatch is a `config`-class error. A pack cannot contain secrets, only names of auth parameters.

Operations are available as `<api>.<op>`: in `http` steps — any of them, in agent tools — only those with `readonly: true` and only those listed in `tools.apis`.

#### 7.4.2 Pack format

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

envelope:                      # optional, response envelope
  unwrap: .
  error_when: '.message != null and .error != null'
  error_message: .message

pagination:                    # default strategy for operations with paginate: true
  style: link_header           # link_header | page | offset | cursor
  max_pages: 20

ops:
  get_change:
    get: /projects/{project}/merge_requests/{iid}/changes
    description: Merge request changes
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
    description: Comment on a merge request
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

Operation fields:

- `get | post | put | patch | delete: <path>` or `kind: graphql` with `query` (file or inline); the path contains `{param}` placeholders
- `readonly` — determines whether the operation is available to the agent. Not derived from the HTTP method: RPC-style APIs (Outline) do reads via `POST`
- `params.<name>`: `pattern` (required for anything that ends up in the path or query), `max_len`, `enum`, `in: path | query | body | form` (default: a path placeholder → `path`, `GET` → `query`, otherwise `body`), `encode: path` for segments containing `/`, `required` (default true), `default`
- `params.<name>.name` — the name the argument is sent under when it differs from the name the scenario writes; for a body argument a dotted name nests the value: `name: fields.project.key` sends `{ "fields": { "project": { "key": … } } }`
- `encode` — body encoding: `json` (default), `form`
- `paginate: true` — apply the pack's strategy or the operation's own in `pagination`; the result before the transform is a concatenated array of pages
- `transform` — a jq expression (gojq), applied after the envelope and pagination; the transform must be pure, without `input`/`env`/`$__loc__`
- `pick: [fields]` — shorthand for a trivial transform
- `max_bytes` — truncation of the result after the transform, marked as `truncated`
- `implements: <interface>/<version>.<op>` — a claim of implementing an interface operation

#### 7.4.3 Auth schemes

| `kind` | Where it is inserted | Example |
|---|---|---|
| `header` | header `name` | GitLab `PRIVATE-TOKEN` |
| `bearer` | `Authorization: Bearer …` | GitHub, Outline, Confluence DC |
| `basic` | `Authorization: Basic base64(user:secret)`, `user` from `config` | Confluence Cloud, Jira Cloud |
| `query` | query parameter `name` | legacy APIs |
| `path` | path segment `{auth}` in `base_url` or the operation path | Telegram `https://api.telegram.org/bot{auth}/` |
| `exchange` | the token is obtained by a pack operation and cached | MediaWiki CSRF, OAuth2 client credentials |

`exchange`:

```yaml
auth:
  kind: exchange
  op: get_csrf_token            # operation of this pack, called with base authorization
  base: { kind: header, name: Authorization }   # what authorizes the exchange itself
  extract: .query.tokens.csrftoken
  inject: { in: form, name: token }
  ttl: 10m
  session: cookies              # keep cookies between calls within a run
  depends_on: login             # preceding exchange chain (bot password -> session)
```

Exchange tokens live only in the run's memory, are not written to the cache or to `runs/`. The cookie jar is per-run, not persisted to disk. The `depends_on` chain depth is limited to 3.

Auth values of any kind pass through the secret redactor, including substitution into the path: a URL with `bot123:ABC/sendMessage` in logs must appear as `bot***/sendMessage`.

#### 7.4.4 Pagination

| `style` | How it works | Parameters |
|---|---|---|
| `link_header` | next page from the `Link: <…>; rel="next"` header | — |
| `page` | page-number parameter, stops on an empty page or `size` smaller than requested | `param`, `size_param`, `size`, `in` |
| `offset` | offset and limit | `param`, `limit_param`, `size`, `in: query | body`, `total: <jq>` optional |
| `cursor` | next-page cursor from the response body | `next: <jq>`, `param`, `in`; if `next` is a full URL, it is used as-is |

Common: `max_pages` (default 20), `items: <jq>` — where the page body's list is (defaults to `.` for arrays, required for objects). Pagination stops at `max_pages` marking `truncated_pages: true` in the result, without failing.

#### 7.4.5 Interfaces

An interface is a contract made of several operations with a fixed argument and result shape (JSON Schema). The interface registry is built into the binary and versioned; in v1 there are three:

`forge/v1`: `get_change(project, id)`, `list_files(project, id)`, `get_file(project, path, ref)`, `post_comment(project, id, body)`, `post_review(project, id, summary, comments[])`.

`tracker/v1`: `get_issue(key)`, `search(query, limit)`, `create_issue(project, title, body, type)`, `comment(key, body)`.

`notify/v1`: `send(target, text, format)`.

Rules:

- A pack that declares `implements` for an operation is checked at load time: `params` cover the interface arguments, and the transform run on a test response from the pack (`examples/<op>.json`, required for `implements`) produces a result valid against the interface schema. Otherwise — `config`
- The example check emulates pagination: for an operation with `paginate: true` the example is the raw body of a single page, `pagination.items` is applied to it, and the transform is fed that page's items. The example therefore checks both `pagination.items` and the transform
- A scenario that declares `interface:` checks at validation time that the chosen pack implements all operations of the interface; otherwise — `config` listing the missing ones
- Interface operations are single calls. If a forge's API cannot batch (GitLab: one discussion per comment), the pack does not implement `post_review`, and the scenario uses `post_comment` in a `foreach`. Validation shows this upfront
- Besides interface operations a pack may contain its own; they are available as `forge.list_pipelines` only when `pack` is set statically

Notification channels (section 12) are `notify/v1`: `notify: telegram` in `on_failure` expands into `http: { op: telegram.send, args: { target: <chat_id from the channel config>, text: <message> } }`.

#### 7.4.6 Format boundaries

The format covers REST- and RPC-style over HTTP with JSON or form-encoded bodies, token-based auth schemes and token exchange. Verified on paper for GitLab, GitHub, Gitea, Jira (v2, for comment text, no ADF), Confluence Cloud and DC (storage format via `md2html`), Outline, MediaWiki (form + exchange for CSRF and bot password), Telegram Bot API (`path` authorization, envelope `{ok, result}`).

Not covered and will not be: interactive OAuth, long polling and websockets, SOAP/XML, gRPC, composite operations with logic. For that — a vendor's MCP server via `tools.mcp` or a `run` step with a script.

#### 7.4.7 `baton apis import`

`baton apis import --openapi <spec> --ops <id,…> [--interface forge/v1] > pack.yaml` — generates a stub: paths, methods, `params` with types from the spec, empty `transform` and `description`. The spec is not used at runtime. The pack's author fills in transforms, pagination and `readonly`.

#### 7.4.8 Tool generation

From operations with `readonly: true` listed in a step's `tools.apis`, the runner generates MCP tools `<api>.<op>` with a JSON Schema of arguments from `params` and a description from `description`. Operations without `description` get a validation warning: the agent picks a tool by its description.

### 7.5 `commands`

```yaml
commands:
  test:
    argv: ["go", "test", "./..."]
    description: Run all tests
    timeout: 5m
    readonly: true
  lint:
    argv: ["golangci-lint", "run", "--out-format", "json", "{{ .args.path }}"]
    description: Linter over a path (whole module by default)
    args: { path: { pattern: '^[\w/.-]+$', default: "./..." } }
    parse: json
    readonly: true
    max_calls: 5
```

Execution — by the runner (not the agent process), `exec` without a shell, `cwd` = workspace, env = a minimal allowlist (`PATH`, `HOME`, `GOCACHE`, `GOPATH`, `GOFLAGS`) plus declared ones. An argument starting with `/` or containing `..` is rejected before the `pattern` check. Tool response: `{ exit_code, stdout, stderr, truncated, duration_ms, result? }`. A nonzero exit code is data, not an error. The total time limit for commands within a step is half of the step's `timeout`.

### 7.6 Other gateway tools

- `fetch(url)`: GET against a domain allowlist, no cookies, no following redirects outside the allowlist, text extraction from HTML, `max_bytes`
- `state.get(key)`, `state.set(key, value)`: file `state/<scenario-name>.json` next to `runs/`, values ≤ 64 KB
- `submit_result(result)`: validated against the step's `result` schema; on error returns the error text to the agent and does not finish the step; on success — the step is marked as finished, the agent process receives a signal to terminate
- Third-party MCP: the runner starts the server process with its own env (secrets from `secrets`), proxies its tools through the gateway; tools whose schema contains a parameter named/formatted `url`/`uri`/`headers` are marked `unsafe` and available only with `allow_unsafe: true` on the step

### 7.7 The Gateway

An MCP server (streamable HTTP) on `127.0.0.1:<ephemeral port>` with a one-time bearer token per run. One instance per run, the set of tools is per step (the agent only gets the tools of its own step). Every call is written to `runs/<id>/steps/<step>/tool-calls.jsonl`: time, name, arguments (after the secret redactor), response size, duration, status. The limits `max_tool_calls`, `max_calls` are checked here.

## 8. Engines

### 8.1 Interface

```go
type Engine interface {
    Run(ctx context.Context, req AgentRequest) (AgentResult, error)
}
type AgentRequest struct {
    Prompt, System string
    Model          string
    Workspace      string
    Skills         []string      // paths to directories with SKILL.md
    Gateway        GatewayInfo   // URL + token
    BuiltinTools   ToolPolicy    // what to allow from the built-ins
    MaxTurns       int
    BudgetUSD      float64
    Env            map[string]string
}
type AgentResult struct {
    Submitted   bool            // whether submit_result was called
    Result      json.RawMessage
    Usage       Usage           // tokens, cost
    Turns       int
    Transcript  string          // path to the file
}
```

### 8.2 CLI engines

#### `claude-code`

- Runs `claude -p` in non-interactive mode, `--output-format json` to get usage and cost, `--max-turns`
- The gateway's MCP config is passed via a temporary `--mcp-config` file
- Built-in tools: always allow `Read`, `Glob`, `Grep`; allow `Edit`, `Write`, `MultiEdit` when `fs.write: workspace`; **always forbid** `Bash`, `WebFetch`, `WebSearch`, `Task`. Mechanism: `--allowedTools` / `--disallowedTools` plus `--permission-mode` with no interactive prompts. Exact flags must be checked against `claude --help` of the installed version; the adapter checks the version at startup and refuses to run with an unknown major version
- Path deny-lists: a temporary settings file with a PreToolUse hook that rejects `Read`/`Edit`/`Write` for paths from `fs.deny` and outside the workspace
- Skills: copying or symlinking directories from `skills` into `<workspace>/.claude/skills/` for the duration of the step, removed afterward
- Process environment: empty, plus `PATH`, `HOME` (a temporary directory), the provider key, the declared `env`
- Termination: on `submit_result` — SIGTERM, after 10 s SIGKILL; on timeout — the same; on context cancellation — the same
- Cost is taken from the JSON output; if absent — estimated from tokens and the price table in the config

#### `codex`

- Runs `codex exec --json` in non-interactive mode. The prompt arrives on stdin, which keeps a long prompt out of the process table; a `system` prompt is prepended to it as a separate block, since this CLI has no flag for one
- The gateway is an MCP server in a `config.toml` written into the run's own `CODEX_HOME`. The bearer token is passed by name (`bearer_token_env_var = "BATON_GATEWAY_TOKEN"`), never written into the file
- Built-in tools: the shell cannot be turned off the way `claude-code`'s `Bash` can, so the sandbox holds the step's policy instead — `sandbox_mode = "read-only"`, or `workspace-write` with `network_access = false` and the workspace as the only writable root when `fs.write: workspace`. `web_search` is off, `approval_policy = "never"` so that a step never waits for a human, and `shell_environment_policy.inherit = "core"` keeps the step's secrets out of the commands the agent spawns
- Path deny-lists inside the workspace are not enforced by this engine, and `skills` are refused as unsupported
- The flag set moves between releases, so the adapter reads `codex exec --help` and passes only what the installation has: `--ephemeral`, `--ignore-rules`, `--color never`, `--model`. `--ignore-user-config` is deliberately not passed — it would drop the run's own `config.toml` along with the gateway. The version is checked at startup and an unknown major is refused
- Process environment: empty, plus `PATH`, `HOME` and `CODEX_HOME` (the same temporary directory, so no user configuration or cached credential reaches the run), and the declared `env`. The provider credential therefore has to be named in the step's `env`
- Termination: as with `claude-code` — SIGTERM, SIGKILL after 10 s
- `max_turns` and `budget_usd` have no flag or config key in this CLI: the step is bounded by the gateway's `max_tool_calls` and by its timeout. The CLI reports no cost, so `cost_usd` stays null; tokens are summed over the `turn.completed` events

### 8.3 Providers for `llm:`

The `llm` step is not tied to a single API. Inside — a unified interface, with three implementations underneath:

```go
type Provider interface {
    Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
    Capabilities() Caps   // structured_output, tools, vision, max_context
}
```

`CompletionRequest` — normalized messages (system, user, assistant, tool_call, tool_result), a list of tools in JSON Schema, the desired result schema, `max_tokens`, `temperature`. `CompletionResponse` — text or tool-calls, usage (input/output/cached tokens), cost if the provider returned it, `finish_reason`.

| Provider | Transport | Structured output | Cost |
|---|---|---|---|
| `anthropic` | Messages API, official Go SDK | a tool-wrapper with `tool_choice` forced onto it; the SDK's native JSON mode if available | `pricing` table |
| `openai` | Chat Completions (or Responses API, if the SDK is stable), official Go SDK | `response_format: json_schema` with `strict: true`; on model refusal — tool-wrapper | `pricing` table |
| `openrouter` | OpenAI-compatible Chat Completions, `base_url: https://openrouter.ai/api/v1` | `response_format: json_schema` where the underlying model supports it (checked via `/models`); otherwise tool-wrapper; otherwise prompt + validation | from the response's `usage.cost` field (request `usage: { include: true }`) |
| `openai_compatible` | the same client as `openrouter`, an arbitrary `base_url` | via the `capabilities` flag in the config | `pricing` table |

`openrouter` is `openai_compatible` with a preconfigured `base_url`, mandatory `HTTP-Referer`/`X-Title` headers, and a cost parser. There is one implementation, the differences are in config and in three extension points. `openai_compatible` gives Ollama, vLLM, LM Studio and corporate proxies for free.

Structured output strategy, common to all: (1) native JSON Schema mode, if the provider and model claim support; (2) a single `submit` tool with the step's schema, forced to be called; (3) an instruction in the prompt plus response validation. The level is recorded in `output.json` as `structured_mode`. Downgrading is automatic based on `Capabilities()`, manual override — `structured_mode:` on the step.

The tool loop for `llm.tools` is driven by the runner, the same for all providers: the tools are the same ones as in the gateway, called directly without HTTP; the limits `max_tool_calls` are the same. Differences in tool-call formats between Anthropic and OpenAI are hidden behind message normalization.

Cost accounting: if the provider returned a cost — use it; otherwise `usage × pricing[model]`; if the model is not in `pricing` — a validation warning and `cost_usd: null` in the report, the dollar budget is not checked for such a step, only the token budget is (`budget_tokens`).

Provider config (global or in the scenario, merged by name):

```yaml
providers:
  anthropic:
    kind: anthropic
    api_key: { from: env, key: ANTHROPIC_API_KEY }
  openai:
    kind: openai
    api_key: { from: env, key: OPENAI_API_KEY }
    base_url: https://api.openai.com/v1          # optional, for Azure/proxy
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

Provider keys are regular `secret`s, they live in the runner process. Only the key of its own provider ends up in the agent process (`claude-code`). Timeouts and retry at the HTTP level are shared: 3 attempts on 429/5xx honoring `Retry-After`, this is part of the `transient` class.

### 8.4 `fake`

Required for tests. Reads a behavior scenario from a file: a sequence of tool calls with arguments and a final `submit_result`. Allows running the entire executor, gateway, retry and `on_failure` without spending on a model. All integration tests are written against it.

## 9. Errors, Retries, Exit Codes

### 9.1 Classes

| Class | Source | Retry by default |
|---|---|---|
| `transient` | network errors, HTTP 408/429/5xx, provider timeouts | yes, `attempts: 2`, exponential backoff starting at 5 s |
| `schema` | result failed JSON Schema, `submit_result` not called | yes, `attempts: 1`, with the error text added to context |
| `command` | `run` with a disallowed exit code, `http` with an unexpected 4xx status | no |
| `timeout` | step exceeded `timeout` | no |
| `budget` | `budget_usd`, `max_turns`, `max_tool_calls`, or run budget exceeded | **never** |
| `policy` | `publish` validation, an `unsafe`-tool call, an argument outside `pattern` | never, an audit event |
| `config` | validation error, an expression evaluation error at runtime | never |

### 9.2 Semantics of `on_error`

- `fail`: the step is `failed`, the run stops, parallel `foreach` elements are cancelled, `on_failure` runs
- `continue`: the step is `failed`, `result = null`, the run continues; subsequent steps see `steps.<id>.status == "failed"`
- `fallback`: the `fallback` body runs as a step with the same `id`; status `success` with the flag `fallback_used: true`; if it also fails — treated like `fail`

### 9.3 `on_failure`

- Runs when the run status is `failed` of any class, including `budget` and a runtime `config` error
- Does not run on exit code 2 (`assert`)
- Steps have no retry, their own 60 s timeout, and are outside the run budget
- Errors inside `on_failure` are logged, they do not trigger `on_failure` again
- Context: `run.failed_step`, `run.error.class`, `run.error.message`, `run.error.stderr_tail` (2 KB), `run.duration`, `run.cost_usd`, `run.dir`
- The `notify: <channel>` form with `message:` is sugar over `http` with the channel's config from the global config

### 9.4 Side effects and retry

A step with a side effect is any `http` with a method other than `GET/HEAD`, a `run` without `readonly: true`, an `agent` with `fs.write`. Such steps are not retried automatically unless they have a `dedupe_key`. When `dedupe_key` is present, before executing the runner checks `runs/<id>/effects.json`; if the key is already marked as executed, the step is skipped as done. The key is marked right after a successful response.

### 9.5 Exit codes

| Code | Meaning |
|---|---|
| 0 | run succeeded |
| 1 | execution error (`transient`, `schema`, `command`, `timeout`, `policy`) |
| 2 | an `assert` triggered |
| 3 | configuration / validation / secrets error |
| 4 | budget exhausted |
| 130 | interrupted by a signal |

### 9.6 Cancellation

A single `context.Context` per run. SIGINT/SIGTERM → cancellation, agent processes get SIGTERM, state is recorded, exit code 130. A `foreach` element failing with `on_item_error: fail` → cancellation of the remaining elements.

## 10. Run Directory, Cache, Resume

### 10.1 Location

`runs/` — next to the scenario by default, overridden by `--runs-dir` or `BATON_RUNS_DIR`. `run-id` — `YYYYMMDD-HHMMSS-<4 hex>`, overridden by `--run-id`.

```
runs/<id>/
  run.json              status, inputs (without secrets), timings, cost, failed_step
  events.jsonl          all run events
  effects.json          executed dedupe_keys
  cost.json             per step: tokens, usd, duration
  steps/<step-id>/
    input.json          the step's rendered inputs (without secrets)
    output.json          status, result, exit_code, usage
    stdout.log, stderr.log
    tool-calls.jsonl    for agent/llm with tools
    transcript.jsonl    for agent, if the engine provides it
    artifacts/          files produced by the step; diff.patch for fix
  steps/<foreach-id>/<n>/...   foreach elements
```

### 10.2 State

`run.json` is updated after every step with an atomic write (temp + rename). The format is JSON with a schema version.

### 10.3 Cache

Key: `sha256(normalized step definition without id and when + rendered inputs + hashes of prompt/schema/skill files + the checksum of the pack an http step calls + engine + model + baton version)`. The cache is a `cache/` directory next to `runs/`, its contents are `output.json` and artifacts.

By default `cache: true` for `llm` and `run` with `readonly: true`; `false` for `http` with mutating methods and `agent` with `fs.write`. `--no-cache` disables reading from the cache, not writing.

### 10.4 Resume

`baton resume <id>`: reads `run.json`, skips steps with status `success` (takes their output), starts from the failed one. Secrets are resolved again. The new run gets the same `id` with a suffix `-r1`, `-r2`, with a reference to the original in `run.json`.

## 11. CLI

```
baton run <scenario.yaml> [-i key=val]... [--input-file f.json] [--run-id ID]
          [--runs-dir DIR] [--workspace DIR] [--no-cache] [--dry-run] [--json] [-v]
baton validate <scenario.yaml>            # validation only, code 0/3
baton resume <run-id> [--runs-dir DIR]
baton runs list [--runs-dir DIR] [-n 20]
baton runs show <run-id>                  # summary, cost, step statuses
baton runs logs <run-id> [--step ID]      # step stdout/stderr
baton tools <scenario.yaml> --step ID     # what the agent will see: names, schemas, descriptions
baton schema                              # scenario JSON Schema on stdout
baton apis import --openapi f --ops a,b   # pack stub from OpenAPI (section 7.4.7)
baton apis validate <pack.yaml>           # check a pack and its examples/ against interfaces
baton apis call <scenario> <api>.<op> -a k=v   # call an operation manually, for debugging packs
```

`--dry-run`: validation, secret resolution (presence check), rendering of the first step's inputs, printing the plan without executing. `--json`: events on stdout as JSONL instead of a human-readable log; the human-readable one goes to stderr.

Human-readable log: one line per event, prefix `[step-id]`, duration and cost when a step finishes, a summary at the end of the run.

## 12. Global Config

`~/.config/baton/config.yaml`, then `./baton.yaml` (merged, local wins):

```yaml
apis:                            # global packs, available to all scenarios
  telegram:
    pack: telegram
    from: github.com/org/baton-apis@v1.3.0
    sha256: "…"
    auth: { secret: tg_bot }
secrets:
  tg_bot: { from: env, key: TELEGRAM_BOT_TOKEN }
notify:                          # channels = a notify/v1 operation + a recipient
  telegram:
    api: telegram                # pack with the notify/v1 interface
    target: "-100123"
  ops:
    kind: webhook                # built-in: POST JSON to a URL
    url: { from: env, key: SLACK_OPS_WEBHOOK }
providers:                       # section 8.3
  anthropic:  { kind: anthropic,  api_key: { from: env, key: ANTHROPIC_API_KEY } }
  openai:     { kind: openai,     api_key: { from: env, key: OPENAI_API_KEY } }
  openrouter: { kind: openrouter, api_key: { from: env, key: OPENROUTER_API_KEY } }
defaults:
  engine: claude-code
  model: anthropic/claude-sonnet-4-6
  budget_usd: 3
on_failure:                      # applies to all scenarios without their own on_failure
  - notify: telegram
    message: "{{ .run.name }} failed: {{ .run.error.class }} at {{ .run.failed_step }}"
pricing:                         # for cost estimation, if the provider did not return one
  anthropic/claude-sonnet-4-6: { input_per_mtok: 3,    output_per_mtok: 15 }
  anthropic/claude-haiku-4-5:  { input_per_mtok: 0.8,  output_per_mtok: 4 }
  openai/gpt-4.1-mini:         { input_per_mtok: 0.4,  output_per_mtok: 1.6 }
mcp_servers:
  context7: { command: ["npx", "-y", "@upstash/context7-mcp"], env: {} }
```

## 13. Security Requirements (Acceptance Checklist)

- [x] No secret appears in `runs/`, logs, notifications, the audit log (test: a secret with a random value, grep the whole directory after the run)
- [x] A template with `{{ .secrets.x }}` in a prompt does not pass validation
- [x] The agent process has nothing in its environment beyond the allowlist (test: the fake engine writes `os.Environ()` to a file)
- [x] The agent cannot call a tool not declared for the step (test via fake: calling someone else's tool → `policy`)
- [x] `commands` do not interpret shell metacharacters (test: an argument `; echo pwned` is rejected by `pattern`, an argument `$(id)` with a permissive pattern is passed through literally)
- [x] `fetch` to a domain outside the allowlist → `policy`; a redirect to a domain outside the allowlist → `policy`
- [x] A path `../x` and `/etc/passwd` in command arguments and `fs` → `policy`
- [x] `.git/config` after workspace preparation contains no tokens
- [x] `http` with `POST` without `dedupe_key` is not retried on `transient`
- [x] `budget` is never retried regardless of `retry` settings
- [x] A pack with a `sha256` mismatch or without a version pin does not load
- [x] The `path`-authorization token is absent from URLs inside logs, `events.jsonl`, `tool-calls.jsonl` and HTTP error messages
- [x] `exchange` tokens do not end up in the step cache or in `runs/`
- [x] The agent cannot call a pack operation without `readonly: true`, even if it is listed in `tools.apis` (validation) and even with a direct call to the gateway (runtime → `policy`)
- [x] A jq transform with `env`, `input`, `$__loc__` is rejected when the pack is loaded

## 14. Testing

- Unit: `expr` context and typing, templates and the `secret` ban, the secret redactor, the scenario validator (golden tests: a directory of YAML files and expected error lists), the cache key (stability), argv substitution
- Integration on the `fake` engine: a full run of a review scenario; retry on `schema`; `fallback`; `on_failure` on `budget`; `foreach` with `continue` and `min_success`; `resume` after a failure; `until` with `max_iterations`; `dedupe_key`
- Providers: a contract test on an `httptest` server for each `kind` — message and tool-call normalization in both directions, all three structured-output levels, parsing usage and cost (including OpenRouter's `usage.cost`), handling 429 with `Retry-After`, switching via `fallback_models`
- Packs (`internal/httpx`, `internal/packs`): an `httptest` server for each auth scheme, including `exchange` with a chain and cookies; each pagination strategy, including stopping at `max_pages`; an envelope with an error; `form` and `graphql`; token redaction in the URL
- The `baton-apis` repository: contract tests — `examples/<op>.json` with recorded responses for each API are run through the transforms and checked against the interface schemas in the repository's CI; `baton apis validate` for every pack
- One end-to-end test with a real `claude-code` behind the `BATON_E2E=1` flag and one real request per provider behind the `BATON_E2E_PROVIDERS=1` flag, run manually before a release
- Linter, `go vet`, `-race` in the project's CI

## 15. Milestones and Readiness Criteria

### M1 — Core (2 weeks)

Parsing, validation, types, `secret`, expressions, templates, `run`, `assert`, run directory, exit codes, CLI `run`/`validate`/`schema`. `internal/httpx`: auth schemes `header`/`bearer`/`basic`/`query`/`path`, pagination `link_header`/`page`, envelope, gojq, URL token redaction. `internal/packs`: loading from a local directory, validation, `http.op`. The first pack — `gitlab` with `get_change` and `post_comment`, local.
Done: a scenario from `run` → `http: { op: gitlab.post_comment }` → `assert` works from GitLab CI; secrets do not leak into `runs/`.

### M2 — LLM and Reports (2 weeks)

The `Provider` interface, implementations `anthropic`, `openai`, `openai_compatible` (+ `openrouter` as its configuration), message and tool-call normalization, three levels of structured output, `fallback_models`, cost accounting; the `llm` step, schemas, `schema`-retry, `foreach`, cache, `resume`, `on_failure`, notification channels, global config, `runs list/show/logs`.
Done: a weekly report across 5 repositories from cron with a Telegram notification; the same scenario runs on `anthropic/…`, `openai/…` and `openrouter/…` with no changes beyond the `model` string; a repeated run spends no tokens.

Order within the milestone: `openai_compatible` first (simplest transport and covers OpenRouter), then `anthropic`, then `openai` with strict JSON Schema.

### M3 — Agent (2 weeks)

The `fake` engine, the `claude-code` adapter, the MCP gateway, `submit_result`, `commands`, tools from readonly pack operations, profiles, workspace preparation, the audit log, `agent` budgets.
Done: a code-review scenario with the `review` profile works on a real MR; the agent has no access to tokens; `baton tools` shows exactly the declared tools.

### M3.5 — Packs and Interfaces (1.5 weeks)

Loading packs from a git source with a pin and a checksum, the pack cache, the interface registry `forge/v1`, `tracker/v1`, `notify/v1` with `implements` checking via `examples/`, `interface:` in a scenario with pack selection by input, `exchange` authorization with a chain and cookies, `offset`/`cursor` pagination, `form`, `graphql`, `baton apis import/validate/call`, `md2html`/`md2text`. The `baton-apis` repository: `gitlab`, `github`, `gitea` under `forge/v1`; `jira` under `tracker/v1`; `telegram`, `slack` under `notify/v1`; contract tests in its CI. The `telegram` channel moves to `notify/v1`.
Done: one review scenario runs on GitLab, GitHub and Gitea by only changing `-i forge=`; `baton apis validate` is green on all packs; Telegram notifications go through the pack.

### M4 — Resilience (1 week)

`until`, `fallback`, `dedupe_key`, `switch`, static checking of references to missing steps, `fetch`, `state`, third-party MCP with `unsafe` marking, cancellation via signals.
Done: the `fix` profile fixes code and runs tests in a loop; the checklist in section 13 passes in full.

### M5 — Polish (1 week)

Documentation: README, a schema reference (generated from the JSON Schema), three example scenarios (review, report, triage). Release build via goreleaser, binaries for linux/amd64 and linux/arm64.

## 16. Open Questions

The items marked **Resolved** are closed; they stay here together with the decision so the context is not lost.

1. **Name. Resolved:** the name stays `baton`, even though the word is taken in this niche by several projects. The binary, the `~/.config/baton` config directory and the `github.com/foxzi/baton` module path are final; `segue` is dropped
2. **Deny-lists for Claude Code's built-in tools. Resolved:** the `PreToolUse` hook is not used. The built-in `Read`/`Glob`/`Grep`/`Write` stay with the CLI (the gateway serves no `fs.*` tools to the `claude-code` engine), and the step's policy is expressed as denials: `settings.json` carries a `permissions.deny` list with a `Read(<pattern>)`/`Edit(<pattern>)` pair per deny-list entry, while tool names (`Bash`, `BashOutput`, `KillShell`, `WebFetch`, `WebSearch`, `Task`, plus `Edit`/`Write`/`NotebookEdit` when `fs.write` is not `workspace`) go both into the settings and into `--disallowedTools`. Implementation: `internal/agent/claudecode/setup.go`, `internal/engine/agent.go`
3. **Structured output via OpenRouter. Resolved:** `/models` is not queried. `openrouter` reports `structured_output: true` and `tools: true`, an arbitrary `openai_compatible` reports `structured_output: false` until the config says otherwise through `capabilities`. From there `provider.ResolveMode` picks the mode from the reported capabilities, and inaccurate metadata is overridden by an explicit `structured_mode: tool|prompt` on the `llm` step. There is no automatic downgrade after a model refuses: the refusal surfaces as a `schema`-class error. Implementation: `internal/provider/compat.go`, `internal/provider/structured.go`
3a. **OpenAI's Responses API. Resolved:** v1 uses Chat Completions (`internal/provider/openai.go`), which is stable. The Responses API is newer and better for tools; reconsider if the Go SDK makes it the primary one
4. **State between runs. Resolved:** Baton does not require `state/` to persist between runs. When needed, a scenario saves its work results as files. An ephemeral CI filesystem is a normal execution environment, not an architectural limitation; no mandatory external state store is needed.
5. **Format of `commands.from`. Resolved:** `commands.from` is not implemented in v1. Commands and their arguments are declared explicitly inside the scenario's own `commands:` map (section 7.5, `internal/scenario/types.go`'s `Commands map[string]Command`); there is no external command set, no per-stack file, and no auto-wiring by the presence of `go.mod`/`package.json`. A scenario that wants the same commands in several places copies the block or is generated from a shared source outside Baton.
6. **Scope of the interfaces. Resolved (with a review trigger):** `forge/v1` with five operations may turn out too small (are `get_pipeline_status`, `list_changes` needed?). Rule: an interface is extended only once an operation is needed by two scenarios on two forges; until then — direct pack operations. Reconsider after the first month of operation
7. **jq in packs as logic outside the binary. Resolved:** the trade-off is accepted deliberately: transforms can have bugs and are tested worse than Go. Compensation — mandatory `examples/` for `implements` and contract tests in `baton-apis`. If transforms start growing toward conditional logic — that is a signal to move the operation into an interface with several simple packs, rather than making the jq more complex
