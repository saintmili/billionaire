# M4 risk gate and kill switch (ticket #3)

Implemented on 2026-11-12 within the approved offline M4 sequencing exception.
M4 remains in progress; the M2 actual-host gate still blocks live monitoring.

## Policy and execution contract

`paper risk set --account <id> [--strategy <definition-id>]` replaces the entire
policy in that scope. `paper risk show` with the same flags inspects it. Omitted
monetary limits and zero integer limits disable that individual limit. All values
are exact plain decimals, monetary limits positive USDT, percentage limits in
(0,100], integer limits nonnegative. Cooldown seconds must fit a Go duration.
Defaults impose available funds, spot leverage 1, market/source readiness and no
pyramiding; risk budgets are opt-in for standalone manual quantity orders.

Account policy always applies across every strategy and manual entry sharing the
account. A strategy policy applies to positions, fills and cooldown attributed to
that strategy definition **within this account**, across its instances/versions.
Both scopes must pass; neither overrides or loosens the other. Linked signals also
retain their immutable strategy-version percentage-risk ceiling. Strategy scope
requires an existing definition. Signal links must identify a ready long entry,
matching strategy, instrument and bound account in manual/auto paper mode.
Intents cannot predate signal availability, and the execution observation must
follow that intent. A filled signal cannot be consumed again under a new request
ID, even after closing its position or enabling pyramiding. Distinct signals from
the same strategy/instrument/timeframe/candle can fill at most once unless all
applicable scopes allow pyramiding; this check uses durable entry history,
including closed positions. Standalone manual orders carry no signal candle;
their deduplication uses request identity, held exposure and configured cooldown.
Strategy bindings now accept generated paper-account IDs (up to 128 characters),
which exceeded the earlier 64-character strategy-identifier limit.
Approval/automatic orchestration remains a later ticket; both can call the same
`SubmitSpotOrder` / `ObserveSpot` API.

| Setting | Behavior |
| --- | --- |
| `--fixed-risk` | Maximum nominal entry-to-stop price-distance loss in USDT |
| `--percent-risk` | Maximum nominal price-distance loss as percent of current account equity |
| `--daily-loss` | Reject when today's net realized price P&L minus fees is at or below negative limit |
| `--max-positions` | Reject when open position count already equals the maximum |
| `--symbol-exposure` | Current marked instrument notional plus proposed executable notional may equal but not exceed cap |
| `--max-leverage` | Entry leverage may equal but not exceed cap; spot independently requires 1 |
| `--margin-percent` | Spot capital usage: scoped marked notional plus proposed notional / account equity |
| `--cooldown-seconds` | Time since last filled entry in scope; exact equality permits entry |
| `--pyramiding` | Off by default; both applicable scopes must permit adding to an instrument already held |

Daily boundaries use **00:00 UTC** and the injected execution clock, never local
wall time. Price P&L is recognized on exits; entry and exit fees are charged on
their fill day without double counting entry costs. Gains offset losses in the
same UTC day. The daily total resets by selecting that day's immutable fills;
there is no mutable counter or restart/reset command. Cooldown survives restart
and midnight, uses the last filled entry (including subsequently closed positions),
and does not advance on pending or rejected requests.

Use `paper open ... --quantity risk --stop-price 90` to size when executable.
The quantity is the smaller applicable risk budget divided by executable entry
minus stop, rounded **down** to the instrument quantity step using exact quotient
and remainder. Manual numeric quantity is checked against the same budgets.
Entry includes adverse slippage and price-tick rounding. Stops must be positive,
below executable entry, and on the price tick. Zero/missing distance, missing
sizing policy, below-minimum quantity/notional, unsupported precision and funds
failure reject explicitly. The nominal budget models price distance; fees and
future stop-gap/slippage losses are additional costs, not a guaranteed loss cap.
`--stop-price` records sizing geometry; protective stop execution is a later M4
ticket. This slice does not pretend to install a protective order.

