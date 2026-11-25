# KingbaseES Go Driver — Capability Matrix

> Status: corrected requirements; M0 core in progress, no live acceptance yet
> Date: 2026-11-25
> Companion documents: [Architecture Design](go-driver-architecture-design.md) · [Development Plan and Progress](go-driver-development.md)

## Reading Guide

- Mode is a server compatibility property. Its exact instance/database scope is release-specific evidence (V14); no unverified DBCOMPATIBILITY syntax is required. The DSN declares/validates semantics, never changes mode.
- wireProtocol=pg is default; mysql is explicit conditional M5. No automatic handshake selection, reconnect or port fallback.
- PG catalog is a qualified server capture; static SDK OID tables are research only. Product identity and supported release capabilities are separate from a PostgreSQL-style kernel version.
- P0/P1 are required for features/profiles included in the release manifest; P1 may use only an explicitly specified safe limitation. P2 may be excluded/deferred with reason; an enabled P2 success claim requires the same applicable proof. V8, M5 and deferred COPY/auth extensions are outside the initial PG release until separately accepted.
- offline = unit/replay; live = actual server; both = both required. Impl/offline/live statuses below are independent: todo/pending/pending initially. n/a live means the requirement is offline-only, not that a missing server passed.
- Aggregate live accepted requires all applicable profiles in the release manifest. A blocked/unavailable profile stays pending and cannot be advertised. Tests/provenance live in the progress tracker, with per-profile records in section 17.

DSN parsing, bounded PG-wire framing, a scripted offline peer, the startup exchange with cleartext/MD5/SM3/SCRAM authentication, TLS trust-policy negotiation, simple/extended query-or-exec, server-side prepared statement reuse with drain/retire semantics, type-catalog codecs, transactions, cancel-channel delivery with bounded synchronization, liveness/reset hooks, explicit array scanning and PG-mode generated-ID capture are implemented and tested offline. No live acceptance exists yet; documentation correction does not change implementation status.

## 1. Connection and Configuration

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-101 | DSN parsing: URL and libpq forms | P0 | offline | spaces/quotes/backslashes/empty values | done | done | n/a |
| F-102 | Driver/runtime allowlist and validation | P0 | offline | Unknown driver keys fail; only listed runtime settings forwarded safely; explicit unsupported P2 options fail | done | done | n/a |
| F-103 | Whole physical Connect timeout | P0 | both | Context/timeout covers DNS through identity, mode, catalog and idle validation; close partial sessions | done | done | pending |
| F-104 | `application_name` / `fallback_application_name` | P1 | live | verified application-name visibility per backend, not assumed PG SQL on MySQL | done | done | pending |
| F-105 | TCP keepalive | P2 | offline | OS-specific settings; explicit unsupported error until implemented | done | done | n/a |
| F-106 | Multiple hosts and target_session_attrs | P2 | live | Separate opt-in target-selection policy; no protocol or port fallback | done | done | pending |
| F-107 | Environment-variable fallback (`KINGBASE_*`) | P2 | offline | explicit parameters win; names are case-insensitive | done | done | n/a |
| F-108 | Parameter-name normalization | P0 | offline | lowercase + strip `_`, so `connect_timeout`/`connectTimeout` collapse; explicit aliases: `compatibilityMode` ← `compatibleMode`, `mode`, `databaseMode` | done | done | n/a |
| F-109 | Case-insensitive enum values | P0 | offline | `MYSQL` / `MySQL` / `mysql` equivalent | done | done | n/a |
| F-110 | Values that keep their case | P0 | offline | `user`, `password`, `dbname`, paths, runtime parameter values are not folded | done | done | n/a |
| F-111 | URL percent-decoding | P0 | offline | `app%40tenant` → `app@tenant`, decode before folding | done | done | n/a |
| F-112 | Duplicate parameter semantics | P1 | offline | after folding, the last occurrence wins | done | done | n/a |
| F-113 | Runtime/lexical parameter handling | P1 | both | Allowlist, exact values, safe backend configuration, refresh lexical context or retire | done | done | pending |
| F-114 | Missing `dbname` defaults to the user name | P1 | offline | libpq convention | done | done | n/a |
| F-115 | Safe session reset before reuse | P0 | both | SessionResetter checkout timing; pinned Conn state; bounded reset, dirty/poisoned retirement, prepared-handle invalidation | done | done | pending |
| F-116 | `Pinger` and `Validator` (connection liveness) | P0 | both | used by `db.Ping` and pool health checks | done | done | pending |
| F-117 | Client encoding | P0 | both | Initially UTF8/UTF-8 only; reject unimplemented encodings; never silently corrupt | done | done | pending |

