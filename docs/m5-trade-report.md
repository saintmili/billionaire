# M5 trade performance and equity — ticket #20

The report service reads retained backtest results or a single read-only,
repeatable-read forward-paper account snapshot. It never reruns a strategy,
acquires financial ownership, creates fills, or writes financial state. Backtest
and forward-paper results have explicit, separate `scope` values.

```sh
millionaire backtest trades RUN-ID --output json
millionaire report trades --scope backtest --run RUN-ID --output json
millionaire report trades --scope forward-paper --account ACCOUNT-ID --output json
millionaire backtest trades RUN-ID --strategy-version VERSION-ID \
  --provider fixture --market spot --symbol BTC/USDT \
  --from 2026-01-01T00:20:00Z --to 2026-01-01T00:22:00Z --output json
```

All commands accept `--account`, `--strategy-version`, a complete
`--provider/--market/--symbol` instrument identity, and RFC3339 `--from/--to`.
An account is required for forward-paper reports. `--stale-after` defaults to one
minute for their read-time valuation. Invalid scopes, ranges, partial instrument
identities, missing accounts/runs and incomplete backtests fail with an error.
Failed/cancelled run evidence remains available through `backtest report RUN-ID`.

## Metric boundaries

- One position lifecycle is one trade. Only closed lifecycles contribute to
  `closed_trades`, wins/losses/breakeven, rates, average win/loss, profit factor,
  expectancy and average holding seconds. Partial exits do not increase sample
  size. Open positions have a separate count and remain visible.
- Dates select the opening instant in `[from,to)`, retaining all subsequent
  partial/final exits of the selected lifecycle. Immutable strategy versions
  come from the entry order's signal, never from a position's stable strategy
  definition ID. Standalone manual entries have an explicit unavailable version.
- Gross P&L is settled price P&L before fees/funding and isolated exit write-offs.
  Net P&L includes funding and all fees charged, including remaining entry fees
  on open positions. The exact identity is
  `net = gross - fees + funding + isolated_deficit`. The deficit field contains
  exit-fill write-offs; funding is the actual collateral credit/debit retained
  on the position, whereas `ledger_funding` shows nominal ledger funding.
- Report P&L/fee/funding totals include realized portions of open positions. They
  exclude unrealized price P&L, which is represented through account equity.
  Closed-trade profit factor is the sum of positive **net** lifecycle P&L divided
  by the magnitude of negative net lifecycle P&L. Average loss is signed negative.
  Expectancy is total closed net P&L divided by closed sample size. Rates are
  fractions, not percentages. Decimal amounts remain exact strings; divisions
  use the project's decimal library's 16 fractional digit rounding.
- No trades means null rates/averages/expectancy. Profit factor is null with
  `no_trades`, `no_losses`, or `no_wins_or_losses`; a defined ratio has status
  `defined`. An absent initial entry stop means null planned risk and R, even
  if a later protection adjustment creates a stop. Planned risk is absolute
  entry-to-original-stop distance times initial quantity; planned R is the
  original target distance divided by stop distance; realized R is closed net
  P&L divided by that initial risk. Fees are included in realized R, not planned
  price-distance risk. Holding time runs from entry to final fill, in seconds;
  missing fill-clock evidence produces null holding time.
- `ledger_total` includes opening capital, trade cash and collateral transfers.
  `cash_balance` and `ledger_cash_reconciled` expose exact cash reconciliation;
  ledger fee/funding fields expose independent account ledger totals. These are
  account-wide values even when lifecycle filters select fewer trades; they are
  never labeled as the filtered subset's ledger or return.

## Equity and observation resolution

Replay stores actual account snapshots initially and after each primary open
and close, using its virtual event clock. Points retain event order at identical
close/open boundary timestamps, so entry fees cannot disappear through timestamp
deduplication. Spot equity adds marked remaining holdings to cash. Linear equity
adds reserved collateral and unrealized directional P&L, through the existing
`paper.ValueAccount` service. Exposure is absolute marked remaining notional;
usage is exposure/equity, null when equity is non-positive or unavailable.

Return is `(final equity - initial capital) / initial capital`. Maximum drawdown
is the largest observed peak-to-subsequent-trough absolute loss and fraction of
that peak. No intrabar equity path is invented. A missing/stale point makes
return and drawdown unavailable with an `incomplete` status. A legacy run with no
saved points reports `unavailable`. Forward-paper financial history did not
retain causal equity snapshots; it exposes only a current `snapshot_only`
valuation and since-opening return, with null historical drawdown. Current marks
past `--stale-after` produce null equity, exposure, leverage and return. Subset
filters suppress account equity/return/drawdown because account capital cannot
be attributed to a selected instrument/version/opening cohort.

MAE/MFE are initial-quantity price-P&L bounds from fully held OHLC bars, with an
explicit approximation label. Partial-size changes and intrabar order/path are
unknown; the final-exit bar and bars spanning the entry instant are excluded.
Backtests use frozen candles; forward paper uses retained accepted full-bar
observations available at report time. With no eligible bar they are null with
an unavailable label. These bounds are not claimed as exact intrabar cash P&L.

## Evidence

The agreed TDD seams were analytics APIs with hand-calculated expected metrics,
public PostgreSQL run/report/repository APIs, and CLI JSON. Tests first failed
for the new analytics package, immutable-version evidence, missing commands, and
retained forward observation excursions before their implementation.

| Independent example | Expected retained report |
| --- | --- |
| Two units enter 100; partial exits one at 110 and one at 120; 0.1% fees | One closed trade; gross `30`, fees `.43`, net `29.57`, ledger/cash `1029.57` |
| Two units enter 100; exits 110/120; supplied fees `4.3`, funding `-3`, initial stop 90 | Gross `30`, net `22.7`, initial risk `20`, realized R `1.135`, 7200 seconds, profit factor null/no losses |
| M4 long: .5 at 110, 1.5 funding debit, final 1.5 at 90 | Gross `-10`, fees `.39`, funding `-1.5`, net `-11.89`, ledger/cash `988.11` |
| M4 short: .5 at 90, 1.5 funding credit, final 1.5 at 110 | Gross `-10`, fees `.41`, funding `1.5`, net `-8.91`, ledger/cash `991.09` |
| M4 isolated short gap liquidation | Gross `-70`, fees `1.37`, funding `1.5`, exit deficit `44.625`, net `-25.245` |
| Fixture run: 10000 capital; entry notional 9000; price loss 100, fees 17.9 | Saved return `-.01179`, maximum drawdown `117.9`/`.01179`, closed net `-117.9`, cash `9882.1`, event-time exposure `9000` |
| Observed equity 1000 → 1200 → 900 | Return `-.1`, drawdown `300`/`.25`; a missing middle point makes return/drawdown null |
| Open spot: 2 units at 100; fee .2; current mark 100 | Cash `799.8`, equity `999.8`, exposure `200`, net charged costs `-.2`; stale mark makes valuation null |

Unit tests cover zero samples, no losses, breakeven, signed averages, missing
risk/time evidence, exact write-off reconciliation, OHLC bounds and missing
valuation. PostgreSQL tests exercise partial-exit aggregation, real fee/funding
ledger values, persisted equity, immutable-version filters, and unchanged ledger
and cash after repeated reads. CLI tests exercise both scopes, filters, exact
JSON decimals, persisted equity and invalid inputs.

Verification commands and results are recorded after the final branch checks.
No live monitoring or authenticated exchange operation is enabled. M2's
actual-host gate and the remaining M5 tickets/combined acceptance remain open.
