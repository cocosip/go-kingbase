# Live V9 Profile Acceptance

Status: **one V9 profile is accepted, scope-limited** — the four-instance
V009R001C010 deployment below, accepted 2026-11-26 for the initial PG-wire scope
(revision `312604d`), with live TLS deferred by O8 and the auth method set limited
to the enabled `scram-sha-256`. This page defines the manifest, commands and
evidence layout for that acceptance. Testpeer replays, static SDK data, skips and
unavailable instances never count as live evidence.

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
returned the INOUT value after completion; later runs below record the measured
SQLServer status/limitation and the MySQL-mode OUT behaviour. In
MySQL mode, opt-in `get_last_insert_id=yes` captured `LAST_INSERT_ID()` after a
single-row INSERT and returned the generated value without changing
`RowsAffected`;  The PG catalog
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
listeners of this deployment because they did not offer TLS; live TLS acceptance
(V8) is therefore deferred by operator decision (O8), not replaced by a fallback.

The same four-listener deployment was re-exercised on 2026-11-25 for profile
behaviour. `sys_hba.conf` on every instance enables `scram-sha-256` for the
172.16.2.0/24 client subnet (peer auth stays local-only), so SCRAM is the enabled
live auth method for this profile; no listener offers TLS, so live TLS acceptance
(V8) is deferred by operator decision (O8) rather than replaced by a fallback. Observed profile facts that
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
Timezone evidence: every listener reports `TimeZone=Asia/Shanghai` at startup
(plus an uppercase `TIME_ZONE` alias). PG, Oracle and SQLServer render a
`timestamptz` text value with its offset, while MySQL renders the session-zone
wall clock with no offset (`2024-03-10 14:30:00` for `06:30:00+00`); the driver
now resolves such text through the startup-reported session zone instead of
assuming UTC, and keeps an unloadable/absent zone an explicit conversion error.

Evidence-matrix run (`live/evidence_test.go`, artifact `evidence-<mode>.json`):
each mode's qualified catalog query returned a full row set (pg 754, oracle
1006, mysql 902, sqlserver 801 rows) with the raw column names retained; the
builtin-shape capture confirms `pg_catalog.bit` is `b/-1` everywhere, MySQL
exposes `sys.tinyint b/1`, `sys.uint4 b/4`, `sys.year d/4 base=int4` and
`sys.bit b/-1`, SQLServer exposes `sys.tinyint b/1`, `sys.bit b/1`,
`sys.money b/8`, `sys.smalldatetime b/8` and Oracle exposes
`pg_catalog.tinyint b/1`. `sslmode=require` fails on every port with
`server does not support TLS`, so live TLS acceptance (V8) is deferred by
operator decision (O8). A
read-only transaction rejected `CREATE TABLE` with SQLSTATE 25006 in every mode
and the connection recovered, while a normal transaction committed. Boundary
values: high-precision numeric and `18446744073709551615` stay exact text where
the mode accepts the cast, `NaN`/`+Inf` decode as float64, interval stays its
exact text, and the mode-specific tinyint/year/datetime/money/bit/smalldatetime
values decode as documented above.

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
driver keeps it opaque); `year` resolves through `int4`, and `money` is 4-decimal text with a 10^4-scaled int64 binary payload. These are profile-behaviour observations, not an
accepted profile.

Acceptance state after the 2026-11-26 run below: the profile is accepted with the scope limits
named there. The only live items that stay pending are the ones this deployment cannot provide —
the TLS matrix (V8, deferred by O8) and the auth methods it does not enable (cleartext/MD5/SM3).
The per-pair override records live in capability matrix section 17.

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

This page is a manifest plus the accepted-profile record, not a general support
statement: the acceptance below covers one deployment and the scope limits it names.

## V1–V19 evidence ledger (2026-11-25 and 2026-11-26 four-mode runs)

Status words: **evidenced** means the named live run produced the required
observation on this deployment; a parenthesised scope names the narrower surface
that was proven; **partial** and **blocked** state why the item is not closed. No
profile is accepted while an applicable item is not evidenced.

