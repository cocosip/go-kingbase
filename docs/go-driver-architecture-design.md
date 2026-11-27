# KingbaseES Go Driver — Architecture Design

> Status: corrected requirements and interface contract; the initial PG-wire scope is implemented, M0–M4 are complete and one operator four-instance profile is **accepted (scope-limited)**; its exact build and driver revision live in the acceptance artifacts, not in this contract
> Date: 2026-11-24 (status line refreshed 2026-11-26)
> Companion documents: [Usage and Configuration](go-driver-usage.md) · [Capability Matrix](go-driver-capabilities.md) · [Development Plan and Progress](go-driver-development.md)
> Premise: an independent implementation, with no vendor implementation copied. Source inspection has informed this specification, so it is not evidence of a formal clean-room process. Vendor source and generated OID lists are research evidence, not live-server acceptance.
> Implementation: the scaffold, connection/query/catalog/mode layers and the M4 quality items are committed and pass the offline suite ([progress tracker](go-driver-development.md)); the profile acceptance with its scope limits is recorded in the [live acceptance record](go-driver-development.md#94-accepted-profile-operator-four-mode-run-2026-11-26), and live TLS acceptance is deferred by operator decision (O8).

## 1. Goals and Confirmed Requirements

1. One driver supports both the **all-mode edition** and the **single-mode editions** (Oracle / MySQL / SQL Server compatibility products).
2. The connection string may declare **mode** (which compatibility mode); the driver validates it against the server and selects behaviour accordingly. No version is declared: the type catalog is read from the server at connect.
3. Unified internals: mode profiles handle semantic differences; server identity, catalog shape and verified capabilities handle release differences without a user-selected version table.
4. Concurrent connections to different modes inside one process do not affect each other.

The resulting core model:

> **Mode selects semantics; protocol selects the handshake; verified identity and catalog select supported capabilities.** V9 PG-wire profiles are the first-release target. V8 and MySQL wire require separate acceptance before any support claim.

## 2. Difference Model (the factual basis of this design)

### 2.1 Two dimensions

| Dimension | What it decides | Scale of difference | Evidence |
|---|---|---|---|
| mode | decoding, placeholders, parameter OIDs, stored-procedure conventions, auto-increment semantics | 25 branch sites (R001/R002/R003 identical, R004 has 18) | site-by-site comparison of the four snapshots |
| release | catalog content: which types exist, which OID they have | differences inside one release + between releases (see 2.3/2.4) | value-by-value comparison of `oid/*/types.go` |

Key conclusion: **the behaviour code of the four snapshots is almost identical; the differences are concentrated in catalog data.** R001 and R003 differ by 7 lines; R002 is an obfuscated copy; R004 is another branch of the same code (four features removed, different catalog tables). Therefore, in the rewrite:

- share behaviour per mode where evidence permits; do not infer that every release has identical protocol, authentication, catalog or routine behaviour;
- the type catalog is pulled from the server at connect; there is no version or catalog parameter to get wrong;
- features missing from the sampled R004 driver are SDK branch differences; their presence or absence on a server remains a separate capability question.

### 2.2 Mode difference inventory (25 sites, grouped by domain)

| Domain | Difference | pg | oracle | mysql | sqlserver |
|---|---|---|---|---|---|
| Type selection | catalog table | reuses oracle table | baseline table | oracle table + override set | standalone table |
| Decoding | `tinyint` | — | — | `int8` | `uint8` |
| Decoding | `bit`/`varbit` | `[]byte` | `[]byte` | `[]byte` | `sql.NullBool` |
| Decoding | `date`/`time` | `time.Time` | `time.Time` | `time.Time` | `civil.Date`/`civil.Time` |
| Decoding | `datetime` | `time.Time` | `time.Time` | `time.Time` | `time.Time` (not civil) |
| Decoding | `year` | — | — | text falls through to `[]byte`, while `ScanType` claims `time.Time` (vendor inconsistency, see 2.5) | — |
| Decoding | `longtext`/`mediumtext`/`tinytext`/`longblob`/`mediumblob`/`tinyblob`/`json` | — | — | present | — |
| Decoding | `money` | decimal | decimal | decimal | decimal (different OID) |
| Metadata | `ColumnTypeLength` | generic | generic | MySQL semantics (int=10, bigint=19, …) | generic |
| Binding | `byte` parameter OID | unset (server infers) | unset | unset | `tinyint` |
| Binding | `[]byte` parameter OID | unset | unset | unset | `binary` |
| Binding | named parameter value conversion | regular | regular | regular | `DateTime1`/`VarChar`/`NullString` |
| Statement | `NumInput` | count | count | count | `-1` |
| Placeholders | accepted styles | `$n`/`?`/`@name`/`:name` | same | same | same |
| Stored procedure | fixed return value | none | none | none | yes (`NumInput=-1`, decided by mode) |
| Stored procedure | procedure-name rewriting | none | none | none | yes (call sites check `databaseMode`, see §8) |
| Stored procedure | OUT parameter decoding | per server | per server | per server | special `tinyint`/`bit` branches |
| Auto-increment | `LastInsertId` | opt-in: append `RETURNING *` | same | same | same |
| Auto-increment | `tinyint` last-ID conversion | `int8` | `int8` | `int8` | `uint8` |

> This table records vendor behaviour, including defects; it is not the new public return-value contract. The new `database/sql` value boundary is §7.1. The 25 sampled sites are `conn.go` 14, `conn_go18.go` 4, `encode.go` 4 and `rows.go` 3; these counts do not establish server support.

### 2.3 Catalog differences within one release (R003/C018 as baseline)

| Comparison | Common | Left only | Right only | OID differs | Notes |
|---|---|---|---|---|---|
| oracle vs mysql | 216 | 0 | 14 | 7 | oracle ⊂ mysql; mysql adds `longtext/_longtext/mediumtext/_mediumtext/tinytext/_tinytext/longblob/_longblob/mediumblob/_mediumblob/tinyblob/_tinyblob/year/_year`; `time/timestamp/bit/_bit/datetime/date/_date` have different OIDs |
| oracle vs sqlserver | 197 | 19 | 120 | 18 | neither contains the other; sqlserver has its own integer/float/binary/time family (`tinyint=5026`, `binary=5022`, `datetime=7881`, `money=5069`, …) |

oracle has no type that mysql lacks; pg mode reuses the oracle table.

### 2.4 Catalog differences between releases (R003/C018 → R004/C019)

| Mode | Added | Removed | OID changed | Notes |
|---|---|---|---|---|
| mysql | 12 | 16 | 2 | drops the `longtext` family and `nchar/nvarchar`; adds `mysql_date/mysql_time/varray/nestedtable/associative_array/pg_privilege/pg_audit_*`; `year` 7025→12642, `_year` 12561→12641 |
| oracle | 12 | 4 | 0 | drops `nchar/nvarchar`; adds the same 12 entries (including `varray` and other Oracle types, so the R004 oracle table absorbed more types) |
| sqlserver | 466 | 0 | 0 | pure addition, mostly `sys_*`, `kdb_*` and `pg_toast_*` catalog pseudo-types; the R004 sqlserver table grows 317→783 |

Inside the sampled R004 SDK the mysql and oracle tables have 224 common entries, with only `year/_year` mysql-only. This describes generated SDK data; it does not prove how a live server registers types or whether a type is exclusive to a mode.

**Direct consequence for the design**: never assume "a type exists in every version", and never assume "a type name has the same OID in every mode". The catalog must be read per **(database, mode)** from the server and must tolerate absence — which is exactly what the connect-time snapshot does (§5.1).

The 466 extra names break down as `sys_*` 183, other `pg_*` 157, `pg_toast_*` 41, arrays 25, `kdb_*` 7 and ordinary names 53. Their names suggest generated catalog/user row types, but the tables do not include `typtype` or namespace identity. Their actual shapes and provenance require a server capture; they cannot certify composites or product-line features.

### 2.5 Gaps in the vendor implementation (do not replicate)

A site-by-site code review found the following inconsistencies or defects. These requirements avoid reproducing them; the M0–M2 slices implement the choices below with named offline proof, while live acceptance remains the M3 gate:

| # | Location | Observation | Our choice |
|---|---|---|---|
| 1 | `rows.go` Type() vs `encode.go` | `tinyint` ScanType is always `int8`, but sqlserver decodes to `uint8` | enforce mode range, expose `int64` and matching ScanType |
| 2 | same | `money` ScanType is `[]byte`, decoding produces `decimal.Decimal` | lossless decimal text as `string`; caller may use a decimal Scanner |
| 3 | same | no ScanType for `int`/`smallint`/`bigint`/`uint4`/`uint8`/`nchar`/`nvarchar`/`binary`/`varbinary` (returns `interface{}`) | all covered |
| 4 | `encode.go` | no `T_year` branch, `year` falls through to `value = s` (`[]byte`) while Type() claims `time.Time` | `int64` (see §7.1) |
| 5 | `encode.go` | `binary`/`varbinary` unconditionally strip 2 bytes (assumes a `0x` prefix), also in non-sqlserver modes | decode hex only when the `0x` prefix is present |
| 6 | `conn.go` parseEnviron | `KINGBASE_GET_AUTO_INCREMENT_ID` maps to `get_auto_increment_id`, but the driver only reads `get_last_insert_id`, so the env-var alias is dead | accept both names |
| 7 | `conn.go` `getID` | defined but never called (dead code) | do not implement |
| 8 | `conn.go` OUT parameters | `bit` is handled only for sqlserver and mysql; the pg/oracle branch is empty (no assignment) | assign only for an accepted mode/routine profile; otherwise report unsupported, never silently leave a zero value |
| 9 | `conn.go` `replaceHolderMarkers` | only single-quoted text is treated as a string; `/* */`, `--`, dollar-quotes and double-quoted identifiers are not recognised, so `?`/`:` inside comments are rewritten | full state machine in §8 |
| 10 | `conn.go` `addReturning` | appends ` RETURNING *` even when the INSERT already has `RETURNING`, producing a syntax error | no `RETURNING *`; verified generated column only, with the restrictions in §8.3 |

## 3. Overall Design

```mermaid
flowchart TD
    APP[Application] --> SQL[database/sql owns pooling]
    SQL --> ADAPTER[Driver / Connector / Conn / Stmt / Rows / Tx]
    ADAPTER --> CONTEXT[Per-connection mode, identity and capabilities]
    CONTEXT --> CATALOG[Qualified server type registry]
    ADAPTER --> PG[PG v3 backend, first release]
    ADAPTER --> MYSQL[MySQL backend, conditional M5]
```

| Decision | Contract |
|---|---|
| Pooling | database/sql owns pooling; provide reset, validation and safe retirement hooks, not a second pool |
| Protocol | wireProtocol=pg by default; mysql is explicit and conditional on M5; mode never selects a handshake |
| Behaviour | share mode profiles; add release/capability rules only when server evidence requires them |
| Types | per-connection qualified catalog for PG; negotiated field codes for MySQL; no product OID constants |
| State | immutable profile/catalog per connection; no mutable process-global mode, OID or version selection |
| Publication | finish initialization within one budget and confirm idle backend state before returning a physical connection |

### 3.1 Evidence boundaries and server identity

- Inspected SDKs use PG-v3 messages. Sample PG-style values such as 12.1/120001 do not prove that every V8/V9 release has identical kernel, protocol extensions or authentication.
- Record raw server_version/server_version_num when supplied and raw product release from a bounded verified version query. SELECT version() is a candidate for V16; do not invent a product function. Preserve unparsed strings and report unknown fields. A required but unestablished identity prevents acceptance of that profile.
- Sampled official mode documentation describes instance-initialization selection. Do not assert CREATE DATABASE ... DBCOMPATIBILITY support or mutable per-database modes without release-specific evidence. Read through the intended target connection without inferring where the mode is stored. V14 checks scope without requiring unsupported DDL.
- compatibilityMode declares client semantics; it never changes the server mode. Startup/verified-query evidence is authoritative; a conflicting explicit declaration fails. If discovery is unavailable, an explicit declaration is allowed only as labelled unverified fallback, subject to identity/catalog requirements (§9).
- V8R3 compatible_level is historical evidence, not an established V9 synonym. V8 is later work; V11/V13 gate future claims.
- PG and sys catalog names are candidates verified per profile. Generated SDK lists do not establish live namespace, shape or access permissions.

### 3.2 Protocol and deployment scope

V9 PG-wire profiles are the first-release target. Listener availability is a deployment fact (V12); MySQL-compatible SQL does not prove a native MySQL listener. Port 54321 or an extension port such as 3307 is an example, never a detection rule. Do not infer universal absence of Oracle Net/TDS or universal MySQL-only packaging from Go SDKs.

The native MySQL extension is a candidate second backend (Appendix C). Actual listeners, TLS, authentication and SQL bootstrap require separate Kingbase evidence. No port scanning, greeting-based auto-selection, cross-protocol reconnect or downgrade is permitted.

## 4. Connection String

```text
kingbase://user:pass@host:54321/db?compatibilityMode=mysql&wireProtocol=pg&sslmode=require
```

compatibilityMode selects semantics; wireProtocol selects transport. There is no user-selected product-version/catalog table. Required identity and metadata come from physical initialization.

| Parameter | Values | Default | Contract |
|---|---|---|---|
| compatibilityMode | auto / pg / oracle / mysql / sqlserver | auto | aliases compatibleMode, compatible_mode, mode, databaseMode; never changes server mode |
| wireProtocol | pg / mysql | pg | no auto value; mysql fails clearly until M5 ships |
| binary_parameters | yes / no | no | binary bytes require verified target type/format; no unconditional bytea assignment |
| disable_prepared_binary_result | yes / no | no | compatibility option; text/binary must preserve values/metadata |
| get_last_insert_id | yes / no | no | alias get_auto_increment_id; restricted contract in §8.3 |
| sslmode | disable / require / verify-ca / verify-full | require | exact trust policy in §12 |
| sslcert / sslkey / sslrootcert | paths | unset | public certificate, private key, trust roots; preserve case |
| connect_timeout | non-negative seconds | 0 (unlimited) | whole physical Connect budget; earlier context deadline wins |
| application_name / fallback_application_name | string | empty | verify native-backend mechanism separately |
| client_encoding | implemented encoding | UTF-8 | initially UTF8/UTF-8 only; reject unimplemented encodings |
| options / search_path | string | unset | explicit runtime allowlist; backend-safe configuration, no value concatenation into SQL |
| keepalives / keepalives_idle / keepalives_interval / keepalives_count | yes/no, seconds | enabled, OS default | P2 tuning; an unsupported platform value fails instead of being ignored |
| host list / target_session_attrs | host or port list / any, read-write, read-only | unset, any | P2 opt-in path: no protocol or port fallback, and an unverifiable target fails closed |

### 4.1 Parameter rules

1. Auto requires authoritative server mode (§9). Explicit mode may skip a selection query but must check any startup mode; record declaration and observation separately.
2. URL and libpq key=value forms are accepted. postgres/postgresql URL schemes are aliases with default PG transport. Quoting/backslash escaping preserves spaces and explicit empty strings; missing dbname defaults to user. Empty password is not replaced by environment fallback.
3. Unknown driver keys fail. Runtime forwarding uses an allowlist, never arbitrary unknown-key pass-through. Values retain case and bytes after DSN unescaping; backend configuration uses safe binding/encoding.
4. UNIX sockets are PG-only initially. No explicitly requested TLS is silently disabled: absent sslmode uses local-socket behaviour; explicitly requesting TLS on a UNIX socket fails configuration validation.
5. KINGBASE_* fallback fills absent fields only; an explicit DSN value (including an empty password) always wins. On case-sensitive platforms aliases are collected deterministically and conflicting normalized environment names are rejected. Unknown KINGBASE_* variables are ignored, values pass the same validation as DSN values, and diagnostics name the variable.
6. The earlier of context deadline and positive connect_timeout covers DNS, TCP/socket, TLS, auth, identity/mode and required catalog/validation. Zero is unlimited only without a context deadline. Timeout closes the socket, stops helpers and never publishes partial initialization; clear temporary deadlines after successful Connect.

### 4.2 Normalization and escaping

Normalize driver keys by ASCII lowercase and underscore removal; aliases are explicit. Only enum values are case-folded. Preserve user/password/database/path/application/runtime values. URL decode exactly once before normalization; malformed escapes fail. Duplicate normalized DSN query keys use the last input occurrence; query fields override authority fields, with credentials/endpoints covered by tests.

The following are equivalent including TLS policy:

```text
kingbase://u:p@h:54321/db?compatibilityMode=MYSQL&SSLMode=VERIFY-FULL
kingbase://u:p@h:54321/db?COMPATIBILITYMODE=mysql&sslmode=verify-full
kingbase://u:p@h:54321/db?compatibleMode=MySql&sslmode=verify-full
kingbase://u:p@h:54321/db?mode=mysql&sslmode=verify-full
```

## 5. Type Catalog

### 5.1 Server capture and qualified identity

For PG obtain namespace names and required type shape before exposing a physical connection. Primary candidate SQL:

```sql
select t.oid, t.typname, t.typtype, t.typelem, t.typbasetype,
       t.typnamespace, n.nspname, t.typlen, t.typdelim, t.typrelid
  from pg_type t join pg_namespace n on n.oid = t.typnamespace;
```

An equivalent sys_type/sys_namespace query is a profile candidate, not a universal alias. V2 verifies exact objects, columns, ordinary-role access and formats. Alternate queries are tried only for classified missing relation/column cases; never hide permissions, timeouts or malformed responses through blanket fallback. All candidates share initialization size/time limits.

- OID maps to namespaceOID/name, typname, typtype, typelem, typbasetype, typlen, typdelim and typrelid; (namespaceName,typname) maps to candidate OIDs. Resolve namespace names on this server, never hardcode their OIDs.
- Codecs require a verified built-in namespace, name and compatible shape. User-created app.int/app.year/app.bit cannot select a built-in codec or set/conflict with mode. Never choose the first ambiguous name or search_path winner as a built-in identity.
- Two-column oid/typname data is diagnostic only. It cannot satisfy required namespace/domain/array shape or enable codecs. A names-only fallback may complete initialization only as a labelled partial catalog (`CatalogStatus`/`CatalogCause`) in which every codec, scan type, length/precision and qualified label stays disabled; otherwise fail initialization naming the actual object/cause and never fabricate missing fields. V2 acceptance still requires the full shape.
- Snapshot size varies; roughly 800 entries in an SDK is not a server row-count guarantee. Implementation/tests must define bounded frame, capture-byte and row budgets and fail safely on exhaustion.
- Domains and arrays follow validated base/element links. Bound recursion, detect cycles and reject inconsistent shapes. Composite decoding requires separately captured shape; otherwise preserve opaque text.

### 5.2 Evidence and lifecycle

Vendor OID lists and JDBC built-in tables are static SDK research, not live pg_type captures. Label/store them separately. Official JDBC uses built-ins plus runtime queries (Appendix B); choosing runtime resolution does not mean JDBC has no tables.

Snapshots last for a physical session. No catalog query is injected while rows stream. Unknown OIDs decode according to selected format: text string, binary []byte; absent OID gives empty DatabaseTypeName. Never guess binary layout by name; malformed framing is an error.

DDL can stale a snapshot. Initial refresh is drain/close/reopen after type-changing DDL when typed access is required; pool reuse alone does not refresh it. Explicit reload is deferred, with no mid-reader mutation. Added/dropped types, changed OIDs and pool reuse are V17 cases.

### 5.3 V8 and V9

V9 first; V8R6 later with separate profile manifest and V11 acceptance. V8R3 is research-only unless separately scoped/verified. Shared code is a goal, not proof that version-specific rules are unnecessary. Product strings provide diagnostics/capability evidence, never manually selected OID tables.

## 6. Mode Profiles

Profiles carry only offline-verified semantic knobs. The verified PG baseline currently holds identifier-quote and placeholder rules consumed by the placeholder lexer; codec fields (tinyint range, boolean BIT versus bit-string policy, routine return conventions and length semantics) are added only with verified mode evidence. Wire protocol, identity, lexical settings and catalog shape remain separate connection context.

- PG/Oracle may share codecs only where shape and accepted behaviour agree; equal SDK tables do not prove semantic equivalence.
- Connector pins configured transport and resolved/declaration mode. Conflicting authoritative mode on a later connection fails. Never remember a greeting as a transport-selection policy.
- Shared placeholder parsing uses verified mode/session lexical rules (§8); SQLServer bare-routine convenience rewriting is profile-specific.
- Do not change mode on an existing physical connection. Later verified mutable-mode support would require retirement/reopen. Track lexical/decoding settings through ParameterStatus or a synchronized verified path; otherwise reject the change or retire the session.

## 7. Type System

### 7.1 database/sql value boundary

Classic Rows.Next exposes only nil, bool, int64, float64, string, []byte and time.Time (R2). Private codec representations normalize at that boundary. No default result is sql.NullBool, a narrow integer, decimal.Decimal, civil.Date/Time or []T. A newer optional scanning interface cannot alter this baseline; the Go baseline is 1.26.0 (§13.1).

| Database value | Default non-NULL value / ScanType | Contract |
|---|---|---|
| signed integers, tinyint, uint4 | int64 | enforce database range; SQLServer tinyint 0..255, others their verified signed range |
| database uint8 / unsigned BIGINT | string | decimal digits for full uint64 range, including above MaxInt64; stable ScanType |
| SQLServer boolean BIT | bool | NULL is nil; bool/sql.NullBool destinations work; do not coerce longer bit strings |
| BIT/VARBIT bit strings | []byte | preserve bit count and leading zeros in verified format |
| numeric/decimal/money | string | exact decimal representation; no float conversion; callers can use decimal Scanner |
| real/float | float64 | enforce verified database range/precision; accept special values only where supported |
| text/nchar/nvarchar/longtext family, uuid/json/xml/inet | string | preserve verified textual form and encoding; no implicit JSON/XML parsing |
| interval | string | preserve accepted textual interval fields/precision; no implicit conversion to a fixed duration |
| date | time.Time | calendar midnight in UTC, no session-zone shift; reject invalid/zero/out-of-Go-range dates unless an accepted explicit fallback exists |
| time without date | string | preserve wall clock, fractional precision and valid negative/duration/>24h forms; no invented date |
| timestamp/datetime | time.Time | distinguish wall-clock without timezone (UTC carrier, no implicit session conversion) from an instant with timezone; datetime is not implicitly civil |
| year | int64 | only verified YYYY or YYYY-MM-DD forms; validate full syntax/range |
| binary/varbinary/bytea/blob | []byte | decode only verified actual type/format, not every 0x-prefixed string |
| arrays/composites | text []byte | explicit Scanner/Valuer helpers for typed access; preserve dimensions/lower bounds/NULLs/delimiters/quoting |
| unknown type/OID | text string / binary []byte | selected format decides; absent OID gives empty type name |

ScanType describes the default value, not the final user destination. Standard sql.Null* destinations handle nil. Typed helpers require round-trip acceptance; allocating []T does not establish general Scan compatibility.

The explicit Array helper must round-trip rectangular one-dimensional and multidimensional arrays, empty arrays, SQL NULL arrays and NULL elements while preserving dimensions and lower bounds. A destination plain slice cannot represent every lower-bound/NULL distinction; reject lossy or unsupported destination conversions rather than silently flattening or substituting zero values. Ragged arrays and excessive dimensions/recursion return errors.

### 7.2 Codec dispatch and boundaries

Resolve by OID plus qualified built-in identity/shape (§5), then profile. Unrecognized user types remain opaque; domains unwrap only validated base links. Unknown binary bytes are a fallback for valid frames, not truncated values.

F-310 requires text/binary equality and explicit errors for uint64 limits, decimal precision/scale/overflow, NaN/infinity where supported, timestamp infinity, BC/out-of-range/zero dates, fractional precision, negative/>24h times, timestamp/timestamptz, timezone/DST gaps/folds and money locale. Record accepted forms per live profile in V18. Timestamp infinities produce a typed unsupported-conversion error under the time.Time policy; callers explicitly CAST/select text to retrieve them losslessly. No process-global infinity mapping or row-dependent ScanType changes.

### 7.3 Parameter binding

| Input | Contract |
|---|---|
| nil / nil pointer / invalid sql.Null* | SQL NULL, never dereference or substitute zero |
| signed/unsigned integers | range check; uint64 above MaxInt64 uses exact decimal encoding and verified target, never int64 cast |
| bool / sql.NullBool | unwrap validity; qualified bool or explicit verified BIT target; not every NullBool is BIT |
| bytes | explicit/verified target or server inference; no unconditional bytea OID for raw/blob/varbinary |
| VarChar* / NChar / DateTime1 | preserve wrapper type until Bind; qualified target resolution |
| civil/decimal / driver.Valuer | explicit adapters return standard values or internal binder values fully handled before wire; reject unsupported types |
| Array / CursorString | preserve array shape/NULLs; cursor name does not transfer ownership or imply auto-fetch |
| sql.Out / ReturnStatus | operation metadata; assign only under verified routine form (§8.2) |

CheckNamedValue validates/converts, not unconditional acceptance. Preserve wrappers until OID selection; check exact ranges/nil/error behaviour. Evaluate a Valuer once per logical argument before duplicate-bind expansion. NumInput follows §8.1.

### 7.4 Metadata

- DatabaseTypeName is the uppercase actual catalog/backend type without length suffix, not a unified PG alias; preserve exact qualified identity privately. Unknown OID returns empty name.
- Nullable is (true,true) for known nullable, (false,true) for known nonnullable, (false,false) for unknown. Base-table NOT NULL alone does not prove outer-join/expression nullability; PG RowDescription generally leaves it unknown.
- Length/precision/scale report ok honestly and define character versus byte units/unbounded values per type/backend. MySQL field byte length is not automatically a character count; integer display width is not precision.
- ScanType follows §7.1 consistently, independent of caller Scanner. A text-preserving special-value policy must be selected before decoding, not changed only on an exceptional row.

## 8. Placeholders and Stored Procedures

### 8.1 Lexing and binding plan

Share one parser for $n, ?, @name and :name, allowing one style per statement. Return SQL plus argument-index/name plan. Mixed styles fail before sending. Cover single quotes/doubled quotes, E-strings, verified standard_conforming_strings/SQL-mode backslash rules; double quotes and ANSI_QUOTES; MySQL backticks/doubled backticks; SQLServer brackets/]]; dollar quotes; comments/nesting and verified enable_unpaired_comment; :: casts, := assignments, operators and server variables.

