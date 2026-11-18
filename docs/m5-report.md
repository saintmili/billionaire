# M5 offline acceptance and operation

M5 offline acceptance was verified on 2026-11-18 (project date), following the
approved offline sequencing exception. Tickets #17–#22 supply isolated historical
replay, trade/signal analytics and comparison/export; #23 joins those paths and
records the milestone gate. The M2 actual-host REST/stream validation gate remains
pending. Live monitoring remains disabled. All trading is simulated; no exchange
credentials or authenticated trading API is involved. Stop at M5: daemon and
notifications (M6), web (M6W), journal delivery (M7) and release (M8) remain pending.

The [independent M5 review and resolutions](m5-review.md) records the separate
Standards/Spec findings, complete-decision persistence fix, CLI parsing cleanup,
and follow-up verification.

## Run, report, compare and export

Use the PostgreSQL setup in the [root guide](../README.md). The application role
needs database CREATE privilege for retained per-run schemas as well as normal
schema/table rights. The initialized forward account is never the replay account.

```sh
go build -o ./bin/billionaire ./cmd/billionaire
export BILLIONAIRE_DATABASE_URL='postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=disable'
./bin/billionaire init
./bin/billionaire strategy add examples/strategies/breakout.yaml --output json
# VERSION is version.id, the immutable content hash from strategy add.
./bin/billionaire backtest run --strategy VERSION \
  --provider fixture --fixture testdata/market/m3.json --market spot \
  --symbol BTC/USDT --timeframes 1m \
  --from 2026-01-01T00:20:00Z --to 2026-01-01T00:22:00Z \
  --seed 7 --capital 10000 --fee-rate 0.001 --slippage-bps 0 --output json
# RUN is id from the completed run. RERUN is the new id from --source-run.
./bin/billionaire backtest report RUN --output json
./bin/billionaire backtest trades RUN --output json
./bin/billionaire backtest signals RUN --threshold-percent 5 \
  --stop-percent 50 --window-bars 1 --output json
./bin/billionaire backtest run --source-run RUN --output json
./bin/billionaire backtest compare RUN RERUN --output json
./bin/billionaire report strategies --scope backtest --run RUN,RERUN --output json
./bin/billionaire backtest export RUN --kind report --format json > report.json
./bin/billionaire backtest export RUN --kind trades --format csv > trades.csv
./bin/billionaire backtest export RUN --kind equity --format csv > equity.csv
```

Signal reports require an explicit hypothetical rule and persist that rule and
evidence; comparison/export only include previously saved signal reports. Generate
the same selected rule on each run when comparing its outcomes. `report signals
--saved ID` reads the immutable report. Success measures subsequent observations
relative to proposed signal price; it does not infer success from realized profit.
The ordinary fixture's losing trade can still have a hypothetical successful 5%
target/50% stop/one-bar observation. Ambiguous, missing and incomplete windows
remain explicit unknown outcomes.

JSON/CSV export schema version 1 is documented in the
[comparison/export report](m5-compare-export-report.md). Exact decimal amounts are
strings; null means unavailable. CSV columns are
`schema_version,kind,row_type,sequence,context_json,row_json`; parse CSV and then
its JSON cells. Context retains manifest, configuration, metrics, rule and full
artifacts. Empty collections carry a context row. Export does not generate a new
signal rule, rerun trading or consult current prices.

For linear replay, use `--market linear --leverage 2` and
`--funding-file testdata/backtest/m5-linear-funding.json` with the same base command. Funding is
explicit caller-supplied history, never automatically acquired. Optional
`--stop-price`, `--target-price`, `--target-quantity`, `--trail-activation`,
`--trail-distance`, `--maintenance-rate`, `--liquidation-fee` and the shared risk
flags are recorded in configuration. The sample funding file supplies a complete
declared boundary after the ordinary fixture's stop, so no exposure receives its
payment; this differs from the funded partial-exit acceptance fixture below.