| Item | Status | Evidence |
|---|---|---|
| V1 | evidenced | `TestLiveSmoke` captures startup/reported mode and product identity per mode; `TestLiveExplicitModeConflict` rejects a contradictory declared mode; `TestLiveOrdinaryRoleCatalog` connects a temporary ordinary login. Empty/unknown mode answers are covered by the offline mode-discovery suite. |
| V2 | evidenced | `TestLiveEvidenceMatrix` runs the driver's qualified `pg_catalog.pg_type`/`pg_namespace` query and retains column names plus the full row counts (pg 754, oracle 1006, mysql 902, sqlserver 801); `TestLiveCatalogBuiltinShapes` captures base kind/`typlen` per builtin. Ordinary-role access is evidenced separately by `TestLiveOrdinaryRoleCatalog`, not by a second raw-shape capture. |
| V3 | evidenced | The evidence artifact records product/release, mode/source, wire protocol, the exact catalog query and its column names, the run time and the driver's observed shapes; no SDK constants are consumed by the driver. |
| V4 | evidenced | `TestLiveCatalogBuiltinShapes` plus the artifact's builtin-shape map check qualified namespace/name/shape per mode; same-named application types are rejected by the offline catalog-shape suites, and absence never triggers a mode switch. |
| V5 | evidenced (scope-limited) | `TestLiveRoutineOutAssignment` passes for PG/Oracle/MySQL direct and prepared `sql.Out`; `TestLiveSQLServerRoutineOutUnsupported` records the measured SQLServer parameterized-CALL limitation; `TestLiveRoutineExecDiscardsStatus` shows the SQLServer status is discarded without failure; `TestLiveSQLServerRoutineConvenience` covers the bare-name `EXEC`; `TestLiveCopyInAndCursor` covers refcursor fetch/close on the PG profile. Non-PG refcursor creation is not offered by these listeners. |
| V6 | evidenced | `TestLiveGeneratedID` (PG form) and `TestLiveMySQLGeneratedID` (LAST_INSERT_ID form, including a tinyint identity) pass; rejection/overflow forms are covered by the offline generated-ID suite. |
| V7 | evidenced (enabled method set) | `sys_hba.conf` enables `scram-sha-256` for the client subnet and every connection authenticated with it on 2026-11-25 and 2026-11-26. Cleartext/MD5/SM3 are not enabled by this deployment, so F-801/F-803 stay implemented and replayed offline with their live state pending; the operator confirmed SCRAM-only scope for this phase, which closes V7 for the enabled method set rather than for every method the driver implements. |
| V8 | deferred (operator decision O8) | Every listener rejects `sslmode=require` with `server does not support TLS`. The trust/material policy is exercised offline only, and live TLS acceptance is deferred for this phase by operator decision; no fallback is accepted and no TLS support claim is published. |
| V9 | evidenced | `TestLiveCopyInAndCursor` passes COPY IN escaping/NULL and cursor handling in every mode; `TestLiveListenerNotification` passes PG LISTEN/NOTIFY delivery. |
| V10 | evidenced | `TestLiveModeTypeCodecs` checks YEAR and the mode-specific text/binary forms in the same run; binary and text values agree for tinyint, uint4, uint8, money, bit and datetime where a codec is claimed. |
| V11 | out of scope | Later V8R6 profile scope; the driver cannot inherit this V9 evidence for it. |
| V12 | evidenced | The four endpoints and ports were supplied by the operator (Oracle 54321, MySQL 54322, SQLServer 54323, PostgreSQL 54324) and each accepted a positive PG-wire handshake; no port scan or packaging inference is used. |
| V13 | out of scope | Legacy `compatible_level` meaning applies to the later V8R6 scope, not this V9 deployment. |
| V14 | evidenced | `pg_settings` reports `database_mode` as `source=override context=internal` for every mode and `TimeZone` as `source=configuration file context=user`; the mode is fixed per instance and never assumed from DDL. |
| V15 | out of scope | Native MySQL backend profile is the conditional M5 scope, independently established and not borrowed from this PG-wire evidence. |
| V16 | evidenced | The bounded `SELECT version()` identity capture records `KingbaseES V009R001C010`/`12.1` per mode; the bounded-init, stalled-identity and deadline-cleanup paths are covered by the offline failure suite. |
| V17 | evidenced (PG scope) | `TestLiveCatalogLifecycle`, `TestLiveCatalogChangedOID` and `TestLiveCatalogPoolReuse` pass on the PG instance: new/dropped/recreated OIDs, stale pooled snapshots, opaque fallback and fresh-connection recovery. The lifecycle probe is PG-scoped (`TestLiveCatalogLifecycle` skips in the other three modes), while the refreshed-OID probes run in every mode; the snapshot logic is mode-independent and its hostile cases stay in the offline suite. |
| V18 | evidenced (scope-noted) | `TestLiveBoundaryValues`, `TestLiveTimezoneAndDST`, `TestLiveModeTypeCodecs` and the artifact boundary map cover exact numeric/uint64 text, NaN/±Infinity, interval, DST/timezone meaning and the mode tinyint/year/datetime/money/bit/smalldatetime forms. The dedicated DST probe runs on the PG instance only and `TestLiveBoundaryValues` is PG-scoped; the per-mode boundary map is captured in every mode by `TestLiveEvidenceMatrix`. Session lexical settings are covered by `TestLiveModePlaceholderAndQuoting` and the offline lexical suite. |
| V19 | evidenced (scope-limited) | `TestLiveTransactionOptions`, `TestLiveReadOnlyTransactionEnforcement`, `TestLiveCancelAndRecovery`, `TestLiveTransactionPreparedAndNamed` and the artifact's transaction/recovery check pass; transaction-state reset and close/drain races are covered by the offline session suite. Backend-handle exposure is not promised by the driver. |