The meaning of ? is profile/session-specific. If an accepted operator is ambiguous with question binding, reject that style and require $n/named binds; do not infer arbitrary Kingbase operator support from PG. Lexical-setting changes must synchronize context or retire/reject; no rewritten-SQL cache across incompatible settings. enable_question_placeholder is a candidate setting to verify per backend.

NumInput counts logical inputs: $n requires positive contiguous indexes and uses the greatest index; repeated names use one argument; each positional ? uses one. SQLServer convenience calls use -1 but still validate the binding plan. Missing/unused names and duplicate supplied names fail deterministically. For MySQL, SELECT $2,$1,$2 becomes SELECT ?,?,? with [arg2,arg1,arg2]; @x+@x expands one value twice. Zero-argument SQL is preserved.

Required lexical cases include quoted a?b in backticks, [a:b], escaped closing brackets, comments with binds, repeated/reversed binds and dollar-quoted routine bodies. Unterminated/ambiguous input fails before execution.

### 8.2 Routines, outputs and cursors

Only an accepted SQLServer profile may rewrite a bare procedure identifier into EXEC. Parse qualified/quoted/bracketed components safely; never concatenate arbitrary SQL fragments. Explicit CALL/EXEC is not translated across dialects. The official source's MySQL-compatible OUT branches must be represented in V5; no blanket non-SQLServer prohibition. A concrete example's exact invocation and product applicability must be captured before claiming example parity.

