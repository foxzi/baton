---
name: baton-scenario
description: Write, review and debug Baton scenarios — the YAML files (`version: 1`, `steps:`) that Baton runs to automate code review, MR/PR comments, Jira reports, LLM pipelines with JSON schemas, agent (claude-code/codex) tasks, foreach/until loops and human gates. Use this skill whenever the user wants to create a new scenario, add or change a step, make a scenario call GitLab/GitHub/Gitea/Jira/Slack/Telegram through a pack, wire an llm or agent step with a prompt and schema, or when `baton validate` or `baton run` fails and they ask why. Trigger even if the user does not say "scenario" but describes a Baton workflow, points at a `.yaml` with `steps:` and `llm:`/`run:`/`http:` keys, or mentions `baton run`, `baton validate`, `baton resume`, packs, `apis/`, `on_failure` or exit code 2/3/5.
---

# Baton scenario skill

Baton runs a DAG of steps described in one YAML file. Every step has one body
(`run`, `http`, `llm`, `agent`, `foreach`, `until`, `file`, `switch`, `gate`,
`assert`) and sees the results of earlier steps through templates. The engine
is strict on purpose: unknown keys, undeclared secrets, missing files and
unsafe argv inputs are rejected at validation time, so most of the authoring
effort is getting the scenario past `baton validate` and then `--dry-run`.

The scenario schema is generated from code and is the only authoritative
list of field names. This file is the workflow and the judgment calls;
field-level detail lives in the references below.

## Where to look

| Need | Read |
|---|---|
| Field names, defaults, allowed values | `docs/en/schema.md` (generated, authoritative) |
| Compact reference: templating, step kinds, packs, control flow, exit codes, validation messages | `references/cheatsheet.md` in this skill |
| Semantics and rationale | `docs/en/spec.md` |
| Real scenarios to copy from | `examples/*.yaml` (see "Which example to copy" in the cheatsheet) |
| Pack operations and their params | `apis/<pack>/pack.yaml` |

Read the cheatsheet first; open `schema.md` or `spec.md` only for the
section you need. Do not guess a field name — grep `docs/en/schema.md`.

## Workflow: writing a new scenario

1. **Pin down the goal in one sentence** and the inputs it needs. Ask the
   user if any of these are unknown: which forge or tracker (GitLab, GitHub,
   Gitea, Jira Cloud, Jira Server), which model provider, whether an agent
   should edit files or only read them, where secrets come from (env or
   file). Do not invent base URLs, tokens or model prices.
2. **Pick the closest example** and copy its shape rather than starting from
   a blank file. `review.yaml` for diff review, `mr-comment.yaml` for
   "fetch change, ask the model, post comment", `jira-report.yaml` for
   tracker queries plus an HTML template, `triage.yaml` for
   `switch`/`foreach`, `review-deep.yaml` for a multi-model pipeline,
   `hello.yaml` for a `run`-only smoke test.
3. **Choose step kinds** with these rules:
   - `run` for anything a CLI already does. `argv` is a list, never a shell
     string with pipes. Mark read-only commands `readonly: true` so they are
     cached and retried.
   - `http` with `op: <api>.<op>` for any service that has a pack in
     `apis/`. Only fall back to raw `url`/`path` when no pack operation
     exists.
   - `llm` when one model call with a JSON answer is enough. `schema` is
     required; write the schema file first, then the prompt.
   - `agent` only when the task needs tools: reading a repository tree,
     editing files, running tests. Give it a `profile` (`review`, `fix`,
     `research`) and a `result` schema; it must end with `submit_result`.
   - `foreach` for per-item work, `until` for retry-until-green loops,
     `switch` for routing on an enum result, `gate` for a human decision,
     `assert` to turn a bad result into exit code 2.
4. **Write the files**: scenario, `prompts/*.md`, `schemas/*.json`,
   `templates/*` next to it. Paths in the scenario are relative to the
   scenario file. Start the file with `# $schema: ./scenario.schema.json`
   when the schema is nearby so editors validate it.