## Accepted profile: V9R001C010 four-mode (2026-11-26)

The 2026-11-26 run re-executed the whole `live` suite against the operator deployment and
wrote a machine-recorded manifest into every evidence artifact. The four instances are
accepted for the initial PG-wire scope under the scope limits listed below.

### Manifest

| Field | Recorded evidence |
|---|---|
| Product / release | `version()` reports `KingbaseES V009R001C010` on every instance; startup `server_version` is `12.1` |
| Edition / packaging | Operator-supplied single V9 installation (`/home/kingbase/ES/V9`) with four mode-specific instances (`data`, `data_mysql`, `data_sqlserver`, `data_pg`). The server reports the release string only, so this profile is recorded by deployment and is not asserted as one specific all-mode/single-mode packaging row of section 17 |
| Compatibility mode | Reported at startup by each instance (`mode_source=startup`, `mode_verified=true`) as `pg` / `oracle` / `mysql` / `sqlserver`; `pg_settings` shows `database_mode` with `source=override context=internal` |
| Transport / listener | PG wire on 172.16.2.45:54324 (PG), :54321 (Oracle), :54322 (MySQL), :54323 (SQLServer); database `test`; no port scan or packaging inference |
| TLS | `sslmode=require` is rejected by all four listeners (`server does not support TLS`), so every run used `sslmode=disable`. Live TLS acceptance stays deferred (O8) and no TLS claim is made |
| Auth | `sys_hba.conf` enables `scram-sha-256` for the client subnet and every connection authenticated with it. Cleartext/MD5/SM3 are not enabled on this deployment |
| Roles | The main suite connects as the operator's `system` login; `TestLiveOrdinaryRoleCatalog` creates a temporary ordinary login per mode for the V2 ordinary-role capture |
| Catalog | Qualified snapshot loaded as `catalog_status=full` with 754 (pg) / 1006 (oracle) / 902 (mysql) / 801 (sqlserver) rows; shapes captured per mode |
| Driver revision | `594be1b` (pinned with `KINGBASE_TEST_REVISION`), module `github.com/cocosip/go-kingbase` |
| Toolchain / host | `go1.27.4` (module baseline `1.26.0`) on `windows/amd64`, host `virtual` |
| Operator / time | `dsh-agent`, 2026-11-26T04:51Z |
| Artifacts | `live/artifacts/<mode>/{full-run.log,live-smoke.json,evidence-<mode>.json}` — local and gitignored; no credential appears in them |

### Commands

```sh
KINGBASE_TEST_DSN='host=<host> port=<port> user=<user> password=<secret> dbname=test sslmode=disable' \
KINGBASE_TEST_MODE=pg KINGBASE_TEST_PROFILE=V9R001C010-pg KINGBASE_TEST_REVISION=594be1b \
KINGBASE_TEST_OPERATOR=<who> KINGBASE_TEST_ARTIFACT_DIR=artifacts/pg \
go test -tags live ./live -count=1 -v
```

Repeat with `port=54321 KINGBASE_TEST_MODE=oracle`, `port=54322 KINGBASE_TEST_MODE=mysql` and
`port=54323 KINGBASE_TEST_MODE=sqlserver`. `KINGBASE_TEST_ARTIFACT_DIR` resolves relative to the
`live/` package directory, so `artifacts/pg` lands in `live/artifacts/pg`.

Set `KINGBASE_TEST_PEER_DSN` to a DSN for a *different* compatibility mode to run the cross-mode
concurrency probe inside the same suite; without it, that single probe skips with a named reason
and everything else runs unchanged. The accepted run set it to the next mode in the ring
(pg→oracle, oracle→pg, mysql→sqlserver, sqlserver→mysql).

