# Spec review: inconsistencies and gaps

This document records the results of a full read-through of `docs/ru/overview.md` and `docs/ru/spec.md` before implementation started. Each item is either a contradiction between the two documents or a reference to an undefined concept within the spec. The items were not folded into the documents themselves, to avoid diverging from their author's edit: fixes are applied as the corresponding section comes up for work.

Section numbering below follows `docs/ru/spec.md`.

## Discrepancies between the overview and the spec

### 1. `switch` is not in the list of step types

`overview.md` and §1/§3.2 of the spec list seven step types. §3.2 states the rule: "exactly one of the fields `run`, `http`, `llm`, `agent`, `foreach`, `until`, `assert` must be present." At the same time, §3.10 introduces `switch`/`cases` as a step-level key — sugar that is expanded at load time.

The validator must apply the "exactly one of" rule **before** sugar expansion, otherwise a step with `switch` will fail it. The wording of §3.2 needs to be extended, or the order must be explicitly stated: `switch` expansion precedes the check.

### 2. `on_failure` behaviour on `assert`

`overview.md`: "The `on_failure` section runs for any error class, including budget exhaustion."
§9.3: "Does not run on exit code 2 (`assert`)."

Formally there is no contradiction — `assert` is defined in §3.9 as a deliberate stop, not an error class. But the overview's phrasing reads as unconditional. The README wording has been clarified.

### 3. The set of the agent's git tools

`overview.md` promises `log`, `diff`, `blame`, `show` and a local `commit`.
§7.3 describes only two flags: `git: { read: true, commit: false }`.

Which exact subcommands `read: true` unlocks is nowhere recorded. An explicit list is needed: it defines both the tool surface and the check in §13 ("the agent cannot call a tool not declared for the step").

## Gaps within the spec

### 4. `budget_tokens` is not declared in the scenario format

§8.3 (cost accounting): "if the model is not in `pricing` — ... the dollar budget is not checked for such a step, only the token budget is checked (`budget_tokens`)."

The field `budget_tokens` does not appear in §3.1 (top level, `budget`), §3.2 (common step fields), or `defaults`. It needs to either be added to the format — at the step and/or run level — or the mention removed and it acknowledged that when the model is absent from `pricing` the step's budget is not controlled at all.

### 5. The `run` step has no `readonly` field

`readonly: true` for `run` defines two behaviours at once:

- §9.4: a step without `readonly: true` is considered to have a side effect and is not retried automatically without a `dedupe_key`;
- §10.3: caching is enabled by default for "`run` with `readonly: true`".

But §3.3 (description of the `run` step) has no `readonly` field. It appears only for declared commands in §7.5. The field needs to be added to §3.3 with a default of `false`.

### 6. The `policy` class references a nonexistent `publish`

§9.1 lists "`publish` validation" among the sources of the `policy` class. The concept of `publish` no longer exists anywhere in the spec — it is a leftover from an earlier draft, when publishing the result apparently was a separate step type. Now that role is played by a plain `http` step. The mention should be removed.

### 7. Incorrect cross-reference for the cache

§3.2, field comment: `cache: true # see 8.3`. The cache is described in §10.3; §8.3 is about LLM providers.

### 8. Two auth syntaxes for `http`

§3.4: `auth: forge_rw` — the secret name as a string.
§7.4.1: `auth: { secret: forge_ro }` — an object.

One needs to be chosen as canonical and the other allowed as sugar, with this fixed in the JSON Schema.

### 9. The `until` example does not match the format

§3.8: `condition: "iter.test.exit_code == 0"`, where `iter` is "the result of the previous iteration." The reference `iter.test` implies that `iter` holds named sub-steps, but the body of `until` is a single `step:` **without** an `id` (same as in `foreach`). Either the example should read `iter.exit_code`, or the body of `until` should allow a list of steps with identifiers. The latter changes the format, the former only the example.

### 10. `Engine` and `Provider` are mixed together in the package tree

