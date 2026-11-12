# M4 manual spot execution — ticket #2

Implemented 2026-11-12 against baseline `d75edf3`, for
[issue #2](https://github.com/saintmili/billionaire/issues/2). This is the manual
spot slice of M4; broader risk, linear margin, signal execution, protective exits,
funding/liquidation and combined acceptance remain tracked in #3–#8. M2 live-host
validation remains pending under the approved offline sequencing exception.

## Changes and commands

- `internal/paper/execution.go`: transport-independent intent validation and fill
  planning, including next-observation eligibility, adverse precision rounding,
  fees/slippage, holdings/cash checks and partial/final cost allocation.
- Migration `0005_spot_execution`: independent orders, fills, positions and durable
  price-event replay records; ledger entries link to fills. Exact decimal strings
  in JSON preserve financial values and original nanosecond clocks. SQL timestamp
  columns aid queries, while event sequence and original clocks govern ordering.
- PostgreSQL owner methods submit intents and deliver observations on the existing
  dedicated ownership session. Each observation commits financial updates, order
  decisions and its replay result atomically; failed observations are retryable.
- CLI `paper open`, `paper close`, `paper observe`, `paper positions`, `paper orders`
  and `paper fills`. All accept text/JSON output. Explicit fixture timestamps are
  required; no live network path or daemon is introduced.
- `testdata/paper/spot-*.json` supplies deterministic metadata/price examples. The
  README contains a complete entry/partial/full-close workflow and assumptions.

Orders freeze instrument metadata and taker fee/slippage assumptions. Quantities
must match their step and minimum without rounding. Buy prices round up to ticks,
sells down; monetary debits/fees round up and sale proceeds down to 18 places.
Partial cost allocation uses proportional 18-place half-away rounding, with the
final exit consuming all residual cost. Entry fees are included in acquisition
cost; exit fees reduce proceeds, so realized P&L includes both. Prices, quantities
and notionals are bounded by 10^18 and 18 fractional places. Maker fees do not apply
to these market orders. No new dependency was added.

A usable price must be source-observed and available strictly after intent time,
and available by the processing clock. Missing/old-known prices leave orders pending;
unavailable future observations fail without delivery. Stale/unready prices reject
execution. Disabled instruments reject entries while explicit closes can still use
fresh prices. Pending orders reserve no funds: account/position locks and deterministic
intent-time/ID ordering prevent overspending/overselling. General portfolio and
strategy risk policy follows in #3. Each entry opens a separate long position;
shorting, borrowing, leverage and reversal are rejected.

## Verification

The user confirmed CLI, PostgreSQL and public execution-policy test seams and review
baseline `d75edf3`. Focused red/green checks established later-price eligibility,
pending/restart persistence, the manual CLI lifecycle, processing latency, and the
nanosecond event-ordering regression.

Passed:

- Public policy tests: entry fees/slippage, adverse ticks, precision/minimums, invalid
  inputs, insufficient cash, disabled/stale/unready sources and partial/final P&L.
- PostgreSQL tests: intent and event idempotency, restart, changed-input conflicts,
  unavailable/out-of-order events, nanosecond clock preservation, concurrent cash
  protection and overlapping-close holdings protection.
- Injected order/account UPDATE and fill/position/ledger/outbox/event INSERT failures
  leave cash, pending order, positions, fills and ledger unchanged through public
  reads; retrying the same event then succeeds.
- CLI fixture scenario: 1000 USDT, buy 2 at 100, sell 0.5 at 110, sell 1.5 at 90,
  0.1% fees => final cash 989.61, net realized P&L -10.39, fees 0.39 and seven ledger
  entries including opening funds. Repeated delivery creates no extra fill.
- `go test ./...` with disposable PostgreSQL 17 enabled, `go vet ./...`,
  `go build ./...`, and `git diff --check`.

Race validation and independent standards/spec review are recorded after completion.

## Remaining gates

No authenticated exchange/order API, live monitoring, M5 replay product, M6 daemon
or M7 delivery is introduced. The application records financial outbox events but
does not invent StopLore capabilities. M2 live-host validation, the supported-Go
patch-refresh release gate and StopLore licensing remain open. Stop after #2;
M4 is not complete.