- Track mode, product release, transport and invocation form separately. Consume SQLServer fixed status and discard an unbound status without failure.
- OUT/INOUT use sql.Out with checked conversion. PG/oracle/mysql forms may expose outputs as rows/binds/cursors; enable only forms established by official examples and live evidence. Unsupported form/type returns a clear error, before execution where detectable; never silent no-op assignment.
- Publish outputs only after all required results/completion messages. A trailing error invalidates output success. Query outputs are unavailable until successful drain/Close; cancellation/incomplete rows leave outputs invalid.
- tinyint OUT can target uint8/int8 only if range fits. BIT/bool/NULL/truncation have separate conversions.
- Refcursor uses the owning pinned connection and transaction; explicit safely quoted FETCH/CLOSE, multiple cursor and cancellation rules are F-506/V5 acceptance. Never fetch on another pool lease or expose a cursor after its owner ends.

### 8.3 Generated values

PG get_last_insert_id=yes applies automatic capture only to Exec/ExecContext. Query/QueryRow with explicit RETURNING is passed through and its rows belong to the caller. For automatic capture, rewrite only a parsed single-row INSERT into one resolved relation with exactly one catalog-verified integral generated/identity column. Resolve metadata before execution under the operation budget; append RETURNING <quoted generated column>. Existing RETURNING on Exec, ambiguous/missing identity and unproven CTE/multirow/conflict forms are never guessed or consumed as column one; reject that requested automatic-ID form before execution and direct callers to QueryRow(... RETURNING id).