### Results

| Instance | Result | Passed | Skipped | Why the skips are not gaps |
|---|---|---|---|---|
| pg | PASS | 37 | 4 | SQLServer-only routine limitation test, MySQL-only generated-ID test, SQLServer-only convenience test, opt-in performance baseline |
| oracle | PASS | 32 | 9 | PG-scoped probes (timezone/DST, CALL/INOUT rows, generated ID, LISTEN/NOTIFY, catalog lifecycle) plus the SQLServer/MySQL-only and perf tests |
| mysql | PASS | 33 | 8 | The same PG-scoped set minus the MySQL-only generated-ID test |
| sqlserver | PASS | 32 | 9 | The PG-scoped set, the measured `sql.Out` listener limitation, and the PG-scoped boundary probe |

No test failed in any mode. Each skipped test states its mode scope in the run log, and every
V-item it would have covered is evidenced by another test on the applicable profile.

Five probes were added for this run so the session and diagnostic surface is covered live rather
than only offline, and each passes in all four modes:

| Test | Proves |
|---|---|
| `TestLiveRuntimeParameterForwarding` | an allowlisted runtime value (`search_path`) reaches the backend exactly, and an unknown parameter is rejected before a session is published (F-113, F-102) |
| `TestLiveCheckoutResetRollsBackTransaction` | a connection returned with an open transaction is rolled back before reuse, so the next checkout starts clean (F-115) |
| `TestLiveSequenceOwnership` | `nextval`/`currval` stay on one pinned connection and never feed `LastInsertId` (F-605) |
| `TestLiveNoticeHandler` | a real server notice reaches `Config.NoticeHandler` through the public `kingbase.NewConnector` + `sql.OpenDB` path (F-704) |
| `TestLiveServerErrorFields` | a definite server error keeps severity/code/message, is not reported as execution-uncertain, and leaves the session reusable (F-901, F-903) |
| `TestLiveMultiHostFailover` | an endpoint list whose first candidate cannot resolve still connects to the live candidate in every mode, and an all-failing list returns an error naming each candidate (F-106) |
| `TestLiveMultipleResultSets` | a two-statement simple query exposes both result sets through `NextResultSet`, ends without a third, and keeps the session usable (F-406) |
| `TestLiveCrossModeConcurrency` | two pools in different compatibility modes run interleaved in one process (8 workers each, `-race` clean) and every connection keeps its own mode (F-1104, F-209); needs `KINGBASE_TEST_PEER_DSN` |

The opt-in baseline (`KINGBASE_TEST_PERF=1`, `TestLivePerformanceBaseline`) was measured
separately on the same host on 2026-11-26: single-row 548/518/443/441 us and COPY IN
1.79/1.80/4.99/1.70 us per row for pg/oracle/mysql/sqlserver. It is log-only with no threshold,
so it stays a reference point rather than a gate.

### Scope limits of this acceptance

- **Live TLS (V8)** and the live halves of F-804/F-805/F-243 stay deferred by operator decision (O8):
  the deployment has no TLS listener. The policy code and its offline suite remain in place.
- **Auth method set (V7):** accepted for the enabled method (`scram-sha-256`, F-802). F-801
  (cleartext/MD5) and F-803 (SM3) are implemented and replayed offline; their live state stays
  pending because the deployment does not enable them.
- **PG-scoped probes:** the catalog lifecycle (V17) and DST (V18) probes run on the PG instance
  only. The mode-independent snapshot and temporal logic stays covered by the offline suites and
  by the per-mode boundary map.
- **Target attributes:** endpoint-list fallback is proven live, but `target_session_attrs=read-write`/`read-only`
  stay offline-only because the deployment provides no replica that would make the distinction observable.
- **SQLServer routine OUT:** the listener does not return an output result set on the extended
  protocol, so `sql.Out` is rejected there with a named limitation (documented, not a gap).
- **Later-scope profiles:** V8R6 (V11/V13) and native MySQL (V15/M5) are not covered and are not
  inherited from this run.

### Acceptance decision

**Accepted (scope-limited) for the initial PG-wire release on 2026-11-26**, revision `594be1b`,
on the four instances above and under the scope limits in this section. Every applicable V-item
for this deployment is evidenced; the capability matrix section 17 holds the per-pair override
records and the feature Live states this acceptance turns over.