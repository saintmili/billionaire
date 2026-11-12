# M3 strategy runtime and reference strategies

Implemented 2026-11-12 against baseline `190c003`. The user requested the next
milestone after M2; the roadmap records an offline sequencing exception. M2's
actual-host REST/stream validation remains pending. M3 does not enable forward
trading or implement M4 account/order/fill behavior.

## Modules and migration

- `internal/strategy/`: strict schema-versioned YAML, a compiled Go registry,
  immutable configuration hashing, as-of snapshots, bounded declarative rules,
  reference strategies, warm-up requirements and pure signal-emission planning.
- `internal/store/postgres/strategies.go` and migration
  `0003_strategies.{up,down}.sql`: definitions, immutable versions, independently
  enabled instances, evaluation/event state and signals. Existing migration
  checksums are unchanged. Versions reject SQL UPDATE/DELETE; instance bindings
  reject mutation while enablement and runtime state remain mutable.
- `internal/cli/strategy.go`: validate/add/list/enable/disable/evaluate and signal
  inspection. Market watch warms up enabled strategies and evaluates matching
  events before acknowledging M2 delivery.
- `internal/yamlutil/`: reuses M1's safe YAML field/line diagnostics for app and
  strategy configuration without echoing submitted values.
- `examples/strategies/`, `testdata/market/m3.json`: declarative examples and a
  labeled synthetic breakout/false-breakout dataset for BTC/ETH spot/linear.

No new dependency was added. The registry accepts Go implementations compiled
into the binary through `Validate`, `Evaluate`, and `Requirements`; YAML cannot
load code, call a shell, or execute arbitrary expressions.

## Definitions, versions and instances

The YAML contract is illustrated by the examples. Required identity fields are
`schema_version: 1`, `id`, `name`, and registered `implementation`. Defaults are
primary `1m`, the primary-only timeframe set, long, both supported markets,
`on_candle_close`, `on_transition`, zero signal cooldown, one percent risk hint,
and `alert_only`. Short definitions must explicitly select linear markets.

Parameters, entry/exit rules, timeframes/markets, evaluation/emission settings,
risk hint, action and schema version all contribute to the deterministic SHA-256
version ID. Identical normalized content reuses its version; editing any of those
fields creates the next numbered version. Existing versions and instance bindings
are unchanged. Instance IDs bind one version, instrument, account reference and
action; create a new instance to change a binding. New instances are disabled.
`account` is a validated symbolic/ID reference; no account or balance is created
or resolved until M4. Action and risk fields are recorded intent metadata only.

## Snapshot and clock semantics

Snapshots select only closed history whose exclusive end and provider observation
are at or before the evaluation availability time. Later revisions of older bars
are excluded; the latest eligible revision is selected from revision history and
canonical observations. The primary series stops at the event candle and can
include that candle's partial update only in `on_update`. Higher frames never
include partial/future closes. Required windows must be contiguous and higher
frames must contain the latest expected completed interval. Missing data yields
an auditable warm-up/gap result rather than a signal.

REST pages can be fetched newest-first, making their observation timestamps run
backwards when candles are recovered chronologically. M2 events now carry a
separate monotonic `available_at` clock, normalized to the source observations
already received. Recovery batches become available after all fetched pages.
Pending delivery persists that original availability time with the revision;
retries cannot see newer data simply because the consumer was temporarily down.
Signal `observed_at` records evaluation availability, while `source_observed_at`
retains the provider timestamp. Manual offline events default to their supplied
observation time. Event identity is unaffected by delivery time.

This is an as-of observation/evaluation contract, not a claim that fetched OHLCV
reconstructs intrabar history. M5 must freeze datasets and provide an explicit
virtual availability clock; faithful update-mode replay requires recorded updates.

## Rules and reference algorithms

Rules select exactly one of `all`, `any`, `not`, or a comparison. Comparisons are
`gt`, `gte`, `lt`, `lte`, `eq`, `crosses_above`, `crosses_below`. Operands are bounded
plain-decimal constants or `open`, `high`, `low`, `close`, `volume`, `sma` values,
with a selected timeframe and nonnegative previous-bar offset. SMA averages closes
using exact sums and division rounded to 18 fractional places, ties away from
zero. Crossovers require current strict inequality and previous non-strict
opposite inequality. Logical children are all evaluated so an OR cannot hide
missing warm-up. Limits: 8 timeframes, depth 8, 128 rule nodes, 5000 period/offset
window plus the extra crossover value. Unknown fields/operators/parameters fail.

