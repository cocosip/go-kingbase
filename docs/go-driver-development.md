# KingbaseES Go Driver — Development Plan and Progress

> Status: implementation in progress (M0 core in flight); no live acceptance
> Date: 2026-11-24
> Companions: [Architecture](go-driver-architecture-design.md) · [Capability Matrix](go-driver-capabilities.md)

## 1. Completion and Evidence Rules

Task status: todo / wip / done / blocked (reason) / deferred (reason). A done task means implementation completed with named offline proof; it does **not** mean live acceptance. Feature implementation, offline and live states are tracked independently in the capability matrix; each applicable (feature,profile) pair defaults to pending until evidence is recorded.

Implementation done requires product build/tests and relevant lint passing, named tests and commands/artifacts in Evidence, testpeer replay for protocol work, docs synchronized and every owned requirement covered. The current repository has no product go.mod, so none of those implementation checks has run. Documentation-only corrections do not mark a product task done. For both/live features, the live state remains pending until an actual accepted profile supplies evidence, regardless of task completion.

A release claim additionally requires every scoped P0/P1 feature's required offline/live proofs, exact server-profile provenance, closed required decisions and the M3/M4 gates. A skipped test, unavailable instance, source example, static SDK OID table or successful replay is not live acceptance. Unsupported forms may pass only an explicitly defined error/fallback contract; this cannot stand in for a promised success path.

Update task Evidence, feature status, live profile records, counts and log together. Keep one canonical task row per ID. Dependencies on O-items are decision gates, not claims of implementation completion. An excluded future feature can stay todo in the backlog without entering the initial-release gate; formal deferral is separately recorded in section 8.

## 2. Progress Summary

| Milestone | Tasks | done | wip | todo | blocked | deferred |
|---|---|---|---|---|---|---|
| M0 skeleton | 19 | 16 | 2 | 1 | 0 | 0 |
| M1 catalog layer | 8 | 0 | 0 | 8 | 0 | 0 |
| M2 mode behaviour | 19 | 4 | 2 | 13 | 0 | 0 |
| M4 release quality | 10 | 0 | 0 | 10 | 0 | 0 |
| M5 MySQL transport (conditional) | 9 | 0 | 0 | 9 | 0 | 0 |
| **Total** | **65** | **20** | **4** | **41** | **0** | **0** |

Verification V1–V19: 0 accepted, 19 pending. Decisions O1–O7: 3 resolved (O1, O2, O7), 4 open/evidence-pending (O3, O4, O5, O6). Features: 94 total; 28 are Impl=done and Offline=done (F-101–F-103, F-108–F-112, F-114, F-116, F-117, F-212, F-215, F-401, F-407, F-701, F-702, F-801–F-805, F-901–F-903, F-1102, F-1106, F-1107), 1 is Impl=done with Offline=pending (F-104), 1 is Impl=wip (F-301 binary parity), and 64 remain Impl=todo/Offline=pending; 69 Live=pending and 25 structurally offline-only Live=n/a (recount when requirements change). No accepted live-profile overrides exist. M3 is a verification gate, not an omitted implementation-task group.

## 3. Milestones

| Milestone | Exit criteria | Status |
|---|---|---|
| M0 skeleton | Offline DSN/auth/query/core value/failure replay works; no live support claim | wip |
| M1 catalog layer | Qualified registry and hostile/real-provenance fixture logic passes offline; live catalog proof remains M3 | todo |
| M2 mode behaviour | Scoped types/binding/routines/identity/transactions/COPY pass named offline cases; no pending-live item is called accepted | todo |
| M3 live acceptance | Every included feature has required proof on each manifest V9 profile; P0/P1 cannot be deferred within scope; enabled P2 needs the same proof; applicable V1–V19 closed, missing evidence blocks, excluded-profile N/A has reason | todo |
| M4 release quality | Scoped F-1101/F-1102/F-1103/F-1104/F-1106/F-1107 and migration/examples pass; O1/O7 closed; P2 baseline/other deferrals explicit, never silently required or passed | todo |
| M5 MySQL transport (conditional) | F-240–F-249 implemented/offline/live accepted per native manifest; V12/V15/V19 closed, O6 resolved; cannot borrow PG acceptance | todo |

### 3.1 Development entry and first implementation order

