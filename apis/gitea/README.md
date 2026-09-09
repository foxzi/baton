# gitea

An API pack that teaches baton the Gitea REST API v1 without putting the
name Gitea into the binary. It implements four of the five operations of the
`forge/v1` interface, so a scenario written against `forge/v1` can run on
Gitea, GitHub or GitLab by changing which pack it loads. Gitea's API mirrors
GitHub's, so this pack looks close to the `github` pack throughout.

| | |
|---|---|
| Pack | `gitea`, version 1 |
| API | Gitea REST API v1 |
| Interface | `forge/v1` — `list_files`, `get_file`, `post_comment`, `post_review` |
| Extra operations | `get_change` (no `implements`; see "What is not here and why") |
| Auth | `bearer`, sent as `Authorization: Bearer <token>` |
| Base URL | required, no default; a Gitea instance is self-hosted |
| Pagination | `Link` header, up to 20 pages |

## Wiring it into a scenario

```yaml
secrets:
  gitea_rw: { from: env, key: GITEA_TOKEN }

apis:
  forge:
    pack: gitea
    from: ../apis/
    config: { base_url: "https://{{ .inputs.host }}/api/v1" }
    auth: { secret: gitea_rw }
    timeout: 20s

steps:
  - id: files
    http:
      op: forge.list_files
      args: { project: "{{ .inputs.project }}", id: "{{ .inputs.pr }}" }
```

The name on the left (`forge`) is how steps address the pack, so the same
scenario reads `forge.list_files` no matter which forge it talks to. A
scenario may also declare `interface: forge/v1`, and then baton refuses to
load a pack that does not cover the whole interface.

## Configuration

| Key | Required | Default | Meaning |
|---|---|---|---|
| `base_url` | yes | — | `https://<host>/api/v1`; there is no default because a Gitea instance is self-hosted |
| `auth.secret` | yes | — | A Gitea access token, sent as `Authorization: Bearer <token>` |

Gitea also accepts a token as `Authorization: token <t>`; the pack uses the
bearer scheme baton already speaks instead of adding a token-specific auth
kind for one service.

## Operations

### `get_change` — `GET /repos/{project}/pulls/{id}`

Read-only. No `implements`: see "What is not here, and why".

| Argument | In | Pattern | Notes |
|---|---|---|---|
| `project` | path | `^[\w.-]+/[\w.-]+$` | `owner/repo` |
| `id` | path | `^\d+$` | The pull request number |

```json
{ "id": 42, "title": "...", "description": "...", "author": "user",
  "base": "sha", "head": "sha", "url": "https://gitea.example.com/..." }
```

### `list_files` — `GET /repos/{project}/pulls/{id}/files`

Read-only, paginated. Implements `forge/v1.list_files`. Same arguments as
`get_change`:

```json
[ { "path": "internal/httpx/paginate.go" },
  { "path": "internal/httpx/testdata/link_header.txt", "deleted": true },
  { "path": "internal/httpx/paginate_v2.go" } ]
```

Gitea reports no diff text on this endpoint, only the file names and their
change counts, so a scenario that needs the diff itself has to read the file
through `get_file`. Gitea calls a removed file "deleted" (GitHub calls it
"removed"); the transform maps that to the `deleted` flag `forge/v1`
expects. Capped at 500k per page.

### `get_file` — `GET /repos/{project}/contents/{path}`

Read-only. Implements `forge/v1.get_file`.

| Argument | In | Pattern | Notes |
|---|---|---|---|
| `project` | path | `^[\w.-]+/[\w.-]+$` | |
| `path` | path | `^[^\s?#]+$` | The whole path is one segment, slashes included |
| `ref` | query | `^[\w./-]+$` | Optional |

```json
{ "content": "package main\n...", "path": "internal/httpx/paginate.go",
  "sha": "9a1c0f5d6e7b8a9c0d1e2f3a4b5c6d7e8f901234" }
```

Unlike GitHub, Gitea does not wrap the base64 content across lines, so the
transform decodes it directly. The response does not echo the ref, only the
blob sha, which is reported as such. Capped at 500k.

### `post_comment` — `POST /repos/{project}/issues/{id}/comments`

Writes. Implements `forge/v1.post_comment`. Body is sent as JSON. A pull
request is an issue as far as this API is concerned, so the path goes
through `/issues/{id}`.

| Argument | In | Limit | Notes |
|---|---|---|---|
| `project` | path | | |
| `id` | path | | |
| `body` | body | 65000 chars | The comment text, Markdown |

```json
{ "id": 456789, "url": "https://gitea.example.com/example/project/pulls/42#issuecomment-456789" }
```

Not read-only, so this step is never served from the cache and `--dry-run`
never sends it.

### `post_review` — `POST /repos/{project}/pulls/{id}/reviews`

Writes. Implements `forge/v1.post_review`. Reviews a pull request with a
summary and line comments in one batch.

| Argument | In | Limit | Notes |
|---|---|---|---|
| `project` | path | | |
| `id` | path | | |
| `summary` | body | 65000 chars, optional | Sent on the wire as `body` |
| `comments` | body | | Each entry is `{ path, body, new_position }` |
| `event` | body | | One of `COMMENT`, `APPROVED`, `REQUEST_CHANGES`; defaults to `COMMENT` |

```json
{ "id": 321, "url": "https://gitea.example.com/example/project/pulls/42#pullrequestreview-321" }
```

Not read-only, so this step is never served from the cache and `--dry-run`
never sends it.

## Errors and rate limits

The pack defines no `envelope` and no `rate_limit`, so a failed request is
reported through the plain HTTP status, and a retrying step falls back to
its own backoff rather than a header Gitea sends.

## What is not here, and why

`get_change` carries no `implements`. Gitea splits a pull request the same
way GitHub does: the pull request itself carries no file list, so
`forge/v1.get_change` — which asks for id, title and files in one call —
cannot be answered in one request. A scenario that needs both pairs
`get_change` with `list_files`. Because `list_files` reports no diff text,
only names and change counts, a scenario that also needs the diff itself
reads it through `get_file`. Because `get_change` carries no `implements`, a
scenario that declares `interface: forge/v1` is told during validation
rather than at run time.

## Validating a change

```sh
baton apis validate apis/gitea
```

This parses the pack and replays every recorded response in
`apis/gitea/examples/` through the transforms, checking the result against
the interface schema for each operation that names an `implements`. The test
suite does the same on every commit, so an operation with an `implements`
needs its example file kept in step with its transform.