## 2. Mode Selection

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-201 | Auto mode from authoritative server evidence | P0 | both | Validated startup/query report; bounded PG candidate chain; native bootstrap separately verified; no bare-name inference | done | done | pending |
| F-202 | Explicit mode declaration and validation | P0 | both | Check known server conflicts; unavailable discovery records modeVerified=false; no fabricated actualMode | done | done | pending |
| F-207 | Mode probe failure contract | P0 | both | Auto unknown fails; distinguish empty/missing/permission/timeout; no PG default or catalog-mode guess | done | done | pending |
| F-208 | Connector mode/protocol pinning | P1 | offline | Protocol fixed by configuration; authoritative mode conflicts fail; no greeting memo | done | done | n/a |
| F-209 | Per-connection state isolation | P0 | offline | concurrent modes with `-race`; no process-global mutable state | done | done | n/a |

## 3. Type Catalog

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-212 | Unknown type/OID handling | P0 | offline | a type the driver has no codec for decodes as `string`/`[]byte`; absent OID has empty DatabaseTypeName; ScanType follows actual text string/binary bytes; malformed frames still error | done | done | n/a |
| F-214 | Qualified registry and fixture provenance | P0 | both | Synthetic/SDK fixtures distinct from actual server captures; live captures include namespace/shape/query/product provenance | done | done | pending |
| F-215 | Required connect-time qualified catalog | P0 | both | Namespace names, typtype, element/base/length/delimiter/row links before user SQL; bounded capture; no fixed row-count promise | done | done | pending |
| F-216 | Ambiguous names and user types | P0 | offline | OID plus qualified identity/shape; app.int/year/bit never acquire built-in codecs or select mode | done | done | n/a |
| F-217 | Verified alternate catalog queries | P0 | both | PG full-shape then accepted sys equivalent for missing-object cases; two-column fallback cannot enable required codecs | done | done | pending |
| F-220 | Qualified profile/catalog validation | P0 | both | Only live-verified required shape rules; year/int markers do not determine mode or create arbitrary conflicts | wip | pending | pending |
| F-221 | Bounded protocol/endpoint diagnostics | P0 | offline | Configured transport only; sanitized capped prefix; no zero-wait peek, cross-protocol reconnect, port scan or downgrade | done | done | n/a |
| F-218 | Catalog refresh boundary | P1 | both | New physical connection after type-changing DDL; stale/unknown OIDs safe; no mid-reader reload | done | done | pending |

