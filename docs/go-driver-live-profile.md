# Live V9 Profile Acceptance

Status: **no V9 profile is accepted yet**. This page defines the manifest,
commands and evidence layout used to close M3. Testpeer replays, static SDK data,
skips and unavailable instances never count as live evidence.

## Manifest identity

Record one manifest per tested server profile, before any acceptance claim:

| Field | Required evidence |
|---|---|
| Product / edition / release | Exact KingbaseES product string and edition; raw `version()`/server report retained |
| Compatibility mode | `database_mode` source plus the driver's verified/explicit state and any conflict |
| Transport / listener | Exact endpoint family and configured port; no port-scan or packaging inference |
| TLS/auth | Mode, certificate chain, hostname/CA provenance and auth method actually used |
| Catalog | Namespace/object/permission result for V1/V2; raw rows retained |
| Driver revision | Commit hash or release under test |
| Operator/time | Who ran it, UTC timestamp, host/platform, Go version |
| Artifacts | Logs, JSON report, SQL/commands and raw error output |

An accepted V9 profile also needs the applicable V1–V19 items from the
[capability matrix](go-driver-capabilities.md); a missing item blocks acceptance
rather than becoming N/A.

## Harness

The first scoped smoke test runs only when the `live` build tag and a DSN are
provided. It checks startup, mode/wire metadata, one qualified parameter and the
server version report.

```sh
KINGBASE_TEST_DSN='host=... port=54321 user=... password=... dbname=...' \
KINGBASE_TEST_MODE=pg \
KINGBASE_TEST_ARTIFACT_DIR=artifacts/v9-pg \
go test -tags live ./live -run TestLiveSmoke -v
```

`KINGBASE_TEST_ARTIFACT_DIR` is optional; when set, the test writes
`live-smoke.json` with the reported metadata and timestamp. The harness does not
run by default (`go test ./...` excludes it), and it never substitutes for the
full V1–V19 checklist.

Connection details are supplied only through the process environment and are
never committed, logged by the repository, or written to artifacts. Artifact
commands must be reviewed for credentials before retention.

## 2026-11-25 sanitized result

An operator-provided KingbaseES **V009R001C010** deployment was exercised with
four compatibility modes through the PG-wire listener. This is a partial
evidence run, not a profile acceptance:

| Mode | Startup/mode | Catalog | Ordinary role | Built-ins | Tx/prepared | COPY IN | Cursor | Binary parameters | Arrays | Metadata | Boundary | Catalog lifecycle | Listener | Generated ID | Cancel recovery |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| pg | pass | full | pass | pass | pass | pass | pass | pass | pass | pass | pass | pass | pass | pass | pass |
| oracle | pass | full | pass | pass | pass | pass | pass | pass | pass | pass | pass | not run | not run | not run | pass |
| mysql | pass | full | pass | pass | pass | pass | pass | pass | pass | pass | pass | not run | not run | not run | pass |
| sqlserver | pass | full | pass | pass | pass | pass | pass | pass | unsupported literal | pass | not run | not run | not run | not run | pass |

The SQLServer-mode run found that `BEGIN` alone is rejected and
`BEGIN TRANSACTION` is required; the driver now renders that form for the
SQLServer compatibility mode in transaction.go. SQLServer mode also rejected the
PG `ARRAY[...]` literal, so array support remains unverified for that profile.
The cancellation check passed in all four modes: each cancelled `pg_sleep` was
reported as `*kingbase.UncertainError`, and a fresh pooled connection remained
usable. This is not a full V19 race matrix. Default, read-committed,
repeatable-read, serializable and read-only `BeginTx` options all opened,
queried and committed in each mode. A scratch-table write inside a read-only
transaction was rejected with SQLSTATE 25006 in all four modes; this verifies the
server's enforcement for the tested statements, not every writable path.
PG metadata reported uppercase `INT4`/`VARCHAR`/`DATE`, real scan types and the
varchar length. `TestLiveCatalogBuiltinShapes` captured the required base kind
and `typlen` for the codec-selecting builtins in all four modes; the driver now
refuses a codec when a qualified name has a different shape. A PG session timezone
of `America/New_York` kept a wall `timestamp` distinct from a DST-normalized
`timestamptz` instant without a driver-side shift. A PG scalar function returned
its value through `QueryRow`, and a `CALL` with an INOUT parameter returned the
updated row; PG `sql.Out` assignment for a direct and a prepared `CALL` also
returned the INOUT value after completion, while SQLServer status and the
MySQL-compatible OUT form remain pending. In
MySQL mode, opt-in `get_last_insert_id=yes` captured `LAST_INSERT_ID()` after a
single-row INSERT and returned the generated value without changing
`RowsAffected`; tinyint range conversion and V6 acceptance remain pending. The PG catalog
lifecycle check created an enum, read it on a fresh connection, dropped it and
confirmed that the stale catalog reports an error instead of scanning a stale
OID. An explicit mode that contradicted the server-reported mode was rejected with
a `conflicts with server database_mode` error in every mode. A pooled connection
kept its stale catalog and rejected a dropped type; after dropping idle
connections, a fresh catalog also rejected it, and recreating the type made it
visible again. A dropped-and-recreated type with the same name and a new OID was
read successfully only through a fresh physical connection, not the stale
catalog. A temporary ordinary login role was also created per mode; it
connected with its own credentials and completed catalog initialization plus a
builtin query, which is ordinary-role access evidence but not the raw
catalog-shape capture V2 requires. `sslmode=require` was rejected by all four
listeners of this deployment because they did not offer TLS; V8 remains
unverified and requires a TLS-enabled deployment, not a fallback.

