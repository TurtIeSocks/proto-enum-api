# Proto Enum API — Design

**Date:** 2026-05-04
**Status:** Approved (auto mode)

## Goal

Provide an HTTP API that exposes the enums defined in one or more
`.proto` files so that clients can resolve enum names ↔ numeric values
without bundling the proto schema themselves. Works with any proto3 /
proto2 schema.

## Non-goals

- Exposing messages, services, or RPC stubs (enums only).
- Resolving proto `import` statements (enum extraction is self-contained
  per file — load each file you care about as a separate `[[sources]]`
  entry).
- Cross-enum search (e.g. "every enum that contains `UNSET`").
- Hot-reload at runtime — restart to refresh.

## Architecture

A single Go binary. On startup it reads a TOML config, resolves each
`[[sources]]` entry (downloading URLs into a cache directory, expanding
globs), parses every resulting `.proto` file, and merges all enums into
an in-memory index keyed by **fully-qualified proto name**. Then it
serves the API behind bearer-token auth.

```
   config.toml
       │
       ▼
[[sources]] resolution
  url  → cache_dir/<hash>.proto
  path → as-is
  glob → filepath.Glob expansion
       │
       ▼
parse each file with go-protoparser ──▶ EnumIndex (map[FQN]Enum)
                                              │
                                              ▼
                                    net/http ServeMux + auth
                                              │
                                              ▼
                                    GET /v1/enums          (list)
                                    GET /v1/enums/{name}   (get)
                                    GET /v1/enums/{name}/values/{key}
```

## Components

| Path                              | Responsibility                                       |
|-----------------------------------|------------------------------------------------------|
| `internal/config/config.go`       | TOML decode, env overrides, validation               |
| `internal/proto/types.go`         | `Enum`, `EnumValue`, `EnumIndex`                     |
| `internal/proto/loader.go`        | Resolve sources, fetch+cache, parse, build index     |
| `internal/api/router.go`          | Wire routes, compute ETag, mount middleware stack    |
| `internal/api/handlers.go`        | HTTP handlers + RFC 7807 problem helper              |
| `internal/api/middleware.go`      | Cache (ETag/304) and Gzip middleware                 |
| `internal/api/auth.go`            | Bearer-token middleware **(user contribution)**      |
| `main.go`                         | Parse `--config`, wire it all up, start server       |

## Data model

```go
type EnumValue struct {
    Name   string `json:"name"`
    Number int32  `json:"number"`
}

type Enum struct {
    Name       string      `json:"name"`        // FQN
    SimpleName string      `json:"simpleName"`  // trailing segment
    Package    string      `json:"package"`     // proto package, may be ""
    Values     []EnumValue `json:"values"`
}

type EnumIndex struct {
    packages map[string]struct{}
    enums    map[string]Enum
}
```

### Naming: proto-canonical FQNs

Enum keys follow protoc's convention: `<package>.<enclosing-messages>.<EnumName>`.

- Top-level, packaged: `my.pkg.ClientOperatingSystem`
- Nested:              `my.pkg.ProxyResponseProto.Status`
- Deep nested:         `my.pkg.Outer.Inner.Status`
- No package:          `Outer.Inner.Status`
- No package, top:     `BareEnum`

This guarantees uniqueness across multi-package configs.

## Configuration (TOML)

```toml
listen    = ":8080"        # or LISTEN env
secret    = ""             # or API_SECRET env (env wins)
strict    = false          # parser strictness toggle
cache_dir = "./.cache"     # or CACHE_DIR env

[[sources]]
url = "https://example.com/foo.proto"

[[sources]]
path = "./protos/bar.proto"

[[sources]]
glob = "./protos/*.proto"
```

Each `[[sources]]` entry must set exactly one of `url`, `path`, `glob`.

### Source resolution

- **`url`**: downloaded once on first run; cached at
  `cache_dir/<sha256(url)[:12]>.proto`. Subsequent runs reuse the cache —
  delete the file (or the whole `cache_dir`) to refresh.
- **`path`**: read directly from disk.
- **`glob`**: passed to `filepath.Glob` (single-level — Go stdlib does
  not support `**`). For recursive trees, list multiple entries.

