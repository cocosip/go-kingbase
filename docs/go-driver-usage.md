# KingbaseES Go Driver — Usage and Configuration

> Status: describes the released PG-wire behaviour. Where status or scope matters, the
> [capability matrix](go-driver-capabilities.md) and the
> [live acceptance record](go-driver-development.md#94-accepted-profile-operator-four-mode-run-2026-11-26)
> are authoritative; this page is the practical reference.

## Install

```sh
go get github.com/cocosip/go-kingbase
```

The driver registers itself as `kingbase` and needs no other dependency: the core is the Go
standard library only.

```go
import (
	"database/sql"

	_ "github.com/cocosip/go-kingbase"
)
```

## Quick start

```go
db, err := sql.Open("kingbase", "host=db.example.com port=54321 user=app password=secret dbname=app sslmode=disable")
if err != nil {
    return err
}
defer db.Close()

ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

var name string
if err := db.QueryRowContext(ctx, `SELECT $1::pg_catalog.text`, "kingbase").Scan(&name); err != nil {
    return err
}
```

Both DSN shapes are accepted: `key=value` pairs and a `postgres://`-style URL. Parameter names are
case-insensitive and ignore underscores, so `connect_timeout` and `connectTimeout` are the same
parameter.

`sslmode` defaults to `require`, matching a TLS-first posture: connecting to a listener that offers
no TLS fails until you pass `sslmode=disable`. The driver never falls back from TLS to plaintext.

## Configuration

| Parameter | Values | Default | Notes |
|---|---|---|---|
| `host` | host name or IP list | `localhost` | `host=a,b` with `port=1,2` opts into the multi-host path |
| `port` | port or port list | `54321` | a shorter port list repeats its last entry |
| `user` | user name | — | |
| `password` | password | — | never written to diagnostics |
| `dbname` | database name | — | |
| `compatibility_mode` | `auto`, `pg`, `oracle`, `mysql`, `sqlserver` | `auto` | aliases: `compatible_mode`, `mode`, `database_mode`; never changes the server mode |
| `wire_protocol` | `pg`, `mysql` | `pg` | `mysql` is rejected until a native MySQL backend ships |
| `sslmode` | `disable`, `require`, `verify-ca`, `verify-full` | `require` | no downgrade, ever |
| `sslrootcert` | path | — | existing root certificate; missing file is an error |
| `sslcert`, `sslkey` | path | — | client certificate and key |
| `connect_timeout` | seconds | none | bounds the whole initialization, not just the dial |
| `application_name` | string | — | reported to the server |
| `fallback_application_name` | string | — | used when `application_name` is empty |
| `client_encoding` | `utf8` | `utf8` | only UTF8 is implemented, and it is verified against the server at connect |
| `options` | startup options | — | passed as a startup parameter |
| `search_path` | schema list | — | passed as a startup parameter |
| `keepalives`, `keepalives_idle`, `keepalives_interval`, `keepalives_count` | boolean, durations | `keepalives=true` | TCP keepalive tuning |
| `target_session_attrs` | `any`, `read-write`, `read-only` | `any` | opt-in multi-host target selection |
| `binary_parameters` | boolean | `false` | send parameters in binary form where the target type is known |
| `disable_prepared_binary_result` | boolean | `false` | keep prepared results in text form |
| `get_last_insert_id` | boolean | `false` | PG-mode generated-ID capture; alias `get_auto_increment_id` |

Unknown parameters fail at `sql.Open` instead of being ignored. The same names can come from the
environment with a `KINGBASE_` prefix (`KINGBASE_HOST`, `KINGBASE_PASSWORD`,
`KINGBASE_SSLMODE`, …); an explicit DSN value always wins, and two aliases of one parameter that
disagree are an error.

### Programmatic configuration

A DSN cannot express every option (for example a notice handler), so a parsed configuration is
also reachable:

```go
cfg, err := kingbase.ParseConfig("host=db.example.com port=54321 user=app sslmode=disable")
if err != nil {
    return err
}
cfg.Password = os.Getenv("APP_DB_PASSWORD")
cfg.ApplicationName = "reporting"
cfg.NoticeHandler = func(notice kingbase.Notice) { log.Printf("server: %s", notice.Message) }

connector, err := kingbase.NewConnector(cfg)
if err != nil {
    return err
}
db := sql.OpenDB(connector)
```

`NewConnector` snapshots the configuration; changing `cfg` afterwards does not affect the pool.

## Compatibility modes

`compatibility_mode` declares the client semantics you want and is validated against the server's
reported mode; a conflict fails the connection instead of silently rewriting statements.

- `auto` (default) discovers the mode from server evidence.
- `pg`, `oracle`, `mysql`, `sqlserver` pin the declaration and must match the server.
- Placeholders: `?` is rewritten to `$n` for every mode. Native binds the listener owns (`:1` in
  Oracle mode, `@p1` in SQLServer mode) pass through untouched.
- Identifiers: MySQL backticks and SQLServer brackets are scanned as quotes, never rewritten.
- `ConnectionMetadata` reports the configured mode, the effective mode, how it was established and
  whether it was verified.

## Values, types and arrays

The type catalog is read from the server at connect; there is no built-in OID table and no
user-selected product version. Standard scalars map to `int64`, `float64`, `bool`, `string`,
`[]byte`, `time.Time`; a type the driver cannot map stays opaque (`string` for text format,
`[]byte` for binary). Arrays use the generic wrapper:

```go
var tags kingbase.Array[string]
err := db.QueryRowContext(ctx, `SELECT ARRAY['a','b']::text[]`).Scan(&tags)
```

A type-changing DDL on a live connection does not refresh that connection's snapshot; open a new
physical connection when typed access must follow the change.

## COPY

Three shipped forms, each owning its own stream:

```go
// Text import: one Exec per row, a zero-argument Exec finishes.
tx, _ := db.BeginTx(ctx, nil)
stmt, _ := tx.PrepareContext(ctx, kingbase.CopyIn("orders", "id", "name"))
stmt.ExecContext(ctx, int64(1), "first")
stmt.ExecContext(ctx)
stmt.Close()
tx.Commit()

// Typed binary import: values are encoded with the storage type the catalog reports.
stmt, _ = tx.PrepareContext(ctx, kingbase.CopyInBinary("orders", "id", "name"))
stmt.ExecContext(ctx, int64(2), "second")

// Raw binary import: the caller supplies the already encoded chunks.
stmt, _ = tx.PrepareContext(ctx, kingbase.CopyInRawBinary("orders", "id", "name"))
stmt.ExecContext(ctx, payloadChunk)
```

`CopyIn` requires a transaction. `Query`/`Exec` reject the row-framed COPY forms before sending
anything, because only the helper owns the import stream. Export runs through `Query`:

```go
rows, err := db.QueryContext(ctx, `COPY (SELECT id, name FROM orders) TO STDOUT`)
var chunk []byte
var payload []byte
for rows.Next() {
    rows.Scan(&chunk)      // one protocol chunk, not one row
    payload = append(payload, chunk...)
}
```

`Exec`, a prepared statement, statement arguments, a COPY that is not the first statement and a
server-side `COPY ... TO <file>` are rejected. Closing an export early drains to a reusable session.

## Transactions, cursors, listeners and metadata

```go
tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true})

cursor, err := kingbase.NewCursor(tx, "orders_cursor")
rows, err := cursor.Fetch(ctx, 100)
err = cursor.Close(ctx)

listener, err := kingbase.NewListener(ctx, cfg, "orders_changed")
note, err := listener.Next(ctx)   // pull-driven, bounded queue
err = listener.Close()

metadata, err := kingbase.ConnectionMetadata(conn)   // conn is a pinned *sql.Conn
```

Notices are delivered to `Config.NoticeHandler` (`Notice{Severity, Code, Message}`); the default
discards them instead of writing to stderr, and a handler must not issue a command on the same
connection. A listener that overflows its bounded queue drops the session, reports the gap with
`ErrNotificationGap` (a zero `Notification` marks it) and never grows memory or blocks the protocol
reader; reconnect cannot replay what was missed.

## Errors

Server errors decode to `*kingbase.Error` with the SQLSTATE in `Code` and the protocol fields
(`Message`, `Detail`, `Hint`, `SchemaName`, `TableName`, `ColumnName`, `ConstraintName`, …):

```go
var serverErr *kingbase.Error
if errors.As(err, &serverErr) && serverErr.Code == "23505" {
    // unique violation
}
```

If a failure happened after a command may have reached the server, the driver reports
`*kingbase.UncertainError`, retires that physical connection and does not hand `driver.ErrBadConn`
to `database/sql`, so the statement is never retried automatically. Treat it as "check the data,
then decide".

## Migrating from the vendor Go SDK

This ledger maps the sampled official Go SDK surface and KingbaseES options to this module. It is
scoped to the released PG-wire target: a `defer` row is an explicit non-promise, not parity.

### Driver and configuration

| Official shape | Status | Go contract |
|---|---|---|
| `sql.Open("kingbase", dsn)` and blank import registration | keep | The driver name remains `kingbase`; the module registers it in `init`. |
| `host`, `port`, `user`, `password`, `database` | keep | DSN, URL and environment forms normalize through `ParseConfig`; the conventional port is only a default. |
| `compatibilityMode` / `compatible_mode` / `mode` / `databaseMode` | keep | Aliases declare client semantics only; verified server evidence wins and a conflict fails at connect. |
| `wireProtocol` | keep | `pg` is the supported transport; `mysql` fails clearly until M5 ships. There is no auto value. |
| `sslmode`, certificate paths | keep | `require`, `verify-ca`, `verify-full`, `disable`; missing trust material is an error, never a plaintext fallback. |
| `binary_parameters` | keep | Applies to server-prepared parameters and verified explicit `$n::pg_catalog.type` targets; raw bytes without a target stay text plus server inference. |
| `get_last_insert_id` / `get_auto_increment_id` | replace | Restricted opt-in generated-ID capture; `LastInsertId` returns a typed unavailable/overflow error, never zero. |
| Explicit `nextval`/`currval` sequence calls | replace | Caller SQL on one pinned `*sql.Conn`; the driver never guesses with `MAX()` or last-sequence state and offers no portable sequence helper. |
| `client_encoding` | keep | UTF8/UTF-8 only in this release; other encodings fail before a connection is published. |
| Connector with a notice handler or other programmatic fields | replace | `ParseConfig(dsn)` then `NewConnector(cfg)` for `sql.OpenDB`; the configuration is deep-copied at construction. |

### Values and helpers

| Official shape | Status | Go contract |
|---|---|---|
| `CopyIn(table, columns...)` / `CopyInSchema` | keep | Returns a `COPY ... FROM STDIN` statement string; prepare on a pinned transaction, `Exec` rows, zero-argument `Exec` to flush and finish, then close. It is not a returned writer. |
| `CopyOut` / exporter | replace | `COPY ... TO STDOUT` through `QueryContext`: one `copy` column of protocol chunks (text or binary). `Exec`, prepared statements, arguments, a COPY that is not the first statement and `COPY ... TO <file>` fail closed; closing early drains to ReadyForQuery. |
| `QuoteIdentifier` | keep | Quotes one identifier; callers still build only bounded SQL fragments. |
| `Array` / typed arrays | replace | The generic `Array[T]` Scanner/Valuer handles rectangular text arrays; default results stay opaque text. |
| `VarChar`, `VarCharMax`, `NVarCharMax`, `NChar` | keep | Compatibility wrappers implement `driver.Valuer`/`sql.Scanner` and keep their text intent through bind. |
| `DateTime1` | keep | Wraps `time.Time`; binary/text temporal policy follows the driver's `time.Time` contract. |
| `CursorString` and refcursor helper | replace | `NewCursor(*sql.Tx, name)` keeps the owning transaction and exposes explicit `Fetch`/`FetchAll`/`Close`; the name is one quoted identifier and ownership never transfers. Automatic routine OUT cursor creation stays deferred (T-037). |
| `sql.Out`, `ReturnStatus`, routine conveniences | defer | F-501–F-505/T-036/T-037; an unsupported form returns a clear error. |
| `GetSchema` / DataAdapter-style metadata | not provided | No portable schema-discovery promise; use explicit catalog queries and `ConnectionMetadata`. |
| Numeric/decimal object | replace | Results are exact decimal `string`; use the caller's decimal Scanner. No hidden float conversion. |
| `civil.Date` / `civil.Time` | replace | Use `time.Time` for date/timestamp and keep time-without-date as `string`. |
| Internal connection diagnostics | replace | `ConnectionMetadata(*sql.Conn)` returns `ServerMetadata`; access it on a pinned `sql.Conn`. |
| Server notices and async messages | keep | `Config.NoticeHandler` receives decoded notices; `NewListener` owns the LISTEN/NOTIFY connection. Both are reachable without internal packages. |
| Binary COPY input | replace | Two shipped forms: `CopyInBinary` encodes Go values using the storage type the catalog reports (F-705) and `CopyInRawBinary` forwards an already encoded payload chunk by chunk (F-707). |
| Native MySQL backend | defer | Conditional M5; no automatic transport selection or downgrade. |

### Standard value boundaries

- `bool`/BIT: strict PG text `t`/`f`; a malformed known value returns `ErrValueConversion`, not a
  silent `false`.
- Integers keep `int64`; full-range `uint64` parameters use exact decimal text.
- Numeric, money, time, timetz, interval, UUID, JSON/XML and inet values keep their verified text
  form; there is no implicit JSON/XML or duration conversion.
- Date, timestamp and timestamptz use `time.Time` only for valid years 1..9999; zero/BC/infinity and
  out-of-range forms return a typed conversion error so callers can explicitly select text.
- Domains resolve only through validated catalog links; ambiguous or unverified identities stay
  opaque.

## Examples and tests

The examples compile with the module and show the supported surface:

| Example | Shows |
|---|---|
| [`examples/basic`](../examples/basic/main.go) | open/ping, a qualified int8 parameter, text-array scan |
| [`examples/transaction`](../examples/transaction/main.go) | explicit transaction, `Exec`, `RowsAffected`, rollback |
| [`examples/copyin`](../examples/copyin/main.go) | statement-shaped COPY IN on a pinned transaction |
| [`examples/cursor`](../examples/cursor/main.go) | explicit refcursor fetch/close on its owning transaction |
| [`examples/listener`](../examples/listener/main.go) | pull-driven LISTEN/NOTIFY and gap handling |
| [`examples/metadata`](../examples/metadata/main.go) | per-connection `ServerMetadata` |

Verify the examples with `go build ./... && go vet ./...`; the offline suite is `go test ./...` and
the live acceptance suite is `go test -tags live ./live`, which needs `KINGBASE_TEST_DSN` and is
described in the [live acceptance record](go-driver-development.md#9-live-acceptance-record).
Examples do not add live evidence.

## Scope of this release

- PG wire against V9 deployments, `pg`/`oracle`/`mysql`/`sqlserver` compatibility modes.
- Live TLS acceptance is deferred by operator decision: the trust policy, certificates and
  downgrade rules are implemented and covered offline, but no TLS listener was available.
- Not in this release: the native MySQL backend (`wire_protocol=mysql`), V8R6 evidence, GSS/SSPI
  channel binding, and TLS live evidence. Each is a recorded deferral with a revisit condition in
  the [development plan](go-driver-development.md).
