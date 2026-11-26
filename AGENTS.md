# AGENTS.md — go-kingbase

Agent and contributor rules for this repository. The documents under `docs/` are the interface
contract and the only source of status; this file says how to work, never what is currently true.

## What this repository is

- A `database/sql` driver for KingbaseES, implemented independently. Module
  `github.com/cocosip/go-kingbase`, package `kingbase`, driver name `kingbase`.
- Go 1.26.0 module baseline; **standard library only** for the driver core. Do not add
  dependencies without a recorded decision in the development plan.
- PG wire is the transport for the first-release scope. Other transports and compatibility
  products enter scope only through the documents, never by accident.
- Root `.go` files are the product package. `internal/wire` (framing/messages), `internal/auth`,
  `internal/sm3` and `internal/testpeer` (scripted peer, test-only) support it. `examples/` must
  keep compiling; `live/` holds the build-tagged acceptance suite.

## Source of truth (read before changing behaviour or claiming status)

1. [Architecture design](docs/go-driver-architecture-design.md) — interface contract and the
   difference model (mode selects semantics; protocol selects handshake; catalog selects capabilities).
2. [Capability matrix](docs/go-driver-capabilities.md) — the `F-*` feature IDs with independent
   Impl / Offline / Live states.
3. [Development plan and progress](docs/go-driver-development.md) — the `T-*` tasks, milestones,
   `V*` verification items, `O*` decisions, deferrals, the live acceptance record and the dated log.
4. [Usage and configuration](docs/go-driver-usage.md) — the practical DSN/API reference and the
   migration ledger; it describes released behaviour and defers to the records above for status.

Status changes over time: driver releases, server releases and operator scope all move it. Never
copy a status, revision, feature ID or deferral list into this file or into a commit message; cite
the document instead, so the next reader cannot act on a stale snapshot.

## Commands (all must pass before a commit that changes code)

```sh
go build ./...
go vet ./...
go test ./... -count=1 -timeout 10m
go test -race ./... -count=1                # where cgo is available
go test -tags=live ./... -run '^$'          # compile the live suite, no database needed
golangci-lint run --build-tags=live ./...   # must report 0 issues
gofmt -l .                                  # must print nothing
go mod tidy -diff                           # must be clean
```

CI enforces these plus a coverage floor whose value lives in `.github/workflows/ci.yml`. Coverage
is a regression floor, not an acceptance claim.

## Live acceptance rules

- The live suite needs `-tags live` and `KINGBASE_TEST_DSN`; it fails hard without a DSN. Optional
  inputs: `KINGBASE_TEST_MODE`, `KINGBASE_TEST_PROFILE`, `KINGBASE_TEST_OPERATOR`,
  `KINGBASE_TEST_REVISION`, `KINGBASE_TEST_ARTIFACT_DIR`, `KINGBASE_TEST_PERF=1`.
  The cross-mode probe also reads `KINGBASE_TEST_PEER_DSN`; without it that probe skips with a
  named reason instead of failing.
- `KINGBASE_TEST_ARTIFACT_DIR` resolves relative to the `live/` package directory, so an artifact
  path in the documents is package-relative. Artifacts are gitignored, sanitized and never
  contain credentials.
- A testpeer replay, a skipped test, a static OID table, an unavailable instance or an opt-in test
  that did not run is **not** live evidence. Only a run against a real deployment with a recorded
  command and artifact counts, on the profile and revision the ledger names.
- Credentials are environment-only. Never commit DSNs, host+credential pairs or artifacts:
  `/.env*`, `/.secrets/`, `/.certs/`, `/artifacts/`, `/live/artifacts/` are ignored on purpose.
- When code changes an observable state, update the documents in the same commit: task `Evidence`,
  feature `Impl/Offline/Live`, the V-item ledger, the summary counts and a dated log row. One
  canonical row per ID; never invent a test name and never mark a task done from specification text.

## Scope discipline (durable rules)

- **Read the status tables before claiming anything.** Acceptance, deferrals and open decisions are
  recorded in the documents with reasons and revisit conditions; this file must not restate them.
  If the ledger does not record a claim, the claim does not exist.
- **Deferral is not permission to break the contract.** A deferred capability keeps its
  fail-closed path, its named error and its offline tests. Removing a guard because the feature is
  out of scope is a regression.
- **No silent fallback, ever.** TLS, protocol and mode selection never downgrade: an unsupported
  request fails with an explicit error. Keep the trust/permission tests green even while live TLS
  acceptance is outstanding.
- **Enabled-only evidence.** Live auth and codec claims cover what the tested deployment actually
  enables. A method the driver implements but the server does not enable stays offline-proven and
  live-pending; never generalize it into a support statement.
- **Evidence is scoped, not pinned.** A run records its server build and driver revision in the
  artifacts (`evidence-<mode>.json`), never as a frozen value in prose: the driver must work
  against any server release it can introspect, and a later run may supersede an earlier one
  without editing every document. Do not write commit hashes or server version numbers into
  status lines, support claims or rules.
- **New scope follows the documents.** A new profile, transport or product version needs its
  manifest, verification items and evidence plan added to the documents before code claims it.
- The self check before touching the wire is that at least one named test proves the behaviour. If
  it does not exist, the behaviour is not implemented yet — add it, don't document it.

## Code conventions

- **Mode profiles.** Per-mode semantics live in the mode profile (`mode.go`); wire framing, server
  identity and lexical context stay separate. Never infer a mode from catalog contents and never
  rewrite a statement for a profile that is not in force.
- **Catalog.** The type catalog is a qualified server query run at connect. Unknown, ambiguous or
  user-defined types stay opaque (text → `string`, binary → `[]byte`); no built-in OID table, no
  user-selected version parameter.
- **Fail closed.** Unsupported forms return an explicit error before bytes are sent. If bytes were
  sent and the outcome is unknown, retire the connection and return `*UncertainError`; never replay
  a command or return `driver.ErrBadConn` after a send.
- **Truthful messages.** Export typed errors in `errors.go`; internal sentinels belong next to their
  domain file. A user-facing message must describe real behaviour, never a stale "not implemented".
- **Public surface is intentional.** Anything a DSN cannot express needs a documented public
  entry point (for example a connector path for a programmatic configuration). A capability that
  only tests can reach is not shipped.
- **Tests.** Add named `Test*` cases to the matching domain file (`copy_test.go`, `mode_test.go`,
  …) with a scripted peer from `internal/testpeer` (`startupOK`, `expectMessage`, `waitTerminate`).
  Keep one minimal check for every non-trivial branch; never weaken or skip a test to pass a change.
- **Language and style.** Code, comments, docs and commit subjects are English. History uses
  `<emoji> <type>(<scope>): <imperative summary>`; follow the same shape.