Fixture selection reads the exact named file even when the cache is populated.
After metadata/history are cached, `--cached` reads only PostgreSQL and excludes
`--fixture`/`--refresh`. For reachable public history, replace selection with
`--provider bybit --market spot --symbol BTC/USDT`; `--refresh` explicitly refetches.
The existing public synchronization boundary handles pagination, precision and
coverage. Actual venue connectivity is not claimed: CLI tests serve Bybit-shaped
history through a local HTTPS server, then stop it and verify cache-only replay.

Ranges are aligned `[from,to)`; required preceding lookback is frozen automatically.
Missing lookback always fails. Other gaps fail unless `--acknowledge-gaps` records
them; acknowledgement cannot invent an open or make missing history ready. CLI
OHLCV selection supports close-based versions; update mode and `recorded-close`
acquisition are rejected. The service's explicitly supplied recorded closed-
revision datasets have the narrower limits in the
[historical replay report](m5-historical-replay-report.md).

## Combined acceptance evidence

New joined regressions are at the agreed public backtest/PostgreSQL, analytics
and CLI seams. Database SQL installs deterministic failure guards; financial and
report assertions use public interfaces. Existing lower-level causal and funding
fixtures remain the source of their detailed evidence.

| Gate | Joined or retained evidence and exact outcome |
| --- | --- |
| Fixture/historical selection through spot reports and exports | `TestBacktestCLICompleteFixtureJourney`: fixture, changed fixture despite cache, local HTTPS public history, network-free cache, frozen rerun, trade/signal reports, compare and JSON/CSV all pass. Entry 1,000 at 9; stop 8.9; gross `-100`, fees `17.9`, net `-117.9`, cash `9882.1`, return `-0.01179`, drawdown `117.9`, one closed lifecycle. |
| Linear partial exit → funding → reports/export | `TestM5CLICombinedLinearAnalyticsFrozenExportAndTrainTest`: same initial 1,000 at 9 with isolated leverage 2, altered last primary bar low 9/high 13/close 12, target 12/quantity 400. Remaining quantity `600`, margin `2628`, funding `-72` (600 × 12 × .01), fees `13.8`, realized gross `1200`, net including costs/funding `1114.2`, cash/ledger `8486.2`, final marked equity `12914.2`, return `.29142`, observed drawdown `9`/`.0009`, zero closed lifecycles and one open position. |
| Signal definition and retained provenance | Linear 5% target/50% stop/one-bar rule yields one generated candidate, one filled entry and hypothetical success. Reports and JSON/CSV retain the exact funding mark/source/provenance, maintenance/liquidation rates and saved success rule after both original files are deleted. |
| Frozen determinism and changed assumptions | New identities with the same manifest reproduce cash and signal outcome; equivalent selected signal reports compare without audit-ID differences. Fee .002 changes configuration/reproducibility hashes and `config.fee_rate`, makes runs incomparable and changes cash to `8472.4`. Existing changed-fixture tests expose dataset hashes; provider corrections and unavailable original strategy/fixture do not rewrite frozen runs. |
| Complete forward isolation | `TestM5ReplayAndReportsCannotWriteForwardState` seeds a real forward signal/decision, filled linear position, risk policy and kill switch while retaining the forward owner. Denial triggers guard strategy definitions/versions/instances (including runtime), evaluations, signals, decisions, controls, risk policies, accounts, ledger, orders, positions, fills, price/funding events and journal outbox. Both spot/linear replay, trade/signal reports, compare and export succeed. Public forward snapshots, bindings, risk and kill switch remain unchanged; the owner can retry its original decision. A public control mutation proves the guards reject forward writes. |
| Compatible normal paper economics | `TestM5ReplayMatchesCompatibleNormalPaperEconomics` uses the same version/warmup/signal and 9 entry/8.9 stop through the ordinary owner. Gross `-100`, fees `17.9`, net `-117.9`, cash `9882.1` match replay. Duplicate price and strategy events add nothing and retain runtime state. A normal price arrives one microsecond after the signal, honoring forward strict-after semantics; replay uses its modeled same-boundary next open. Agreement is financial under these compatible observations, not a claim of equal latency or general intrabar equivalence. |
| Failure, cancellation and restart | Existing run/CLI tests retain failed/cancelled IDs, nonzero command errors and readable audit reports when provisioning permissions fail or cancellation interrupts the migration lock. `TestM5FinalReportFailureStaysIncompleteAfterRestartAndRecoversFromFrozenInputs` injects a final report write failure: the error names uncertain persistence, a reopened Store reads `running`, and comparison has no completed metrics. A fresh frozen replay completes with `9882.1` while preserving the original unfinished record. |
| Causal inputs and execution edge cases | #17–#19 tests retain multi-timeframe future traps, exact-boundary higher closes, later corrections, unavailable warmup, acknowledged/unacknowledged gaps, rejected updates, duplicate events, delayed final intents, partial targets, ambiguous stop/target bars, trailing ratchets, long/short funding, complete/missing/unknown funding and approximate isolated liquidation. They are run in the combined full/race suites. |

