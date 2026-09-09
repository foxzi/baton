# jira-server

An API pack that teaches baton the Jira Server / Data Center REST API
without putting the name Jira into the binary. It is a separate pack from
`jira`, which speaks Jira Cloud: the two differ in three ways that a single
pack cannot straddle — authorisation, the search endpoint and what an Agile
board reports about itself. It implements one operation of the `tracker/v1`
interface and adds several read-only reporting operations beyond it.

Everything in this pack was checked against a live Jira Server 9.17.5 site,
and the recorded examples are that site's own responses with the values
anonymised.

| | |
|---|---|
| Pack | `jira-server`, version 1 |
| API | Jira Server / Data Center REST API |
| Interface | `tracker/v1` — `get_issue` |
| Extra operations | `search`, `search_all`, `search_summary`, `list_boards`, `list_board_issues` |
| Auth | `bearer`, personal access token |
| Base URL | none, `base_url` is required |
| Pagination | `startAt` offset, per-operation page size, up to 50 pages |

This pack is read-only: no operation writes to Jira.

## Wiring it into a scenario

```yaml
secrets:
  jira: { from: env, key: JIRA_TOKEN }

apis:
  tracker:
    pack: jira-server
    from: ../apis/
    config: { base_url: "{{ .inputs.jira_url }}" }
    auth: { secret: jira }
    # A wide JQL over a busy project walks tens of pages; the per-request
    # timeout has to leave room for the slowest of them.
    timeout: 60s

steps:
  - id: issues
    http:
      op: tracker.search_summary
      args:
        query: "{{ .inputs.jql }}"
```

The name on the left (`tracker`) is how steps address the pack, so the same
scenario reads `tracker.search_summary` no matter which tracker it talks to.
A scenario may also declare `interface: tracker/v1`, and then baton refuses
to load a pack that does not cover the whole interface.

A second scenario, `examples/jira-quality.yaml`, walks one board with
`list_board_issues` and asks a model to score each issue's description:

```yaml
apis:
  tracker:
    pack: jira-server
    from: ../apis/
    config: { base_url: "{{ .inputs.jira_url }}" }
    auth: { secret: jira }
    timeout: 60s

steps:
  - id: issues
    cache: true
    http:
      op: tracker.list_board_issues
      args:
        board: "{{ .inputs.board }}"
        # description is what is being judged, so it has to be asked for.
        fields: summary,status,description
        # The pack calls this argument query; jql is its name on the wire.
        query: "{{ .inputs.jql }}"
```

## Configuration

| Key | Required | Default | Meaning |
|---|---|---|---|
| `base_url` | yes | — | The site root, without a REST prefix; the paths below carry their own `/rest/api/2/...` and `/rest/agile/1.0/...` prefixes |
| `auth.secret` | yes | — | A personal access token (Profile -> Personal Access Tokens), sent as `Authorization: Bearer` |

A Jira Server personal access token has no user half to pair it with, unlike
the Cloud pack's Basic scheme.

## Operations

### `get_issue` — `GET /rest/api/2/issue/{key}`

Read-only. Implements `tracker/v1.get_issue`.

| Argument | In | Wire name | Pattern | Default | Required |
|---|---|---|---|---|---|
| `key` | path | `key` | `^[A-Z][A-Z0-9_]*-\d+$` | — | yes |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status,description,reporter` | yes (has a default) |

```json
{ "key": "DEMO-8661", "title": "Search misses returned orders",
  "url": "https://jira.example.com/browse/DEMO-8661",
  "status": "In review", "body": "Steps:\n\n...", "author": "Jane Doe" }
```

`fields` is requested explicitly so the response carries only what the
transform reads: this endpoint's own default field set is every field of the
issue, tens of kilobytes on a busy project. A field that was not requested,
or one the issue leaves empty, comes back as null or missing, and
`tracker/v1` types `status`, `body` and `author` as strings, so each is added
to the result only when it has a value. `url` is the browsable link,
rebuilt from the REST url of the issue, since Jira reports no other: `self`
is `.../rest/api/2/issue/86908` and a reader wants `.../browse/DEMO-8661`.
The transforms of this pack build it as
`((.self | split("/rest/"))[0]) + "/browse/" + .key`.

### `search` — `GET /rest/api/2/search`

Read-only, single page. Beyond `tracker/v1`.

| Argument | In | Wire name | Pattern | Default | Required |
|---|---|---|---|---|---|
| `query` | query | `jql` | `^[\s\S]+$`, max 4000 chars | — | yes |
| `limit` | query | `maxResults` | `^\d+$` | — | no |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status` | yes (has a default) |

```json
[ { "key": "DEMO-8661", "title": "Search misses returned orders",
    "status": "In review", "url": "https://jira.example.com/browse/DEMO-8661" } ]
```

This stays single-page on purpose: `tracker/v1.search` takes a `limit`, and a
paginated operation would overwrite it with the page size. `search_all`
below walks every page instead.

### `search_all` — `GET /rest/api/2/search`

Read-only, paginated. Beyond `tracker/v1`.

