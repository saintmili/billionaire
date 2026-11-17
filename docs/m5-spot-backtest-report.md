# M5 spot fixture backtest — ticket #17

This records the initial #17 boundary; [ticket #18](m5-historical-replay-report.md) supersedes its single-timeframe/fixture-only selection limits.

Ticket #17 supplies the persisted run-to-report path. M5 remains in progress;
#18–#23 own historical datasets, linear execution, analytics, comparison/export
and combined acceptance. The pending M2 actual-host gate remains in force.
This workflow uses fixtures and simulated trading only.

## Run and inspect

```sh
go build -o ./bin/millionaire ./cmd/millionaire
./bin/millionaire init
./bin/millionaire strategy add examples/strategies/breakout.yaml --output json
# Substitute the immutable version.id printed above.
./bin/millionaire backtest run --strategy VERSION_ID \
  --provider fixture --fixture testdata/market/m3.json \
  --market spot --symbol BTC/USDT --timeframes 1m \
  --from 2026-01-01T00:20:00Z --to 2026-01-01T00:22:00Z \
  --seed 7 --capital 10000 --fee-rate 0.001 --slippage-bps 0 --output json
./bin/millionaire backtest report RUN_ID --output json
./bin/millionaire backtest run --source-run RUN_ID --output json
./bin/millionaire backtest run --help
```

`--strategy` accepts an immutable version ID. The run creates a fresh automatic
paper instance without changing the original definition/action. Entries pass
through existing `DecideSignal`, admission, sizing, fill and protection services.
Exit-rule signals retain the existing observation-only semantics; stops/targets
use existing protection. This initial slice supports fixture spot long
close-based strategies with one timeframe. Multi-timeframe, historical providers,
recorded updates and linear trading are rejected. CSV, comparison and analytics
arrive in subsequent M5 tickets. The command makes no network requests.

Time ranges have aligned inclusive starts and exclusive ends. Required preceding
lookback is selected automatically from the fixture and frozen. Missing lookback,
missing bars, conflicting revisions, invalid capital/fees/slippage/risk and
unsupported capabilities fail before run creation. Exact duplicate deliveries
are deduplicated. `--timeframes` must match the version.

Risk flags are `--fixed-risk`, `--percent-risk`, `--daily-loss`, `--max-positions`,
`--symbol-exposure`, `--max-leverage`, `--margin-percent`, `--cooldown-seconds`
and `--pyramiding`. They configure the same account risk policy as forward paper
mode; the version's percent risk also applies. Sizing uses actual modeled fill
price. A risk rejection is a completed simulation with an explicit rejected
order/reason. The deterministic model uses no randomness; seed remains recorded.

## Frozen inputs, clocks and results

Exact original candles, revisions/observation times, metadata, immutable strategy,
lookback and configuration are frozen in PostgreSQL before execution. The
manifest records their hashes, build/version, seed, model, virtual clock and end
policy. Provider-store corrections and removal of the original fixture cannot
change a saved run. `--source-run` uses frozen inputs and rejects changed input
flags. Reruns get independent run/account/instance identities; equal inputs and
build/model produce equal economic fills and balances.

The virtual OHLCV model makes closed candles available at their exclusive ends;
original observation provenance remains in frozen Inputs. Opening observations
contain only the open price. At a shared boundary, previous close/protection and
strategy evaluation precede the next open. Explicit `next_open` eligibility
requires both gate opt-in and an isolated owner namespace; forward owners reject
replay phases. No artificial nanosecond is added and no completed high/low is
exposed at opening. Existing conservative ambiguity/protection rules apply.

JSON reports include scope/status, manifest/configuration, isolated account ID,
signals, decisions, current order outcomes, fills, positions, exact final USDT
cash, ledger and warnings. Fill clocks are virtual; ledger `created_at` is the
wall-clock persistence time and must not be used as historical trade time.
Run IDs and audit times differ across reruns. Final cash is **not total equity**.
Open exposure and pending last-bar intents remain; no final fill is invented.
No automatic strategy optimization or statistical profitability claim is made.

The fixture's two-bar trading range follows twenty warm-up candles. Its breakout
closes at 11, but entry fills at the next open of 9 at 00:21 UTC. With capital
10,000, 1% risk, fee rate 0.001 and no slippage: quantity 1,000, stop 8.9,
price loss 100, total fees 17.9, final cash **9,882.1**. At 100 bps slippage the
opening price is 9.09. A range ending at 00:21 retains the signal as pending
without a fill. These are execution examples, not investment recommendations.