## API

All endpoints live under `/v1` and require `Authorization: Bearer <secret>`.
Missing/wrong header → `401 Unauthorized` with empty body and a
`WWW-Authenticate: Bearer realm="proto-enum-api"` header (RFC 7235).
Unknown enum or value → `404 Not Found` with an RFC 7807 problem body.
Internal failures → `500 Internal Server Error`.

### `GET /v1/enums?search=<substring>`

```json
{
  "count": 1030,
  "enums": ["my.pkg.AccountSettings", "..."]
}
```

Filter is a case-insensitive substring match on the FQN.

### `GET /v1/enums/{name}`

```json
{
  "name": "my.pkg.ClientOperatingSystem",
  "simpleName": "ClientOperatingSystem",
  "package": "my.pkg",
  "values": [
    {"name": "CLIENT_OPERATING_SYSTEM_OS_UNKNOWN", "number": 0},
    {"name": "CLIENT_OPERATING_SYSTEM_OS_ANDROID", "number": 1}
  ]
}
```

### `GET /v1/enums/{name}/values/{key}`

`{key}` is auto-detected: all-digit (optionally signed) keys are looked
up by number, otherwise by value name. Proto identifier rules guarantee
no collision since value names cannot start with a digit.

```json
{
  "enum":   "my.pkg.ClientOperatingSystem",
  "name":   "CLIENT_OPERATING_SYSTEM_OS_ANDROID",
  "number": 1
}
```

## Caching

Every 200 response carries:

```
ETag: "<8-byte hex>"
Cache-Control: public, max-age=3600, immutable
```

The ETag is a SHA-256 fingerprint of the loaded index (sorted FQNs +
value counts), so a no-op restart against the same proto sources
produces the same ETag and clients keep their cached responses.
`If-None-Match` is honored — matching requests return `304 Not Modified`
with empty body. 4xx/5xx responses are *not* tagged or cached.

## Compression

Clients sending `Accept-Encoding: gzip` get a gzip-encoded body with
`Content-Encoding: gzip` and `Vary: Accept-Encoding`. Compression is
opportunistic — without the header, responses come through verbatim.

## Errors (RFC 7807)

4xx/5xx responses use `application/problem+json`:

```json
{
  "type":     "/errors/enum-not-found",
  "title":    "Enum not found",
  "status":   404,
  "detail":   "No enum named \"foo\" is indexed.",
  "instance": "/v1/enums/foo"
}
```

Error type slugs: `enum-not-found`, `value-not-found`.

## Authentication

Bearer-token in `Authorization` header, compared with
`crypto/subtle.ConstantTimeCompare`. The middleware is left as a
**user contribution** — see `internal/api/auth.go` for the full contract
and trade-offs.

If `secret` is empty (config and env both unset), main logs a warning
and proceeds; the user's middleware decides what to do with that case.

## Error handling

- Missing/invalid auth → 401, empty body, `WWW-Authenticate: Bearer realm="proto-enum-api"`.
- Unknown enum FQN → 404, problem+json `enum-not-found`.
- Unknown value (by number or name) within enum → 404, problem+json
  `value-not-found`.
- Parse failure or download failure at startup → fatal log + non-zero
  exit; no degraded mode.

## Conflict policy

If two sources define the same FQN (e.g. two files in a glob that
declare the same enum), **last-write-wins** silently. This is a known
limitation; if your sources are noisy, a future revision may add a
strict mode that errors on conflict.

## Testing strategy

- `internal/config`: TOML decode, env overrides, validation errors.
- `internal/proto`: single-file fixture, multi-file via tempdir, glob
  expansion, FQN keying, resolve helpers.
- `internal/api`: `httptest.NewRecorder` against the wired router for
  every endpoint and error path. Auth tests are skipped until the
  middleware is implemented.

## Out of scope (future work)

- Exposing messages and services.
- Hot-reload via SIGHUP or polling.
- ETag / `If-None-Match` for cacheable responses.
- Conflict-rejecting load mode.
- Proto `import` resolution (would matter if we ever expose messages).
- Recursive glob (`**`).