The comparison defect found by the joined CLI test was reproduced before fixing
production code. Previously saved signal reports compared random run/report/
evidence/signal IDs as differences. Comparison now uses the retained causal signal
with its execution identity removed, selected rule/filter/observation clock and
outcomes. Snapshot and difference payloads keep original IDs and evidence hashes.
Inherited run warnings remain in report context; they do not make an execution's
random pending-order ID a changed signal assumption. Unknown signal references
remain literal; changed causal time, rule, outcome or observation clock still
produce a signal-report difference. The public Compare test also verifies inputs
are not mutated. Focused and combined checks cover this fix.

## Separate chronological train/test examples

Actual CLI process outputs, formatted as JSON, are checked in as
[training trade report](examples/m5-train-trades.json) and
[test trade report](examples/m5-test-trades.json). They use unchanged
`examples/strategies/breakout.yaml`, fixture BTC/USDT spot/1m, seed 7, capital
10000, fee .001 and slippage 0. Each run starts fresh; test history can include
preceding training candles solely as warmup, without carrying signals, positions,
capital or runtime. The fixture's available range is intentionally tiny.

| Sample | Range on 2026-01-01 UTC | Signals / pending / closed trades | Return / drawdown / cash |
| --- | --- | --- | --- |
| Train | `[00:20,00:21)` | 1 / 1 / 0 | `0` / `0` / `10000` |
| Test | `[00:21,00:22)` | 0 / 0 / 0 | `0` / `0` / `10000` |

These are separate example reports, not evidence of predictive performance.
Zero closed-trade samples have null win rate/expectancy/profit factor. The final
training intent never receives an invented fill, and is not transferred into the
test run. The periods are disjoint: comparison has null matched period and warns
that results are not directly comparable. IDs in these checked-in examples refer
to a disposable acceptance database that was removed; recreate the samples to
obtain local usable run IDs.

Reproduce by running the base spot command twice with the table's date bounds,
then `backtest trades TRAIN` / `backtest trades TEST` and
`backtest compare TRAIN TEST`. The joined CLI regression verifies those exact
counts, pending semantics and zero-sample metrics. Do not tune a strategy against
the test period or select only successful instruments/periods: that introduces
look-ahead/selection bias. Survivorship, tiny samples and missing historical
revisions prevent a profitability inference. No automatic optimization is
performed; the examples choose no "best" strategy.

## Recovery, retention and operating limits

Validation failures return nonzero without creating a run. Once creation starts,
execution failure/cancellation returns an identifiable failed/cancelled JSON run
and nonzero error. Read it with `backtest report ID`; analytics do not invent
completed results. A final status write error explicitly says it may remain
`running`; a crash can have the same durable outcome. Inspect/retain that record,
then use `backtest run --source-run ID` for a fresh isolated execution. No in-place
resume, stale-run repair or automatic crash recovery is claimed. Do not substitute
new provider data into the old run. Stop/kill-switch changes to forward operation
neither cancel nor rewrite an isolated backtest.

