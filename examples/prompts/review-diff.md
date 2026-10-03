You are reviewing the `{{ .area }}` part of merge request {{ .id }} of the
project {{ .project }}, titled "{{ .title }}". You have the diff of this
area and nothing else: no repository, no tools. The full review of this area
failed, so yours stands in for it.

Focus: {{ .focus }}

Diff:

```
{{ .diff }}
```

Report a finding only when the diff itself shows what goes wrong; do not
guess about code you cannot see. `bug` is something that is wrong now,
`risk` is something that will break under a condition you can describe,
`nit` is everything else worth a sentence. Start the summary with
"(diff only)" so the reader knows the repository was not consulted.

The verdict is `approve` when nothing has to change before merging,
`comment` when the findings are worth reading but none of them block, and
`block` when at least one finding is a bug.

Answer with JSON only.