The same four-listener deployment was re-exercised on 2026-11-25 for profile
behaviour. `sys_hba.conf` on every instance enables `scram-sha-256` for the
172.16.2.0/24 client subnet (peer auth stays local-only), so SCRAM is the enabled
live auth method for this profile; no listener offers TLS, so V8 remains
unverified by infrastructure rather than by fallback. Observed profile facts that
drive the mode-specific code: every listener rejects a bare `?` and accepts `$n`;
double-quoted identifiers work in all modes, MySQL also accepts backticks and
SQLServer brackets, while Oracle and SQLServer reject backticks and MySQL
rejects brackets; native `:1` binds server-side in Oracle/MySQL/SQLServer; a bare
procedure path is rejected by the SQLServer listener while `EXEC <name>` works;
`tinyint` exists in Oracle/MySQL/SQLServer modes (signed in Oracle/MySQL,
unsigned 0..255 in SQLServer), `YEAR` exists only in MySQL mode as a domain over
`int4`, and SQLServer `BIT` is a one-byte base type. INOUT parameters return a
row when supplied as bound parameters (`CALL proc(1, $1)`) in PG/Oracle/MySQL
modes; MySQL `@var` user variables and SQLServer `@p OUTPUT` variables are not
supported by these PG-wire listeners, and `CALL proc(1, :1)` also works through
the server-native colon bind. NumInput evidence: the listener reports accurate parameter counts for `$n` in
every mode, including SQLServer; the SQLServer profile still reports `-1` by
design so database/sql leaves the count check to the driver, whose binding plan
rejects a mismatch before sending.
Routine evidence: a PG-style `CALL p(IN a, INOUT b)` returns one row with the
INOUT value on the extended protocol in PG, Oracle and MySQL, and no result set
at all in SQLServer mode (the simple protocol does return it there); MySQL
`@var` OUT forms are rejected by the listener, while `CALL p(1, :1)` works.
SQLServer mode also rejects bare `COMMIT`/`ROLLBACK` (it requires the
`... TRANSACTION` form) and bare `BEGIN` outside the `BEGIN TRANSACTION` form;
its `sys.bit` is a one-byte boolean while MySQL `sys.bit` stays a bit string.
Binary result payloads were captured for these types: `tinyint` is one signed or
unsigned byte, `uint4` four bytes, `uint8` eight bytes, `binary`/`varbinary` the
raw bytes, `bit` one byte, and `datetime` an eight-byte PostgreSQL-epoch
timestamp that carries the wall-clock text as a session-timezone instant (so the
driver keeps it opaque); `year` resolves through `int4`. These are profile-behaviour observations, not an
accepted profile.

Still missing for acceptance: the raw catalog query/shape/error capture as an
ordinary role, V1 fallback cases, V5 routine/OUT/status forms, auth/TLS matrix,
V12 deployment confirmation, V14 storage scope, V16 initialization budget, the
mid-reader part of V17, and the full V19 session/reset matrix.

## Acceptance ledger

Copy this table into the release artifact and replace `pending` only with a
linked, reviewed result:

| Profile | V1 | V2 | V3 | V4 | V5 | V6 | V7 | V8 | V9 | V10 | V11 | V12 | V13 | V14 | V15 | V16 | V17 | V18 | V19 |
|---|---|---|---|---|---|---|---|---|---|---|
| V9 PG-wire, \_\_release\_\_ | pending | pending | pending | pending | pending | pending | pending | pending | pending | pending | n/a | pending | n/a | pending | pending | pending | pending | pending | pending |

V11/V13 are later V8 scope; V15/V19 native cases enter only when M5 is enabled.
An excluded item needs a named profile/feature reason.

## Feature-pair mapping

Acceptance is per capability/profile pair, not per test name. Use the V-item
feature lists in the capability matrix, then mark each pair's live state only
after its command and artifact are linked. P0/P1 features cannot be deferred
inside the release scope; an enabled P2 feature needs the same proof.

## Commands to extend

Each V-item should contribute a reproducible command and an artifact path. Keep
server-side queries bounded and runnable by an ordinary role; permission
failures must be recorded, not bypassed with a superuser. Do not add a passing
claim when the command was skipped, the profile was unreachable, or only
testpeer replay ran.

This page is intentionally a manifest, not a support statement. M3 remains open
until every applicable pair is accepted.