## 4. Type Encoding and Decoding

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-301 | Core scalar types | P0 | both | bool/int/float/numeric/text/bytea/date/time/timestamp/interval/uuid/json/xml/inet; standard driver values; text/binary parity for implemented formats; unknown binary remains bytes | wip | pending | pending |
| F-302 | Mode-specific codecs | P0 | both | tinyint/smallint/int/bigint/real/float/bit/datetime/year/money/binary/varbinary/nchar/nvarchar/longtext family/blob/uint4/uint8; verified qualified identity/range; standard values per Architecture 7.1 | todo | pending | pending |
| F-303 | Mode semantics at standard value boundary | P0 | both | tinyint range checked then int64; boolean BIT bool/nil; date time.Time, time text, datetime time.Time | todo | pending | pending |
| F-304 | Array/domain/composite contracts | P1 | both | Validated links; opaque default text; explicit Array Scanner/Valuer for rectangular 1-D/multi-D, empty/NULL arrays and NULL elements; dimensions/lower bounds preserved; bounded recursion | done | done | pending |
| F-305 | Binary parameters/results and text option | P1 | both | Verified type/format only; disable_prepared_binary_result parity; no unconditional bytea target | done | done | pending |
| F-306 | Timezone and temporal meaning | P0 | both | Date/wall time distinct from instants; session zone/DST/precision; no implicit shift | todo | pending | pending |
| F-307 | Actual uppercase DatabaseTypeName | P1 | both | Backend/catalog name without length; no unified PG relabel; absent OID empty | done | done | pending |
| F-308 | Honest column metadata | P1 | both | Nullable known/unknown ok flags; outer joins/expressions; real ScanType, byte/character lengths and precision | done | done | pending |
| F-309 | YEAR standard value | P1 | both | int64 for validated live forms; reject malformed/truncated/overflow input | todo | pending | pending |
| F-310 | Numeric/temporal conversion boundaries | P0 | both | Full uint64/decimal/NaN/infinity/zero/BC/precision/negative-time/DST/locale cases; no truncation | todo | pending | pending |

## 5. Execution and Binding

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-401 | Simple query and extended protocol | P0 | both | prepared-statement reuse; one-shot extended and server-side prepare with reuse | done | done | pending |
| F-402 | Placeholder lexer and binding plan | P0 | offline | One style; quotes/backticks/brackets/dollar/comments/operators/settings; repeated/reversed names/indexes expand correctly | wip | pending | n/a |
| F-403 | Logical NumInput and call validation | P0 | both | Unique names/max contiguous dollar index; SQLServer -1 still validates bindings | todo | pending | pending |
| F-404 | NamedValue/Valuer/NULL conversion | P1 | both | Preserve wrapper target metadata; nil/Null validity, full uint64 range, Valuer once per logical argument | wip | pending | pending |
| F-405 | Qualified target OID inference | P0 | both | Explicit verified target or inference; byte/bool/bytes wrappers cannot hardcode same-named user targets | done | done | pending |
| F-406 | Multiple results and terminal errors | P1 | both | NextResultSet/early Close drains all trailers; trailing errors preserved before outputs/success | done | done | pending |
| F-407 | Cancellation and synchronization | P0 | both | PG cancel EOF plus original ReadyForQuery barriers; races/timeout retire; MySQL aborts until control path accepted | done | done | pending |
| F-408 | Session lexical settings | P0 | both | Verified quote/backslash/question/comment rules; context updates or reject/retire; no stale rewrite cache | done | done | pending |

## 6. Stored Procedures and OUT Parameters

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-501 | SQLServer fixed routine status | P0 | both | Per accepted invocation; unbound status discarded without failure | todo | pending | pending |
| F-502 | OUT/INOUT assignment | P0 | both | Checked sql.Out conversion after complete results; incomplete/cancelled call has no valid outputs | todo | pending | pending |
| F-503 | SQLServer routine-name convenience | P1 | both | Profile-gated parsed qualified identifier; no arbitrary SQL concatenation | todo | pending | pending |
| F-504 | Non-SQLServer routine forms | P1 | both | Include official MySQL-compatible OUT path; mode/form/wire-specific acceptance; no blanket rejection or silent assignment | todo | pending | pending |
| F-505 | OUT tinyint signedness/range | P1 | both | Checked uint8/int8 destinations; NULL/overflow explicit | todo | pending | pending |
| F-506 | Refcursor ownership and lifecycle | P1 | both | Pinned session/transaction, explicit fetch/close, multiple cursor and cancellation handling | todo | pending | pending |

## 7. Auto-Increment and Sequences

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-601 | Safe opt-in PG generated identity | P0 | both | Verified one generated integral column on supported single-row INSERT; never RETURNING * or column-one guess; MySQL form remains F-603 | done | done | pending |
| F-602 | Generated-ID type/range coverage | P0 | both | Checked exact int64 only; unsigned high-bit/float/nonintegral/ambiguous IDs unavailable or overflow; explicit Query for other values | done | done | pending |
| F-603 | Generated tinyint conversion | P0 | offline | Apply accepted mode range and exact int64 conversion | todo | pending | n/a |
| F-604 | Unavailable/overflow LastInsertId | P0 | offline | Typed error, not zero; preserve RowsAffected; reject unsafe requested rewrite before executing | done | done | n/a |
| F-605 | Sequence access boundaries | P1 | both | Explicit verified SQL on owning connection; no MAX/last-sequence identity guess; no portable GetSchema promise | todo | pending | pending |

