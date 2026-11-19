# M6 recovery and operational health (#33)

This slice enables intentional public monitoring on the workstation whose M2
host validation was accepted on 2026-11-19. All trading remains simulated. It
introduces no authenticated endpoint, exchange credentials, live order, second
financial owner, daemon manager or StopLore delivery. Systemd #34 and combined
M6 acceptance #35 remain separate gates. A future VPS needs its own host check.

## Startup and shutdown

```sh
billionaire --config /path/config.yaml init
billionaire --config /path/config.yaml --control-socket /private/dir/control.sock start --live
billionaire --config /path/config.yaml --control-socket /private/dir/control.sock status --output json
# Send SIGINT/SIGTERM to the foreground process for graceful cancellation.

# Deterministic alternative, using the shipped fixture and engine.lookback: 1:
billionaire --config /path/config.yaml start --fixture testdata/market/m2.json
# Add --hold to serve controls after fixture completion (feeds remain disconnected).
```

Exactly one of `--live` and `--fixture PATH` is required. Source-selection errors
occur before provider/database access. Live mode stays running until interrupted;
fixture mode finishes unless held. Startup/stopped JSON retains `engine`,
`fixture`, `live_monitoring`, subscription count and queue capacity as applicable.
`live_monitoring: true` selects public input, never live trading. Neither a missing
fixture nor a network failure silently selects another source. Remote endpoints
require HTTPS/WSS; HTTP/WS are accepted only for loopback protocol tests, matching
the public provider adapter. Existing strict configuration and secret rejection
remain in force.

The current-user socket directory must be 0700 and the socket 0600. Existing
paths/symlinks or another owner fail explicitly; a stale socket requires explicit
operator inspection/removal. Owner acquisition still fences financial execution
and evaluation on one pinned PostgreSQL session. Loss never reconnects that
session. Shutdown cancels feeds, waits for their workers, closes the socket,
finishes bounded notification work, and releases ownership. A forced process kill
can retain a socket file; persisted feed connection flags cannot prove health.

## Readiness and observations

The owner starts with empty current-session monitoring facts even when previous
feed rows say connected. History is warmed and synchronization restores pending
closed revisions and delivery watermarks, backfills missing closes, and waits for
durable processing before acknowledgment. Recovered candles retain their original
availability and identity. Recorded observations reuse their original readiness
and result. A replay never changes the financial outcome of an already committed
observation or rewrites its availability clock.

After warmup, instrument metadata and funding retrieval, processing checks actual
subscription clocks again. The owning financial transaction refreshes the clock
and entry gate after durable ordering reads, and strategy source readiness is
checked again after price processing. A candle that aged in the bounded queue or during I/O
cannot enter using its earlier enqueue-time readiness. Fixture clocks remain
explicitly deterministic. An immutable pending availability can be ahead of a
restarted fixture clock: it is retained without granting a new observation
execution eligibility.

The entry policy is conservative: every configured subscription and every
required timeframe of every active instance must be connected, fresh, gap-free
and complete through the current boundary. An active instance's historical filter
backfill cannot substitute for its missing live subscription. Owner-backed manual
opens and approvals use the same policy within the durable control transaction;
rejected requests retain their original receipt. Existing pending opens wait
while aggregate readiness is blocked and execute only on an eligible subsequent
actual observation. Evaluation-to-intent recovery preserves durable signals and
never fills on their own signal candle.

Price validity is separate from entry readiness. A fresh actual price can update
protection and pending closes while another required feed is recovering or a
close/gap requirement blocks entries. Recovered history, stale prices, future
availability and missing current prices never manufacture historical protective
fills. Forward processing remains observed price points, without importing ADR
0007's isolated replay OHLC phases. Strategy signals still retain their snapshot
readiness rules. Exposure on an instrument removed from subscriptions is
explicitly unmonitored; a close request can be recorded, but needs a later valid
observation to fill.

## Authoritative status contract

`status` preserves schema, `mode: paper`, database state, advisory-lock engine
observation and kill-switch fields. It adds UTC `observed_at`,
`engine_connectivity`, and `health`. Text output includes feed and exposure
limitations and aggregate counts. The private control protocol also accepts
`{"health":true}` as a read, separate from a command/outcome lookup. In-process
callers use `Control.Health` or `control.ReadHealth`.

Each read obtains a new owner snapshot, checks its pinned session and the database,
and refreshes time-dependent readiness after database reads. No cached response
is used on reconnect. The actual runtime subscriptions determine live coverage;
status verifies the snapshot's database identity against the configured database.
A different/missing socket or database mismatch cannot provide ready health.

- `engine_connectivity: connected` means an owner health read succeeded;
  `unavailable` or `unavailable_or_configuration_mismatch` cannot authorize entries.
  This is separate from `engine: running|stopped|unavailable`, observed via the
  configured database's ownership fence.