5. **Declare secrets** under `secrets:` and reference them only where they
   are allowed (`apis.*.auth`, `http.auth`, `env.*.secret`). A secret in a
   prompt, `with`, `argv` or `message` is a validation error, and templates
   cannot read secrets at all.
6. **Guard argv inputs**: every string input that reaches `run.argv` needs
   a `pattern`. Without it validation fails.
7. **Validate, dry-run, run** — see below. Fix every error and read every
   warning; a warning about a model missing from `pricing` means the dollar
   budget is not enforced for that step. Add the model's prices to the
   config `pricing:` section only if the user supplies them — never invent
   prices.

## Workflow: reviewing or debugging an existing scenario

1. Run `./baton validate <file>` first and paste the output into your
   reasoning. Validation messages are precise; the cheatsheet lists the
   common ones with cause and fix. Do not start editing before you have
   seen them.
2. If validation passes but the run fails, map the exit code:
   `2` = an `assert` fired (and `on_failure` did not run), `3` = config,
   validation or secret error, `4` = budget, `5` = waiting at a `gate`,
   `1` = a step failed. Remember that `~/.config/baton/config.yaml` and
   `./baton.yaml` are loaded automatically (local wins), so a "config"
   error may come from a file the user forgot about. Then
   `./baton runs show <run-id>` for step statuses
   and cost, `./baton runs logs <run-id> --step <id>` for stdout/stderr, and
   `runs/<id>/steps/<id>/output.json` for the parsed result.
3. Typical root causes, roughly in order of frequency:
   - a template references `.steps.x.result` of a step that was skipped by
     `when` (use `coalesce`/`default`, or give the consumer the same `when`);
   - `steps.x.status` used where `steps.x.http_status` was meant;
   - a model answer that does not match `schema` (tighten the prompt, add
     `enum`s, keep `additionalProperties: false`);
   - an agent that never called `submit_result`;
   - a side-effect step retried without `dedupe_key` (duplicate comments);
   - `run: "cmd | grep"` — shells are never used, convert to `argv`.
4. Review checklist for someone else's scenario:
   - every `http` POST/PUT and every non-readonly `run` has `dedupe_key`
     and the right `on_error`;
   - `budget:` or `defaults.budget_usd` is set when `llm`/`agent` steps
     exist;
   - `needs:` is explicit when the default "all previous steps" would
     serialize work that could run in parallel, and `foreach.max_parallel`
     is bounded;
   - secrets come from `env`/`file`, never literals;
   - `agent` steps use the narrowest `profile` and list `tools.apis` only
     for read-only ops;
   - `on_failure:` notifies somewhere useful and does not itself call
     anything that can fail loudly.

## Verification commands

Build once, then iterate:

```bash
make build                                   # -> ./baton
./baton validate path/to/scenario.yaml       # exit 0 and no warnings is the goal
./baton run path/to/scenario.yaml --dry-run \
  -i project=group/repo -i mr=42             # validates, resolves secrets, prints the plan
# --dry-run still resolves every secret, including those in a loaded
# baton.yaml / --config; an unset key variable fails here, not later.
./baton run path/to/scenario.yaml -i ... --json
./baton runs show <run-id>; ./baton runs logs <run-id> --step <id>
./baton resume <run-id> --approve            # after exit 5 at a gate
```

Scenarios placed in `examples/` are also checked by `make validate-examples`
and the full `make check`, which must stay green.

When the user has no credentials or network at hand, use `engine: fake`
with an `agent.script` file for agent steps (script format: `docs/en/spec.md`
§8.4) and `examples/llm-smoke.yaml` as the model of a scenario
that needs only a provider key, so the structure can still be exercised.

## Output expectations

- Hand back complete files on disk, not fragments: the scenario plus every
  prompt, schema and template it references, so `baton validate` can pass.
- Keep scenarios small and linear; reach for `foreach`/`until`/`switch`
  only when the data is actually a list, a retry loop or an enum.
- Comment the scenario header with what it does and which inputs and
  secrets it expects, in the style of `examples/*.yaml`.
- Report the exact `baton validate` output you got, including warnings you
  chose to leave in place and why.