| Argument | In | Wire name | Pattern | Default | Required |
|---|---|---|---|---|---|
| `query` | query | `jql` | `^[\s\S]+$`, max 4000 chars | — | yes |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status` | yes (has a default) |

Pagination: `style: offset`, `param: startAt`, `limit_param: maxResults`, in
query, page size 100, up to 50 pages. Paginated results reach the transform
as the concatenation of the pages' items, so the transform input is the
array of issues itself.

```json
[ { "key": "DEMO-8661", "title": "Search misses returned orders",
    "status": "In review", "url": "https://jira.example.com/browse/DEMO-8661" } ]
```

### `search_summary` — `GET /rest/api/2/search`

Read-only, paginated. Beyond `tracker/v1`. `search_all` reshaped for a
report: the same walk, but the transform also counts the issues by status,
assignee and type. The counting belongs in the transform because jq is the
only place in a scenario that can group a list; a template can iterate but
not aggregate.

| Argument | In | Wire name | Pattern | Default | Required |
|---|---|---|---|---|---|
| `query` | query | `jql` | `^[\s\S]+$`, max 4000 chars | — | yes |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status,assignee,priority,issuetype,created,updated` | yes (has a default) |

Pagination: `style: offset`, `param: startAt`, `limit_param: maxResults`, in
query, page size 100, up to 50 pages.

```json
{
  "total": 4,
  "by_status": [ { "name": "Open", "category": "To Do", "count": 2 } ],
  "by_assignee": [ { "name": "Unassigned", "count": 1 } ],
  "by_type": [ { "name": "Bug", "count": 2 } ],
  "issues": [ { "key": "DEMO-8663", "title": "Add retry logic to the payment webhook consumer",
    "status": "Open", "category": "To Do", "assignee": "Unassigned", "priority": "Critical",
    "type": "Story", "created": "2025-08-16", "updated": "2025-08-16",
    "url": "https://jira.example.com/browse/DEMO-8663" } ]
}
```

`total` here is the number of issues actually walked, which is short of the
site's own total when the walk stops at `max_pages`. The default field set
is wider than `search`'s because a report names the assignee, the priority
and the type, and sorts by the update date.

### `list_boards` — `GET /rest/agile/1.0/board`

Read-only, paginated. Beyond `tracker/v1`.

| Argument | In | Wire name | Pattern | Default | Required |
|---|---|---|---|---|---|
| `project` | query | `projectKeyOrId` | `^[\w-]+$` | — | no |
| `type` | query | `type` | `^(scrum\|kanban\|simple)$`, enum `scrum`, `kanban`, `simple` | — | no |
| `name` | query | `name` | `^[\s\S]+$`, max 200 chars | — | no |

Pagination: `style: offset`, `param: startAt`, `limit_param: maxResults`, in
query, page size 50, up to 50 pages. The Agile API caps this endpoint at 50
whatever is asked for: a request for 100 comes back with `maxResults` 50.
The page size has to match that cap, or the first page looks short and the
walk ends after one page.

```json
[ { "id": 91, "name": "(AT) scrum", "type": "scrum",
    "url": "https://jira.example.com/secure/RapidBoard.jspa?rapidView=91" } ]
```

A Server board carries no location, unlike a Cloud one, so there is no
project key to report here; `list_board_issues` gets the issues of a board,
and their keys name the project.

### `list_board_issues` — `GET /rest/agile/1.0/board/{board}/issue`

Read-only, paginated. Beyond `tracker/v1`.

| Argument | In | Wire name | Pattern | Default | Required |
|---|---|---|---|---|---|
| `board` | path | `board` | `^\d+$` | — | yes |
| `query` | query | `jql` | `^[\s\S]+$`, max 4000 chars | — | no |
| `fields` | query | `fields` | `^[\w,.-]+$` | `summary,status` | yes (has a default) |

Pagination: `style: offset`, `param: startAt`, `limit_param: maxResults`, in
query, page size 100, up to 50 pages. The board's own filter already scopes
the result; `query` narrows it further, for instance to one sprint or one
status.

```json
[ { "key": "DEMO-8438", "title": "Move the catalogue page to Inertia",
    "status": "In review", "url": "https://jira.example.com/browse/DEMO-8438" } ]
```

`description` comes along only when `fields` asked for it, so the key stays
absent from the result instead of turning into null for the default fields.

## What is not here, and why

Both `comment` (`POST /rest/api/2/issue/{key}/comment`) and `create_issue`
(`POST /rest/api/2/issue`, using dotted body names) are expressible now, and
the `jira` pack carries both, but the personal access token this pack was
developed against is read-only, so no write request was ever made against a
live site. They belong here once someone can check them; until then the
pack ships only what was verified. A scenario that must write to a Server
issue posts it with a raw http step through this api.

## A note on the recorded examples

`baton apis validate` replays an example the way a run does: for a
paginated operation (`search_all.json`, `search_summary.json`,
`list_boards.json`, `list_board_issues.json`) the file is the raw body of
one page, `pagination.items` is applied to it, and the transform is fed
that page's items. Recording a fresh example is therefore a plain
single-page request against the API — no post-processing.

## Validating a change

```sh
baton apis validate apis/jira-server
```

This parses the pack and replays every recorded response in
`apis/jira-server/examples/` through the transforms, checking the result
against the interface schema for each operation that names an `implements`.
The test suite does the same on every commit, so an operation with an
`implements` needs its example file kept in step with its transform.
