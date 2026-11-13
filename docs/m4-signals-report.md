# M4 signal approval and automatic paper entries — ticket #5

Implemented against baseline `4f3358e` for
[issue #5](https://github.com/saintmili/billionaire/issues/5), under the approved
offline M4 exception. M2 actual-host validation remains pending; live monitoring
is not enabled. M4 combined acceptance and funding/liquidation remain separate.

## Implementation

Migration 8 adds `signal_decisions`, keyed by immutable signal ID with a unique
optional order reference. Decisions distinguish observations, awaiting approval,
accepted intents and rejection. Reads join the current order outcome; an accepted
intent is not a filled trade. New instances and enabling legacy instances require
real paper accounts; immutable placeholder bindings must be recreated.

`PaperOwner.DecideSignal` owns action policy and approval independently of CLI
transport. It serializes with the existing owner, locks the signal, and records
its decision and optional order in one transaction. Both automatic and approved
manual entries use the same transaction-level admission service as standalone
manual orders. Signal-linked standalone submissions are rejected; the explicit
approval service is required. Alert-only and exit observations cannot open or
reverse positions. Source-unready signals retain a durable rejection.

The existing observation service rechecks account risk, kill switch, source
freshness and now strategy enabled state before filling pending signal intents.
The executable price comes from a later observation, with source/latency recorded;
stop/target hints and fees/sizing assumptions are frozen on the intent. Signal
percent risk remains enforced alongside account/strategy policies. Financial
changes and journal-outbox items commit atomically in the existing fill service.

`paper consume` is a bounded offline queue drain per fixture instrument (default
100, maximum 1000). `paper approve` requires the signal ID and explicit timestamp;
identical approval retries return current order outcome, conflicting payload or
metadata reuse fails. `paper decisions` exposes pending and final outcomes.
`strategy evaluate --source-ready` explicitly asserts fixture readiness while
preserving the default observation-only behavior. Each command acquires and
releases exclusive ownership; restart retries consume only undecided signals and
resume existing pending orders through a separate `paper observe`. No daemon,
socket, live execution or journal delivery is added. README includes a runnable
fixture invocation and failure behavior.

## Verification

The user confirmed PostgreSQL decision/execution and fixture CLI seams, plus
review baseline `4f3358e`. TDD slices established account validation, approval and
restart, rejection of approval bypass, CLI consumption and conflicting metadata.
Tests cover all action modes, immutable signal observations, current rejection
outcomes, kill switch and disabled-instance revalidation, unready sources,
per-candle guards for distinct while_true signals, explicit pyramiding, and
transaction rollback on order/decision writes. Existing financial rollback tests
exercise the shared observation and ledger/outbox path.

Validation used disposable PostgreSQL 17 on local port 55435. Every integration
test created and removed an isolated database through `internal/testdb`.

- `go test ./...` with PostgreSQL enabled: passed.
- `go test -race ./internal/paper ./internal/store/postgres ./internal/cli` with
  PostgreSQL enabled: passed.
- `go build ./...`, `go vet ./...`, `git diff --check`: passed.

The protection-upgrade fixture now rolls back explicitly to schema 6 rather than
assuming one rollback always reaches that version; its upgrade test passes with
migration 8 present. The temporary PostgreSQL container was removed after checks.

## Standards

The independent Standards reviewer found no documented-standard breaches or
material baseline smells. It confirmed separate signal/decision records,
single-owner mutations, shared admission, explicit clocks and transaction scope
against ADRs 0002–0004. Its documentation caveat about pending verification
evidence is addressed by the completed results above.

## Spec

The independent Spec reviewer found no missing/partial acceptance criteria,
confirmed defects or scope creep. Invalid caller sizing/fee/metadata options
return validation errors; runtime source/risk rejections persist rationale.
Accepted decisions expose the nested order's current outcome without changing
the signal. Spot fixtures establish this ticket's acceptance; shared linear
execution remains available, while combined M4 linear/funding/liquidation
acceptance remains a separate milestone gate.

Review totals: Standards — zero remaining findings; Spec — zero findings.
