# KingbaseES Go Driver — Development Plan and Progress

> Status: implementation in progress (T-001 scaffold done); no live acceptance
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
| M0 skeleton | 19 | 1 | 0 | 18 | 0 | 0 |
| M1 catalog layer | 8 | 0 | 0 | 8 | 0 | 0 |
| M2 mode behaviour | 19 | 0 | 0 | 19 | 0 | 0 |
| M4 release quality | 10 | 0 | 0 | 10 | 0 | 0 |
| M5 MySQL transport (conditional) | 9 | 0 | 0 | 9 | 0 | 0 |
| **Total** | **65** | **1** | **0** | **64** | **0** | **0** |

Verification V1–V19: 0 accepted, 19 pending. Decisions O1–O7: 3 resolved (O1, O2, O7), 4 open/evidence-pending (O3, O4, O5, O6). Features: 94 total; 2 Impl=done/Offline=done (F-1106, F-1107), 92 Impl=todo/Offline=pending; 69 Live=pending and 25 structurally offline-only Live=n/a (recount when requirements change). No accepted live-profile overrides exist. M3 is a verification gate, not an omitted implementation-task group.

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

The specification is ready to serve as a development baseline. The confirmed Go version is **1.26.0** and module path is **github.com/cocosip/go-kingbase**, derived from Git origin git@github.com:cocosip/go-kingbase.git. T-001 created go.mod with that module path and go 1.26.0. O1 and O7 are resolved: supported platforms are linux/windows/darwin on amd64 and arm64, the license is Apache-2.0 (LICENSE/NOTICE committed), the dependency policy is standard-library-only for the core driver, and CI pins Go by `go.mod` with golangci-lint v2.14.0. Reproducible build/test/lint commands are documented in README.md and mirrored in `.github/workflows/ci.yml`.

After T-001, start T-002 (DSN/configuration) and T-012 (testpeer); then follow the declared dependencies through T-004/T-005 and the query/core-value tasks. Each protocol/parser implementation must choose and document concrete message/allocation/recursion/crypto-work limits with boundary tests before that task is done. Move a task to wip when implementation starts and record named offline proof before done; leave unrelated backlog and live acceptance pending.

Arrange the requested V9 profiles and ordinary-role catalog/mode captures early alongside offline work, recording O3/O5 evidence when available. Missing live infrastructure does not prevent the offline skeleton, but continues to block the applicable M3 support claims. Configure local credentials through ignored environment/config files or local secret/certificate directories; commit only redacted templates and intentional fixtures.

## 4. Backlog and Requirement Owners

Every capability ID is owned by at least one canonical task below. Ranges expand only over declared feature IDs. Every task remains todo; test names/evidence are added when code exists, not invented by this documentation repair.

### M0 skeleton

| ID | Deliverable | Capabilities | Depends on | Status | Evidence |
|---|---|---|---|---|---|
| T-001 | Scaffold github.com/cocosip/go-kingbase with Go 1.26.0; complete platform/license/dependency/CI contract | F-1106, F-1107 | O1 (resolved), O7 (resolved) | done | `go build ./...`, `go vet ./...`, `go test ./...`, `golangci-lint run ./...` (0 issues) on 2026-11-24; go.mod, LICENSE/NOTICE (Apache-2.0), README contract, `.github/workflows/ci.yml`, `.golangci.yml` unchanged; no product behaviour yet |
| T-002 | DSN parsing, allowlists, normalization, aliases, explicit empties | F-101, F-102, F-108–F-112, F-114 | T-001 | todo | — |
| T-003 | Deterministic environment fallback/conflicts (P2) | F-107 | T-002 | todo | — |
| T-004 | Connector/Dialer, full initialization budget, sockets and TLS policy | F-103, F-104, F-804, F-805 | T-002 | todo | — |
| T-005 | PG startup, cleartext/MD5, startup parameters and synchronization | F-801 | T-004, T-012 | todo | — |
| T-006 | Bounded SCRAM handshake/signature/nonce/iteration negative vectors | F-802 | T-005 | todo | — |
| T-007 | Evidenced SM3 authentication and unsupported variants | F-803 | T-005 | todo | — |
| T-008 | Simple/extended query, send-stage and protocol state tracking | F-401 | T-005 | todo | — |
| T-009 | Core text codecs normalized to supported driver values | F-301 | T-008 | todo | — |
| T-010 | Verified binary codecs/parameters and text-result option parity | F-305 | T-009 | todo | — |
| T-011 | Server fields/redaction, execution uncertainty and no unsafe ErrBadConn replay | F-901, F-902, F-903 | T-008 | todo | — |
| T-012 | testpeer auth/query/result/COPY plus frame/EOF/trailing-error/async/failure injection | F-1102 | T-001 | todo | — |
| T-013 | Pinger/Validator, poisoned-connection retirement without retry | F-116, F-903 | T-008, T-011 | todo | — |
| T-014 | Checkout reset, pinned-session preservation, budgets/handle invalidation | F-115 | T-008, T-013 | todo | — |
| T-015 | UTF-8 startup/status and explicit unsupported encoding | F-117 | T-005 | todo | — |
| T-016 | OS-specific TCP keepalive and unsupported-platform cases (P2) | F-105 | T-004 | todo | — |
| T-017 | Opt-in multiple hosts/target attributes, no protocol fallback (P2) | F-106 | T-004 | todo | — |
| T-018 | Allowlisted runtime/application/lexical settings and safe context changes | F-113, F-104, F-408 | T-008, T-015 | todo | — |
| T-019 | Configured-protocol mismatch and bounded sanitized diagnostics | F-221, F-240 | T-004, T-012 | todo | — |

### M1 catalog layer

| ID | Deliverable | Capabilities | Depends on | Status | Evidence |
|---|---|---|---|---|---|
| T-020 | Required qualified catalog SQL and classified alternate-query failures | F-215, F-217 | T-008 | todo | — |
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
| T-035 | Logical NumInput/NamedValue/Valuer/NULL/wrapper and uint64 conversion | F-403, F-404 | T-034 | todo | — |
| T-036 | Parsed SQLServer routine identifiers and profile-gated convenience rewrite | F-503 | T-033, T-030 | todo | — |
| T-037 | Per-form OUT/INOUT/status assignment only after completion | F-501, F-502, F-504, F-505 | T-035, T-036, T-040 | todo | — |
| T-038 | Safe generated-column INSERT subset, pre-send rejection, unavailable/overflow IDs | F-601–F-604 | T-008, T-021, T-033 | todo | — |
| T-039 | Actual uppercase type names, nullable unknown, length/precision and real ScanType | F-307, F-308 | T-021, T-031 | todo | — |
| T-040 | Multiple results, early Close/drain and trailing server errors | F-406 | T-009, T-011 | todo | — |
| T-041 | PG cancel-channel EOF/original synchronization and late-cancel races | F-407 | T-008, T-011, T-012 | todo | — |
| T-042 | Default/supported/unsupported isolation, ReadOnly and transaction cleanup | F-701 | T-008 | todo | — |
| T-043 | CopyIn/CopyInSchema SQL helpers, Tx Exec/flush/completion/Close semantics | F-702 | T-008, T-040 | todo | — |
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
| O7 | Minimum Go/platform/license/dependency/release policy | resolved | Go 1.26.0; module github.com/cocosip/go-kingbase; license Apache-2.0 with NOTICE; standard-library-only core dependency policy; platforms linux/windows/darwin on amd64/arm64; CI pinned by go.mod with golangci-lint v2.14.0; commands in README.md and `.github/workflows/ci.yml` |

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