- `health.session` changes per owner runtime; `database` distinguishes ready from
  unavailable; `ownership` distinguishes held, lost, unknown, none, and
  held-but-health-unavailable (`held_health_unavailable`). `entries_ready` is
  always false when current ownership/database/required coverage is unconfirmed.
- `health.feeds` names instrument/timeframe, provider `connected` flag, gap count,
  last provider event, close/delivery/availability checkpoints, last successful
  processing update and last valid actual observation. `clock` identifies wall or
  fixture clocks. `status` is recovering, ready, disconnected, stale or
  unmonitored. Initial ownership alone does not mean readiness.
- `health.exposure` names each open position and its monitoring limitation.
  `monitored` requires a fresh actual processed price, independently of new-entry
  gates. Otherwise monitoring is disconnected, stale, unmonitored or
  awaiting_valid_observation. Candle subscription heartbeats alone cannot claim
  that protection has processed a current executable price.
- Linear feed/exposure funding fields expose the durable settlement watermark,
  known supplied/missing/unknown coverage and retained missing interval count.
  A later supplied interval cannot erase an earlier missing interval. The public
  schedule and approximation limits remain those of [#29](m6-funding-report.md).
- `health.counts` includes active instances, signals, open positions, pending
  orders, backtests by running/completed/failed/cancelled state, notification
  failures and pending/inflight deliveries, and journal pending rows, oldest UTC
  timestamp and lag seconds. A pending journal row is local queued work:
  `journal_delivery: not_implemented_pending_M7` never claims external delivery.
  Database failure leaves counts null/unknown rather than zero.

With no authoritative owner read, status can still return current database counts
and configured-but-unmonitored feeds/exposure. It never trusts a persisted old
`Connected` flag. These are operational snapshots rather than one serializable
financial report: counts and feed reads can occur at different instants, and
engine connectivity, provider freshness and database state remain independent.

## Recovery evidence

Public engine/control/CLI seams were exercised with disposable real PostgreSQL:

- Failure after evaluation commits but before intent commit; durable signal is
  recovered once, the next actual observation fills once, and signal/order-open
  notification enqueue counts remain one each.
- Consumer failure after a durable intent and again after the subsequent fill
  commits but before feed acknowledgment; repeated restart yields one signal,
  one order, one fill and the original exact entry ledger count/latency/price.
- Existing funding crash-boundary tests restore schedule/cursor after a committed
  funding outcome, retain missing coverage, and reconcile signed payments and
  cash without duplicate charges. Funding remains ordered before new prices.
- An admitted candle ages during public metadata work or an injected slow
  intermediate database write: entries and subsequent strategy source readiness become unready,
  the recorded observation is invalid for execution, and availability is unchanged.
- Initial recovery rejects owner entry controls; fresh subscriptions admit them;
  gaps block new exposure while valid actual price monitoring remains visible.
  Missing required filter subscriptions, removed exposure subscriptions, a killed
  owner session and unavailable storage are individually distinguished.
- The CLI `--live` journey uses a local public REST/WebSocket protocol server,
  never the internet. It covers actual startup, fresh-to-silent/stale status
  refresh, competing ownership, graceful shutdown/socket removal and restart with
  a new session. Existing suites cover corrupt configuration, unavailable DB,
  socket permissions, control receipt rollback and restart, pending closes,
  protection/trailing persistence, outbox claim recovery and exact reconciliation.

No new migration is needed: existing evaluation, observation, financial,
protection, funding, feed and notification checkpoints own recovery. Session
health deliberately is not a redundant persisted financial checkpoint.

## Verification

Tests use the pinned PostgreSQL 17 image in `compose.test.yaml` under isolated
Compose project `billionaire-m6-recovery`, on an ephemeral loopback port. Normal
application databases are never modified. Export that disposable project's URL
as `BILLIONAIRE_TEST_DATABASE_URL` for the commands below.

```sh
go test ./...
go test -race ./internal/app ./internal/control ./internal/cli ./internal/store/postgres \
  -run 'TestForeground|TestOwnerHealth|TestHealthGates|TestExplicitLive|TestControlCannot|TestSpot|TestProtection|TestObservation|TestFunding' -count=1
go vet ./...
go build ./...
```

The full PostgreSQL Go suite passed during implementation. Final transaction-clock
and strategy-readiness tightening was then rechecked with the targeted race suite,
vet and build on the final slice source. Regression sensitivity was confirmed by
disabling clock refresh temporarily: the delayed observation incorrectly filled;
restoring refresh prevented that fill. The final assembled-source `./scripts/check`
is run by the integration owner.
No external-provider acceptance was rerun here; the existing accepted workstation
host evidence remains the authority. Systemd installation, live unattended service
acceptance, M6W and M7 are outside this slice.