The specification is ready to serve as a development baseline. The confirmed Go version is **1.26.0** and module path is **github.com/cocosip/go-kingbase**, derived from Git origin git@github.com:cocosip/go-kingbase.git. T-001 created go.mod with that module path and go 1.26.0. O1 and O7 are resolved: supported platforms are linux/windows/darwin on amd64 and arm64, the license is Apache-2.0 (LICENSE/NOTICE committed), the dependency policy is standard-library-only for the core driver, and CI pins Go by `go.mod` with golangci-lint v2.14.0. Reproducible build/test/lint commands are documented in README.md and mirrored in `.github/workflows/ci-cd.yml`.

After T-001, start T-002 (DSN/configuration) and T-012 (testpeer); then follow the declared dependencies through T-004/T-005 and the query/core-value tasks. Each protocol/parser implementation must choose and document concrete message/allocation/recursion/crypto-work limits with boundary tests before that task is done. Move a task to wip when implementation starts and record named offline proof before done; leave unrelated backlog and live acceptance pending.

Current progress follows that order: T-001/T-002/T-004–T-009/T-011–T-013/T-015 are done, T-020/T-014 remain wip, and server-side prepared statements now reuse Parse state with the server reporting NumInput. Next: T-010 binary codecs, T-033–T-035 placeholder/binding rules, and T-040 multiple results.

Arrange the requested V9 profiles and ordinary-role catalog/mode captures early alongside offline work, recording O3/O5 evidence when available. Missing live infrastructure does not prevent the offline skeleton, but continues to block the applicable M3 support claims. Configure local credentials through ignored environment/config files or local secret/certificate directories; commit only redacted templates and intentional fixtures.

## 4. Backlog and Requirement Owners

Every capability ID is owned by at least one canonical task below. Ranges expand only over declared feature IDs. Every task remains todo; test names/evidence are added when code exists, not invented by this documentation repair.

### M0 skeleton

