# Quickstart

Ten minutes from an empty directory to a scenario that calls a model, checks the
result against a schema and reports what the run cost.

Every command and every listing below is the real output of the version in this
repository, not an illustration.

## 1. Build

Baton is a single binary with no runtime dependencies. There is no tagged
release yet, so build it from source; Go 1.25.7 or newer is required.

```sh
git clone https://github.com/foxzi/baton.git
cd baton
make build          # writes ./baton with version metadata
./baton version
```

Put the binary somewhere on your `PATH` if you like — the rest of this page
assumes plain `baton`.

## 2. Run the smallest scenario

A scenario is a YAML file. `examples/hello.yaml` is the whole format in
seventeen lines: one command and one check.

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

## 3. What a run leaves behind

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

## 4. Failure is a distinct exit code

Change the assert to `steps.greet.exit_code == 42` and the run fails where the
check is, not somewhere downstream:

```console
$ baton run examples/hello.yaml
[check_exit] step_started
[check_exit] step_failed: assert: assert failed: steps.greet.exit_code == 42
run 20260909-003207-93f9: failed in 6ms
failed step check_exit: assert: assert failed: steps.greet.exit_code == 42
run directory: examples/runs/20260909-003207-93f9
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

## 5. Add a model

The scenario names a model as `<provider>/<model>`; where that provider lives
and which key it uses belongs to the global configuration, not to the scenario.
Baton reads `~/.config/baton/config.yaml` and then `./baton.yaml`, later values
winning; `--config FILE` and `BATON_CONFIG` override the search.

Create `baton.yaml` next to the scenario:

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

## 6. Where to go next

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
- [Project overview](overview.md) — what Baton is for and why it is shaped this way
- [Specification for v1](spec.md) — the authoritative technical document