## 8. Transactions, COPY, Notifications

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-701 | TxOptions and transaction lifecycle | P0 | both | LevelDefault, four explicit levels, unsupported error, ReadOnly, commit/rollback/cancel recovery | done | done | pending |
| F-702 | CopyIn/CopyInSchema text import | P1 | both | Statement-string helpers; Tx.Prepare/Exec/flush/Close; identifiers, NULL/escaping, completion/trailing errors | done | done | pending |
| F-703 | LISTEN/NOTIFY | P2 | both | Async parsing; bounded 256-message queue, overflow gap/reconnect, no replay, nil gap marker | done | done | pending |
| F-704 | NOTICE/WARNING callbacks | P2 | both | Default discard; no stderr, same-connection reentrancy forbidden | done | done | pending |
| F-705 | Binary COPY import | P2 | both | Deferred from first PG release; future complete/abort/ownership contract and live acceptance | todo | pending | pending |
| F-706 | COPY output | P2 | both | Deferred from first PG release; distinguish text/binary output and reader ownership | todo | pending | pending |
| F-707 | Raw COPY streams | P2 | both | Deferred from first PG release; no backup/CDC/cross-version promise | todo | pending | pending |

## 9. Authentication and Security

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-801 | Cleartext and MD5 auth | P0 | both | Only accepted server request forms; offline transcripts plus per-profile live proof | done | done | pending |
| F-802 | SCRAM-SHA-256 auth | P0 | both | Signature/nonce/iterations/salt/attribute limits and negative vectors | done | done | pending |
| F-803 | SM3 password auth | P0 | both | Exact evidenced method; does not imply cryptographic TLS or arbitrary variants | done | done | pending |
| F-804 | Explicit TLS trust policy | P0 | both | require plus configured existing root verifies CA; verify-ca/full; no downgrade; explicit missing files fail | done | done | pending |
| F-805 | Private-key/certificate handling | P1 | both | Strict POSIX private-key permissions only; readable public certs accepted; Windows cases separate | done | done | pending |
| F-806 | Additional auth/security extensions | P2 | both | GSS/SSPI/SCRAM-PLUS etc deferred; unsupported form fails; no downgrade | todo | pending | pending |

## 10. Errors and Diagnostics

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-901 | Full mapping of server error fields | P0 | both | `errors.As` | done | done | pending |
| F-902 | Error/operation classification | P1 | offline | Execution uncertainty separate from retry safety; redacted config/protocol/catalog/cancel errors | done | done | n/a |
| F-903 | Safe retirement without duplicate execution | P0 | both | After possible execution mark invalid without ErrBadConn matching; only retry-safe boundaries may signal ErrBadConn | done | done | pending |
| F-1001 | Verified connection diagnostics | P1 | both | Configured/observed mode/protocol, source/verification, raw kernel/product release and capability profile; documented accessor | done | done | pending |
| F-1002 | Public API migration ledger/examples | P1 | offline | Keep/replace/defer official helpers and options; compiling examples required after implementation | todo | pending | n/a |