| ID | Deliverable | Capabilities | Depends on | Status | Evidence |
|---|---|---|---|---|---|
| T-001 | Scaffold github.com/cocosip/go-kingbase with Go 1.26.0; complete platform/license/dependency/CI contract | F-1106, F-1107 | O1 (resolved), O7 (resolved) | done | `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run ./...` (0 issues) on 2026-11-24; go.mod, LICENSE/NOTICE (Apache-2.0), README contract, `.github/workflows/ci-cd.yml`, `.golangci.yml` unchanged; no product behaviour yet |
| T-002 | DSN parsing, allowlists, normalization, aliases, explicit empties | F-101, F-102, F-108–F-112, F-114 | T-001 | done | `go test ./...` `TestParseConfigURL`, `TestParseConfigEquivalentURLs`, `TestParseConfigKeyValue`, `TestParseConfigAliasesAndDuplicates`, `TestParseConfigDefaults`, `TestParseConfigMissingDatabaseDefaultsToUser`, `TestParseConfigErrors`, `TestParseConfigErrorsDoNotLeakPassword`; `golangci-lint run ./...` (0 issues) on 2026-11-24 |
| T-003 | Deterministic environment fallback/conflicts (P2) | F-107 | T-002 | todo | — |
| T-004 | Connector/Dialer, full initialization budget, sockets and TLS policy | F-103, F-104, F-804, F-805 | T-002 | done | `TestConnectCancelInterruptsHandshake`, `TestConnectTimeoutBudget`, `TestConnectEarlierContextDeadlineWins`, `TestConnectClearsHandshakeDeadline`, `TestConnectClosesSocketOnFailedHandshake`, `TestConnectUnixSocket`, `TestTLSModes` (require/verify-ca/verify-full success, wrong root, missing root, untrusted), `TestTLSVerifyFullRejectsWrongHostname`, `TestTLSClientCertificatePairing`, `TestPrivateKeyPermissions`; `go test -race ./...` and `golangci-lint run ./...` (0 issues) on 2026-11-24. Live TLS/material provenance remains M3 |
| T-005 | PG startup, cleartext/MD5, startup parameters and synchronization | F-801 | T-004, T-012 | done | `TestConnectCleartextAuth`, `TestConnectMD5Auth`, `TestConnectStartupParameters`, `TestConnectErrorResponse`, `TestConnectCancelledContext`; `golangci-lint run ./...` (0 issues) on 2026-11-24 |
| T-006 | Bounded SCRAM handshake/signature/nonce/iteration negative vectors | F-802 | T-005 | done | `internal/auth` SCRAM-SHA-256 client passes the RFC 7677 test vector; negative vectors cover nonce mismatch, iteration bounds, malformed/duplicate attributes, mandatory extension, salt/base64 errors, oversized messages and server-signature mismatch; `TestConnectSCRAM`, `TestConnectSCRAMWrongPassword`, `TestConnectSCRAMRejectsBadServerSignature`, `TestConnectSCRAMRejectsUnsupportedMechanism` |
| T-007 | Evidenced SM3 authentication and unsupported variants | F-803 | T-005 | done | `internal/sm3` passes both GB/T 32905-2016 vectors; the double-HMAC-SM3 composition is source-evidenced (vendor conn.go auth case 13) and replayed by `TestConnectSM3`; wrong password keeps SQLSTATE 28P01 through `TestConnectSM3WrongPassword`; missing password and unknown auth codes fail closed |
| T-008 | Simple/extended query, send-stage and protocol state tracking | F-401 | T-005 | done | `TestQuerySimpleRows`, `TestQueryExtendedBinding`, `TestExecExtended`, `TestQueryServerErrorKeepsConnection`, `TestQueryEarlyCloseDrains`, `TestQueryMultipleResultSetsRejected`, `TestQueryContextDeadlineRetires`, `TestQueryNoRows`; send/read failures retire the connection and never replay |
| T-009 | Core text codecs normalized to supported driver values | F-301 | T-008 | done | `codec.go` decodes bool/int/float/numeric/text/bytea/date/time/timestamp/timestamptz/interval/uuid/json/jsonb/xml/inet by catalog identity; `TestRowsCoreTextCodecs` pins values, type names and scan types; unknown OIDs keep string/[]byte per F-212 (`TestRowsUnknownOIDFallback`); binary parity remains T-010 |
| T-010 | Verified binary codecs/parameters and text-result option parity | F-305 | T-009 | done | Binary result decoding covers bool/int2/int4/int8/float4/float8/text/bytea/json/jsonb/date/timestamp/timestamptz/uuid with raw-byte fallback for unverified layouts; prepared statements request binary results by default with `disable_prepared_binary_result` text parity; binary parameters are keyed by ParameterDescription OIDs with per-parameter formats and int2/int4 overflow rejection (`TestBinaryValueCodecs`, `TestPreparedStatementRoundTrip`, `TestPreparedStatementBinaryResultsDisabled`, `TestBinaryParameters`, `TestBinaryParameterOverflow`) |
| T-011 | Server fields/redaction, execution uncertainty and no unsafe ErrBadConn replay | F-901, F-902, F-903 | T-008 | todo | — |
| T-012 | testpeer auth/query/result/COPY plus frame/EOF/trailing-error/async/failure injection | F-1102 | T-001 | done | bounded PG-wire framing and boundary tests; scripted peer serves cleartext/MD5/SCRAM/SM3 auth, simple/extended results, async notice/parameter/notification injection, error/trailing-error injection and chunked-write fragmentation. COPY frames and reset/cancel races land with T-043/T-014 |
| T-013 | Pinger/Validator, poisoned-connection retirement without retry | F-116, F-903 | T-008, T-011 | todo | — |
| T-014 | Checkout reset, pinned-session preservation, budgets/handle invalidation | F-115 | T-008, T-013 | todo | — |
| T-015 | UTF-8 startup/status and explicit unsupported encoding | F-117 | T-005 | done | Requests `client_encoding=UTF8`, validates the reported ParameterStatus and fails on others: `TestConnectRejectsNonUTF8Encoding` on 2026-11-24 |
| T-016 | OS-specific TCP keepalive and unsupported-platform cases (P2) | F-105 | T-004 | done | `keepalives` (default on) and `keepalives_idle` seconds map onto `net.Dialer.KeepAlive`, with -1 disabling the probe; `keepalives_interval`/`keepalives_count` fail with an explicit unsupported-parameter error (`TestConfigKeepAlives`, `TestConnectWithKeepAlivesDisabled`) |
| T-017 | Opt-in multiple hosts/target attributes, no protocol fallback (P2) | F-106 | T-004 | todo | — |
| T-018 | Allowlisted runtime/application/lexical settings and safe context changes | F-113, F-104, F-408 | T-008, T-015 | wip | done: `standard_conforming_strings` is verified as `on` during startup and a mid-session change to any other value retires the connection instead of continuing under unverified lexical rules (`TestConnectRejectsLegacyStringLiterals`, `TestConnectAcceptsStandardStringLiterals`, `TestLexicalSettingChangeRetiresConnection`); `application_name`/`fallback_application_name` and exact `options`/`search_path` startup values are forwarded as parameters, never concatenated into SQL. Remaining: the T-033 lexer's verified quote/backslash/comment rules and runtime context updates |
| T-019 | Configured-protocol mismatch and bounded sanitized diagnostics | F-221, F-240 | T-004, T-012 | done | Handshake failures name the configured transport and never echo peer payload bytes; untrusted startup values are control-stripped and capped at 64 runes (`TestWrongProtocolPeerDiagnosticsBounded`, `TestNonUTF8EncodingDiagnosticIsBounded`); `wireProtocol` accepts pg only, rejects mysql with an explicit error, defaults to pg and never auto-detects |

