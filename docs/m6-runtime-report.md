# M6 foreground runtime slice (#25)

**Current operation (2026-11-19):** This dated slice report retains its original gates and test evidence. The accepted local-host decision and subsequent funding, live recovery/health and systemd implementation supersede earlier startup restrictions; see the [combined M6 report](m6-report.md) for current scope, verification and limits.

The foreground engine runs configured subscriptions through the existing market
synchronizer, immutable strategy evaluation, signal decisions, risk gate and paper
execution services. This is offline fixture acceptance only. M2 actual-host
validation remains pending and `start` rejects operation without `--fixture`.
The executable is `billionaire` (the ticket's `millionaire` spelling is a typo).

## Run

Use a disposable initialized database and the existing paper account/strategy
commands with explicit `--offline` to create accounts and enable instances while
the owner is stopped. Bind instances to the `fixture`
provider when running fixtures. The fixture must cover every configured instrument
and all warm-up requirements. For the shipped M2 dataset, add this to a copy of
`configs/example.yaml` (its four subscriptions are supported):

```yaml
engine:
  queue_capacity: 64
  lookback: 1
  quantity: risk
```

```sh
export BILLIONAIRE_DATABASE_URL='postgres://...'
go run ./cmd/billionaire --config /tmp/engine.yaml init
go run ./cmd/billionaire --config /tmp/engine.yaml --output json start --fixture testdata/market/m2.json
go run ./cmd/billionaire --config /tmp/engine.yaml --output json status
```

JSON foreground output is newline-delimited objects: engine starting, feed/event
results (including emitted signals), and engine stopped. Fixture exhaustion stops
normally; add `--hold` to retain ownership and the private control socket after
fixture exhaustion until interrupted. SIGINT/SIGTERM uses the existing root cancellation context. `status`
reports running/stopped by observing the engine ownership fence.

Defaults when `engine` is absent or numeric fields are zero: queue capacity 64,
lookback 20 intervals per subscription, quantity `risk`. Queue capacity is capped
at 4096, lookback at 10000; at most 128 subscriptions are admitted. Quantity can
be `risk` or a positive exact base quantity. Signal intents retain the existing
1x leverage, 0.1% taker fee and zero adverse slippage defaults. The shipped M2
fixture has one hour of history, so the normal 20-interval default cannot warm
its 15-minute subscriptions; explicitly use `lookback: 1`. Strategy-required
history is fetched separately and can still fail if absent.

## Ownership, processing and recovery

`internal/app.Run` is the foreground application boundary, accepting configured
subscriptions, processing options and an optional result callback. It supervises
one worker per subscription and serializes evaluation/financial processing.
Each subscription has a separate fixture clock, avoiding cross-feed clock races.

The engine acquires the existing PostgreSQL paper ownership lock and an additional
strategy evaluation lock on the same pinned, non-reconnecting session. Other
paper owners and standalone strategy evaluators are rejected while it runs.
Evaluation and financial writes use that owner session; losing it cannot reconnect
through a pool and write again. Idle ownership is checked every second with a
2-second timeout. Independent report reads remain possible. Subsequent ticket #26
now routes default CLI mutations through the private Unix socket, with atomic
original-outcome receipts. Explicit `--offline` fixture/setup mutations require
the owner stopped. See the [control report](m6-control-report.md).

Queue overflow blocks admission until capacity becomes available or cancellation
occurs. A feed waits for processing acknowledgment before advancing its persisted
closed-event checkpoint. No closed event is silently dropped. Shutdown cancels
admission, interrupts database work safely, waits for workers and their feed-state
cleanup, then closes the owner session. A failure returns an actionable error;
already committed financial work remains idempotent on replay.

The engine consumes previously pending paper intents at the next valid observed
close/update price before evaluating that observation. It then creates decisions
from persisted signals, including recovery after evaluation committed before the
intent was created. Partial/closed evaluation and entry freshness rules remain
in existing services. Recovered prices do not authorize new entries. Redelivery
reuses an already persisted paper observation's original readiness and availability,
preventing recovery from rewriting immutable observation content. Distinct market
feeds are supervised concurrently; this forward runtime does not promise a total
historical replay order across feeds.

No migration is required: existing candle revisions, feed pending checkpoints,
strategy evaluations/signals, paper observations/decisions and financial ledger
already persist the required state. Session advisory locks provide exclusion
until disconnect without introducing lease expiry or a second storage system.
Backtest schema scoping extends to the new evaluation lock, preserving isolation.

## Verification and boundaries

TDD used the approved CLI, application/PostgreSQL and fixture seams. Targeted
real-PostgreSQL checks cover a signal at 100 filling only on the next 102
observation (60,000 ms latency), injected post-intent consumer failure and closed
checkpoint recovery, repeated restart with exactly one fill, four configured
instrument/timeframe workers, bounded queue cancellation, owner release,
competing evaluator/writer rejection, forced owner-session termination and
subsequent writer exclusion, and structured foreground CLI completion/live gate.

Live market monitoring, notification delivery, Unix control transport, systemd
operation and combined M6 acceptance are outside this slice. Funding-event
injection and historical OHLC interval protection remain the existing explicit
paper/backtest services; the forward runtime consumes observed prices rather
than claiming intrabar fills from unavailable observations. No exchange order
submission or authenticated trading access is introduced.

Commands completed for this slice:

```sh
go test ./...
go vet ./internal/app ./internal/cli ./internal/config ./internal/store/postgres
BILLIONAIRE_TEST_DATABASE_URL=... go test -race ./internal/app ./internal/store/postgres ./internal/cli -run 'TestForeground|TestEngine|TestStart|TestInitAndStatus' -count=1
```

All passed. The full test command used fixture/local protocol checks; PostgreSQL
coverage was enabled for the targeted lifecycle/CLI/race commands above. The
integration milestone owner runs the final combined full PostgreSQL suite.