No RETURNING *, MAX(id), last-sequence guess or cross-session follow-up. Multirow/bulk uses explicit returned rows. LastInsertId must fit int64; overflow/unavailable is a typed error, never a misleading zero. Disabled/non-INSERT paths return unavailable; RowsAffected and trailing errors remain independent. MySQL reads an accepted OK identity with the same overflow rule and no INSERT rewrite.

## 9. Connection Setup

One physical Connect budget (§4) covers every step. Select protocol from configuration before handshaking.

1. Validate configuration; connect and apply the selected backend's TLS.
2. Authenticate, collect startup parameters/backend identity and reach the synchronization point.
3. Establish raw kernel/product identity through the fixed verified candidate set (V16), then mode (§9.1).
4. PG loads qualified type metadata (§5); MySQL uses separately verified native bootstrap/field policies (Appendix C).
5. Build context, check Connector mode consistency, confirm idle transaction/protocol state, clear temporary deadlines and publish the connection.

Failure closes the physical connection. If a profile's required identity cannot be established, return an initialization/unsupported-profile error; an unparsed product string may be retained, but must not fabricate a release or activate unverified rules. A reported 12.1 alone does not establish an accepted profile. Tests include fast auth followed by stalled identity/catalog queries and failure after partial initialization.

### 9.1 Mode discovery