### M1 catalog layer

| ID | Deliverable | Capabilities | Depends on | Status | Evidence |
|---|---|---|---|---|---|
| T-020 | Required qualified catalog SQL and classified alternate-query failures | F-215, F-217 | T-008 | wip | done: qualified `pg_catalog.pg_type`/`pg_namespace` query loads OID, schema, name, typtype, typlen, typbasetype, typelem, typrelid and typdelim before user SQL, bounded to 100000 entries, with domain resolution (`TestCatalogQueryIsQualified`, `TestCatalogDomainResolution`) and classified failure preserving SQLSTATE (`TestCatalogFailureIsClassified`). Remaining: verified sys-schema alternate query and array/composite links (F-217) |
| T-021 | OID/namespace/shape registry; same-name ambiguity and malicious user types | F-214, F-216 | T-020 | todo | — |
| T-022 | Separate SDK constants, synthetic fixtures and genuine captured catalog provenance | F-214 | T-021 | todo | — |
| T-023 | Startup/query Auto discovery, explicit fallback/conflicts and labelled mode source | F-201, F-202, F-207 | T-005 | todo | — |
| T-024 | Verified required qualified shape checks; no catalog-mode inference | F-220 | T-021, T-023 | todo | — |
| T-025 | Unknown OID/codec text-binary fallback and truthful metadata | F-212 | T-021 | todo | — |
| T-026 | Bounded product/kernel identity capture and documented metadata accessor | F-1001, F-103 | T-008, T-023 | todo | — |
| T-027 | Stale catalog after DDL, safe opaque fallback and physical-reopen boundary | F-218 | T-021, T-025 | todo | — |

### M2 mode behaviour

