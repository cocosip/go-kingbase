# go-kingbase

A `database/sql` driver for KingbaseES, implemented independently in Go.

| Item | Value |
|---|---|
| Module | `github.com/cocosip/go-kingbase` |
| Package | `kingbase` |
| Driver name | `kingbase` |
| Go baseline | 1.26.0 |
| Platforms | linux, windows, darwin on amd64 and arm64 |
| License | Apache-2.0 (see `LICENSE`, `NOTICE`) |
| Dependencies | Go standard library only |

## Status

Early development. The driver is not usable yet and makes no live-server
support claim. The specification, feature matrix and delivery plan are the
source of truth for scope and acceptance:

- [Architecture design](docs/go-driver-architecture-design.md)
- [Capability matrix](docs/go-driver-capabilities.md)
- [Development plan and progress](docs/go-driver-development.md)
- [API migration ledger and examples](docs/go-driver-migration.md)

## Contract notes

- PG-wire V9 profiles are the first-release target. V8 and the native MySQL
  transport are separate, later acceptance scopes.
- `compatibilityMode` declares client semantics and is validated against the
  server; it never changes the server mode. The type catalog is read from the
  server at connect time; there is no user-selected product version and no
  built-in OID table.
- No automatic protocol/port fallback, no reconnect across protocols, no
  duplicate connection pool.

## Development

Requires Go 1.26.0 or newer, and golangci-lint v2.14.0.

```sh
go build ./...
go test ./...
go test -race ./...
golangci-lint run ./...
gofmt -l .
go mod tidy && git diff --exit-code
```

CI (`.github/workflows/ci-cd.yml`) runs the same commands on linux/windows/darwin
amd64/arm64 with the Go version pinned by `go.mod`.

## Dependency policy

The core driver uses the Go standard library only. Any new external dependency
requires a specification decision and an entry in the development plan before
it is added.