Prefer a validated startup database_mode. For Auto without it, try the accepted PG candidate set: pg_settings, a verified sys_settings equivalent, then SHOW database_mode. Candidate SQL:

```sql
select name, setting from pg_settings
 where name in ('database_mode','server_version','server_version_num');
```

Exact availability/permission behaviour is V1. Empty results/missing relations do not mean PG. Continue only for classified unsupported/missing-metadata cases; retain timeout, permission and malformed-response causes. Legacy compatible_level is eligible only for a separately accepted V8 profile. Conflicting server reports fail.

| Observation | Action |
|---|---|
| valid authoritative mode | resolve; modeSource=startup or query; compare explicit declaration |
| declaration conflicts with server | fail with expected/actual/source |
| mode unavailable, explicit declaration | modeSource=explicit, modeVerified=false; never fabricate actualMode; still require identity/catalog |
| mode unavailable, Auto | fail asking for explicit declaration; never infer from type names or default to PG |

Explicit configuration need not run a query solely to select its profile, but must check known server reports. Record failed-probe evidence without credentials.

### 9.2 Catalog validation is not mode detection

year/int in static SDK lists are clues, not authoritative/exclusive server markers. Only after V2/V4 establish a qualified built-in's required shape for a release may the driver enforce it. Missing marker does not prove another mode; same-named user types prove nothing. Hints never set/override mode. A verified required-type mismatch fails as a catalog/profile error naming the qualified identity; an unverified hint is diagnostic only.

### 9.3 Protocol mismatch

Configured PG sends PG startup/SSL only; configured MySQL awaits a bounded validated greeting only after that backend ships. Recognizable mismatches return a protocol/endpoint configuration error with a capped sanitized header prefix. Partial/oversized headers, EOF, banners and silence remain bounded and retain causes; silence is not positive PG evidence.

No zero-deadline peek, automatic cross-protocol reconnect, port scan or downgrade after auth/handshake failure. Fixing a mismatch requires explicit configuration. Ping uses the same backend and never changes Connector transport.

## 10. Sessions, Transactions, COPY and Notifications

### 10.1 Pool lifecycle

SessionResetter runs before reuse, not at return; Validator can reject a poisoned connection before pooling (R2). Do not promise eager return-time reset. A pinned sql.Conn preserves deliberate session state until release; never reset after every statement on that lease.

An idle returned session may remain dirty until checkout. Before the next user's command, a verified reset restores baseline transaction, role, search_path, runtime/lexical parameters, temporary objects, advisory locks and LISTEN state as applicable. V19 determines actual SQL/coverage; DISCARD ALL is not assumed universal. Disable state-creating features whose cleanup is unverified or retire their sessions. Reset obeys checkout context plus a cleanup ceiling of 5 seconds; failure/unknown state retires the session.

Local prepared handles must be invalidated/reprepared if server reset destroys them. Rows.Close/cleanup drains to ReadyForQuery (PG) or the accepted MySQL terminal state within the cleanup budget; failed recovery marks invalid without making a possibly executed write retryable (§11). Catalog refresh is separate (§5.2).

### 10.2 Transactions and cancellation

LevelDefault leaves server policy unchanged. Map the four accepted explicit isolation levels and reject others before BEGIN. Honour TxOptions.ReadOnly=true using a verified read-only transaction or return unsupported, never a writable substitute. READ UNCOMMITTED promotion is recorded per profile, not assumed for all Kingbase releases. Commit/Rollback ends the Tx; no reuse. Savepoint SQL is possible but no extra standard-library savepoint API is implied.

PG CancelRequest uses a separate backend-key channel. Observe completion/EOF within a bounded budget and synchronize the original channel through Error/ReadyForQuery before reuse. A successful write alone is not a cancel barrier. Cancel/completion races must not hit the next borrower. If either barrier is uncertain, retire the target session and return context/execution uncertainty without ErrBadConn matching after possible execution.

MySQL initially cancels by aborting/retiring the physical session. KILL QUERY is deferred until verified permissions, exact routing and race/budget behaviour in V15/V19; no unbounded control connection or wait for an exhausted ordinary pool slot. Cover open rows, transactions and cursors.

### 10.3 COPY

Initial PG text import retains official helper shape: CopyIn(table,columns...) and CopyInSchema(schema,table,columns...) return a statement string. Prepare on a pinned transaction, Exec rows, zero-argument Exec to flush/finish, observe server completion, then close. It is not a returned writer. Quote identifiers separately and encode values through COPY, not SQL concatenation.

V9 covers NULL/delimiter/newline/backslash, row/flush/Close/trailing errors, cancellation and transaction status. No success before completion. Import error does not itself roll back the caller transaction.

Export (F-706) reads `COPY ... TO STDOUT` through `QueryContext` and returns one column, `copy`, whose values are protocol chunks: concatenate them for the payload. Chunk boundaries follow messages, not rows or lines, and the payload stays opaque, so text and binary output use the same call. The driver rejects the forms it cannot honour instead of faking a stream: `Exec` (payload would be discarded), a prepared statement (the extended protocol has no streaming row set), statement arguments (COPY has no placeholders), a COPY inside a multi-statement query (it is not the first statement), and a server-side `COPY ... TO <file>` (completion without data). Closing early drains to ReadyForQuery, so an abandoned export still leaves a reusable session. KingbaseES writes its own binary header signature (`KBCOPY`) rather than PostgreSQL `PGCOPY`; the driver never inspects or rewrites the payload.

Raw import (F-707) uses the same statement shape with an explicit `FORMAT`: `CopyInRaw`/`CopyInRawBinary` (plus schema variants) make every `Exec` argument one payload chunk that the driver forwards unchanged. The caller owns row framing, escaping and the binary header/trailer, so a payload produced by export can be imported verbatim; a non-payload or multi-argument call aborts the stream with `CopyFail`. An explicit `FORMAT` is the switch, which keeps row-framed `CopyIn` unchanged and keeps its binary rejection.

