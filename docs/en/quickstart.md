# Quickstart

Ten minutes from an empty directory to a scenario that calls a model, checks the
result against a schema and reports what the run cost.

Every command and every listing below is the real output of the version in this
repository, not an illustration.

## 1. Install

Baton is a single binary with no runtime dependencies. Take it from a release
or build it from source.

The [releases page](https://github.com/foxzi/baton/releases) carries `tar.gz`
archives for linux/amd64 and linux/arm64 alongside a `checksums.txt`:

```sh
tag=v0.1.0
curl -fsSLO https://github.com/foxzi/baton/releases/download/$tag/baton_${tag#v}_linux_amd64.tar.gz
curl -fsSLO https://github.com/foxzi/baton/releases/download/$tag/checksums.txt
sha256sum --check --ignore-missing checksums.txt
tar -xzf baton_${tag#v}_linux_amd64.tar.gz baton
./baton version
```

Building from source needs Go 1.25.7 or newer:

```sh
git clone https://github.com/foxzi/baton.git
cd baton
make build          # writes ./baton with version metadata
./baton version
```

Put the binary somewhere on your `PATH` if you like — the rest of this page
assumes plain `baton`.

## 2. First run: `baton init`

`baton init` writes a runnable scenario into a directory of your choice —
no checkout of this repository needed:

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

The `hello` template (the default) needs no API key and no network. It checks
every target path before writing anything, so running it again against the
same directory is safe rather than destructive:

```console
$ baton init myworkflow
baton: refusing to overwrite 2 existing files:
  myworkflow/hello.yaml
  myworkflow/scenario.schema.json
```

`init` also writes `scenario.schema.json` next to the scenario — this build's
own JSON Schema of the format — and points `hello.yaml` at it with a
`yaml-language-server` modeline comment, so editors that support it validate
and autocomplete the file offline. See [Editor setup](editor-setup.md) for
which editors that covers and how to point one at a scenario you did not
generate with `init`.

`baton init myworkflow --template summarize` writes a two-step scenario — an
`llm` call checked against a JSON schema, then a `file` step that saves the
result — plus a `baton.yaml` already pointed at a provider — the shortest
path to a real model call, covered in [section 6](#6-add-a-model). The text
to summarize and the output path are scenario inputs, overridable with
`-i text=... -i out=...` on any run. `baton help init` (or `baton init -h`)
documents both templates and every option; `-h`/`--help` never write a file,
on this command or any other.

## 3. Run the smallest scenario

`myworkflow/hello.yaml`, the file `baton init` just wrote, is the whole format
in seventeen lines: one command and one check. It is also `examples/hello.yaml`
in this repository, byte for byte, if you cloned it instead.


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

Three things in there are worth naming, because they are the shape of every
larger scenario:

- **Inputs are typed and constrained.** `pattern` is not optional decoration:
  a string input that reaches `argv` must have one, or validation fails. There
  is no shell in the picture — `argv` is passed to the process directly — so
  the pattern is the boundary, not quoting.
- **Steps are a DAG, not a list.** `needs:` declares the dependency; steps
  without a dependency between them may run in parallel.
- **A step can fail the run on purpose.** `assert` exists so a scenario can
  block a merge in CI with a distinct exit code.

Check it, then run it:

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

The input constraint is enforced before anything executes:

```console
$ baton run examples/hello.yaml -i 'who=Baton 1'
baton: input "who": value does not match pattern "^[A-Za-z]+$"
```

`--dry-run` resolves inputs and secrets and prints the plan without executing a
single step — the cheapest way to see what a scenario would do:

```console
$ baton run examples/hello.yaml -i who=Baton --dry-run
scenario: hello
inputs:
  who = Baton
plan:
  1. greet (run)
  2. check_exit (assert)
```

## 4. What a run leaves behind

Every run is a directory. By default it is `runs/` next to the scenario;
`--runs-dir` puts it elsewhere.

```
runs/20260909-003107-0bfb/
├── run.json             # inputs, status, timings, the resolved plan
├── events.jsonl         # the structured event log, secrets redacted
├── cost.json            # tokens and dollars, per step and total
└── steps/
    └── greet/
        ├── output.json  # the step result other steps interpolate
        └── stdout.log
```

`output.json` is the contract between steps. For the `greet` step above:

```json
{
  "exit_code": 0,
  "result": "hello Baton\n",
  "status": "success"
}
```

Three commands read runs back, so you rarely need to open the files by hand:

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

The summary line `run` and `resume` print at the end grows three more lines
when they apply: `cost:` once a step actually priced tokens, `artifacts:`
listing every path a `file` step's `write`/`append` op actually wrote, and —
only when the run failed — a ready-to-paste `resume:` command. Take `greet`
above and add a second step, `file: { write: report.txt, content: "{{
.steps.greet.result }}" }`:

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

`details:`/`logs:` are always printed, `--runs-dir` spelled out because the
CLI's own default for that flag depends on the current directory, which need
not be the one the run actually used.

## 5. Failure is a distinct exit code

Change the assert to `steps.greet.exit_code == 42` and the run fails where the
check is, not somewhere downstream:

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

CI can tell the cases apart without parsing logs:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | a step failed |
| 2 | an `assert` tripped |
| 3 | bad configuration — an unset secret, an input that fails its pattern, an invalid scenario |
| 4 | the budget ran out |
| 130 | interrupted |

`baton resume <run-id>` continues a failed run at the step that failed, reusing
the results of the steps that already succeeded.

## 6. Add a model

The scenario names a model as `<provider>/<model>`; where that provider lives
and which key it uses belongs to the global configuration, not to the scenario.
Baton reads `~/.config/baton/config.yaml` (or `$XDG_CONFIG_HOME/baton/config.yaml`)
first, then `./baton.yaml` in the current directory, merging the two with the
local file's values winning; `--config FILE` (repeatable, for `run`, `apis` and
`tools`) or `BATON_CONFIG` replaces that search entirely with the file(s) named.

The fastest way to see this work end to end is `baton init myworkflow
--template summarize`: it writes the `baton.yaml` below already filled in for
one provider, plus a scenario and a schema, so only the key is left to set.
Before setting it, `baton doctor` checks everything that does not need the
network — the scenario, which configuration file it found, and whether the
provider and its api key variable are in place:

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

`doctor` never makes a network call, so a clean report is not proof the key is
valid or that the provider is reachable — only `baton run` confirms that. The
`MISSING .../config.yaml` line names `os.UserConfigDir()` on the machine that
ran this, `/home/you/...` here to avoid printing an actual username; yours
will differ.

Run it as-is and the template's own default `text` gets summarized into
`summary.json` in the current directory, from the template's `file` step;
pass your own text and a different output path with `-i`:

```console
$ baton run summarize.yaml -i text="The migration finished ahead of schedule." -i out=notes.json
$ cat notes.json
{"keywords":["migration","schedule"],"summary":"The migration finished early."}
```

`baton.yaml` is only auto-loaded from the current directory (see above), not
resolved relative to the scenario file, so `cd myworkflow` first; running
`baton run myworkflow/summarize.yaml` from the parent directory would miss it
and fail with "unknown provider".

To build the same configuration by hand, create `baton.yaml` next to the
scenario:

```yaml
providers:
  openrouter:
    kind: openrouter
    api_key: { from: env, key: OPENROUTER_API_KEY }
    app_name: baton

notify:
  report:
    kind: stdout        # the other built-in kind is webhook

pricing:
  # OpenRouter reports the real cost in the response; this table is the
  # fallback for a provider that does not.
  openrouter/openai/gpt-4.1-nano:
    input_per_mtok: 0.1
    output_per_mtok: 0.4
```


Keys are never written into the scenario, the run directory or the cache, and
interpolating one into a prompt is a validation error rather than a leak. They
are read from the environment or from a file at the moment a step needs them.
Anthropic and OpenAI providers are configured the same way with
`kind: anthropic` / `kind: openai`; `kind: openai_compatible` with a `base_url`
covers Ollama, vLLM and corporate proxies.

A channel has a third form besides the two built-in kinds: it names an `apis`
entry whose pack implements `notify/v1` and the address to send to. This
repository ships the `telegram` pack in `apis/telegram/`, so notifying a chat
takes a pack entry and a channel that points at it:

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
    target: "-1001234567890"   # a chat id, or @channelname
```

The entry authorises with a secret of the configuration, which a scenario
cannot name and never sees; `notify: chat` in a step calls the pack's `send`
operation with the channel's target and the rendered message.

`examples/llm-smoke.yaml` is the shortest scenario that exercises a provider
end to end: a one-word answer, a JSON answer validated against a schema, and a
notification. Note that `schema:` is **required** on an `llm` step — a model
result that other steps can branch on has to be a validated structure, never
free text.

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

`steps/extract/output.json` records what the provider actually did, which is
what you check when a model or a key misbehaves:

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

`mode` is the structured-output strategy the provider accepted — native JSON
schema, a forced tool call, or a prompt instruction with validation.
`model_used` differs from the requested model when a `fallback_models` entry
took over. Money is accounted per step:

```console
$ baton runs show 20260909-003146-f99c
...
cost:     $0.0000
cost by step:
  ping                 openrouter/openai/gpt-4.1-nano in=60 out=6 $0.0000
  extract              openrouter/openai/gpt-4.1-nano in=156 out=25 $0.0000
```

with the exact figures in `cost.json` (`total_usd: 0.000034` for the run above).
A scenario's `budget:` section caps dollars, tokens and wall time; exhausting it
stops the run with exit code 4 and still runs `on_failure`.

## 7. Where to go next

The other examples in `examples/` are working scenarios the test suite runs, in
rough order of complexity:

| Scenario | What it adds |
|---|---|
| `hello.yaml` | a command and an assert |
| `llm-smoke.yaml` | two model calls, a schema, a notification |
| `mr-comment.yaml` | reading a merge request over HTTP and answering with a comment |
| `weekly-report.yaml` | a parallel `foreach` over projects, digested and sent to a channel |
| `triage.yaml` | classification against an `enum`, labels, a comment, paging the on-call only when critical |
| `review.yaml` | an `agent` step working over a checkout |
| `jira-report.yaml` | pagination and aggregation inside the pack, an HTML report rendered to a file |
| `jira-quality.yaml` | a `foreach` asking the model to score every issue of a board against a schema, collected into an HTML report |

The packs the repository ships live in `apis/` and are loaded with
`from: ./apis/`. Each one names the interface it implements, so a scenario that
declares `interface: forge/v1` can be moved between them by changing an input:

| Pack | Interface | Operations |
|---|---|---|
| [`gitlab`](../../apis/gitlab/README.md) | `forge/v1` | `get_change`, `list_files`, `get_file`, `post_comment`, `list_merge_requests` |
| [`github`](../../apis/github/README.md) | `forge/v1` | `list_files`, `get_file`, `post_comment`, `post_review`, plus `get_change` without the file list |
| [`gitea`](../../apis/gitea/README.md) | `forge/v1` | the same as GitHub, except that `list_files` carries no diff text |
| [`jira`](../../apis/jira/README.md) | `tracker/v1` | `get_issue`, `search`, `create_issue`, `comment` |
| [`jira-server`](../../apis/jira-server/README.md) | `tracker/v1` | `get_issue`, `search`, `search_all`, `search_summary`, `list_boards`, `list_board_issues`; read-only |
| [`telegram`](../../apis/telegram/README.md) | `notify/v1` | `send`, `send_document`, `get_me` |
| [`slack`](../../apis/slack/README.md) | `notify/v1` | `send`, `auth_test` |

`baton apis validate apis/*` parses every one of them and replays the recorded
responses in `apis/<pack>/examples/` through the transforms, which is what the
test suite does on each commit.

Useful commands while writing one:

```sh
baton validate scenario.yaml           # every problem at once, not just the first
baton run scenario.yaml --dry-run      # the plan, without executing
baton tools scenario.yaml --step ID    # the tools an agent step would be given
baton schema                           # the JSON Schema of the format
baton run scenario.yaml --json         # events as JSONL, for a CI collector
```

In CI a run is one command and its exit code:

```yaml
- run: baton run review.yaml -i project=$CI_PROJECT_PATH -i mr=$CI_MERGE_REQUEST_IID
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
```

Baton has no scheduler and no webhooks by design: cron, a systemd timer or your
CI's schedule is the trigger.

Then read on:

- [Scenario schema reference](schema.md) — every field of the format, generated
  from the JSON Schema
- [Editor setup](editor-setup.md) — validation and autocomplete for scenario
  files in VS Code, IntelliJ and any other yaml-language-server client
- [Project overview](overview.md) — what Baton is for and why it is shaped this way
- [Specification for v1](spec.md) — the authoritative technical document