- **Breakout:** compare the event close with the highest high (long) or lowest
  low (short) of the preceding 20 completed candles by default. Exclude the event
  candle from that boundary. Long stop is event low minus offset; short stop is
  high plus offset. Targets use configured reward/risk (default 2). Reject invalid
  nonpositive or reversed geometry. Proposed prices are observations, not fills;
  next-observable-price/next-open execution belongs to M4/M5.
- **Support/resistance:** within the configured lookback, use the latest pivot
  strictly below/above all left/right neighbors. Ties are not pivots. Confirmation
  requires the configured right-hand completed bars; partial bars cannot confirm.
  Tolerance is an absolute quote-price distance. Rejection mode uses support for
  long/resistance for short, counts post-pivot near-zone touches (including the
  current rejection), and requires a directional body closing back beyond the
  level. Break mode uses resistance for long/support for short, prior touches,
  and close beyond level plus/minus tolerance. Risk hints use the event candle's
  low/high and the same offset/reward model. Evidence records level, touches and
  confirmation time.
- **Engulfing:** both candles must be completed; the previous body is opposite
  the requested direction, the current body is directional and contains the
  previous body. Boundaries may touch but at least one must extend strictly.
  Dojis do not qualify; wicks do not participate in the pattern. Wicks do supply
  stop hints. Partial engulfing updates never trigger.
- **Rules:** entry and exit trees are independent. Built-in entries can also be
  filtered by an entry tree. Signals record comparison operands/evidence.

## Emission, persistence and recovery

`on_transition` emits only when a ready condition changes false→true;
`once_per_candle` permits one signal per kind/candle; `while_true` permits a signal
on each distinct eligible evaluation event. Entry and exit maintain independent
state and cooldown timestamps. Ignored partial events and unavailable warm-up do
not manufacture false transitions. Out-of-order evaluation availability is
recorded as skipped rather than rewinding state. Signal cooldown is distinct from
future alert-delivery and paper-execution cooldowns; those remain M6/M4.

PostgreSQL locks the instance, checks deterministic event identity, constructs the
snapshot, then commits event audit, signal rows and next policy state together.
A duplicate returns no new signals, including after a lost acknowledgment.
Concurrent replays serialize. A failed signal write rolls back state and dedup,
allowing retry. Watch evaluates every matching enabled instance before M2 source
acknowledgment; if one instance fails, already committed instances deduplicate on
replay. Initial data warm-up seeds history without inventing historical signals.

Watch fetches required primary/higher-frame windows and refreshes missing tails.
Newly fetched data is not retroactively included in an earlier as-of event;
subsequent events can use it. Enabled close-only instances ignore partial events.
Update-mode evaluation audits preserve the actual evaluated update and its clock;
M2 still does not persist every partial market update by default.

No notification, paper order, position, reversal or backtest run is produced.
`source_ready` is market-data context, not account/risk permission. Recovery and
manual offline evaluation record it as false. Single-engine daemon ownership and
supervision remain M6; use one watcher per instrument/timeframe meanwhile.

## Verification and remaining gates

User-confirmed seams: YAML/registry, as-of snapshots/evaluation, transactional
version/instance/signal persistence and CLI operations. Focused red/green tests
and builds ran throughout. Tests cover preceding-20 boundaries, false breakouts,
BTC/ETH spot/linear identity, symmetric linear shorts/spot rejection, insufficient
warm-up, crossover, future higher closes/corrections, pivot lag, completed bodies,
independent entry/exit transitions, all emission policies and signal cooldown.

Real PostgreSQL tests cover immutable version guards, unchanged-file reuse/new
versions, concurrent duplicate replay, distinct `while_true` updates, disabled
instances, as-of revision selection, and rollback after an injected signal-write
failure. CLI acceptance covers add/enable/watch/signals/disable/list and explicit
offline evaluation/replay with bounded decimal input. Pending replay retains the
original availability clock; reversed REST page timestamps preserve chronological
condition transitions.

`go test ./...` passed with the disposable PostgreSQL database enabled. Race tests
passed for strategy, PostgreSQL, CLI and all market-data packages. `go vet ./...`,
`go build ./...`, `go mod verify` and `git diff --check` passed. Independent
the initial independent review found and prompted fixes for recovery-window truncation
and entry/exit warm-up coupling. Regression tests reproduced both failures before
the fixes. Primary history is now bounded before SQL LIMIT; unavailable entry/exit
rules preserve their own state while the other kind remains eligible. Warm-up
orchestration moved from CLI into the strategy service, and each built-in owns its
validation/evaluation/history requirements. Final re-review is pending. M2 live-host validation,
StopLore licensing and the supported-Go patch-refresh release gate remain pending
as previously documented. M4 was not started.
