# M5 linear historical replay — ticket #19

Ticket #19 extends historical replay to isolated linear long/short positions.
All financial mutations use the shared paper owner, risk, funding, protection
and liquidation services inside the retained run schema. Forward ownership,
accounts, runtime state, risk controls and outboxes remain isolated. M5 combined
acceptance remains separate; M2 actual-host validation remains pending and live
monitoring stays disabled. Every operation is simulated.

## Run and inspect

```sh
billionaire backtest run --strategy VERSION --provider fixture \
  --fixture testdata/market/m3.json --market linear --symbol BTC/USDT \
  --timeframes 1m --from 2026-01-01T00:20:00Z --to 2026-01-01T00:22:00Z \
  --capital 10000 --fee-rate 0.001 --slippage-bps 0 --leverage 2 \
  --funding-file testdata/backtest/m5-linear-funding.json --output json
billionaire backtest report RUN_ID --output json
billionaire backtest trades RUN_ID --output json
billionaire backtest run --source-run RUN_ID --output json
```

Public and cached selection use the existing historical-data boundaries. Neither
path automatically fetches funding. `--funding-file` supplies JSON with separate
`expected_intervals` and `events` arrays. Each event includes its identity,
market-specific instrument, effective and availability clocks, exact signed rate,
explicit contemporaneous mark, source and provenance. Missing events or explicit
nil rates produce missing coverage; no declared schedule produces **unknown**
coverage. An empty event list never proves zero funding. This coverage describes
only the caller-declared schedule; the application cannot prove that schedule is
the venue's complete settlement calendar. Sample funding settles after the
fixture's stop, so its application list is empty.

Expected intervals and exact deduplicated supplied payloads are frozen in
`BacktestInputs.Config.Funding`, incorporated in the dataset/configuration hashes,
and retained with the run. Unknown historical funding cannot be recovered by a
frozen rerun. Identical duplicate events are idempotent; conflicting intervals or
reused event identities fail before provisioning a run. Original input files,
provider cache changes and current upstream strategy versions cannot change a
saved run.

`--leverage` selects isolated entry leverage, subject to the shared risk policy;
spot still requires 1. Optional absolute `--stop-price` and `--target-price`
override the strategy's proposed protection. `--target-quantity` configures one
partial target and requires `--target-price`; `--trail-activation` and
`--trail-distance` configure the existing trailing service. These settings flow
through signal decisions and the common order/risk validation, with precision,
directional geometry, collateral and holdings restrictions preserved. Unspecified
stops and targets retain the strategy's proposal. Invalid or unaffordable entry
geometry produces an explicit rejected decision/order, never an invented fill.

`--maintenance-rate` and `--liquidation-fee` each default to `0.005` for linear
runs. Exact selected rates are frozen in configuration and copied to orders,
positions and report coverage. This approximate isolated liquidation model has
no venue maintenance tiers, index/mark reconstruction or insurance fund model.
A liquidation gap cannot take collateral from another position or available
cash: explicit isolated-deficit evidence reconciles the capped settlement.

## Causal phases and capability limits

[ADR 0007](adr/0007-replay-interval-funding-point-phases.md) records the accepted phase and capability decision. The linear manifest names `linear-isolated-ohlcv-interval-funding-point-v1`.
The general replay phase contract is version 3. At one virtual clock:

1. Publish all available completed timeframes into causal strategy history.
2. Settle the completed primary **interval**, using the protection state valid
   over that interval. Its exclusive end belongs to the next point phase.
3. Apply funding for exposure opened strictly before that boundary, including
   remaining holdings after any partial target. Evaluate liquidation/protection
   against the explicitly supplied funding mark after collateral changes.
4. Publish the funding mark for its equity snapshot, then the completed close
   point for evaluation, risk and close equity. Evaluate the primary strategy and
   create entry decisions.
5. Observe the next actual primary open and fill eligible pending intents.

Funding therefore precedes same-clock **point** observations. The prior completed
interval is settled first, rather than applying a future funding-adjusted state
retroactively to the bar. Two narrow isolated-only phase gates permit a funding
point or actual next open to follow interval settlement at the same timestamp.
Forward-paper strict-after clocks and overlapping-bar protection stay intact.
There are no synthetic nanosecond clocks and no second financial engine.

