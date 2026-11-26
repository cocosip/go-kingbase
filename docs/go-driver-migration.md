# Go API Migration Ledger

This ledger maps the sampled official Go SDK surface and KingbaseES options to
this module (`github.com/cocosip/go-kingbase`, package `kingbase`). It is scoped
to the V9 PG-wire first-release target; deferred entries are explicit and do not
promise parity.

## Driver and configuration

| Official shape | Status | Go contract |
|---|---|---|
| `sql.Open("kingbase", dsn)` and blank import registration | keep | The database/sql driver name remains `kingbase`; `github.com/cocosip/go-kingbase` registers it in `init`. |
| `host`, `port`, `user`, `password`, `database` | keep | DSN, URL and environment forms normalize through `kingbase.ParseConfig`; the conventional port is only a configuration default. |
| `compatibilityMode` / `compatible_mode` / `mode` / `databaseMode` | keep | Aliases declare client semantics only; verified server evidence wins and a conflict fails at connect. |
| `wireProtocol` | keep | `pg` is the supported transport; `mysql` fails clearly until M5 ships. There is no auto value. |
| `sslmode`, certificate paths | keep | `require`, `verify-ca`, `verify-full`, `disable`; explicit missing trust material is an error, never a plaintext fallback. |
| `binary_parameters` | keep | Applies to server-prepared parameters and verified explicit `$n::pg_catalog.type` targets; raw bytes without a target stay text + server inference. |
| `get_last_insert_id` / `get_auto_increment_id` | replace | Restricted opt-in generated-ID capture; `LastInsertId` returns a typed unavailable/overflow error, never zero. |
| Explicit `nextval`/`currval` sequence calls | replace | Caller SQL on one pinned `*sql.Conn`; the driver never guesses with `MAX()`/last-sequence state and offers no portable sequence/GetSchema helper. |
| `client_encoding` | keep | UTF8/UTF-8 only in this release; other encodings fail before publishing a connection. |
| Connector with a notice handler / programmatic config | replace | `kingbase.ParseConfig(dsn)` then `kingbase.NewConnector(cfg)` for `sql.OpenDB`, so `Config.NoticeHandler`, pools and every other programmatic field stay reachable; the configuration is deep-copied at construction. |

## Values and helpers

| Official shape | Status | Go contract |
|---|---|---|
| `CopyIn(table, columns...)` / `CopyInSchema` | keep | Returns a `COPY ... FROM STDIN` statement string. Prepare on a pinned transaction, `Exec` rows, zero-argument `Exec` to flush/finish, then close. It is not a returned writer. |
| `CopyOut` / exporter | replace | Run `COPY ... TO STDOUT` through `QueryContext` on a connection: one `copy` column of protocol chunks (concatenate for the payload, text or binary). `Exec`, prepared statements, arguments, a COPY that is not the first statement and `COPY ... TO <file>` fail closed; closing early drains to ReadyForQuery. |
| `QuoteIdentifier` | keep | Quotes one identifier; callers still build only bounded SQL fragments. |
| `Array` / typed arrays | replace | Use the generic `kingbase.Array[T]` Scanner/Valuer for rectangular text arrays; default results stay opaque text. |
| `VarChar`, `VarCharMax`, `NVarCharMax`, `NChar` | keep | Compatibility wrappers implement `driver.Valuer`/`sql.Scanner` and keep their text intent through Bind. |
| `DateTime1` | keep | Wraps `time.Time`; binary/text temporal policy follows the driver's `time.Time` contract. |
| `CursorString` and refcursor helper | replace | `kingbase.NewCursor(*sql.Tx, name)` keeps the owning transaction and exposes explicit `Fetch`/`FetchAll`/`Close`; the name is one quoted identifier and never transfers ownership. Automatic routine OUT cursor creation remains deferred with T-037. |
| `sql.Out`, `ReturnStatus`, routine conveniences | defer | F-501–F-505/T-036/T-037; unsupported forms must return a clear error. |
| `GetSchema` / DataAdapter-style metadata | not provided | No portable schema-discovery promise; use explicit catalog queries and `ConnectionMetadata`. |
| Numeric/decimal object | replace | Results are exact decimal `string`; use the caller's decimal Scanner. No hidden float conversion. |
| `civil.Date` / `civil.Time` | replace | Use `time.Time` for date/timestamp and preserve time-without-date as `string`. |
| Internal connection diagnostics | replace | `kingbase.ConnectionMetadata(*sql.Conn)` returns `ServerMetadata`; access it on a pinned `sql.Conn`. |
| Server notices / async messages | keep | `Config.NoticeHandler` receives decoded notices; `kingbase.NewListener` owns the LISTEN/NOTIFY connection. Both are reachable without internal packages. |
| COPY OUT | replace | F-706/T-057 shipped: `COPY ... TO STDOUT` through `QueryContext`, see the row above. |
| Binary COPY input | replace | Raw form shipped (F-707/T-058): `CopyInRawBinary` plus `Exec` payload chunks forwards the bytes unchanged; typed binary encoding from Go values (F-705) stays deferred. |
| Native MySQL backend | defer | M5 conditional; no automatic transport selection or downgrade. |

## Standard value boundaries

- `bool`/BIT: strict PG text `t`/`f`; malformed known values return
  `kingbase.ErrValueConversion`, not a silent `false`.
- Integers keep `int64`; full-range `uint64` parameters use exact decimal text.
- Numeric, money, time, timetz, interval, UUID, JSON/XML and inet values keep
  their verified text form; no implicit JSON/XML or duration conversion.
- Date, timestamp and timestamptz use `time.Time` only for valid years 1..9999;
  zero/BC/infinity/out-of-range forms return a typed conversion error so callers
  can explicitly select text.
- Domains resolve only through validated catalog links; ambiguous or unverified
  identities stay opaque.

## Compiling examples

The examples compile with the module and show the supported surface:

| Example | Shows |
|---|---|
| [`examples/basic`](../examples/basic/main.go) | open/ping, qualified int8 parameter, text-array scan |
| [`examples/transaction`](../examples/transaction/main.go) | explicit transaction, Exec, RowsAffected, rollback |
| [`examples/copyin`](../examples/copyin/main.go) | statement-shaped COPY IN on a pinned transaction |
| [`examples/cursor`](../examples/cursor/main.go) | explicit refcursor fetch/close on its owning transaction |
| [`examples/listener`](../examples/listener/main.go) | pull-driven LISTEN/NOTIFY and gap handling |
| [`examples/metadata`](../examples/metadata/main.go) | per-connection `ServerMetadata` |

Verify with:

```sh
go build ./...
go vet ./...
golangci-lint run ./...
```

See [architecture](go-driver-architecture-design.md) and
[capabilities](go-driver-capabilities.md) for the authoritative scope. Examples
do not add live-profile evidence; the accepted profile and its limits are in the
[live ledger](go-driver-live-profile.md#accepted-profile-operator-four-mode-run-2026-11-26).
