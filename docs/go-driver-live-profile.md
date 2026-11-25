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
KINGBASE_TEST_DSN='host=... port=54321 user=... password=... database=...' \
KINGBASE_TEST_MODE=pg \
KINGBASE_TEST_ARTIFACT_DIR=artifacts/v9-pg \
go test -tags live ./live -run TestLiveSmoke -v
```

`KINGBASE_TEST_ARTIFACT_DIR` is optional; when set, the test writes
`live-smoke.json` with the reported metadata and timestamp. The harness does not
run by default (`go test ./...` excludes it), and it never substitutes for the
full V1–V19 checklist.

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
