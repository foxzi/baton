# Editor setup

Point your editor at Baton's scenario JSON Schema to get completion,
hover hints and checks for field names, types and required fields while
you type. This does not replace `baton validate`: semantic checks such as
step dependencies still require the CLI.

## What `baton init` already gives you

`baton init` writes `scenario.schema.json` — this build's own JSON Schema of
the scenario format, the same document `baton schema` prints — next to the
scenario file it generates, and adds a schema comment as the scenario's
first line:

```yaml
# $schema: ./scenario.schema.json
```

This is the IntelliJ-compatible `$schema` comment form, and
[yaml-language-server](https://github.com/redhat-developer/yaml-language-server)
(the engine behind the VS Code YAML extension, Neovim's YAML tooling, and
others) supports it directly, resolving the path relative to the scenario
file, not your workspace root. Nothing else to configure, and no network
access needed: the schema is a plain local file, generated from the binary
you ran `init` with, so it always matches the format that binary actually
parses.

This covers:

- **VS Code**, with the [YAML extension](https://marketplace.visualstudio.com/items?itemName=redhat.vscode-yaml)
  installed
- **IntelliJ-based IDEs** (IntelliJ IDEA, GoLand, PyCharm, WebStorm, ...),
  whose built-in JSON Schema support recognizes the `# $schema:` comment
  natively
- **Neovim**, through `yaml-language-server` wired up via `nvim-lspconfig` or
  a similar LSP client
- Any other client of `yaml-language-server`, since it also accepts the
  `# $schema:` form alongside its own `# yaml-language-server: $schema=...`
  modeline

## Pointing an editor at a scenario you did not generate with `init`

A scenario not created by `init` — one you wrote by hand, or one from before
this feature existed — has no `scenario.schema.json` next to it and no
schema comment. Two ways to add validation:

**Add the comment yourself.** Copy `scenario.schema.json` from a scenario
`init` already generated (or run `baton schema > scenario.schema.json` in the
scenario's directory), then add as the file's first line:

```yaml
# $schema: ./scenario.schema.json
```

Adjust the path if the schema file lives elsewhere relative to the scenario.

**Or configure your editor instead of the file**, if you would rather not
add a line to every scenario. In VS Code's `settings.json`:

```json
{
  "yaml.schemas": {
    "./scenario.schema.json": ["hello.yaml", "summarize.yaml"]
  }
}
```

The key is the schema path (or a URL); the value is a glob matching the
scenario files it applies to. See the
[yaml-language-server README](https://github.com/redhat-developer/yaml-language-server#associating-schemas)
for the full set of options, including per-workspace and remote-schema
setups.

## Keeping the schema current

`scenario.schema.json` is a snapshot: it reflects the format understood by
the `baton` binary that wrote it, at the moment it was written. If you
upgrade `baton` and the format has changed since, regenerate it:

```console
$ baton schema > scenario.schema.json
```

`init` refuses to overwrite an existing `scenario.schema.json` (the same
"nothing is overwritten" guarantee that covers the rest of what it writes),
so re-running `init` in the same directory will not refresh it — use `baton
schema` directly instead.