Typed binary import (F-705) uses the same statement shape plus a marker: `CopyInBinary(table, columns...)` resolves each target column's storage type with the qualified `pg_catalog.pg_attribute`/`pg_type` query and encodes the Go value in that representation, because the caller cannot know how a mode stores a name (MySQL maps `real` to a double, an Oracle-mode `date` is eight bytes with a time part). The query reports the effective shape rather than the name alone: a domain over a base type borrows that base (the MySQL-mode `json` is a domain over `jsonb`) while a domain over a domain stays unsupported, and the qualified schema is part of the identity, so a user type that shares a builtin name never borrows the builtin layout. The mapping follows the measured per-mode shapes: signed Oracle/MySQL `tinyint` versus unsigned SQL Server `tinyint`, SQL Server `bit`, microsecond `time` except the SQL Server 100 ns ticks, the mode `timestamp` (an instant encoded from the caller's wall-clock fields in the session zone) versus the plain-wall-clock `datetime`, and the PG scalar family. Encoding is the vendor stream: the signature header once, per-row field count, per-field length with -1 for NULL, and the trailer before CopyDone, with header and trailer also sent for an empty import. A column whose storage type has no encoder (`numeric`, `interval`, `rowversion`, arrays, unknown schemas) fails at prepare, and a value that does not fit its storage type aborts the stream with `CopyFail`. No implied parity with .NET importer/exporter APIs.

### 10.4 Asynchronous delivery

Accept Notice/Notification/ParameterStatus at legal asynchronous points while reading command responses. PG12 describes current send timing but explicitly warns against assuming NotificationResponse can never occur within a command sequence (R4). A fake-server DataRow -> NotificationResponse -> DataRow tests robustness, not actual Kingbase emission behaviour.

A command issued while a previous result set is still open finishes that result through ReadyForQuery before it is sent, so an abandoned `Rows` cannot leave a stray terminator that desynchronizes the next command; the abandoned handle stays closed and reports EOF. The same rule already covers an open COPY stream. Listener delivery is read/pull-driven with a bounded queue (default 256). On overflow report a nonblocking gap/error and retire/reconnect that Listener; never silently grow memory or block the protocol reader forever. Callbacks cannot reenter commands on the same connection. Reconnect/re-LISTEN cannot replay lost messages; a documented nil notification marks a gap. Default NOTICE/WARNING is discarded, never stderr. Idle waiting/keepalive must not inject SQL into active results. V9 verifies actual timing, overflow/disconnect and long-stream behaviour.

## 11. Errors and Diagnostics

- Preserve server SQLSTATE, severity, message/detail/hint, positions, object/constraint and source file/line/routine fields plus accepted Kingbase extensions. MySQL preserves native number; absent PG-only fields stay absent. Expose via errors.As.
- Configuration/protocol/catalog/timeout/cancel/conversion errors wrap the real cause. Redact credentials in URL/libpq/options/callback diagnostics. Protocol samples are capped headers, never auth payloads.
- Separate connection validity from permission to retry. Track whether request bytes may have reached the server. After possible execution, return the original/execution-uncertain error without wrapping or matching driver.ErrBadConn; mark invalid so Validator prevents reuse. A truncated INSERT reply must not cause replay.
- ErrBadConn is restricted to retry-safe pre-execution or reset/validation boundaries under R2. Connect/Open return concrete initialization errors, never ErrBadConn. No command/transaction/commit replay after uncertain execution.
- F-1001 exposes configured protocol, declared/resolved/observed mode, modeSource/modeVerified, raw kernel/product strings, identity status and accepted profile through a documented Connector/sql.Conn.Raw accessor. Do not hardcode 12.1 or deliberately discard product release.

## 12. Authentication and TLS

Authentication is implemented offline for the initial PG targets: cleartext, MD5, SCRAM-SHA-256 with server-signature verification and the evidenced SM3 method (`internal/auth`, `internal/sm3`, scripted testpeer replay). Enable only implemented/verified message forms. GSS/SSPI, SCRAM-PLUS/channel binding and other extensions are deferred F-806, with explicit unsupported errors and no downgrade.

SCRAM acceptance covers server nonce extension, duplicate/malformed attributes, bounded iteration count, salt/base64 failures, invalid signature and truncation. Bound message size and cryptographic work; never log secrets. V7 accepts each enabled method per product/profile.

For PG preserve sampled official Go R003/R004 semantics: require encrypts without hostname verification, but an explicitly configured existing sslrootcert enables CA-chain verification. Explicit missing trust material is an error here, not silently removed as in legacy code. verify-ca verifies chain; verify-full also verifies hostname/IP; disable requests no TLS. Use verify-ca/full when verification is required rather than relying on require's historical behaviour (R5). Never fall back from requested TLS to plaintext.

Read configured material at physical connect. Strict sampled POSIX secret permissions apply to sslkey only, not public sslcert/root certificates; Windows handling is separate. Tests include readable public certificates, unsafe private keys, missing/invalid material, key mismatch, untrusted/expired chain and wrong hostname. §14 records strict-missing-file migration differences.

Live TLS acceptance is deferred by operator decision (O8 in the [progress tracker](go-driver-development.md)): the reference deployment offers no TLS listener, so the trust/verification policy measured by the offline suite (F-804/F-805 offline done) stays unaccepted live and no TLS support claim is published until a TLS-enabled deployment is confirmed.

MySQL TLS is independently accepted in V8/V15 with the same configured trust policy. SM3 password authentication does not establish national-crypto TLS/certificate support. Runtime TLS-version/cipher policy is verified against the Go 1.26.0 baseline.

## 13. Package Layout

```text
go-kingbase/
  doc.go, driver.go, connector.go, config.go       entry points and configuration
  conn.go, session.go, query.go, statement.go, rows.go, transaction.go,
  ping.go, cancel.go                                operation, session and recovery lifecycle
  mode.go, catalog.go, codec.go, identity.go        mode profiles, qualified catalog, mode codecs
  encode.go, decode.go, numeric.go, timebinary.go, intervalbinary.go,
  inetbinary.go, bitbinary.go, array.go, wrapper.go standard values and explicit helpers
  placeholders.go, routine.go, routine_out.go, generated.go
                                                    binding plan, routines and generated IDs
  copy.go, copy_binary.go, cursor.go                COPY IN/OUT forms and cursors
  listener.go, notice.go                            asynchronous delivery
  errors.go, tls.go                                 structured errors and the TLS policy
  internal/wire/                                    bounded PG codecs and state machine
  internal/auth/, internal/sm3/                     independently implemented authentication
  internal/testpeer/                                protocol and failure simulator (test-only)
```

`examples/` holds the compiling programs and `live/` the build-tagged acceptance suite. A native
MySQL backend would add `internal/mysql/` only when M5 is accepted; the root package stays
PG-wire-shaped until then.

### 13.1 Confirmed module and Go baseline

Use Go **1.26.0** for development and the baseline CI build/test/lint jobs, which install Go 1.26.x. The product module path is **github.com/cocosip/go-kingbase**, derived from the configured Git origin, git@github.com:cocosip/go-kingbase.git. Package name is kingbase and database/sql driver registration remains kingbase. The vendor sample package name gokb is not reused, so imports cannot be confused with the official sample tree.

When T-001 creates the root go.mod, it must begin with:

```go
module github.com/cocosip/go-kingbase

go 1.26.0
```

