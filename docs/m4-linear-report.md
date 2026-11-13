# M4 isolated linear execution — ticket #4

Implemented 2026-11-14 against `f7fc112`, for
[issue #4](https://github.com/saintmili/billionaire/issues/4), under the approved
offline M4 exception. M2 live-host validation still blocks live monitoring.

## Implementation and persistence

The shared paper planner and PostgreSQL owner now accept spot and linear intents
through `PrepareOrder`, `SubmitOrder`, `Observe` and `PlanFill`. Independent
instrument identities include market type. Linear positions persist side,
leverage, remaining isolated collateral and unallocated entry fees. Existing spot
records default to long/1×. Position JSONB already supports these fields, so no SQL
migration or ledger rewrite is required; the schema stays at version 7.

Linear entries reserve notional/leverage rounded upward to 18 places and charge
fees on full notional. Partial exits proportionally release collateral and entry
fees; final exits consume all residuals. Gross P&L is quantity × directional price
change, rounded downward; exit fees and allocated entry fees produce net realized
P&L. Margin reservation/release, price P&L and fees have distinct immutable ledger
movements. Orders, fills, positions, balance, ledger, outbox and observation replay
results commit together. Retries preserve identities and recorded outcomes.

The shared risk gate checks current cash/free margin, both scoped leverage and
margin limits, gross symbol exposure and directional stop sizing. Linear holdings
require fresh marks before new entries, including attempts to enter spot. Close
requests validate account, market, side, leverage and remaining exposure; opposite
entries cannot implicitly close or reverse a position. Existing protection now
handles short stop/target/trailing directions and adverse-first candle collisions.

`PaperAccountValuation` reads a repeatable read-only database snapshot. CLI
`paper account show --at` exposes cash, reserved collateral, realized net P&L,
gross unrealized price P&L, equity and free margin. Mark-dependent fields are null
with a reason if any open holding lacks a fresh usable mark. The README documents
formulas, valuation clocks, simulation leverage bounds, commands and failure cases.
Fixtures `testdata/paper/linear-{entry,partial,final}.json` drive both directions.

## Verification

The user agreed paper execution/risk, PostgreSQL and fixture CLI test seams, and
review baseline `f7fc112`. Red/green slices established linear admission and
settlement, short protection, directional risk/margin behavior and account valuation.
Tests include both directions, partial/final collateral release, precision,
minimum notional, invalid leverage/quantity, adverse slippage, policy boundaries,
execution-time sizing, duplicate delivery, owner restart, concurrent collateral
requests, spot/linear observation separation, stale collateral and short
ambiguous-bar protection while paused. The financial rollback fault matrix runs
for both markets across order/account/fill/position/ledger/outbox/event writes.
CLI fixtures reconcile 1000 USDT to 989.61 (long) and 1009.61 (short).

Checks use disposable PostgreSQL 17 via `MILLIONAIRE_TEST_DATABASE_URL`; every test
creates a separate database. Go's build cache is under `/tmp/billionaire-go-cache`.

- `go test ./...` with PostgreSQL enabled: passed.
- `go vet ./...`, `go build ./...`, `git diff --check`: passed.
- `go test -race ./internal/paper ./internal/store/postgres ./internal/cli`
  with PostgreSQL enabled: passed after all review fixes.

## Standards

The independent reviewer found no documented-standard breaches and one duplication
smell: admission and settlement separately calculated collateral/fees with different
rounding. Both now use `paper.CalculateFillAmounts`, including buy/sell notional
rounding and collateral precision. A PostgreSQL regression admits a short with
exactly sufficient collateral at the 18-place rounding boundary. Re-review found
no remaining material Standards findings.

## Spec

The independent reviewer found one incorrect behavior: a fill’s immediate adverse
slippage could make resulting free margin negative or breach a configured margin
percentage despite passing admission against pre-fill equity. Linear admission
now includes prospective price P&L and fees in projected free margin and the
margin-limit equity denominator; risk sizing still uses current equity. Regression
cases cover long/short deficits and a margin-policy breach with positive free
margin. A targeted review of initial isolated solvency also confirmed that a new
position must retain positive reserved collateral plus immediate unrealized P&L,
independently of account cash. Admission rejects zero/negative initial isolated
equity; long/short regressions cover both boundaries. Subsequent price-driven
liquidation remains #7. Re-review confirmed both corrections and no residual
material Spec findings. Full tests, vet, build and PostgreSQL race checks passed
after the final guard; the disposable PostgreSQL container was removed.

Review totals: Standards — one duplication finding addressed, none remaining;
Spec — two admission findings addressed, none remaining.

## Limits and remaining gates

Leverage 1–100 is a bounded simulation assumption, not verified venue tiers.
Funding and maintenance/liquidation are #7, approval/auto orchestration is #5,
and combined M4 acceptance is #8. Until #7, losses settle in full: a severe gap can
exceed reserved collateral and produce cash debt. There is no automatic top-up or
liquidation/loss cap. Reservation is per position; complete isolated liquidation
acceptance remains a milestone gate. No M5/M6/M7 delivery, live orders,
authenticated exchange clients or StopLore API methods are introduced.