Equity is USDT cash plus current marked spot holdings. Percentage/margin/exposure
checks require ready, fresh marks for other holdings, with availability no later
than the execution clock; missing/stale marks reject instead of silently valuing
at entry cost. Leverage and margin policy contracts are present; isolated linear
execution/funding/liquidation are subsequent M4 work. There is no linear execution
path to bypass this spot gate.

## Persistence, concurrency and failures

Migration 0006 adds a singleton `paper_controls` row and account/strategy JSON
policies. Run `billionaire init` to migrate existing installations. Its down
migration removes those policies and the switch state. Existing pending intents
are evaluated under the current policy at execution, not grandfathered in.

Every submission/execution locks the account row; the existing offline
single-engine session owner and mutex also serialize concurrent callers. Policy
updates lock the same account row. Eligible entries recheck the current switch,
policies, holdings, daily fills, source readiness and funds before committing.
Rejected requests are terminal `paper_orders` records with request, optional signal
and strategy IDs and explicit reasons; they create no reservation, position,
fill, ledger entry or financial outbox item. Syntax/metadata errors still return
validation errors before creating an order. Replay retains the recorded decision;
a changed request under the same ID fails. Retrying a rejected request requires a
new request ID. Replaying a committed observation cannot change its decisions.

`billionaire kill-switch on|off|status` persists the switch globally;
`billionaire status` also shows `kill_switch`. Switch mutations use a separate
connection and serialize with execution's shared row lock. If an entry transaction
wins the race it completes before `on` returns; after `on` returns, subsequent
entries cannot commit until disabled. Close submission/execution skips entry risk
and switch checks, so partial/full exits remain available with a disabled market
or switch on. Existing source and owned-holdings validation still applies to exits.
There is no daemon/control socket in this slice.

Financial writes still atomically include the journal outbox. Failure rolls back
order decisions and financial changes and leaves the observation retryable;
StopLore delivery remains M7. No trading API, credentials or live monitor added.

## Verification

Agreed seams: paper risk API, PostgreSQL execution API, CLI. Red/green checks
covered exact sizing, persisted switch and execution-time sizing, and CLI controls.
Targeted checks cover policy boundaries, precision/missing stops, shared-account
strategy limits, concurrent requests, kill-switch races, switch restart, daily
reset/cooldown, signal-linked rejection, stale valuation, idempotent retry and
partial/full exit availability. Existing spot rollback fault-injection tests run
against the new gate. The prior replay test now explicitly permits pyramiding
because it deliberately opens two positions in the same instrument.

Validation commands use a disposable PostgreSQL 17 server and isolated test
databases via `BILLIONAIRE_TEST_DATABASE_URL`; no existing application database is
modified. Final verification passed with PostgreSQL integration tests enabled:

- `go test ./...` — all packages pass, including migration up/down and rollback.
- `go vet ./...` — passes.
- `go test -race ./internal/paper ./internal/store/postgres ./internal/cli` — passes.
- `git diff --check` — passes.

The temporary PostgreSQL 17 container was removed after verification. The nominal
risk model excludes fees and future gap/slippage losses as described above;
protective exits, linear execution, and approval orchestration retain their
separate M4 acceptance gates.

## Standards

Review found no documented-standard violations and three optional maintainability
observations. Shared `SpotObservationEligibility` now eliminates duplicate
eligibility predicates; explicit `scopedRiskPolicy.strategyScoped` replaces
slice-position scope inference. The documented `quantity="risk"` syntax remains
a deliberate interface choice. Recheck found no residual standard violations.

## Spec

Review found two defects: linked intents could predate signal availability, and
closed positions could permit another entry from a consumed signal or its candle.
Both were fixed under the account transaction and regression-tested, including
explicit same-candle pyramiding allowance. Recheck found no residual spec findings.

Review totals: Standards 3 optional observations (2 addressed, 1 retained interface
choice), no hard violations; Spec 2 findings, both resolved.
