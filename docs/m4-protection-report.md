# M4 protection — ticket #6

Implemented 2026-11-13 against baseline `21f8499` for
[issue #6](https://github.com/saintmili/billionaire/issues/6),
**M4: Manage stops, targets, and trailing exits**. This completes the spot
protection slice; M4 combined acceptance and symmetric linear-long/short coverage
remain pending. M2 actual-host validation remains pending under the existing
offline sequencing exception.

## Changes

- `internal/paper/protection.go` defines transport-independent geometry,
  patch validation, completed-bar validation and conservative protection decisions.
  Positions retain stop/target, target consumption, trailing activation/high water,
  state clocks and frozen entry execution assumptions.
- `PlanSpotFill` validates initial protection against the actual modeled fill and
  risk-sized quantity, persisting an explicit rejection for unusable geometry.
- `ObserveSpot` evaluates existing protection before pending manual intents inside
  the same owner transaction. Generated close orders use the existing fill,
  position, ledger and journal-outbox boundary. Protective fills expose exit reason,
  gap and ambiguity. Failed partial exits do not consume the target.
- `AdjustProtection` preserves unspecified fields, validates against the latest
  fresh observed price, persists an immutable request/result and enqueues a journal
  event atomically. Retries return the original outcome; conflicting inputs fail.
  Trailing stop changes also enqueue transactional journal events.
- Migration `0007_protection` adds the adjustment replay/audit table. Existing
  position JSON is extended with entry fee/slippage assumptions obtained from its
  original order and fill. Old sizing-only stops are deliberately not activated
  automatically; an explicit adjustment can attach protection after upgrade.
- CLI: entry flags `--stop-price`, `--take-profit`, `--target-quantity`,
  `--trail-activation`, `--trail-distance`; `paper adjust <position-id>` accepts
  `--stop-loss` and the other protection flags, plus stable `--request-id` and
  explicit `--at`. Empty values disable fields; omitted values preserve them.
- README documents commands, geometry, configuration, failure and restart behavior.
  `testdata/paper/spot-protection-collision.json` demonstrates the OHLC contract.
  No dependency or authenticated provider interface was added.

## Observation and financial semantics

Long stops must be below the modeled entry/current adjustment reference, targets
above it; prices/distances must match exact instrument ticks. An absolute activation
price and absolute distance configure trailing. On activation, the highest observed
price minus distance ratchets the stop upward, rounded down to a tick. Changes apply
only to a subsequent observation. Stops always close remaining holdings; a partial
target fires once and remains consumed through restart. Explicit target changes
rearm it. Stops can still be adjusted after a consumed partial target.

For completed OHLC observations, evaluate the old stop before the old target,
marking simultaneous touches ambiguous. Only afterward update trailing for the
next event. A bar overlapping entry, adjustment or prior protection state is skipped
for that position, preventing retroactive execution. A gap through a level uses
bar open, or the observed point price, followed by adverse slippage. Ordinary
crossings use the touched level plus slippage. No intrabar path is inferred.
Entries on a completed-bar observation use its close and cannot receive protection
from that already-completed interval. These are fixture observations, not M5 replay.

Protection bypasses entry limits, disabled-entry configuration and the kill switch.
Invalid, unavailable, stale or unready observations cannot execute protection.
Minimum quantity/notional rules still apply; rejected protective exits retain
protection for the next valid event. Manual closes execute after protection and
revalidate current holdings, preventing over-close. Exact decimal prices, fees,
cost allocation and principal/fee ledger entries use the existing execution rules.

## Verification

The user confirmed public paper decisions, PostgreSQL owner/store operations and
CLI commands as test seams, and `21f8499` as the review baseline. Focused red/green
cycles covered collision/gap decisions, trailing delayed activation and tick
rounding, persistent partial targets, manual stop adjustment, CLI execution,
legacy upgrades and journal rollback.

Verification uses a temporary PostgreSQL 17 container on loopback port 55436 and
uniquely created disposable databases via `BILLIONAIRE_TEST_DATABASE_URL`; existing
application databases are never used.

- Public policy tests cover exact boundaries, invalid/off-tick geometry, partial
  targets, trailing next-event semantics, unusable/overlapping bars, stop adjustment
  after partial target, stale/backdated adjustments and explicit disabling.
- PostgreSQL tests cover restart, replay, request conflicts, candle SL/TP collisions,
  adverse slippage, trailing restart, kill-switch/disabled-entry bypass, stale,
  unready and invalid prices, and competing manual/protective closes.
- Injected order/position/fill/account/ledger/outbox/event write failures roll back
  the entire close. Manual and trailing adjustment outbox failures roll back state;
  identical retries recover safely. Public reads verify holdings, fills and balances.
- Buy 2 at 100, sell 0.5 at 120, then stop the remainder at observed 80 with 0.1%
  fees: final cash **979.62**, three fills, no repeated partial target.
- CLI integration covers initial protection, adjustment/retry, preserved target,
  invalid adjustment and observed-price gap execution.
- `go test ./... -count=1` with PostgreSQL integration enabled: passed.
- `go vet ./...`, `go build ./...` and `git diff --check`: passed.
- `go test -race ./internal/paper ./internal/store/postgres ./internal/cli -count=1`
  with PostgreSQL integration enabled: passed.

## Standards

Independent review found no documented-standard violations. The change preserves
PostgreSQL ownership, exact decimal calculations, transactional financial/outbox
writes, deterministic identities and paper-only boundaries. Milestone status and
remaining gates are documented.

One nonblocking **possible Data Clumps** judgment identified the five repeated
protection fields on `OrderRequest` and `ProtectionConfig`, copied by
`ProtectionConfig()`. Embedding the config could reduce parallel field edits.
The existing request fields are retained because embedding would break existing
Go composite literals that set `StopPrice` directly, including existing risk
callers. The explicit conversion preserves that source contract and flattened JSON.
This is a documented maintainability tradeoff, not a required behavior gap.

## Spec

Independent review found no confirmed Spec findings: no missing requirements in
the authorized spot slice, no M5–M7/live-exchange scope creep, and no concrete
incorrect behavior. Adverse-first collisions, delayed trailing, observed-price
gap fills, persistent partial targets, transactional writes, entry-control bypass,
replay identities and current-holdings checks match the ticket. The reviewer
performed static inspection; the implementation verification above ran the real
PostgreSQL suite and race checks.

Review totals: Standards — 0 hard violations, 1 nonblocking maintainability
judgment retained for compatibility; Spec — 0 findings.

## Remaining gates

Symmetric linear-long/short execution joins the combined M4 gate after linear
execution lands. M2 live-host validation is still required before monitoring.
M5 replay/reporting, M6 process/socket work and M7 StopLore delivery remain outside
this ticket. Outbox enqueue is implemented; no StopLore method is fabricated.
