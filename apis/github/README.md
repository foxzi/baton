# github

An API pack that teaches baton the GitHub REST API without putting the name
GitHub into the binary. It implements four of the five operations of the
`forge/v1` interface, so a scenario written against `forge/v1` can run on
GitHub, GitLab or Gitea by changing which pack it loads.

| | |
|---|---|
| Pack | `github`, version 1 |
| API | GitHub REST API |
| Interface | `forge/v1` — `list_files`, `get_file`, `post_comment`, `post_review` |
| Extra operations | `get_change` (no `implements`; see "What is not here and why") |
| Auth | `bearer`, sent as `Authorization: Bearer <token>` |
| Base URL | `https://api.github.com` by default |
| Pagination | `Link` header, up to 20 pages |

## Wiring it into a scenario

```yaml
secrets:
  github_rw: { from: env, key: GITHUB_TOKEN }

apis:
  forge:
    pack: github
    from: ../apis/
    # Omit config entirely for api.github.com; a GitHub Enterprise Server
    # instance would need a config.base_url pointing at its own API root.
    auth: { secret: github_rw }
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
| `base_url` | no | `https://api.github.com` | API root |
| `auth.secret` | yes | — | The token, sent as `Authorization: Bearer <token>` |

## Operations

### `get_change` — `GET /repos/{project}/pulls/{id}`

Read-only. No `implements`: see "What is not here, and why".

| Argument | In | Pattern | Notes |
|---|---|---|---|
| `project` | path | `^[\w.-]+/[\w.-]+$` | `owner/repo`; a real slash in the path, so it is not percent-encoded as one segment |
| `id` | path | `^\d+$` | The pull request number |

```json
{ "id": 42, "title": "...", "description": "...", "author": "user",
  "base": "sha", "head": "sha", "url": "https://github.com/..." }
```

### `list_files` — `GET /repos/{project}/pulls/{id}/files`

Read-only, paginated. Implements `forge/v1.list_files`. Same arguments as
`get_change`:

```json
[ { "path": "internal/httpx/paginate.go", "diff": "@@ -18,7 +18,7 @@\n..." },
  { "path": "internal/httpx/testdata/link_header.txt", "deleted": true } ]
```

GitHub calls a removed file "removed", not "deleted"; the transform maps that
to the `deleted` flag `forge/v1` expects. A binary file comes back without a
`.patch`, and `diff` is added only when `.patch` is present, because
`forge/v1` types `diff` as text and an absent key beats a null. Capped at
500k per page.

### `get_file` — `GET /repos/{project}/contents/{path}`

Read-only. Implements `forge/v1.get_file`.

| Argument | In | Pattern | Notes |
|---|---|---|---|
| `project` | path | `^[\w.-]+/[\w.-]+$` | |
| `path` | path | `^[^\s?#]+$` | The whole path is one segment, slashes included; GitHub keeps real path segments in the URL, so there is no `encode: path` here, unlike GitLab |
| `ref` | query | `^[\w./-]+$` | Optional |

```json
{ "content": "package main\n...", "path": "internal/httpx/paginate.go",
  "sha": "9a1c0f5d6e7b8a9c0d1e2f3a4b5c6d7e8f901234" }
```

GitHub wraps the base64 content across lines at 60 characters; the transform
strips the newlines before decoding. The response does not echo the ref,
only the blob sha, which is reported as such. Capped at 500k.

### `post_comment` — `POST /repos/{project}/issues/{id}/comments`

Writes. Implements `forge/v1.post_comment`. Body is sent as JSON. A pull
request is an issue as far as GitHub's comment API is concerned, so the path
goes through `/issues/{id}`.

| Argument | In | Limit | Notes |
|---|---|---|---|
| `project` | path | | |
| `id` | path | | |
| `body` | body | 65000 chars | The comment text, Markdown |

```json
{ "id": 123456789, "url": "https://github.com/example/project/pull/42#issuecomment-123456789" }
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
| `comments` | body | | Each entry is `{ path, line, body }` |
| `event` | body | | One of `COMMENT`, `APPROVE`, `REQUEST_CHANGES`; defaults to `COMMENT` |

```json
{ "id": 987654321, "url": "https://github.com/example/project/pull/42#pullrequestreview-987654321" }
```

Not read-only, so this step is never served from the cache and `--dry-run`
never sends it.

## Errors and rate limits

The pack defines no `envelope`, so a failed request is reported through the
plain HTTP status rather than a service-specific error field. Throttling is
read from `X-RateLimit-Remaining` and `Retry-After`, so a retrying step waits
as long as GitHub asks it to.

## What is not here, and why

`get_change` carries no `implements`. GitHub splits a pull request across two
endpoints: the pull request itself carries no file list, and the files
endpoint carries no title, so `forge/v1.get_change` — which asks for id,
title and files in one call — cannot be answered in one request. A scenario
that needs both pairs `get_change` with `list_files`, which carries the
diffs. Because the operation carries no `implements`, a scenario that
declares `interface: forge/v1` is told during validation rather than at run
time.

## Validating a change

```sh
baton apis validate apis/github
```

This parses the pack and replays every recorded response in
`apis/github/examples/` through the transforms, checking the result against
the interface schema for each operation that names an `implements`. The test
suite does the same on every commit, so an operation with an `implements`
needs its example file kept in step with its transform.