| ID | Deliverable | Capabilities | Depends on | Status | Evidence |
|---|---|---|---|---|---|
| T-030 | Per-mode semantic profile separated from wire/identity/lexical context | F-303 | T-001 | todo | — |
| T-031 | Mode codecs, bool/nil BIT, standard values and timezone policy | F-302, F-303, F-306, F-309 | T-010, T-021, T-030 | todo | — |
| T-032 | Array Scanner/Valuer, NULL/dimensions/lower bounds, domains and opaque composites | F-304 | T-021 | todo | — |
| T-033 | Mode/session-aware placeholder lexer plus index/name expansion plan | F-402, F-408 | T-018, T-030 | todo | — |
| T-034 | Qualified parameter target binding; no unconditional bytea/BIT OID | F-405 | T-021, T-033 | todo | — |
| T-035 | Logical NumInput/NamedValue/Valuer/NULL/wrapper and uint64 conversion | F-403, F-404 | T-034 | wip | done: server-reported NumInput, positional NamedValue binding, `CheckNamedValue` delegates to the standard converter so Valuers resolve once, nil stays nil and uint64 above MaxInt64 fails before send, named parameters are rejected explicitly (`TestCheckNamedValue`). Remaining: wrapper target metadata and the placeholder-lexer validation (T-033) |
| T-036 | Parsed SQLServer routine identifiers and profile-gated convenience rewrite | F-503 | T-033, T-030 | todo | — |
| T-037 | Per-form OUT/INOUT/status assignment only after completion | F-501, F-502, F-504, F-505 | T-035, T-036, T-040 | todo | — |
| T-038 | Safe generated-column INSERT subset, pre-send rejection, unavailable/overflow IDs | F-601–F-604 | T-008, T-021, T-033 | todo | — |
| T-039 | Actual uppercase type names, nullable unknown, length/precision and real ScanType | F-307, F-308 | T-021, T-031 | todo | — |
| T-040 | Multiple results, early Close/drain and trailing server errors | F-406 | T-009, T-011 | todo | — |
| T-041 | PG cancel-channel EOF/original synchronization and late-cancel races | F-407 | T-008, T-011, T-012 | done | Cancellation dials the independent cancel channel, sends the 16-byte CancelRequest with the server's backend key, observes the channel close, and shortens a later operation deadline to the cancel window so the original stream reports the terminal state in bounded time; the connection is always retired afterwards so a late cancel cannot abort the next command, and an undeliverable cancel interrupts the read immediately. `TestCancelRequestAbortsAndSynchronizes` pins the key, the 57014 server error and the retirement, `TestCancelShortensLateDeadline` pins the window against a server that ignores the cancel, and `TestQueryCancelOnlyContextInterruptsRead`/`TestQueryContextDeadlineRetires` cover bounded interruption |
| T-042 | Default/supported/unsupported isolation, ReadOnly and transaction cleanup | F-701 | T-008 | done | `Begin`/`BeginTx` map LevelDefault plus the four standard levels and READ ONLY to BEGIN clauses, reject other levels before sending, refuse a second BEGIN, and require idle state after COMMIT/ROLLBACK; `TestBeginCommit`, `TestBeginRollback`, `TestBeginTxOptions`, `TestBeginTxUnsupportedIsolation`, `TestBeginWhileActiveRejected` |
| T-043 | CopyIn/CopyInSchema SQL helpers, Tx Exec/flush/completion/Close semantics | F-702 | T-008, T-040 | done | `CopyIn`/`CopyInSchema` return quoted `COPY ... FROM STDIN` statements (no column list when empty) consumed through `Tx.Prepare`; rows stream as COPY text (NULL marker, backslash/newline/CR/tab escaping, 64 KiB CopyData flushes, oversized rows aborted), zero-value Exec or Close sends CopyDone and waits for CommandComplete/ReadyForQuery, an unencodable value aborts with CopyFail so nothing is imported, a command issued during the stream aborts it before running, binary COPY and COPY TO fail explicitly, and the completion read records state that the owning goroutine applies (tx status, deadlines, retirement). `TestCopyInStatementHelpers`, `TestCopyInRoundTrip`, `TestCopyInRequiresTransaction`, `TestCopyInRejectsCopyOut`, `TestCopyInRejectsBinaryFormat`, `TestCopyInServerErrorRetires`, `TestCopyInMidStreamServerError`, `TestCopyInRejectsIncompleteCompletion`, `TestCopyInFlushesLargeBatches`, `TestCopyInCommandAbortsStream`, `TestCopyInRowContextTimesOut`, `TestCopyInViaDatabaseSQL`, `TestCopyInRollbackAfterBadArgument`, `TestCopyInEncodingErrorAbortsStream` |
| T-044 | Async parser/listener queue limits/gaps and non-reentrant notice callbacks (P2) | F-703, F-704 | T-008, T-040 | todo | — |
| T-045 | Connector mode pinning and concurrent per-connection state isolation | F-208, F-209 | T-023, T-030 | todo | — |
| T-046 | Numeric/temporal/locale/unsigned boundary and standard Scan contracts | F-310, F-306 | T-031, T-035 | todo | — |
| T-047 | Refcursor pinned connection/transaction, explicit fetch/close and cancellation | F-506 | T-037, T-041, T-042 | todo | — |
| T-048 | Explicit sequence SQL/ownership and separation from LastInsertId | F-605 | T-038, T-042 | todo | — |

### M4 release quality

| ID | Deliverable | Capabilities | Depends on | Status | Evidence |
|---|---|---|---|---|---|
| T-050 | Multi-mode concurrency and -race on supported platform/toolchain | F-1104 | T-045 | todo | — |
| T-051 | Existing repository lint rules against the product module | F-1106 | T-001 | todo | — |
| T-052 | Real single-row/COPY performance baseline (P2) | F-1105 | T-043 | todo | — |
| T-053 | Compiling API migration examples, scope/evidence and cross-doc consistency | F-1002 | T-001, T-031, T-035, T-043 | todo | — |
| T-054 | Named unit/contract suite and meaningful coverage threshold | F-1101 | T-012, T-046 | todo | — |
| T-055 | Scoped V9 live-profile manifest, commands/artifacts and feature-pair acceptance | F-1103 | T-026, T-046 | todo | — |
| T-056 | Future binary COPY import complete/abort/ownership contract (P2) | F-705 | T-043 | todo | — |
| T-057 | Future COPY OUT text/binary readers and ownership contract (P2) | F-706 | T-043 | todo | — |
| T-058 | Future raw COPY stream limits/ownership, no backup promise (P2) | F-707 | T-043 | todo | — |
| T-059 | Future GSS/SSPI/channel-binding extension profiles (P2) | F-806 | T-006 | todo | — |