## 11. Quality Assurance

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-1101 | Unit/contract suite | P0 | offline | Coverage target at least 70 percent plus named risk tests; percentage alone is not acceptance | todo | pending | n/a |
| F-1102 | Fake-server/failure suite | P0 | offline | Fragmentation, sizes/counts/EOF, auth faults, async rows, trailing errors, reset/cancel/retry races | done | done | n/a |
| F-1103 | Scoped real-server profile matrix | P0 | live | V9 profiles first; exact accepted edition/release/mode/wire; unavailable profile pending, V8 later | todo | pending | pending |
| F-1104 | Concurrency tests | P0 | both | multiple modes interleaved + `-race` | todo | pending | pending |
| F-1105 | Performance baseline | P2 | live | Actual single-row/COPY workloads; explicitly deferrable outside release blockers | todo | pending | pending |
| F-1106 | Repository lint rules | P0 | offline | Run existing .golangci.yml once product module exists | done | done | n/a |
| F-1107 | Toolchain/platform/package release contract | P0 | offline | Go 1.26.0; module github.com/cocosip/go-kingbase from Git origin; Apache-2.0; linux/windows/darwin amd64+arm64; stdlib-only core; CI pinned; package smoke checks at T-051/T-053 | done | done | n/a |

## 12. MySQL Transport Backend (conditional)

For deployments whose native MySQL endpoint is established by V12/V15 (Architecture Appendix C). Packaging and compatibility names do not prove listener availability. Selected only by explicit wireProtocol=mysql; excluded from initial PG release.

| ID | Capability | Priority | Verification | Notes | Impl | Offline | Live |
|---|---|---|---|---|---|---|---|
| F-240 | Explicit transport selection | P1 | offline | wireProtocol=mysql only; default pg; no auto/peek/reconnect; unsupported backend clear error | done | done | n/a |
| F-241 | MySQL handshake/capability negotiation | P1 | both | Implemented flags only; multi-results and PS multi-results separate; charset, AuthSwitch/MoreData, bounded header | todo | pending | pending |
| F-242 | MySQL auth plugins | P1 | both | Verified native/caching_sha2 paths; protected full auth, no downgrade or unverified key retrieval | todo | pending | pending |
| F-243 | MySQL in-band TLS | P1 | both | Independent SSLRequest/handshake/trust acceptance; same configured CA policy, no plaintext fallback | todo | pending | pending |
| F-244 | Native prepared query/binding | P1 | both | COM_QUERY/PREPARE/EXECUTE/CLOSE; SQL plus expanded/reordered argument plan; no interpolation | todo | pending | pending |
| F-245 | Native result/state parsing | P1 | both | Text/binary NULL/unsigned flags; negotiated EOF/OK; all results/status/warnings before reuse | todo | pending | pending |
| F-246 | Native column metadata/codecs | P1 | both | Actual uppercase MySQL types; standard values, exact numerics/temporals; byte/char units | todo | pending | pending |
| F-247 | Verified native bootstrap | P1 | both | No PG catalog; independently verify identity/mode/encoding; conflict fails, not metadata warning | todo | pending | pending |
| F-248 | Native packet framing and budgets | P1 | both | 0xffffff continuation/empty terminator; split headers, sequence/wrap, length/allocation/EOF limits | todo | pending | pending |
| F-249 | Native lifecycle/reset/transaction/cancel | P1 | both | Accepted status/idle/reset/handles; initially cancellation aborts; uncertain completion never retries | todo | pending | pending |

## 13. Live Verification Requirements

All V-items are pending until the progress tracker records real evidence. Not-applicable is allowed only for a named excluded profile/feature with reason, never lack of infrastructure. V11/V13 apply to later V8 scope; V15 and native cases of V8/V19 apply when M5 enters the release manifest. Authentication/COPY/notification cases follow enabled features; unsupported extensions remain unclaimed.