Trailing uses the stop valid before its interval and ratchets for a later
observation. It cannot use the bar's favorable extreme to stop out within that
same bar. A separately supplied funding mark or actual next opening price is a
later point phase and may trigger that ratcheted stop, including a gap. A close
point summarizing the interval does not invent another protection opportunity.
Adverse-first stop/target ambiguity, partial-target consumption, liquidation
priority and adverse opening-gap execution remain the shared engine's rules.
Only primary bars drive execution; higher frames cannot apply protection twice.

Funding strictly inside a primary OHLC interval is rejected explicitly. Choose
an aligned lower primary timeframe with finer observations; OHLCV cannot split
an interval at an unknown intrabar path. Funding must be available at its
effective time with an explicit contemporaneous mark; delayed funding is
rejected. `recorded-close` plus supplied funding is also rejected because delayed
completed intervals can overlap funding state. Recorded-close without funding
retains the earlier causal publication/revision behavior and labels funding
coverage unknown. These failures prevent quietly skipped protection or
retroactive payments. Update-mode replay remains unsupported.

Boundary funding at the final close can settle exposure held over the last
completed interval. The end policy retains remaining positions and pending
intents, with explicit warnings; it never fabricates a tradable final price or
forces a closing order. Reports include funding applications/coverage, ledger,
rejected decisions, unfilled orders, open exposure, approximate-liquidation
parameters and fill ambiguity/gap/isolated-deficit evidence. Repeated report reads
have no financial effects. Equal-time fills follow interval/funding/open phases;
equity preserves distinct phase snapshots and the supplied funding mark.

## Verification evidence

Approved TDD seams: public backtest configuration, PostgreSQL repository and CLI
interfaces. Red/green slices first rejected linear configuration, then failed
missing funding results, failed absent protection configuration, failed unknown
CLI flags, exposed incorrect funding-mark equity, rejected identical duplicate
funding inputs, and failed missing frozen liquidation parameters. Passing slices
were committed as they became reviewable.

Focused real PostgreSQL checks include long debit and short credit, entry/exit
fees, partial target before funding, unchanged remaining collateral, trailing
activation versus a later boundary mark, funding-triggered approximate liquidation
and isolated-deficit caps, liquidation before a wider stop, opening gaps,
ambiguous stop/target bars, prior-interval protection immediately before funding,
unknown/missing/complete coverage, duplicate events, frozen reruns, unavailable
clock rejection, forward-owner denial and account/ledger isolation. CLI checks
cover linear selection, leverage, missing coverage, JSON and rerun after deleting
the funding source file. Existing shared-service tests cover additional funding,
protection, risk rejection and reconciliation cases.

Final verification commands/outcomes are appended below after execution. No
live venue, credentials, daemon, journal delivery, tracker mutation, push or PR
is involved; this slice does not claim M5 combined acceptance or any later
milestone.

Verified on 2026-11-17 against the provided disposable PostgreSQL server with
`BILLIONAIRE_TEST_DATABASE_URL` configured and `GOCACHE=/tmp/billionaire-go-cache`:

- `go test ./...` — passed, including real PostgreSQL (66.117s) and CLI (15.415s).
- `go test -race ./internal/backtest ./internal/paper ./internal/store/postgres ./internal/cli -run 'TestBacktest|TestFunding|TestLinear|TestTrailing|TestRecorded|TestFreeze' -count=1`
  — passed; PostgreSQL 27.553s and CLI 5.685s.
- `go vet ./...` — passed.
- `go build -o /tmp/billionaire-ticket19 ./cmd/billionaire` and the resulting
  `backtest run --help` — passed; linear, funding, protection, leverage and
  liquidation assumptions are exposed.
- `git diff --check` — passed.

The assigned branch descends from and was merged with integration tip
`22ab5ce3e2ba56f6ef606dca86d5db9fa0b14ef0`, incorporating completed #20 and #21.
No migration was added for #19. Funding acquisition remains explicit; no live
provider validation or venue-realistic funding/calendar guarantee is claimed.