### M5 MySQL transport (conditional)

| ID | Deliverable | Capabilities | Depends on | Status | Evidence |
|---|---|---|---|---|---|
| T-060 | Explicit MySQL configuration/backend availability; no peek/reconnect/memo | F-240 | T-019 | todo | — |
| T-061 | Handshake/capabilities plus continuation/sequence/size/EOF framing | F-241, F-248 | T-060, T-012 | todo | — |
| T-062 | Native/plugin-switch/MoreData authentication, negative states and no downgrade | F-242 | T-061, T-063 | todo | — |
| T-063 | Independent SSLRequest/TLS/trust/material acceptance | F-243 | T-061 | todo | — |
| T-064 | Native query/prepare/execute/close and expanded argument plan | F-244 | T-062, T-033, T-035 | todo | — |
| T-065 | Text/binary results, NULL flags, errors/warnings/all completion statuses | F-245 | T-064 | todo | — |
| T-066 | Native field metadata, exact scalar/temporal/unsigned conversion | F-246 | T-065, T-046 | todo | — |
| T-067 | Native identity/mode/encoding bootstrap, no PG SQL assumption | F-247, F-1001 | T-062 | todo | — |
| T-068 | Native reset/read-only/transactions/drain/handles/cancel-abort lifecycle | F-249, F-407, F-701 | T-065, T-067 | todo | — |

## 5. Live Verification

Full procedures/blocking features are in capability matrix section 13. Evidence must include profile ID, exact edition/product release, mode, wire, role/permissions, named test/query, command, output/artifact and capture time; credentials are redacted. Feature-pair results go in matrix section 17 records, with this table referencing them. Accepted means all applicable profiles/cases passed. N/A is justified by excluded scope, never missing infrastructure.

| ID | Item | Status | Evidence |
|---|---|---|---|
| V1 | Authoritative mode discovery per scoped V9 mode/edition | pending | — |
| V2 | Qualified catalog shape and access | pending | — |
| V3 | Actual catalog fixture provenance | pending | — |
| V4 | Verified qualified built-ins | pending | — |
| V5 | Routine/OUT/refcursor matrix | pending | — |
| V6 | Generated identities and explicit sequences | pending | — |
| V7 | Every enabled PG auth method | pending | — |
| V8 | TLS trust/material/platform matrix | pending | — |
| V9 | Text COPY and asynchronous delivery | pending | — |
| V10 | YEAR/binary format conventions | pending | — |
| V11 | V8R6 profile acceptance (later scope) | pending | — |
| V12 | Actual deployed protocol listeners | pending | — |
| V13 | Legacy compatible_level meaning (later scope) | pending | — |
| V14 | Mode storage scope and immutability | pending | — |
| V15 | Native MySQL backend profile | pending | — |
| V16 | Product identity and whole initialization budget | pending | — |
| V17 | Catalog lifecycle after DDL/pool reuse | pending | — |
| V18 | Scalar/temporal/lexical boundary acceptance | pending | — |
| V19 | Session/transaction/cancel recovery | pending | — |

## 6. Decisions and Investigation Gates

| ID | Decision | State | Effect |
|---|---|---|---|
| O1 | Published module identity/oid migration | resolved | github.com/cocosip/go-kingbase from Git origin; package gokb/registration kingbase retained; oid subpackage omitted per Architecture 14 |
| O2 | Standard public scalar/temporal values | resolved | Architecture 7.1: bool/nil BIT, int64 small ints, exact text numerics/uint64/time, date/timestamp time.Time; no implicit civil/NullBool |
| O3 | Qualified catalog shape/access | evidence-pending | V2; fail if required metadata unavailable, no built-in escape hatch |
| O4 | V8 support | open, later scope | V11/V13 before adding a concrete V8 manifest; no inherited V9 acceptance |
| O5 | Reachable V9 acceptance profiles | evidence-pending | required profiles block M3 until evidence or explicit justified scope decision |
| O6 | Native MySQL release | open, conditional | V12/V15/V19 and M5; excluded from initial PG release |
| O7 | Minimum Go/platform/license/dependency/release policy | resolved | Go 1.26.0; module github.com/cocosip/go-kingbase; license Apache-2.0 with NOTICE; standard-library-only core dependency policy; platforms linux/windows/darwin on amd64/arm64; CI pinned by go.mod with golangci-lint v2.14.0; commands in README.md and `.github/workflows/ci-cd.yml` |