| ID | Item | Required evidence | Blocks |
|---|---|---|---|
| V1 | Authoritative mode discovery per scoped V9 mode/edition | Capture startup/query values, ordinary-role permissions, empty/unknown/conflicting results; no type-marker fallback | F-201, F-202, F-207, F-220 |
| V2 | Qualified catalog shape and access | Capture exact PG/sys query objects, namespace names, shape/link fields and errors as ordinary role; two-column data cannot pass | F-215, F-216, F-217, F-307 |
| V3 | Actual catalog fixture provenance | Record server product/edition/mode/protocol/query/time and raw shape; compare registry; keep SDK constants separate | F-214, F-215 |
| V4 | Verified qualified built-ins | Check required namespace/name/shape, same-named app types and ambiguous candidates; absence is not a mode switch | F-212, F-216, F-220, F-302 |
| V5 | Routine/OUT/refcursor matrix | Mode plus invocation plus wire; source examples/paths, scalar/NULL/INOUT/status/multiple results/trailing errors/cursor owner cleanup | F-501, F-502, F-503, F-504, F-505, F-506 |
| V6 | Generated identities and explicit sequences | Non-first identity, ambiguous/missing identity, existing RETURNING, multirow/CTE/conflict rejection before execution, int64 overflow, sequence ownership | F-601, F-602, F-603, F-604, F-605 |
| V7 | Every enabled PG auth method | Cleartext/MD5/SCRAM/SM3 transcripts and server identity; unsupported variants explicit; no downgrade | F-801, F-802, F-803 |
| V8 | TLS trust/material/platform matrix | Each sslmode, configured CA with require, missing CA, wrong host, public cert/private-key permissions; native MySQL separately | F-804, F-805, F-243 |
| V9 | Text COPY and asynchronous delivery | Prepare/Exec/flush/Close errors, NULL/escaping, cancel/tx state; notifications during reading, queue overflow/reconnect gaps | F-702, F-703, F-704 |
| V10 | YEAR/binary format conventions | Actual type formats by qualified profile; full syntax, 0x/no-prefix cases, binary/text equivalence | F-309, F-303, F-305 |
| V11 | V8R6 profile acceptance (later scope) | Exact sub-release, modes, auth/handshake/identity/catalog; do not inherit V9 coverage | F-201, F-215, F-1103 |
| V12 | Actual deployed protocol listeners | Administrator-confirmed endpoint/configuration plus positive client handshake; no fixed-port/packaging assumption or driver port scan | F-221, F-240 |
| V13 | Legacy compatible_level meaning (later scope) | Version-specific official docs and observed values; do not require two differently aged GUCs on one release | F-201, F-408 |
| V14 | Mode storage scope and immutability | Read release-specific official initialization docs/pg_settings context and target connection reports; no assumed DBCOMPATIBILITY DDL | F-201, F-202, F-220 |
| V15 | Native MySQL backend profile | Independently establish bootstrap SQL/identity/mode, auth/TLS, prepared/binary rows, identities/errors, capabilities/framing and endpoint families | F-240–F-249 |
| V16 | Product identity and whole initialization budget | Verify bounded version query/raw release parsing; fast auth then stalled mode/catalog, partial init and deadline cleanup | F-103, F-1001 |
| V17 | Catalog lifecycle after DDL/pool reuse | New/drop/changed type OIDs, stale pooled snapshot, safe opaque fallback, explicit drain/close/reopen, no mid-reader query | F-214, F-218 |
| V18 | Scalar/temporal/lexical boundary acceptance | Exact numeric/uint64/decimal/money, zero/infinite/BC/duration/DST, encoding and session quote/question/comment settings | F-117, F-301, F-302, F-304, F-306, F-310, F-408 |
| V19 | Session/transaction/cancel recovery | Pinned state, runtime/application settings, reset coverage/handles, read-only/default/unsupported levels, close/drain/trailing errors/cancel races; native backend independently | F-104, F-113, F-115, F-116, F-401, F-406, F-407, F-506, F-701, F-249 |

## 14. Non-Goals and Explicit Deferrals

- A second connection pool, ORM/provider, SQL dialect translation and database administration are outside this driver. database/sql integration remains required.
- No built-in product OID table, arbitrary business-SQL translation or automatic protocol/port fallback.
- V8 is later scope; native MySQL is conditional M5. Neither is advertised by passing V9 PG tests.
- Binary COPY input, COPY output/raw streams and extra auth extensions are tracked P2 F-705/F-706/F-707/F-806, outside the initial PG release. LISTEN/callbacks, keepalive, multi-host, env fallback and benchmarks are P2 and may be explicitly deferred.
- Compression, LOCAL INFILE, multi-statement execution, unverified long-data streaming and control-query cancellation are not enabled on native MySQL.
- A separate EF provider is future work, not a Go driver acceptance requirement. No ADO.NET-specific APIs are mechanically copied.

