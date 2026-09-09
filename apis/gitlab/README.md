# gitlab

An API pack that teaches baton the GitLab REST API v4 without putting the name
GitLab into the binary. It implements four of the five operations of the
`forge/v1` interface, so a scenario written against `forge/v1` can run on
GitLab, GitHub or Gitea by changing which pack it loads.

| | |
|---|---|
| Pack | `gitlab`, version 1 |
| API | GitLab REST API v4 |
| Interface | `forge/v1` — `get_change`, `list_files`, `get_file`, `post_comment` |
| Extra operations | `list_merge_requests` |
| Auth | `header`, `PRIVATE-TOKEN` |
| Base URL | `https://gitlab.com/api/v4` by default |
| Pagination | `Link` header, up to 20 pages |

## Wiring it into a scenario

```yaml
secrets:
  gitlab_rw: { from: env, key: GITLAB_TOKEN }

apis:
  forge:
    pack: gitlab
    from: ../apis/
    # Omit config entirely for gitlab.com; GitLab CI exports CI_API_V4_URL
    # for a self-managed instance.
    config: { base_url: "{{ .inputs.api_url }}" }
    auth: { secret: gitlab_rw }
    timeout: 20s

steps:
  - id: changes
    http:
      op: forge.get_change
      args: { project: "{{ .inputs.project }}", id: "{{ .inputs.mr }}" }
```

The name on the left (`forge`) is how steps address the pack, so the same
scenario reads `forge.get_change` no matter which forge it talks to. A
scenario may also declare `interface: forge/v1`, and then baton refuses to
load a pack that does not cover the whole interface.

## Configuration

| Key | Required | Default | Meaning |
|---|---|---|---|
| `base_url` | no | `https://gitlab.com/api/v4` | API root, including `/api/v4` |
| `auth.secret` | yes | — | Personal, project or group access token, sent as `PRIVATE-TOKEN` |

The token needs `read_api` for the read-only operations and `api` to post a
comment.

## Operations

### `get_change` — `GET /projects/{project}/merge_requests/{id}/changes`

Read-only. Implements `forge/v1.get_change`.

| Argument | In | Pattern | Notes |
|---|---|---|---|
| `project` | path | `^[\w./-]+$` | Numeric id or `namespace/path`; percent-encoded as a single segment |
| `id` | path | `^\d+$` | The merge request `iid`, the number shown in its URL |

```json
{ "id": 42, "title": "...", "description": "...", "author": "user",
  "base": "sha", "head": "sha",
  "files": [ { "path": "a.go", "diff": "@@ ...", "deleted": false } ] }
```

Capped at 500k of response body.

### `list_files` — `GET /projects/{project}/merge_requests/{id}/diffs`

Read-only, paginated. Implements `forge/v1.list_files`. Same arguments as
`get_change`, and the result is the `files` array alone:

```json
[ { "path": "a.go", "diff": "@@ ...", "deleted": false } ]
```

The pages are concatenated before the transform runs, so a step sees one flat
array of every file. Capped at 500k per page.

### `get_file` — `GET /projects/{project}/repository/files/{path}`

Read-only. Implements `forge/v1.get_file`.

| Argument | In | Pattern | Notes |
|---|---|---|---|
| `project` | path | `^[\w./-]+$` | Percent-encoded as a single segment |
| `path` | path | `^[^\s?#]+$` | The whole path is one segment for GitLab, slashes included |
| `ref` | query | `^[\w./-]+$` | Defaults to `HEAD`; `forge/v1` leaves the ref optional, GitLab always wants one |

```json
{ "content": "package main\n...", "path": "cmd/main.go", "ref": "HEAD" }
```

GitLab returns the content base64-encoded whatever the file is; the transform
decodes it. Capped at 500k.

### `list_merge_requests` — `GET /projects/{project}/merge_requests`

Read-only, paginated. Beyond `forge/v1`: a report over merged work.

| Argument | In | Pattern | Notes |
|---|---|---|---|
| `project` | path | `^[\w./-]+$` | |
| `state` | query | `^[a-z]+$` | One of `opened`, `closed`, `merged`, `locked`, `all`; defaults to `merged` |
| `updated_after` | query | `^[\d:.TZ+-]+$` | Optional ISO 8601 date or timestamp |

```json
[ { "id": 42, "title": "...", "author": "user", "url": "https://...",
    "merged_at": "2024-05-01T10:00:00Z", "updated_at": "2024-05-01T10:00:00Z" } ]
```

### `post_comment` — `POST /projects/{project}/merge_requests/{id}/notes`

Writes. Implements `forge/v1.post_comment`. Body is sent as JSON.

| Argument | In | Limit | Notes |
|---|---|---|---|
| `project` | path | | |
| `id` | path | | |
| `body` | body | 65000 chars | The comment text, Markdown |

```json
{ "id": 1, "url": "https://gitlab.example/.../notes/1" }
```

Not read-only, so this step is never served from the cache and `--dry-run`
never sends it.

## Errors and rate limits

GitLab reports a failure with a status code and a `message` field, and that
message becomes the error of the step:

```yaml
envelope:
  error_when: 'type == "object" and .message != null'
  error_message: .message
```

The type check comes first because a list endpoint answers with an array,
which has no fields at all. Throttling is read from `RateLimit-Remaining` and
`Retry-After`, so a retrying step waits as long as GitLab asks it to.

## What is not here, and why

`post_review` is left out on purpose. GitLab posts one discussion per comment
and has no batch review call, so a scenario walks `post_comment` in a
`foreach` instead. Because the operation carries no `implements`, a scenario
that declares `interface: forge/v1` is told during validation rather than at
run time.

## Validating a change

```sh
baton apis validate apis/gitlab
```

This parses the pack and replays every recorded response in
`apis/gitlab/examples/` through the transforms, checking the result against
the interface schema for each operation that names an `implements`. The test
suite does the same on every commit, so an operation with an `implements`
needs its example file kept in step with its transform.