[ADR 0006](adr/0006-backtest-execution-isolation.md) requires retaining the main
registry's immutable inputs/manifests/results/signal reports together with its
`bt_<run-id>` execution schemas. There is no automatic deletion/retention command;
each run has schema/migration/storage cost. Back up both ownership areas and
restore together. Registry migrations/rollback do not automatically remove the
execution schemas. Saved trade reports/compare/export can read frozen results
without execution schemas, but loss of registry inputs prevents a verified frozen
rerun and is explicitly unavailable evidence. This acceptance does not exercise a
backup/restore drill or promise deletion tooling; release operations remain M8.

A frozen dataset includes selected lookback, exact original observations,
metadata, strategy, fees/slippage/risk, funding and model. Current cache/provider
changes cannot update it. Build attribution is only as good as recorded VCS data:
test binaries may show `revision=unavailable`, and modified builds do not identify
an exact tree. Keep a clean source revision, Go/dependency versions and binary;
set `-ldflags "-X github.com/saintmili/billionaire/internal/backtest.BuildRevision=REVISION"`
when producing an attributed binary. Equal seeds alone do not prove equivalence;
this model uses no randomness. Unknown/dirty builds remain warned and cannot
establish authoritative direct comparability even when economic results match.

OHLCV is an approximate observation model, with adverse-first ambiguous bars and
no inferred intrabar path. Return/drawdown use retained sampled equity, not an
unobserved intrabar curve. Cash excludes open holdings/collateral; read equity,
open positions and sample size with return. Forward historical drawdown and
unobserved hypothetical missed fills are unavailable. Funding coverage proves
only the caller's declared schedule. Unknown/missing funding is not zero cost;
funding inside a primary interval, delayed funding and recorded-close revisions
combined with supplied funding are rejected per
[ADR 0007](adr/0007-replay-interval-funding-point-phases.md). Approximate isolated
liquidation has no venue tiers, insurance fund or reconstructed mark/index basis.
No live venue, real execution or investment-performance claim follows.

## Verification

Commands ran with `GOCACHE=/tmp/billionaire-go-cache` and
`BILLIONAIRE_TEST_DATABASE_URL=postgres://postgres@127.0.0.1:55435/postgres?sslmode=disable`
on the disposable local PostgreSQL server. Integration tests created/dropped
independent databases; none were skipped for an unset URL.

- `go test ./... -count=1` — passed; PostgreSQL `112.364s`, CLI `20.064s`.
- `go vet ./...` — passed.
- `go build -o /tmp/billionaire-m5-acceptance ./cmd/billionaire` — passed;
  built-binary run/compare/export/strategies help passed.
- Separate built-binary processes executed init, immutable strategy add, both
  chronological fixture runs, trade and signal reports, comparison and JSON/CSV
  export; all exited zero. The disposable database was removed after saving the
  example reports. The complete funded linear journey uses the same CLI command
  handlers against PostgreSQL in the joined acceptance test.
- `go test -race ./internal/analytics ./internal/backtest ./internal/paper
  ./internal/strategy ./internal/marketdata/... ./internal/store/postgres
  ./internal/cli -count=1` — passed in all packages; PostgreSQL `112.530s`,
  CLI `27.760s`. This includes concurrent ownership/risk/event tests and real
  PostgreSQL acceptance cases.
- `go build ./...` and `git diff --check` — passed.
- Final deterministic fill/equity/metric assertions: `go test ./internal/cli
  ./internal/store/postgres -run TestM5 -count=1` — passed against PostgreSQL
  (CLI `2.890s`, PostgreSQL `5.473s`).
- Baseline `b60dd9e` was verified as an ancestor before work. Final
  `git merge --no-edit integration/m5-backtesting-analytics` reported already
  up to date at `b60dd9e`; integration ancestry check passed.

The historical #17–#22 slice reports remain useful implementation evidence:
[spot](m5-spot-backtest-report.md), [historical](m5-historical-replay-report.md),
[linear](m5-linear-replay-report.md), [trades](m5-trade-report.md),
[signals](m5-signal-report.md), [comparison/export](m5-compare-export-report.md).
Their "in progress" statements describe their slice dates; this combined report
owns the current M5 acceptance decision. Earlier M4 evidence is unchanged.
