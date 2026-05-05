# proto-enum-api

A small HTTP API that serves the enums from one or more `.proto` files,
keyed by their proto-canonical fully-qualified name.

## Quick start

```sh
cp config.example.toml config.toml
# edit config.toml — point [[sources]] at your proto file(s) and set a secret
API_SECRET=changeme go run . --config config.toml
```

Then:

```sh
curl -H "Authorization: Bearer changeme" http://localhost:8080/v1/enums | jq .count
curl -H "Authorization: Bearer changeme" http://localhost:8080/v1/enums/my.pkg.ClientOperatingSystem
curl -H "Authorization: Bearer changeme" --compressed http://localhost:8080/v1/enums  # gzip
```

## Endpoints

All require `Authorization: Bearer <secret>`. All under `/v1`.

| Method | Path                                | Returns                                          |
|--------|-------------------------------------|--------------------------------------------------|
| GET    | `/v1/enums`                         | All enum FQNs (filter via `?search=`)            |
| GET    | `/v1/enums/{name}`                  | Full enum (values + numbers)                     |
| GET    | `/v1/enums/{name}/values/{key}`     | Resolve value by **either** number or name       |
| POST   | `/v1/refresh`                       | Forces fresh protos to be read/fetched           |

`{name}` is the canonical FQN (e.g. `my.pkg.Outer.Inner.Status`).

`{key}` auto-detects: an all-digit token is looked up by number; anything
else is looked up by value name. Proto identifier rules guarantee no
collision (value names cannot start with a digit).

## Response features

* **ETag + Cache-Control**: 200 responses carry an `ETag` derived from the
  loaded index. Send `If-None-Match: <etag>` to get `304 Not Modified`
  with no body.
* **gzip**: send `Accept-Encoding: gzip` and the response is compressed.
* **RFC 7807 errors**: 4xx/5xx responses use `application/problem+json`
  with `type`, `title`, `status`, `detail`, `instance` fields.
* **WWW-Authenticate**: 401s include `WWW-Authenticate: Bearer realm="proto-enum-api"`
  per RFC 7235 (once you implement the middleware).

## Configuration

See [`config.example.toml`](config.example.toml) and the design spec at
[`docs/superpowers/specs/2026-05-04-proto-enum-api-design.md`](docs/superpowers/specs/2026-05-04-proto-enum-api-design.md).

Each `[[sources]]` entry must set exactly one of `url`, `path`, or `glob`.
