# jira

An API pack that teaches baton the Jira Cloud REST API without putting the
name Jira into the binary. It implements the whole `tracker/v1` interface,
so a scenario written against `tracker/v1` can run on Jira Cloud by loading
this pack.

| | |
|---|---|
| Pack | `jira`, version 1 |
| API | Jira Cloud REST API |
| Interface | `tracker/v1` — `get_issue`, `search`, `create_issue`, `comment` |
| Extra operations | none |
| Auth | `basic`, user from config, token as the secret |
| Base URL | none, `base_url` is required |
| Pagination | none (see `search` below) |

## Wiring it into a scenario

```yaml
secrets:
  jira: { from: env, key: JIRA_TOKEN }

apis:
  tracker:
    pack: jira
    from: ../apis/
    config:
      base_url: "{{ .inputs.jira_url }}"
      user: "{{ .inputs.jira_user }}"
    auth: { secret: jira }

steps:
  - id: issue
    http:
      op: tracker.get_issue
      args: { key: "{{ .inputs.key }}" }
```

The name on the left (`tracker`) is how steps address the pack, so the same
scenario reads `tracker.get_issue` no matter which tracker it talks to. A
scenario may also declare `interface: tracker/v1`, and then baton refuses to
load a pack that does not cover the whole interface.

## Configuration

| Key | Required | Default | Meaning |
|---|---|---|---|
| `base_url` | yes | — | The Jira Cloud site, `https://<site>.atlassian.net`; the paths of this pack already carry their own `/rest/api/...` prefix |
| `user` | yes | — | The Atlassian account email the API token was issued for |
| `auth.secret` | yes | — | The API token itself, sent as the password half of HTTP Basic |

Jira Cloud accepts only HTTP Basic for an API token: the account email as the
user, the token as the password. A Bearer token is an OAuth 2.0 concept, and
Jira Cloud rejects an API token sent that way.

Jira Cloud offers two REST API generations at once: v3 speaks the Atlassian
Document Format for rich text, v2 still accepts and returns plain text for
the same resources. This pack sticks to v2 everywhere it sends or reads a
text field, so a transform never has to walk an ADF document tree, and only
reaches into v3 for search, which v2 no longer serves at all.

## Operations

### `get_issue` — `GET /rest/api/2/issue/{key}`

Read-only. Implements `tracker/v1.get_issue`.

| Argument | In | Pattern | Notes |
|---|---|---|---|
| `key` | path | `^[A-Z][A-Z0-9_]*-\d+$` | The issue key, for instance `PROJ-123` |

```json
{ "key": "PROJ-123", "title": "Fix login button alignment on mobile",
  "status": "In Progress", "body": "The login button overlaps the password field...",
  "author": "Alice Johnson", "url": "https://example.atlassian.net/rest/api/2/issue/10002" }
```

Jira hands out no browsable link, only `self`, the REST URL of the issue; a
scenario that wants a link to click builds it from the site and the key
itself.

### `search` — `GET /rest/api/3/search/jql`

Read-only. Implements `tracker/v1.search`.

| Argument | In | Wire name | Pattern | Default | Required |
|---|---|---|---|---|---|
| `query` | query | `jql` | `^[\s\S]+$`, max 4000 chars | — | yes |
| `limit` | query | `maxResults` | `^\d+$` | — | no |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status` | yes (has a default) |

```json
[ { "key": "PROJ-123", "title": "Fix login button alignment on mobile",
    "status": "In Progress", "url": "https://example.atlassian.net/rest/api/2/issue/10002" } ]
```

The old `/rest/api/2/search` was retired and now answers 410 Gone; this v3
endpoint is the only search Jira Cloud still serves. It paginates through an
opaque `nextPageToken` rather than an offset, and this revision of the pack
does not walk it: a scenario gets the first page only, sized by `limit`
(the wire name is `maxResults`). `fields` is requested explicitly and
defaults to `summary,status`, because without it Jira answers with a smaller
default field set that omits status.

### `comment` — `POST /rest/api/2/issue/{key}/comment`

Writes. Implements `tracker/v1.comment`. Body is sent as JSON.

| Argument | In | Pattern / Limit | Notes |
|---|---|---|---|
| `key` | path | `^[A-Z][A-Z0-9_]*-\d+$` | |
| `body` | body | 32000 chars | Plain-text comment |

```json
{ "id": "10001", "url": "https://example.atlassian.net/rest/api/2/issue/10002/comment/10001" }
```

Not read-only, so this step is never served from the cache and `--dry-run`
never sends it.

### `create_issue` — `POST /rest/api/2/issue`

Writes. Implements `tracker/v1.create_issue`. Body is sent as JSON.

| Argument | In | Wire name | Pattern / Limit | Default | Required |
|---|---|---|---|---|---|
| `project` | body | `fields.project.key` | `^[A-Z][A-Z0-9_]*$` | — | yes |
| `title` | body | `fields.summary` | 255 chars | — | yes |
| `body` | body | `fields.description` | 32000 chars | — | no |
| `type` | body | `fields.issuetype.name` | 255 chars | `Task` | yes (has a default) |

```json
{ "key": "OPS-17", "url": "https://example.atlassian.net/rest/api/2/issue/10002" }
```

Jira nests the fields of a new issue under a single `"fields"` object, and
the dotted wire names above build exactly that: the request body comes out as
`{ "fields": { "project": { "key": ... }, "summary": ..., "description": ...,
"issuetype": { "name": ... } } }`. `type` defaults to `Task` because every
project template ships one; a project without it answers 400 with the list of
types it does have.

The response of a create is the bare identity of the issue — `id`, `key` and
`self`, no fields — so the result carries the key and the REST URL only.

## Validating a change

```sh
baton apis validate apis/jira
```

This parses the pack and replays every recorded response in
`apis/jira/examples/` through the transforms, checking the result against
the interface schema for each operation that names an `implements`. The test
suite does the same on every commit, so an operation with an `implements`
needs its example file kept in step with its transform.
