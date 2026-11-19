# M6 monitored public funding (#29)

`app.Run` retrieves expected linear funding intervals before consuming each price
observation, through the same pinned financial owner. `bybit.Client` implements
`app.FundingSource` automatically; `fixture.Provider` supplies explicit events
from the market fixture's optional `funding` array. Spot feeds are unaffected.
The application result includes `funding` outcomes and `funding_coverage`:
`unknown`, `no_due_interval`, `supplied`, or `missing`. Retained outcomes remain
available through `paper funding list --output json` after shutdown or restart.

## Verified public contracts

Directly checked the Bybit primary documentation on 2026-11-19:

- [Funding history](https://bybit-exchange.github.io/docs/v5/market/history-fund-rate):
  public `GET /v5/market/funding/history`, linear category and symbol, explicit
  start/end milliseconds, maximum 200 rows. Pagination moves the inclusive end
  before the oldest returned timestamp; records are normalized chronologically.
- [Instrument metadata](https://bybit-exchange.github.io/docs/v5/market/instrument):
  public `GET /v5/market/instruments-info` supplies funding interval in minutes.
  Only active USDT linear perpetuals are accepted.
- [Tickers](https://bybit-exchange.github.io/docs/v5/market/tickers): public
  `GET /v5/market/tickers` supplies the next funding timestamp, anchoring the
  interval schedule. The future anchor is refreshed when a settlement becomes
  due; no history requests are made between known expected settlements.
- [Mark candles](https://bybit-exchange.github.io/docs/v5/market/mark-kline): public
  `GET /v5/market/mark-price-kline` supplies the exact settlement minute's open.
  Its high, low and close are never used as settlement prices. This public
  valuation is an explicitly approximate simulator input.

No authenticated endpoint, exchange credential or order submission is introduced.
`FundingEvent` retains the instrument, provider provenance, interval minutes,
schedule anchor, effective time and actual retrieval availability time. Retrieval
is never backdated to settlement. The current public interval and next timestamp
are extrapolated backward over the requested window; they cannot establish past
schedule changes. This assumption is included in each event's provenance. A
window exceeding 10,000 expected settlements is refused explicitly.

## Ordering and recovery

The durable per-instrument funding outcome is the checkpoint, including explicit
funding applied before engine startup. Initial coverage starts at the configured
warm-up boundary or the oldest open exposure, whichever is earlier. Every
returned interval commits through `ApplyMonitoringFunding` before price
execution, strategy evaluation and subsequent signal intents. Multiple timeframe
workers share this serialized engine path and durable cursor. No additional
process, lease, scheduler database or migration is introduced.

Only exposure opened strictly before settlement is eligible. Existing exact
signed funding, isolated collateral limits, ledger/outbox transactions and
approximate liquidation rules apply. A mark can trigger the existing protective
or approximate liquidation exit before the subsequent price. A price received
before the funding retrieval's actual availability remains subject to the shared
out-of-order checks; it cannot move financial state backward, and the next valid
observation resumes processing. This forward service uses observed price points;
it does not reinterpret completed OHLC intervals using replay-specific phases.

Missing history rows, history outages and missing marks produce explicit missing
intervals, never known zero rates. Schedule/anchor failure is an actionable
engine error because the expected interval itself is unknown. Existing network
bounds, retry limits, cancellation and safe diagnostics apply. A missing
monitoring interval is marked `coverage_only`: its availability does not block
later protection or price management, and it changes no balances.

A rate discovered after price execution or position management has reached its
effective time becomes missing coverage with its `reported_rate` retained. No
retroactive correction is applied to current holdings. Such intervals remain
approximate even if the provider later supplies a rate. First outcomes are
immutable: repeated monitoring submissions return the original recorded result,
including its original availability, rather than rewriting an interval. The
unique instrument/settlement constraint and transaction protect against duplicate
charges. Restart advances from the last committed interval; a failed subsequent
price/checkpoint cannot charge it twice. Several missed settlements can become
missing coverage once an earlier settlement advances the position management
clock; this preserves the existing causal financial boundary.

## Reproduce offline

Use an initialized disposable database and configure one linear BTC/USDT feed
with timeframe `1m`, `engine.lookback: 1`, and `engine.queue_capacity: 1`:

```sh
billionaire --config /tmp/funding.yaml init
billionaire --config /tmp/funding.yaml --output json start --fixture testdata/market/m6-funding.json
billionaire --config /tmp/funding.yaml --output json paper funding list
```

The shipped synthetic fixture declares positive, negative and missing one-minute
intervals. An empty fixture funding array means coverage is unknown. Existing
`paper funding apply --offline --fixture FILE` remains available for explicit
single-event fixtures. Foreground funding needs no separate flag. Live startup,
health/reconnect enablement and deployed service acceptance belong to #33–#35;
this slice retains the existing CLI live gate.

## Verification

Red/green provider and PostgreSQL tests first failed for the missing adapter,
monitoring owner method and premature history polling. Targeted tests cover:

- Public schedule identity, positive/negative exact rate parsing, mark open,
  missing intervals, outages, malformed rates/instruments, 201-settlement
  pagination, overlap identity and cached no-due behavior.
- Actual PostgreSQL application settlement before price, an injected consumer
  failure after committed funding, repeated restart, signed collateral changes
  of -2 and +2, missing coverage and exact ledger/cash reconciliation.
- Delayed rates retained without retroactive charges, immutable first outcomes,
  owner reacquisition and price/protection progress after missing coverage.
- Shipped fixture through CLI startup, shutdown, restart and structured inspection.
- Existing funding positive/negative long/short reconciliation, funding-triggered
  approximate liquidation, duplicate concurrent replay and transaction rollback.

Commands use a dedicated pinned disposable PostgreSQL Compose project; no normal
application database is modified. Practical race tests run on the changed public
seams. Final assembled-source repository verification is performed by the
integration milestone owner. No later ticket or milestone is implemented here.

Completed verification (disposable PostgreSQL 17):

```sh
go test ./...
go vet ./internal/app ./internal/marketdata/bybit ./internal/marketdata/fixture ./internal/store/postgres ./internal/cli
go test -race ./internal/app ./internal/marketdata/bybit ./internal/marketdata/fixture ./internal/store/postgres ./internal/cli -run 'TestPublicFunding|TestMonitoringDelayed|TestForegroundFunding|TestCLIForegroundFunding|TestFunding' -count=1
```