§2 places `anthropic/` inside `internal/engine` next to `claudecode/` and `fake/`. But these are two different interfaces: `Engine` (§8.1) spawns an external CLI agent, `Provider` (§8.3) makes a call to a model API. They have different contracts, different implementations and different test suites.

A separate `internal/provider` is needed, with `anthropic`, `openai`, `openai_compatible` implementations. Accordingly the secret-access rule from §2 ("secrets are accessible only to `secrets`, `httpx`, `gateway`, `notify` and `engine`") must also include `provider` — provider keys are exactly what it needs.

### 11. It is undefined who owns the `submit_result` state

§7.6: `submit_result` is validated by the **gateway**; on success the step is marked complete and the agent process receives a signal to finish.
§8.1: the result is returned in `AgentResult.Submitted` / `AgentResult.Result`, i.e. from the **engine**.

The engine cannot know the content of the result — the call arrived at the gateway over HTTP. So either the engine needs access to the submission state kept by the gateway, via a handle passed to it, or the result must be assembled by the step executor rather than the engine. This is the only implicit coupling between the two packages, and it should be fixed in §8.1 before M3 starts.

### 12. The resume suffix breaks the run-id format

§10.1: `run-id` — `YYYYMMDD-HHMMSS-<4 hex>`.
§10.4: a repeat run gets the same `id` with a suffix `-r1`, `-r2`.

If the format is validated by a regex (and it should be, since `run-id` can be overridden with the `--run-id` flag and is used in paths), the regex must allow an optional `-r<n>` suffix.

### 13. `render` is not covered by path checks

§5.2 gives templates the function `render(path, data)`. The path is taken relative to the scenario file, but the restriction against escaping it is nowhere stated, and the checklist in §13 has no item about path traversal in `render` — only about command arguments and `fs`. An item should be added: `render "../../etc/passwd"` must be rejected.

## Technical risks

### R1. Static reference checking does not come free with expr-lang

§4 requires two checks that look like the work of an expression compiler:

- rule 5: referencing a nonexistent `steps.<id>` is an error;
- rule 6: referencing a step with `when:` without a `coalesce`/`null` check wrapper is an error.

`expr.Compile` with `expr.Env(...)` does type the environment, but the behaviour depends on its kind: with a **struct** environment, an unknown field is a compile error, while with `map[string]any` unknown keys get the type `any` and silently return `nil` at runtime. Step identifiers in Baton are inherently dynamic, so the natural representation of the context is exactly a map.

Consequence: both checks will require either dynamically building an environment type for the specific scenario, or a custom AST walk of the expression matching `steps.*` paths against the list of declared steps. This is a distinct, noticeable chunk of work within M1 that is not called out as a separate line item in the "2 weeks" estimate.

The syntax from §3.9 — `filter(steps.review.result.findings, .severity == 'blocker')` — has been checked and is correct: in expr-lang predicates, accessing a field via a leading dot is allowed, `#` is not required.

### R2. Deny-lists for Claude Code's built-in tools

Open question #2 of the spec. The mechanism from §8.2 — a temporary settings file with a PreToolUse hook plus checking flags against `claude --help` of the installed version — is the least stable part of the project: it depends on someone else's release cycle and has no stable contract. The spec already provides a plan B (expose `fs` through the gateway and forbid the built-in `Read`/`Grep`); the decision is made based on the outcome of an experiment in M3.

The version installed in the development environment is Claude Code 2.1.263.

### R3. Project name

Open question #1 proposes settling the name question by M5. But the name is part of the module path, the binary name, the `~/.config/baton/` path, the `BATON_*` environment variable prefix, and the name of the packs repository `baton-apis`. Renaming after M1 means edits in hundreds of places plus broken config compatibility for early users.

Decision at the time of repository initialisation: stay with `baton`, module path `github.com/foxzi/baton`. The question is considered open until M5, as in the spec, but the cost of deferring it grows with every stage.