## 7. Required Risk Cases by Owner

| Owner | Acceptance cases that must be named in Evidence |
|---|---|
| T-004/T-026 | auth succeeds then initialization stalls; context versus DSN deadline; all partial sockets/helpers cleaned |
| T-011/T-013 | server executes INSERT then reply truncates; one execution, original uncertain error, poisoned connection not reused |
| T-014/T-042/T-068 | pinned session state retained; next lease reset; reset timeout; destroyed server handles not reused; ReadOnly/default/unsupported isolation |
| T-021/T-024 | app.int/year/bit and duplicate qualified names cannot select codecs/mode; cycles/missing shape fail safely |
| T-022/T-027 | SDK OIDs never labelled live capture; stale type after DDL uses safe fallback/reopen; no streaming catalog query |
| T-031/T-035/T-039/T-046 | bool/nil BIT standard Scan; NullBool/NULL; full uint64; decimal/temporal extremes; true nullable unknown and stable ScanType |
| T-033/T-035 | backticks/brackets/dollar/comments/settings; repeated names and SELECT $2,$1,$2 expand values [2,1,2]; one Valuer call |
| T-037/T-040/T-047 | outputs only after final completion; trailing error; multiple cursors on owning tx/session and cancel cleanup |
| T-038/T-048 | tenant_id precedes generated id; ambiguous/multirow/existing RETURNING forms rejected before execution; ID overflow, no sequence guess |
| T-041 | late cancel cannot cancel next command; observe cancel EOF plus original terminal state; failure retires |
| T-043/T-044 | COPY flush/Close/trailing errors; async notification between rows, bounded overflow/gap and callback reentrancy |
| T-006/T-007/T-062 | malformed nonce/iterations/signature/plugin/auth-switch; bounded crypto/message work and no downgrade |
| T-004/T-063 | require with CA checks chain; missing CA; readable public cert versus unsafe private key; hostname and native TLS separately |
| T-061/T-065/T-068 | split headers, 0xffffff continuation/zero terminator, sequence wrap/oversize/EOF; all multi-results/status before reuse |

## 8. Scope Exclusions and Deferrals

Initial PG release excludes V8 (O4/V11/V13), native MySQL (O6/M5), binary COPY/import-export/raw work (T-056/T-057/T-058) and extra auth (T-059). These remain todo backlog requirements, not false done/accepted rows. Other P2 tasks T-003/T-016/T-017/T-044/T-052 can be formally deferred with reason and revisit condition before release. Do not defer an in-scope P0/P1 proof to close M3.

| Formal deferral ID | Reason | Revisit condition |
|---|---|---|
| — | No additional formal task deferrals recorded | — |

## 9. Log