## 15. Delivery Milestones

| Milestone | Exit criteria |
|---|---|
| M0 skeleton | Offline DSN/auth/query/core value/failure replay works; no live support claim |
| M1 catalog layer | Qualified registry and hostile/real-provenance fixture logic passes offline; live catalog proof remains M3 |
| M2 mode behaviour | Scoped types/binding/routines/identity/transactions/COPY pass named offline cases; no pending-live item is called accepted |
| M3 live acceptance | Every included feature has required proof on each manifest V9 profile; P0/P1 cannot be deferred within scope; enabled P2 needs the same proof; applicable V1–V19 closed, missing evidence blocks, excluded-profile N/A has reason |
| M4 release quality | Scoped F-1101/F-1102/F-1103/F-1104/F-1106/F-1107 and migration/examples pass; O1/O7 closed; P2 baseline/other deferrals explicit, never silently required or passed |
| M5 MySQL transport (conditional) | F-240–F-249 implemented/offline/live accepted per native manifest; V12/V15/V19 closed, O6 resolved; cannot borrow PG acceptance |

## 16. Requirement Traceability

| Requirement | Capabilities |
|---|---|
| V9 all-mode and actual single-mode profiles | F-201, F-202, F-214, F-215, F-220, F-302, F-1103 |
| Explicit compatibility declaration and transport | F-101, F-102, F-202, F-221, F-240 |
| Server identity and no manual OID-version selection | F-1001, F-214, F-215, F-216, F-218 |
| Case-insensitive DSN without value corruption | F-108–F-112 |
| Shared mode-safe internals and pool lifecycle | F-115, F-208, F-209, F-303, F-903 |
| Exact values, binding, routine/cursor and identities | F-301–F-310, F-402–F-408, F-501–F-506, F-601–F-605 |
| Native MySQL when independently accepted | F-240–F-249, V12, V15, V19 |
| V8 later, separate manifest | F-1103, V11, V13 |
| Migration and release evidence | F-1002, F-1101–F-1107 |

Ranges above denote existing IDs in that interval, not invented capabilities. Every concrete feature has at least one T-task owner in the progress tracker.

## 17. Release Profiles and Evidence Records

These are requested target profiles, not a support list. Exact product/edition/release must replace TBD before acceptance; if a combination does not exist, document evidence and explicitly exclude it instead of manufacturing a Cartesian-product result. Changing scope must update all three documents and the public support list; untested targets cannot disappear merely to turn M3 green.

| Profile | Requested deployment/mode | Wire | Release scope | Exact product/build | Acceptance |
|---|---|---|---|---|---|
| V9-A-PG | all-mode packaging initialized as PG | pg | first-release target | TBD | pending |
| V9-A-ORA | all-mode packaging initialized as Oracle | pg | first-release target | TBD | pending |
| V9-A-MY | all-mode packaging initialized as MySQL | pg | first-release target | TBD | pending |
| V9-A-SQL | all-mode packaging initialized as SQLServer | pg | first-release target | TBD | pending |
| V9-S-ORA | actual Oracle single-mode packaging | pg | first-release target if confirmed available | TBD | pending |
| V9-S-MY | actual MySQL single-mode packaging | pg | first-release target if confirmed available | TBD | pending |
| V9-S-SQL | actual SQLServer single-mode packaging | pg | first-release target if confirmed available | TBD | pending |
| V8-LATER | concrete V8 build/mode to be selected | pg | later, excluded initially | TBD | pending |
| MYSQL-M5 | each confirmed native-endpoint family separately | mysql | conditional, excluded initially | TBD | pending |

For every scoped (feature,profile) pair with live/both verification, the default is pending. Record overrides only with: feature ID, profile ID, status (pending/accepted/failed/not-applicable), exact product/build, command/test, output/artifact, UTC capture time, role/permissions and exclusion reason if N/A. No override records exist yet; therefore every pair remains pending. Aggregate Live stays pending until all applicable pairs are accepted. Offline-only feature Live=n/a is structural and needs no server record.
