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

Development against KingbaseES PG-wire (V9) deployments is in progress. The PG-wire
implementation covers the initial scope (DSN/auth, catalog, query/binding,
mode-specific codecs, routine INOUT/`sql.Out` where the listener returns an
output row, transactions, COPY IN and COPY OUT, cursors, listeners, metadata, generated IDs)
and passes the offline suite plus a live acceptance harness. One deployment
profile is accepted (scope-limited): an operator-provided four-instance V9
deployment, verified in pg/oracle/mysql/sqlserver modes with no failing live test.
The exact server build and driver revision of that run live in the acceptance
artifacts under `live/artifacts/`, not in this file, so a later run can supersede
them without a documentation edit. Two limits stay explicit: the deployment
enables only `scram-sha-256` authentication, and live TLS acceptance (V8) is
deferred by operator decision because it offers no TLS listener — the TLS trust
policy is implemented and covered offline, with no plaintext fallback. The native
MySQL transport, V8R6, GSS/SSPI and live TLS acceptance remain out of scope.
The specification, feature matrix and delivery plan are the source of truth for
scope and acceptance:

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
- `COPY ... FROM STDIN` must run through the `CopyIn` helper on a transaction;
  `Query`/`Exec` reject that form before sending it, because only the helper
  owns the import stream.
- `COPY ... TO STDOUT` is read through `Query`: one `copy` column of protocol
  chunks to concatenate (text or binary, payload opaque). `Exec`, prepared
  statements, arguments, a COPY that is not the first statement and
  `COPY ... TO <file>` fail closed.
- `COPY ... FROM STDIN WITH (FORMAT binary)` has two shipped forms: `CopyInBinary`
  takes column names and encodes Go values using the storage type the catalog
  reports (so a mode that stores `real` as a double or a `date` in eight bytes is
  handled), while `CopyInRawBinary` forwards an already encoded payload. A column
  with no encoder fails at prepare and a value that does not fit aborts the stream.

## Development

Requires Go 1.26.0 or newer, and golangci-lint v2.14.0.

```sh
go build ./...
go test ./...
go test -race ./...
go test -tags=live ./... -run '^$'
golangci-lint run --build-tags=live ./...
gofmt -l .
go mod tidy && git diff --exit-code
```

CI (`.github/workflows/ci.yml`) runs the same commands on linux/windows/darwin
amd64/arm64 with Go 1.26.x, uploads the Linux coverage profile and enforces its
70% floor. Tag pushes publish a GitHub release (`release.yml`); CodeQL and
Dependabot run on their own schedules.

## Dependency policy

The core driver uses the Go standard library only. Any new external dependency
requires a specification decision and an entry in the development plan before
it is added.