## Isolation, retention and failure recovery

See [ADR 0006](adr/0006-backtest-execution-isolation.md). Migration 0010 creates
main-schema `backtest_runs`. Its input/manifest/identity/namespace fields are
immutable; status/report updates are atomic. Each run uses a generated
`bt_<32 hex digits>` execution schema initialized through the same migrations.
The private static SQL routing wrapper contains runtime/history, global controls,
financial clocks, pending orders, positions, risk state, dedup and journal outbox.
Existing services remain authoritative. A held forward owner or forward kill
switch does not interfere. There is no notification or journal delivery.

The database role needs CREATE privilege on the application database in addition
to normal application rights. Missing schema privileges produce an identifiable
failed run. Schemas and frozen records are retained; no automatic retention or
delete command is supplied. Back them up together. Main-schema rollback does not
remove retained execution schemas; handle destructive rollback/retention
explicitly. Saved reports remain readable without consulting execution tables.

Execution errors persist `failed`; cancellation persists `cancelled` through a
bounded independent context. The CLI emits identifiable run/status JSON and
returns an error if execution started; validation errors have no run ID. A final
status persistence failure explicitly says the record may remain `running`.
A process crash can also leave `running`, which is never presented as complete.
Inspect it and use `--source-run` to create a fresh isolated replay; no automatic
resume is claimed. Inputs are never replaced from a provider during recovery.

Build metadata uses Go/VCS information when available. Builds without a revision
explicitly show `revision=unavailable`; dirty build metadata does not identify an
exact source tree. For reproducible release attribution use a clean checkout and
set the revision when producing the artifact:

```sh
go build -ldflags "-X github.com/saintmili/billionaire/internal/backtest.BuildRevision=$(git rev-parse HEAD)" \
  -o ./bin/millionaire ./cmd/millionaire
```

## Verification and extension boundaries

The agreed seams are run/report service, PostgreSQL repository APIs, CLI commands
and analytics APIs (later tickets). Vertical red/green slices established the
missing run service and CLI command. Focused real PostgreSQL checks cover exact
financial results, held forward ownership/pending orders, unchanged forward
accounts/signals/kill switch, distinct identities, frozen data after provider
corrections, deletion of the original fixture, restart/read reports, duplicate
delivery, changed slippage hashes, pending last-bar intents, risk rejection,
invalid configurations, schema privilege failure and cancellation while blocked
on migration ownership. Cancelled CLI JSON retains the run ID/status and returns
an error. The funding upgrade test now targets predecessor version 8 explicitly
instead of assuming funding remains the latest migration.

`internal/backtest/backtest.go` owns Config/Inputs/Manifest/Report.
`Store.RunBacktest`, `Store.BacktestInputs`, `Store.BacktestReport` are the public
application/persistence path. #18 can extend datasets/replay in
`postgres/backtest.go`, new `internal/backtest` files and CLI run flags. #20 can
add `internal/analytics`, `postgres/backtest_reports.go` and separate CLI reporting
files over saved reports. Coordinate shared Report fields; use virtual fill
clocks and position lifecycle aggregation, not ledger persistence timestamps.

#19 must explicitly address funding at OHLC boundaries: existing funding must
precede price observations at its effective instant, and it advances protection
state. Passing an overlapping completed bar afterward can skip protection.
The spot-only slice does not claim this is solved; do not hide it with +1ns.

Final command outcomes are appended after the full verification run.

Verified on 2026-11-17 against the disposable PostgreSQL 17 server, with
`MILLIONAIRE_TEST_DATABASE_URL` configured and `GOCACHE=/tmp/billionaire-go-cache`:

- `go test ./...` — passed, including all real PostgreSQL tests (PostgreSQL
  package 47.069s; CLI package 12.068s).
- `go test -race ./internal/store/postgres ./internal/cli -run TestBacktest -count=1`
  — passed (4.109s / 3.267s).
- `go vet ./...` — passed.
- `go build -o /tmp/billionaire-ticket17 ./cmd/millionaire` and the resulting
  `backtest run --help` — passed; all run/risk/recovery flags are documented.
- `git diff --check` — passed.

No live provider validation was attempted, and no later milestone is claimed.