Use github.com/cocosip/go-kingbase in imports, examples and package-release checks. T-001 created the module, license (Apache-2.0 with NOTICE), dependency policy (standard library only for the core driver), support matrix (linux/windows/darwin on amd64/arm64) and CI contract (Go 1.26.x, golangci-lint v2.14.0). O7 is therefore resolved; package smoke checks remain part of T-051/T-053.

## 14. API and Migration Compatibility

Driver registration is `kingbase`, the package is `kingbase` and the module is
`github.com/cocosip/go-kingbase` (O1 resolved). The new path requires an import migration, not a
drop-in claim. The keep/replace/defer surface for the vendor Go SDK is the
[migration ledger](go-driver-usage.md#migrating-from-the-vendor-go-sdk); this section only fixes
the contract points where a mechanical port would change meaning:

| Official surface | Contract point |
|---|---|
| `NullTime` | use `sql.NullTime` for `time.Time` results and an explicit `Scanner` for time-of-day; no compatibility alias is promised |
| `civil`/decimal/narrow-int/`[]T` defaults | standard boundary §7.1 applies; direct type assertions migrate to `Scan`/an explicit `Scanner` |
| `EnableInfinityTs` | no global toggle: unsupported infinity/BC/zero temporal forms require explicit text retrieval (§7.2) |
| `oid` subpackage and constants | not provided; use the qualified runtime catalog and typed binding |
| `GetSchema`/DataAdapter, `TransactionScope`, an independent .NET-style pool | not Go requirements; `ConnectionMetadata` and explicit catalog queries replace portable schema discovery |

Comparison source is the sibling `kbconnector-dotnet` design, used as research evidence only.

## 15. Verification and Release Strategy

| Layer | Required evidence |
|---|---|
| Unit | DSN, binding index plans, Scan/Valuer conversions, ranges, qualified type selection and parser limits |
| SDK research | static OID fixtures labelled package/version/hash; never called captured pg_type |
| Catalog replay | synthetic hostile namespace/shape cases plus genuine captures with product/edition/mode/protocol/query/time and redacted provenance |
| Protocol | testpeer auth/query/COPY/results plus negative/oversized lengths, split frames/counts, mid-header/body EOF, async messages, trailing errors and bounded drain/reset/cancel races |
| Integration | live evidence per supported V9 profile; unavailable profiles pending/unclaimed; separate V8/MySQL manifests |
| Concurrency | multi-mode isolation/-race on eligible supported toolchain/platform; no global mode/codec |
| Release | Go/platform/module/license decisions closed; scoped P0/P1 accepted; P2 deferrals visible; lint/package/examples/dependencies verified |

Implementation, offline proof and live acceptance are separate [progress](go-driver-development.md)
dimensions. Skipped tests and static SDK data do not establish live acceptance.

## 16. Decisions and Evidence Gates

Each decision's current state and effect is recorded once in the
[development plan](go-driver-development.md#6-decisions-and-investigation-gates); this contract
only fixes what no decision may override:

- The compatibility mode comes from server evidence or an explicit declaration that the server
  confirms; a catalog marker, a port or a product string never selects it.
- Capabilities come from the qualified catalog loaded over the connection; there is no built-in OID
  table and no user-selected product-version parameter.
- A deferred capability keeps its fail-closed error: the gate may decide not to ship support, never
  to fall back silently.

## Appendix A: Snapshot Comparison

| Comparison | Conclusion |
|---|---|
| R001 vs R003 | 17 of 19 inspected files have identical MD5; 7 differing lines: 4 timezone-parsing sites in `conn.go`, 3 constant names in `kb_types.go` |
| R002 vs R003 | the three catalog tables are value-for-value identical; the 25 mode branch sites are in the same places; only identifier obfuscation and stripped comments |
| R003 vs R004 | behaviour: missing `addReturning`/`CreateDialer`/`getID`/`getPrimary`; catalog: see §2.4 |
| Catalog sizes | R001/R002/R003: mysql 230 / oracle 216 / sqlserver 317; R004: mysql 226 / oracle 224 / sqlserver 783 |

## Appendix B: Official SDK Evidence

### B.1 Official Java driver (Maven `cn.com.kingbase:kingbase8`)

- sampled artifacts: 8.6.0 / 8.6.1 and 9.0.0 / 9.0.1 (including jre6/jre7 variants); this is a research sample, not an exhaustive/current release list;
- class structure: `KbTypeInfoCache` / `MysqlTypeInfoCache` / `SQLServerTypeInfoCache` / `TypeInfoCacheFactory` / `DynamicOid`. the inspected 8.6.1 artifact has no SQLServerTypeInfoCache; class absence alone does not establish when server SQLServer support was introduced;
- mode probing (`KbConnection` bytecode; every mode goes through `pg_settings`, `sys_settings` is never used):

```sql
select name,setting from pg_settings where name = 'kdb_flashback.db_recyclebin'
  or name='database_mode' or name='ora_input_emptystr_isnull'
  or name='enable_unpaired_comment' or name='sql_mode'
```

- extra per-mode queries: oracle mode runs `select oid,typname from pg_type where typname in ('raw','rowid')`; sqlserver mode runs `select oid,typname from pg_type where typname='int'`. the sampled SDK therefore has runtime lookup paths; ordinary-role server accessibility is still V2;
- `setStoreCase`: `original` in mysql mode, `lower` in the other modes;
- cache entries are `{typname, oid, java.sql.Types, javaClass, arrayOid}`; **one typname may list several OIDs**:

| typname | OIDs listed by JDBC | java.sql.Types |
|---|---|---|
| tinyint | 7014, 8100 | TINYINT (Integer) |
| bit | 1560, 16, 4655 | BIT (Boolean) |
| date | 1082, 7944 | DATE |
| time | 1083, 7950 | TIME |
| datetime | 7952 | TIMESTAMP |
| year | 7024 | DATE (`java.sql.Date`) |
| longtext | 7018 | LONGVARCHAR |
| money | 790 | DOUBLE (Double) |
| binary | 3383 | BINARY |
| varchar | 1043 | VARCHAR |
| nvarchar (sqlserver) | 5041 | NVARCHAR |

- the sampled sqlserver JDBC cache agrees with several vendor Go entries (tinyint 5026, int 5063, bit 7915, datetime 7881, date 7754, time 7755, binary 5022, varchar 1043), but money is JDBC 790 versus Go R003 sqlserverTypes.go 5069. This is an SDK discrepancy, not a verified server OID; never call the entire table identical;
- the mysql cache partially matches the vendor Go table (date 7944, datetime 7952, time 7950, money 790, binary 3383, longtext 7018) and partially differs (tinyint 7014 vs 8100, year 7024 vs 7025, bit 1560/16 vs 4655).

Conclusion: the same type name may have multiple OIDs across versions and official SDKs, and the official approach lists them all. This directly drives the multi-valued `typname → []OID` registry in §5.1.

Source: <https://repo1.maven.org/maven2/cn/com/kingbase/kingbase8/> (bytecode review of `com/kingbase8/jdbc/*TypeInfoCache.class`).

### B.2 Official .NET driver

A NuGet search sampled Kdbndp 8.0.0 on 2026-11-23; it is not the complete official-driver inventory. The sibling kbconnector-dotnet/docs/reference/official-driver-inventory.json records bundled DLL/API research. Neither NuGet availability nor DLL reflection proves real server feature acceptance.

### B.3 Third-party corroboration (non-official)

No third-party report establishes accepted server support here. The primary-source candidates and failure contract in §9 govern this design; actual query accessibility and server behaviour remain V1/V2 evidence requirements.

### B.4 Items the accepted profile did not settle

- whether a single-mode edition server restricts connections to its own compatibility mode (the
  accepted deployment runs four instances, so the driver itself restricts nothing);
- catalog visibility and permissions on builds later than the accepted deployment (the snapshot
  evidence comes from the accepted release, and a later release must be re-measured, not assumed);
- the mapping between a server release and the driver version bundled in each installation package
  (the sampled figures come from package directory names, not from a vendor statement).
## Appendix C: MySQL Transport Backend (conditional)

### C.1 Scope and selection

This candidate backend is M5, selected only by wireProtocol=mysql. V12 establishes which editions/deployments actually expose it; V15 independently accepts the standalone packaging and all-mode extension where they exist. The SQL compatibility name or SDK packaging cannot prove listener availability. Until M5 passes, selecting mysql fails clearly; no automatic substitution of another driver.

Shared database/sql, error and operation-lifetime contracts still apply. PG catalog SQL, CancelRequest, ReadyForQuery, COPY and LISTEN are not sent on this path. Native bootstrap for product identity, mode, encoding and reset must be verified separately. Do not run SHOW database_mode or SELECT pg_type merely because it works over PG. Auto mode fails if native discovery cannot establish it; an explicit declaration remains labelled unverified and cannot override a conflicting server report.

### C.2 Committed subset and protocol bounds

- Handshake v10 validates advertised lengths, charset and plugin data. Negotiate only implemented flags: PROTOCOL_41, SECURE_CONNECTION, PLUGIN_AUTH, SSL as required, DEPRECATE_EOF only when both parsers support it, and MULTI_RESULTS/PS_MULTI_RESULTS for the promised result forms. Keep MULTI_STATEMENTS, compression and LOCAL_FILES disabled. Verify utf8mb4 negotiation on each accepted Kingbase endpoint.
- Frame header/payload reads tolerate arbitrary TCP splits/coalescing. Reassemble 0xffffff-byte continuation packets; an exact multiple requires its terminating zero-length packet. Validate per-exchange sequence numbers/wrap and total/frame/allocation limits. Wrong sequence, oversized lengths and truncated continuation fail and retire the session (R6).
- Authentication targets mysql_native_password and caching_sha2_password only as actually supported. Implement AuthSwitch/AuthMoreData state explicitly; full auth requires the configured protected channel, while accepted cached fast auth follows plugin rules. No automatic unauthenticated public-key retrieval or weaker-plugin downgrade. Unknown forms return a precise error; PG authentication tests do not certify these plugins.
- TLS uses SSLRequest then TLS before protected credentials, without plaintext fallback. Trust semantics match §12 including configured CA behaviour; verify all modes independently in V8/V15.
- Commands: COM_QUERY, COM_STMT_PREPARE/EXECUTE/CLOSE, COM_INIT_DB only through verified configuration, COM_PING and COM_QUIT. Always server-side binding, no client interpolation. Shared lexing returns both question-mark SQL and the expanded parameter list. Long data/streaming parameters are not advertised until separately accepted; no silent full-buffer support claim.
- Results: text/binary rows, NULL bitmaps, type/unsigned flags, OK/ERR/EOF variants conditioned on negotiated flags. Preserve native error number and SQLSTATE when present. Define matched-versus-changed RowsAffected (initial policy: changed rows, do not negotiate FOUND_ROWS), warning counts and generated-ID overflow. Multiple routine results and multi-statement execution are independent (R7).
- Track SERVER_MORE_RESULTS_EXISTS, IN_TRANS and AUTOCOMMIT. Consume every result and terminal status before reuse. Rows.Close drains within the cleanup budget; malformed trailers/uncertain state retire the session. Invalidate local prepared handles after resets/close; transaction/default/read-only semantics are independently accepted in V19.
- Cancellation initially aborts/retires the session (§10.2); native server identity and any future KILL path are not PG backend keys.

### C.3 Types and acceptance

MySQL column type codes/flags/charset drive decoding; no OID snapshot. DatabaseTypeName is the actual uppercase MySQL name. Apply the standard driver-value and exact numeric/temporal policies in §7, with byte/character length units and NULL/zero/unsigned edge cases. Do not import PG encodings or infer SQL mode from a field code. Mode/identity are diagnostics and validated semantics, not an OID cross-check; a conflicting explicit mode fails rather than being downgraded to a warning.

V15 must cover greeting/capabilities, auth/TLS, bootstrap, prepared statements, binary rows, zero/multiple/large results, generated values, warnings/errors, frame limits and ordinary-role permissions on every claimed endpoint family. V19 additionally gates reset/transactions/recovery. Missing infrastructure leaves these pending; it does not prove a client bug. If required behaviour is unsupported, exclude that profile/backend from release, document the reason and let administrators choose a separately verified client explicitly.

## Appendix D: Primary References and Applicability

References were reviewed on 2026-11-24. Go interface/protocol specifications establish client contracts, not Kingbase server acceptance. Vendor source paths are local ignored research inputs and must have package/hash provenance when turned into fixtures. JDBC observations above are sampled artifacts, not claims about the newest available release.

| Ref | Primary source | What it establishes / limit |
|---|---|---|
| R1 | [Kingbase V9 parameter FAQ](https://help.kingbase.com.cn/v9/faq/faq-new/parameter.html) | database_mode is listed under internal parameters set at build/initdb time; exact product/release applicability remains V14 |
| R2 | [Go database/sql/driver](https://pkg.go.dev/database/sql/driver) | Value, ErrBadConn, ConnBeginTx, reset/validation timing and metadata contracts; implementation must verify the selected Go 1.26.0 baseline |
| R3 | [Go database/sql](https://pkg.go.dev/database/sql) | Scan/Scanner/Valuer/Out and pinned Conn lifecycle; newer optional interfaces are not a baseline requirement |
| R4 | [PostgreSQL 12 protocol flow](https://www.postgresql.org/docs/12/protocol-flow.html) | asynchronous-message parsing and cancellation model; verify Kingbase behaviour separately |
| R5 | [PostgreSQL 12 SSL](https://www.postgresql.org/docs/12/libpq-ssl.html) | historical require-with-CA and private-key policy; official Go source below is the actual migration baseline |
| R6 | [MySQL basic packets](https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_basic_packets.html) | continuation, length and sequence framing; not Kingbase certification |
| R7 | [MySQL stored-program results](https://dev.mysql.com/doc/dev/mysql-server/latest/page_protocol_command_phase_sp.html) | multi-result capability/completion rules; not permission to enable multi-statements |
| R8 | [Kingbase official Go guide](https://help.kingbase.com.cn/v9/development/client-interfaces/go/go-1.html) | official Go documentation entry; OUT branches are verified in local source below, not attributed to a nonexistent example page |

Local owning evidence: .source-code/KingbaseES_V009R003C018B0003_GOLANG/golang/kingbase.com/gokb/{ssl.go,ssl_permissions.go,conn_go18.go,conn.go,copy.go,array.go,notice.go,encode.go}; R004 counterpart corroborates TLS. TLS require-with-root and private-key-only checks are in ssl.go/ssl_permissions.go; BeginTx defaults/read-only are in conn_go18.go; CopyInSchema returns SQL in copy.go; conn.go contains explicit mysql OUT handling (including BIT). Locate routine samples in the installed official package and record their exact path/hash in V5 before asserting compatibility. Do not present a source example as a successful live test.