| Date | Change |
|---|---|
| 2026-11-24 | Original draft contained 49 todo tasks, 15 pending verification items and 6 open decisions |
| 2026-11-24 | Aligned interface contracts and requirement ownership: 94 features, 65 todo tasks, 19 pending V-items, O2 resolved/O7 added; separated implementation/offline/live states and scoped release gates |
| 2026-11-24 | Confirmed Go 1.26.0 and module github.com/cocosip/go-kingbase from Git origin; resolved O1 and the Go-version part of O7; implementation tasks remain todo |
| 2026-11-24 | T-001 done: scaffold committed; resolved O7 (Apache-2.0, linux/windows/darwin amd64+arm64, stdlib-only core, pinned CI/linter); build/vet/test/lint pass offline; no driver behaviour implemented yet |
| 2026-11-24 | T-002 done: URL/libpq DSN parsing with normalization, aliases, last-wins duplicates, percent decoding, explicit empties, unknown/P2 parameter rejection and error redaction; table tests and lint pass |
| 2026-11-24 | Connection core landed: bounded PG-wire framing, scripted testpeer, Connector/Driver with whole-connect budget and TLS trust policy, startup with cleartext/MD5 auth and server error mapping. T-005/T-015 done, T-004/T-012 wip; no live acceptance |
| 2026-11-24 | T-004/T-008 done: TLS trust matrix and POSIX key permissions tested, cancellation and budget classification verified, unix-socket dial tested; simple/extended query plus Exec/Rows with drain and retire semantics landed; T-012 remains wip for SCRAM/SM3/COPY/async injection |
| 2026-11-24 | T-006/T-007 done: SCRAM-SHA-256 (RFC 7677 vector plus bounded negative vectors) and evidenced SM3 password authentication land in internal/auth/internal/sm3, replayed against a scripted SCRAM/SM3 peer; T-012 completes with async/trailing-error/failure injection and the query layer now interrupts cancel-only contexts and preserves server errors across failed drains |
| 2026-11-24 | T-009 done and T-020 wip: connect-time qualified catalog capture with bounded entries and domain resolution, classified catalog failures, and text codecs for core scalar/date-time/uuid/json types proven end to end; binary parity and sys-schema alternate queries remain |
| 2026-11-24 | T-011/T-013 done: execution uncertainty is explicit (*gokb.UncertainError) and never surfaces as driver.ErrBadConn after a send, definite server errors stay definite, and Ping/IsValid cover liveness without duplicate execution |
| 2026-11-24 | T-042 done: BeginTx maps default plus four standard isolation levels and READ ONLY, rejects snapshot/write-committed/linearizable before sending, and commits/rolls back through the simple protocol with an idle-state check |
| 2026-11-24 | T-014 wip: SessionResetter rolls back open/failed transactions within the caller budget and retires instead of reusing an unclean connection; prepared-handle invalidation and pinned GUC reset remain dependent on later tasks |
| 2026-11-24 | Prepared statements landed: Parse/Describe/Sync at prepare time with server-reported NumInput, Bind/Describe/Execute/Sync reuse without re-parsing, argument count validated before send, Close drops the server statement, and prepare errors keep the connection reusable |
| 2026-11-24 | T-040 done: multi-statement responses expose each set through RowsNextResultSet, per-set decoders reset, unread sets drain on next-set and Close, and trailing errors surface before success |
| 2026-11-24 | T-010 wip: binary result codecs landed with raw-byte fallback for unverified layouts, prepared statements request binary results by default, and disable_prepared_binary_result restores text; binary parameter encoding remains open |
| 2026-11-24 | T-010 done: prepared-statement binary parameters are encoded from ParameterDescription OIDs with per-parameter formats, int2/int4 overflow is rejected before sending, unverified targets stay text, and CI moved to .github/workflows/ci-cd.yml |
| 2026-11-24 | CI/CD workflow renamed to .github/workflows/ci-cd.yml and aligned with go-oceanbase conventions: concurrency groups, GOTOOLCHAIN local, coverage artifact, gofmt/tidy/actionlint gates |
| 2026-11-24 | T-014 done: prepared statements survive checkout reset and stay reusable, completing F-115's reset/handle contract |
| 2026-11-24 | T-019 done: handshake diagnostics name the configured transport, cap and control-strip untrusted startup values, never echo peer payload bytes, and the driver never auto-detects or falls back across protocols |
| 2026-11-24 | T-039 wip: real column metadata landed (honest nullable unknown, catalog/typmod lengths, numeric precision/scale, real ScanType); uppercase backend type names stay with the lexical-mode tasks |
| 2026-11-24 | T-016 done: TCP keepalive is configurable (on by default, idle seconds, -1 disables) and keepalives_interval/count fail with an explicit unsupported-parameter error |
| 2026-11-24 | T-035 wip: CheckNamedValue routes arguments through the standard converter (Valuer once, nil, uint64 range) and rejects named parameters before sending |
| 2026-11-24 | F-104 offline evidence added: application_name and fallback_application_name are forwarded verbatim in the startup packet (TestConnectStartupParameters, TestConnectFallbackApplicationName) |
| 2026-11-24 | T-041 done: the PG cancel channel is delivered on an independent connection with the backend key, a later operation deadline is shortened to the cancel window, the original stream keeps its protocol state until the server reports the cancellation, and the connection is retired so late cancels cannot reach the next command (prepared-statement bind failures now retire too); testpeer recognizes cancel connections without exposing them to scripts |
| 2026-11-24 | T-018 wip: sessions using non-standard-conforming string literals are rejected at connect, a mid-session change retires the connection, and application/runtime values stay exact startup parameters; the lexer-owned quote/backslash/comment rules remain with T-033 |
| 2026-11-24 | T-043 done: CopyIn/CopyInSchema statement helpers plus a streaming COPY FROM STDIN statement (row Exec, 64 KiB CopyData flushes, CopyDone completion with observed CommandComplete/ReadyForQuery, CopyFail on encode errors, explicit binary-COPY/COPY-TO rejection, transaction-only guard); review hardening moved connection state application to the owning goroutine, bounded row contexts and stalled completions, made an interleaved command abort the stream, and added database/sql-level coverage |
