# Millionaire

A Linux-first Go CLI for public-market monitoring and paper trading. M3 provides
configuration, PostgreSQL storage, public candle synchronization, versioned
strategies and recorded signals. M4 now adds durable paper accounts and an opening
ledger, offline spot/linear orders, risk controls, signal approval, protective exits,
funding and approximate isolated liquidation. Backtesting and the daemon remain
upcoming work. [M4 offline acceptance is complete](docs/m4-report.md); M2 actual-host
validation remains pending and live monitoring remains disabled.

Use Go **1.27.2** or a compatible newer supported toolchain. For the local setup,
install Docker with Compose and OpenSSL. No exchange account or API keys are used.

## Local setup

Run these commands from the repository in the same shell. Generate a database
password once for a new volume; keep it securely for subsequent sessions.

```sh
export POSTGRES_PASSWORD="$(openssl rand -hex 24)"
# Optional: export MILLIONAIRE_POSTGRES_PORT=5433
export MILLIONAIRE_DATABASE_URL="postgres://millionaire:${POSTGRES_PASSWORD}@127.0.0.1:${MILLIONAIRE_POSTGRES_PORT:-5432}/millionaire?sslmode=disable"
docker compose up -d --wait

go build -o bin/millionaire ./cmd/millionaire
./bin/millionaire init
./bin/millionaire config validate
./bin/millionaire status --output json
```

Compose pins PostgreSQL 17 by digest, persists its data in a named volume, requires
`POSTGRES_PASSWORD`, and binds only to loopback. A pre-existing volume retains its
original database password; changing the environment does not reset it. For an
existing PostgreSQL service, set `MILLIONAIRE_DATABASE_URL` to its connection URL
instead. Percent-encode reserved characters in manually chosen URL passwords.
Use the appropriate TLS settings for a remote database.

`init` creates `millionaire.yaml` with mode `0600` if missing, preserves an existing
file, validates it, and applies pending migrations atomically. If PostgreSQL or
its environment variable is unavailable, the generated config remains for editing;
set the variable and rerun `init`. It creates no paper accounts or trading engine.
For installation outside the checkout, run `go install ./cmd/millionaire` and add
the Go bin directory to `PATH`; configuration and SQL are embedded in the binary.

## Commands and configuration

```sh
./bin/millionaire --help
./bin/millionaire config validate --config configs/example.yaml --output json
./bin/millionaire database migrate up
./bin/millionaire database migrate down
./bin/millionaire status
```

All result commands accept `--config` (default `millionaire.yaml`) and `--output
text|json`. Results go to stdout; structured UTC logs go to stderr. Failures exit
1; interruption/cancellation exits 130. `status` is read-only: it reports config
mode, storage readiness/schema version, and `engine: not_implemented`. Missing,
uninitialized, incompatible or unavailable storage is unhealthy and exits nonzero.
It does not claim provider connectivity or daemon health. `config validate` is
entirely offline and does not require a database URL.

The exact schema is in [configs/example.yaml](configs/example.yaml). Every field
is required; unknown/duplicate YAML keys and multiple documents are rejected.
Only paper mode, UTC, PostgreSQL, USDT, the Bybit provider, spot/linear instruments,
and supported native or aggregatable intervals are accepted. Prices/balances use plain decimal
strings, never binary floats. There are no exchange-credential fields. Additional
market-data, strategy, paper-risk, notification and journal settings arrive with
their implementing milestones.

Environment overrides (nonempty values take precedence over YAML):

| Variable | Configuration field |
| --- | --- |
| `MILLIONAIRE_MODE` | `app.mode` (only `paper`) |
| `MILLIONAIRE_LOG_LEVEL` | `app.log_level` (`debug`, `info`, `warn`, `error`) |
| `MILLIONAIRE_DATABASE_TIMEOUT` | `storage.timeout` (positive, at most `1m`) |
| `MILLIONAIRE_PROVIDER` | `market_data.provider` (currently `bybit`) |
| `MILLIONAIRE_REST_URL` | `market_data.rest_url` (HTTPS origin) |
| `MILLIONAIRE_SPOT_WS_URL` | `market_data.spot_ws_url` (public spot WSS path) |
| `MILLIONAIRE_LINEAR_WS_URL` | `market_data.linear_ws_url` (public linear WSS path) |
| `MILLIONAIRE_STALE_AFTER` | `market_data.stale_after` (positive duration) |
| `MILLIONAIRE_INITIAL_BALANCE` | `paper.initial_balance` (positive decimal) |

`storage.dsn_env` names the environment variable holding the DSN; its default is
`MILLIONAIRE_DATABASE_URL`. Inline DSNs are rejected. Schema/type errors are
sanitized; validation errors identify fields without echoing their values. Keep
secrets outside source control; `.env` and generated config are ignored.

## Storage and migrations

The application owns the `millionaire` schema. `schema_migrations` tracks ordered
versions/checksums, and migration 1 creates paper-mode application metadata.
Migrations are embedded versioned SQL in `internal/store/postgres/migrations/`;
`up` applies all pending versions, `down` removes the last version's objects. The
tracking schema/table remain at version zero after rollback. Repeating `up` at
latest or `down` at zero is harmless. A transaction-scoped advisory lock serializes
migrations; SQL failures roll back and checksum/version mismatches are rejected.
A lost commit acknowledgment requires checking status before retrying.

Migration 2 adds instrument metadata, exact numeric closed candles, correction
revisions, and feed/checkpoint state. Identical REST/socket closes share a key
and cannot double insert. Older observations cannot overwrite newer values.

The database user must be able to create the application schema and its objects.
No StopLore tables are touched. Migration 4 adds paper accounts, an append-only
ledger and a financial journal outbox. Migration 5 adds independent spot orders,
fills, positions and price-observation replay records. Accounts use exact NUMERIC(38,18) columns;
account initialization, opening funds and the outbox event commit atomically.

## Paper accounts (M4, ticket #1)

Run `init` after upgrading to apply migration 4, then:

```sh
./bin/millionaire paper account create --name demo --balance 10000.125 --request-id demo-opening
./bin/millionaire paper accounts --output json
./bin/millionaire paper account show <account-id> --output json
```

`--balance` defaults to `paper.initial_balance` (including its environment override).
Currency is USDT only. Balances must be positive, at most 1,000,000,000,000,000,000
USDT, and have at most 18 fractional digits; excess precision is rejected rather
than rounded. The same bounds apply during configuration validation. JSON uses
strings for decimal values. Names are display labels, need not be unique, and
contain 1–80 ASCII letters, digits, spaces, underscores, dots or hyphens, beginning
with a letter or digit.

A creation requires `--request-id`: 1–128 ASCII letters, digits, underscores, dots,
colons or hyphens, beginning with a letter or digit. Preserve that ID when retrying
an interrupted/uncertain creation. An identical request returns the same account
without crediting it again; reusing the ID with different name, currency or initial
balance fails. Equivalent decimal spellings are identical. A new ID creates a new
account even if the name matches. Account IDs are stable hashes of request IDs.
Inspection includes the immutable opening ledger entry. The displayed balance is cash: opening funds plus trade proceeds minus purchases
and fees. Position inspection shows holdings, remaining acquisition cost and realized P&L.

Offline mutations acquire a database-wide PostgreSQL session advisory lock;
a competing process fails with an ownership error and can retry after the owner
finishes. Read-only inspection remains available. The owner uses its dedicated
connection for every mutation and closes it on completion. PostgreSQL releases
the lock on disconnect/process death (network failure detection may take longer);
a lost owner cannot silently reconnect and continue writing. Application users
must call `Close` on acquired owners. M6 will hold this ownership for the engine's
lifetime and route CLI mutations through its private control socket. Account
creation uses `storage.timeout`; errors are sanitized and uncertain commits instruct
you to retry with the same request ID.

An `account_opened` financial outbox event is recorded locally; it does not call
StopLore or imply that StopLore supports an account event. Mapping/reconciliation
and delivery belong to M7. M4 remains fixture-driven under its approved sequencing
exception; M2 actual-host validation is still required before live monitoring.

## Manual spot execution (M4, ticket #2)

Run `init` after upgrading to migration 5. These commands use only explicit offline
fixtures. Replace `<account-id>` and `<position-id>` with the IDs printed by account
creation and position inspection; no Bybit connection is used.

```sh
./bin/millionaire paper open --account <account-id> --market spot --symbol BTC/USDT \
  --side long --quantity 2 --request-id buy --at 2026-11-12T10:00:00Z \
  --fee-rate 0.001 --slippage-bps 0 --fixture testdata/paper/spot-metadata.json
./bin/millionaire paper observe --fixture testdata/paper/spot-entry.json --at 2026-11-12T10:00:01Z
./bin/millionaire paper positions --account <account-id> --output json
./bin/millionaire paper close <position-id> --quantity 0.5 --request-id partial \
  --at 2026-11-12T10:00:02Z --fee-rate 0.001
./bin/millionaire paper observe --fixture testdata/paper/spot-partial.json --at 2026-11-12T10:00:03Z
./bin/millionaire paper close <position-id> --quantity 1.5 --request-id final \
  --at 2026-11-12T10:00:04Z --fee-rate 0.001
./bin/millionaire paper observe --fixture testdata/paper/spot-close.json --at 2026-11-12T10:00:05Z
./bin/millionaire paper orders --account <account-id> --output json
./bin/millionaire paper fills --account <account-id> --output json
./bin/millionaire paper account show <account-id> --output json
```

Open/close creates a durable pending market intent. Only a subsequently delivered
price whose source-observed **and** availability times are strictly after the intent
can fill it. A missing or already-known price leaves it pending; a future/unavailable
observation is rejected without recording delivery. These are explicit offline
clocks (`--at` is required), not an enabled live engine or historical candle replay.
M5 will supply its own next-open observations; this slice never extracts a fill
from the already-known close of a signal candle.

Reuse the same request ID, intent time, quantity, fee/slippage assumptions and
metadata for retries. Decimal spelling differences are normalized. Changing those
inputs under the same ID fails. A position close requires an explicit positive
quantity; use its entire remaining quantity for a full close. Spot long holdings
only: no borrowing, leverage other than 1, naked shorts, or implicit reversal.
Each entry opens a separate position subject to account and strategy risk limits.

A fixture supplies `metadata` (the normalized InstrumentInfo shape) and, for
`observe`, an `observation` with stable `id`, instrument, matching `source`, positive
`price`, `observed_at`, `available_at` and `ready`. The checked-in examples show
both shapes. Metadata must identify the fixture provider, match the requested
market/symbol, and supply positive price/quantity steps and minimum notional.
Missing or invalid metadata fails; no precision is guessed. Steps and all modeled
values must fit positive/nonnegative decimals up to 10^18 with at most 18 fractional
digits. Orders persist their metadata snapshot and execution assumptions.

Quantity must match its step and minimum exactly. Slippage is adverse: increase buy
price and reduce sell price by `--slippage-bps` (default 0, range [0,10000)). Buy
prices round up to a price tick, sells down. USDT purchase notional and fees round
up to 18 places; sale notional rounds down. `--fee-rate` is the market/taker fraction
(default 0.001, range [0,1]); maker fees do not apply to market orders. Partial exits
allocate remaining cost, including entry fees, proportionally with 18-place
half-away rounding; the final exit consumes the entire residual cost. Realized
P&L is net sale proceeds minus that allocated cost. No floating-point money is used.

Entry eligibility is rechecked when a price arrives. The instrument must still be
listed in `market_data.instruments`; `ready` must be true and the price must be no
older than `market_data.stale_after` relative to the processing clock. Unready or
stale prices cannot fill exits either; disabling new entries still permits an
explicit close at a usable current price. Insufficient cash, minimum notional and
excessive close quantities produce recorded rejections. Commands print the record
and exit nonzero on rejection. Pending orders reserve no funds: deterministic
intent-time/ID ordering and transactional account locks prevent overspending when
several orders become eligible together.

Each fill atomically updates its order, position, cash, principal/fee ledger entries
and financial outbox event. Fills record source, observed/available/execution times
and intent-to-processing latency. No StopLore methods are called. Replaying an
identical observation returns its original decisions; different content under its
ID fails. Earlier out-of-order observations are recorded as skipped. Replayed old
events cannot fill a newly submitted intent. Both original clocks retain nanosecond
precision in durable JSON, independent of PostgreSQL's timestamp precision.
Restart restores pending intents; retry an uncertain commit with the same request
or observation identity. `storage.timeout` bounds each offline invocation, and the
existing dedicated-session ownership excludes competing processes. M2 live-host
validation remains pending; this command never enables live monitoring.

## Protective spot exits

Run `millionaire init` to apply migration 0007 before using protection. Initial
`--stop-price` now attaches an executable stop as well as supplying risk sizing.
A stop must be below the actual modeled entry fill, and `--take-profit` above it.
Initial geometry is checked at execution; invalid geometry produces a persisted
rejection without opening a position. All protection prices and distances must be
positive exact price ticks. The existing account and entry-risk checks still apply.

For a fresh account, using the existing entry fixture:

```sh
./bin/millionaire paper open --account <account-id> --quantity 2 --request-id protected-buy \
  --at 2026-11-12T10:00:00Z --fixture testdata/paper/spot-metadata.json \
  --stop-price 90 --take-profit 120 --target-quantity 0.5
./bin/millionaire paper observe --fixture testdata/paper/spot-entry.json --at 2026-11-12T10:00:01Z
./bin/millionaire paper positions --output json
./bin/millionaire paper adjust <position-id> --stop-loss 95 --request-id raise-stop \
  --at 2026-11-12T10:00:02Z
./bin/millionaire paper observe --fixture testdata/paper/spot-protection-collision.json \
  --at 2026-11-12T10:01:00Z
./bin/millionaire paper fills --output json
```

Adjustments preserve omitted fields; an explicit empty value disables a field.
Use `--take-profit`, `--target-quantity`, `--trail-activation` and `--trail-distance`
on either entry or adjustment. To disable trailing, clear both trailing fields.
`--trail-activation` is an absolute price and `--trail-distance` an absolute price
distance, smaller than activation. Once a valid observation reaches activation,
the highest observed price minus distance ratchets the stop upward, rounded down
to the price tick. It never lowers the stop automatically. The updated stop applies
only to a **later** observation. An explicit manual stop adjustment or trailing
reconfiguration resets trailing activation/high-water state.

A target closes all remaining holdings by default. `--target-quantity` makes it a
one-time partial exit, capped at current remaining holdings after intervening
manual closes. The configured quantity must match the quantity step/minimum and
fit the position when set. Stops always close the remaining holdings. A completed
partial target stays consumed across restart; explicitly adjusting target price
or quantity rearms it. Every exit still validates minimum quantity/notional: an
unexecutable exit is recorded as rejected and protection remains available for a
later observation. No reverse position is opened.

Observations may include a completed `bar` with `start`, exclusive `end`, `open`,
`high`, `low`, `close`; observation `price` must equal `close`, OHLC must be
consistent and all prices positive and bounded. `observed_at` must be at or after
`end`. This is an explicit approximate OHLC model, not recorded intrabar ticks.
A bar starting before entry, an adjustment, or the last protection update cannot
retroactively trigger that position's protection. Use consecutive nonoverlapping
bars or recorded point observations. Initial market entries use the observation's
price (bar close), so protection begins with a subsequent observation.

Per event: evaluate existing stops, then targets, then update trailing for the
next event; execute protective decisions before pending manual orders. If the
old stop and target are both touched, choose the stop and record `ambiguous=true`
on the fill. At a gap through a level, use bar open (or the point observation's
price) plus adverse modeled slippage, rather than the crossed level. Otherwise
use the touched level plus slippage. The collision fixture above closes at the
adjusted stop and marks ambiguity; it cannot use the same bar's high to invent a
new trailing stop hit earlier in that bar.

Protection and manual exits bypass entry limits, disabled instruments and the
kill switch, while requiring fresh, ready, valid observations. Unusable prices
cannot fabricate a fill or consume a target. Entry fee/slippage assumptions also
apply to protective exits. Adjustment reference prices must be fresh under
`market_data.stale_after`, available by `--at`, and ordered after position state.
Retry an adjustment with the same request ID, fields, timestamp and stale threshold;
its original outcome is returned even after closure. Changed inputs fail.

Position state, replay outcomes, ledger changes and outbox enqueue are transactional.
Trailing changes and manual adjustments enqueue journal events without calling
StopLore. Existing positions upgraded from migration 0006 retain entry execution
assumptions but do not automatically activate old sizing-only stops; use `paper
adjust` explicitly. Inspect JSON positions for persisted protection, high water,
activation and target-consumed state, and fills for exit reason, gap and ambiguity.
See [ticket #6 evidence](docs/m4-protection-report.md). Linear coverage remains a
later M4 gate; this does not enable live monitoring.

## Checks

```sh
go test ./...
go vet ./...
go build ./...
```

Default tests are offline; local HTTP test servers exercise the M0 spike.
PostgreSQL integration tests skip unless explicitly configured:

```sh
export MILLIONAIRE_TEST_DATABASE_URL="$MILLIONAIRE_DATABASE_URL"
go test ./internal/store/postgres ./internal/cli -count=1
go test -race ./...
```

Use a disposable test server and a user with `CREATEDB`. Tests create uniquely
named `millionaire_test_*` databases, inject failures there, then drop only those
databases. They cover migration up/down/repeat/reopen, concurrent callers,
cancellation, corrupt version metadata, rollback after failure, and CLI behavior.
CI supplies an ephemeral PostgreSQL service and runs tests, vet and build.

## Market data (M2)

```sh
./bin/millionaire provider list --output json
# Explicit online capability/history check; no database needed:
./bin/millionaire provider test --provider bybit
./bin/millionaire market backfill --market linear --symbol ETH/USDT \
  --timeframe 15m --from 2026-01-01T00:00:00Z --to 2026-01-01T01:00:00Z
./bin/millionaire market watch --market spot --symbol BTC/USDT --timeframe 1m
./bin/millionaire market status --market spot --symbol BTC/USDT --timeframe 1m --output json
```

Run `init` after upgrading to apply migration 2. Backfill bounds are UTC-aligned,
`from` inclusive and `to` exclusive; no forming/future candle is accepted.
Cached ranges are reused; `--refresh` fetches corrections. Missing candles are
reported and fail the command rather than being silently skipped. Provider
metadata must identify trading spot or USDT linear perpetual instruments.

Watch warms up `--lookback 20` intervals, subscribes, and REST-backfills on reconnect.
`--retries 5` bounds reconnects per invocation (0..10). Ctrl-C/SIGTERM cancels it.
`--output json` streams one JSON event per line; partial updates are ephemeral,
closed candles and health/checkpoint state are durable. Recovered closes arrive
in order as `recovered_candle` events with new entries blocked; `delivered_through`
advances after successful consumption. A bounded pending-revision slot is saved atomically with live candle changes,
so failed corrections to older candles also replay on restart.
Consumers must deduplicate stable event IDs across callback/checkpoint crashes;
initial history warm-up is not emitted as new observations. Synchronous callbacks
apply backpressure without an unbounded or silently dropping queue. A storage or
consumer failure stops watch for explicit restart and recovery.

`market status` reports connection, last event/close, gaps and
`allows_auto_entries`. This flag is a source-health prerequisite for the future
risk gate; no M2 command submits an order. It expires dynamically when data stalls,
a close is missing, or the source disconnects. Root `status` still checks storage.
M6 will add the single-owner daemon and control interface.

Native intervals are preferred. Supported whole minute/hour/day intervals up to
30d (for example `2m`, `90m`), `1w` (Monday UTC), and `1M` (calendar month) can be
aggregated from complete 1m history. Missing inputs fail aggregation. Derived live
streams emit completed buckets only; use native intervals for partial updates.
Requests are capped at 100,000 candles, including source 1m fallback ranges.
The provider rate limit, HTTP/frame bounds, deadlines, and 30s clock-drift guard
are documented in the [M2 report](docs/m2-report.md).

An explicit, network-free provider uses separate `fixture` identities:

```sh
./bin/millionaire provider test --provider fixture --fixture testdata/market/m2.json
./bin/millionaire market backfill --fixture testdata/market/m2.json \
  --market spot --symbol BTC/USDT --timeframe 15m \
  --from 2026-01-01T00:00:00Z --to 2026-01-01T01:00:00Z --output json
./bin/millionaire market watch --fixture testdata/market/m2.json --output json
./bin/millionaire market status --fixture testdata/market/m2.json --output json
```

The fixture is synthetic and finite. It labels completion as disconnected and
never substitutes for a failed live request. Bybit REST remains geographically
blocked from this execution environment; actual-host REST and streaming checks
are required before forward monitoring. Offline tests use local protocol servers.

## Strategies and signals (M3)

Run `init` after upgrading to apply pending migrations. Strategy validation is offline;
registration and evaluation use PostgreSQL.

```sh
./bin/millionaire strategy validate examples/strategies/breakout.yaml --output json
./bin/millionaire strategy add examples/strategies/breakout.yaml \
  --instance breakout-demo --provider fixture --market spot --symbol BTC/USDT --account ACCOUNT_ID
./bin/millionaire strategy enable breakout-demo
./bin/millionaire market watch --fixture testdata/market/m3.json --output json
./bin/millionaire signals list --instance breakout-demo --output json
./bin/millionaire strategy disable breakout-demo
./bin/millionaire strategy list --output json
```

Unchanged YAML reuses the same content-hashed version; edited YAML creates the
next immutable version. Instances pin their version/instrument/account/action and
are created disabled. Use a new instance ID to change a binding; re-adding an
existing binding preserves its enabled state. Create a paper account first and
substitute its returned ID for `ACCOUNT_ID`; strategy commands validate that
binding without creating an account. Evaluation records action mode and risk
hints on signals. Paper approval/consumption below performs execution separately.

Market watch warms up enabled instances' required primary/higher-frame history
and evaluates matching updates before acknowledging source delivery. It checks
closed-candle versus update mode, distinct event identity, emission policy and
signal cooldown. Entry/exit state and signals commit together; source replay
cannot duplicate a committed signal. Watch JSON includes `signals` when emitted.
Only one watcher per instrument/timeframe should run until M6 adds ownership.

For explicit offline evaluation, seed history and use a fresh enabled instance:

```sh
./bin/millionaire strategy add examples/strategies/breakout.yaml \
  --instance offline-demo --provider fixture --account ACCOUNT_ID
./bin/millionaire strategy enable offline-demo
./bin/millionaire market backfill --fixture testdata/market/m3.json \
  --from 2026-01-01T00:00:00Z --to 2026-01-01T00:22:00Z
python3 - <<'PYCODE'
import json
with open('testdata/market/m3.json') as source:
    event = json.load(source)['candles'][20]
with open('/tmp/millionaire-event.json', 'w') as target:
    json.dump(event, target)
PYCODE
./bin/millionaire strategy evaluate offline-demo --event /tmp/millionaire-event.json --output json
```

Repeated evaluation returns no new signals. Offline events never claim live
source readiness. Event financial fields require bounded plain decimal strings.
Snapshots exclude future higher closes and later corrections; missing warm-up
records an unavailable result rather than a signal. Availability time is separate
from provider observation time and remains stable across pending replay. A
backtest/fill model is not implemented by `strategy evaluate`.

See [strategy examples](examples/strategies/), [exact algorithms and rule limits](docs/m3-report.md),
and [availability-clock ADR](docs/adr/0004-preserve-event-availability-clock.md).
`signals list` applies `--instance`/`--strategy` filters before returning at most
10000 results. Separate notification/execution cooldowns arrive in M6/M4;
`signal_cooldown` only controls signal emission. S/R defaults need 50 bars, and
the higher-timeframe example needs its stated SMA history; the small breakout
fixture is not claimed to satisfy every example's warm-up.

## M0 candle reconnaissance

```sh
go run ./cmd/candle-spike --fixture testdata/market/bybit-spot-btcusdt-1m.json
# Explicit online probe of a fixed BTCUSDT spot minute:
go run ./cmd/candle-spike --base-url https://api.bybit.com
```

The offline fixture is synthetic, not captured exchange history. The spike is
separate from the application and has no automatic fallback. Bybit REST returned
geographic HTTP 403 during M0; WebSocket handshakes succeeded, but subscriptions
were not tested. Actual-host connectivity remains a gate before monitoring.
See the [M0 report](docs/m0-report.md), [M1 report](docs/m1-report.md), [M2 report](docs/m2-report.md), [M3 report](docs/m3-report.md),
[StopLore map](docs/stoplore-integration.md), and [project documents](docs/README.md).

### Paper risk controls

Run `millionaire init` to apply migration 0006. Configure account limits with
`millionaire paper risk set --account <id> --fixed-risk 10 --max-positions 3`.
`set` replaces that scope's full policy; add `--strategy <definition-id>` for an
additional strategy ceiling. Inspect with `paper risk show --account <id>`.
Use `paper open ... --quantity risk --stop-price 90` for execution-time sizing,
or a numeric quantity checked against the same budgets. Pyramiding is off by
default. `--stop-price` supplies sizing geometry and attaches an executable stop.
Daily loss includes fees, settled price P&L and funding collateral changes for
the effective UTC day; explicitly waived isolated deficits are excluded.

`millionaire kill-switch on` persists a global block on new entries. Use
`kill-switch off` to allow them again and `kill-switch status` or `status` to
inspect it. Pending entries recheck the switch and current policy at execution.
Partial/full closes remain available. See the [risk contract](docs/m4-risk-report.md)
for precedence, UTC daily reset, cooldown, exact sizing and failure behavior.

## Isolated linear paper execution (M4, ticket #4)

The existing offline `paper open`, `observe`, `close`, `positions`, `orders` and
`fills` commands support USDT linear longs and shorts. The example configuration
enables ETH/USDT linear. No exchange trading connection is used.

```sh
./bin/millionaire paper open --account <account-id> --market linear --symbol ETH/USDT \
  --side short --leverage 5 --quantity 2 --request-id linear-short \
  --fixture testdata/paper/linear-entry.json --at 2026-11-14T10:00:00Z
./bin/millionaire paper observe --fixture testdata/paper/linear-entry.json --at 2026-11-14T10:00:01Z
./bin/millionaire paper account show <account-id> --at 2026-11-14T10:00:01Z --output json
./bin/millionaire paper positions --account <account-id>
./bin/millionaire paper close <position-id> --quantity 0.5 --request-id linear-partial --at 2026-11-14T10:00:02Z
./bin/millionaire paper observe --fixture testdata/paper/linear-partial.json --at 2026-11-14T10:00:03Z
./bin/millionaire paper close <position-id> --quantity 1.5 --request-id linear-final --at 2026-11-14T10:00:04Z
./bin/millionaire paper observe --fixture testdata/paper/linear-final.json --at 2026-11-14T10:00:05Z
./bin/millionaire paper account show <account-id> --output json
```

On an otherwise empty 1000 USDT account, these prices (100, 110, 90) and default
0.1% fees leave 1009.61 USDT for a short, or 989.61 for a long. Entry reserves
40 USDT plus 0.20 fees. Close commands inherit position side/leverage; they never
open a reverse position. Both directions use adverse tick/slippage rounding.

Leverage is an integer from 1 to 100, a simulation bound rather than a claim about
venue-specific leverage tiers; account/strategy limits can lower it. Spot still
requires long/1×. Fixture precision/minimums are authoritative; unsupported or
missing metadata fails validation. Pending intents reserve nothing; eligibility
rechecks the shared risk gate and locks collateral transactionally. Short risk
sizing requires a stop above entry. Stops, targets, trailing and adverse-first
ambiguous candles use position direction, including exits while entries are paused.

`account show` adds `valuation`: available cash, reserved margin, realized P&L
including fees recognized so far, gross unrealized price P&L, equity and free
margin. Linear equity is cash + reserved collateral + unrealized P&L; spot
holdings add marked value to equity but cannot collateralize linear entries.
Free margin is cash + linear unrealized P&L; an entry must fit both free margin
and cash, so unrealized gains do not become spendable funds. Linear entry checks
also include the new fill’s immediate slippage/tick loss and fees in projected
free margin and margin-limit equity, and require positive isolated equity in the
new position itself. Margin usage sums
reserved linear collateral and marked spot capital; symbol exposure remains gross
marked notional. Both account and strategy policies apply.

Marks are the latest accepted ready offline observations, checked against
`market_data.stale_after` at the valuation clock (`--at`, default current time).
Unavailable/stale/future marks yield null equity/free-margin/unrealized values and
an explicit reason; known cash, reserved margin and realized P&L remain visible.
Entry checks requiring these marks reject. `--at` checks freshness of the current
stored snapshot; it does not reconstruct historical account state.

Funding and approximate isolated liquidation are described below and in
[the funding report](docs/m4-funding-report.md). M4 combined acceptance and live
monitoring remain gated. See also [the linear report](docs/m4-linear-report.md).

### Funding and approximate liquidation (M4, ticket #7)

Run `init` to apply migration 9. After the linear entry observation above, before
processing later prices, apply the illustrative funding interval:

```sh
./bin/millionaire paper funding apply --fixture testdata/paper/linear-funding.json --output json
./bin/millionaire paper funding list --output json
# Record a later expected interval with unavailable data explicitly:
./bin/millionaire paper funding apply --fixture testdata/paper/linear-funding-missing.json --output json
```

Use the missing-interval example after finishing earlier price examples: it moves
the instrument's event clock to 11:00 UTC. Replay an identical event to retrieve
its original outcome; conflicting identity reuse, duplicate effective intervals,
clock regression and retroactive funding fail. Supply intervals in increasing
effective/availability order **before** observations and adjustments at that
instant. Positions filled at that instant are excluded; positions held strictly
before it receive funding on their remaining quantity. Already closed or advanced
exposure cannot be retrospectively funded.

Positive funding debits longs and credits shorts; negative rates reverse this.
Payment is valuation price × remaining quantity × signed rate, rounded downward
to 18 decimal places. It changes isolated collateral and realized funding P&L;
a paired collateral-transfer ledger movement keeps available cash unchanged.
Funding-triggered exits use the same transaction as funding and outbox enqueue.
`funding list` retains rates, effective/available clocks, source, provenance,
missing reasons, per-position payments/collateral changes and exit outcomes.
`rate: null` requires a reason, persists a visible warning, and makes no synthetic
zero-rate claim. No scheduler infers expected intervals: callers must supply
missing intervals explicitly. An empty history means coverage is unknown.

The service accepts normalized public or fixture events, with an explicit public
valuation at the effective instant. The CLI accepts offline fixtures only. There
is no automated historical funding fetch or public-provider coverage verification
in this slice. A rate copied into a fixture remains illustrative unless its
provenance records a verified source; results are never venue-realistic by default.

Linear entries persist `--maintenance-rate` (default `0.005`) and
`--liquidation-fee-rate` (default `0.005`); each is a fraction, both nonnegative,
and their sum must be below 1. Signal entries use these same defaults. Liquidation
is **approximate**: reserved collateral + gross unrealized price P&L at the
observed price <= current notional × (maintenance + liquidation fee) triggers
full closure. An entry already at this threshold after slippage is rejected
before filling. The price is the fixture/public observation, not a venue mark or
risk-tier calculation. Funding changes collateral first. A completed candle uses
its adverse extreme; a stop encountered before liquidation wins, while a gap
opening through liquidation closes at the open. Liquidation precedes a target
or a pending manual close when the observed path is ambiguous. Slippage/tick
rounding remains adverse; liquidation uses its configured fee instead of the
normal exit fee. Trailing changes take effect on the next event.

Risk-reducing linear closes can execute below entry minimum notional after a
price collapse. All linear closes cap losses at the collateral released for that exit. Excess
price loss/cost is explicitly recorded as `isolated_deficit` (also exposed on
fills and funding applications); available cash and other positions are not
charged or topped up. This is a simplifying write-off, not venue insurance or
bankruptcy settlement. Financial fills, margin, ledger, outcomes and outbox
commit atomically. Stale/unready prices do not claim observed liquidation.

### Offline signal approval and automatic entries (M4)

New strategy instances must bind an existing paper account ID, including
`alert_only` instances. Replace old placeholder bindings by creating a new
instance; bindings remain immutable. Migration 8 adds durable signal decisions.
Run `init` to migrate before using these commands.

The following bounded fixture workflow uses an account created with
`paper account create`; substitute its returned ID for `ACCOUNT_ID`:

```sh
./bin/millionaire strategy add examples/strategies/paper-entry.yaml \
  --instance paper-entry-demo --provider fixture --account ACCOUNT_ID
./bin/millionaire strategy enable paper-entry-demo
./bin/millionaire strategy evaluate paper-entry-demo \
  --event testdata/paper/signal-entry.json --source-ready --output json
./bin/millionaire paper consume --fixture testdata/paper/spot-metadata.json \
  --at 2026-11-12T10:00:00Z --limit 100 --output json
./bin/millionaire signals list --instance paper-entry-demo --output json
# Substitute the signal ID from the preceding output:
./bin/millionaire paper approve SIGNAL_ID \
  --fixture testdata/paper/spot-metadata.json --at 2026-11-12T10:00:00Z --output json
./bin/millionaire paper observe --fixture testdata/paper/spot-entry.json \
  --at 2026-11-12T10:00:01Z --output json
./bin/millionaire paper decisions --output json
```

Use a fresh instance/account with `strategy add ... --action auto_paper` to make
`paper consume` create the intent without approval. `alert_only` signals and exit
rule signals are recorded as observations and never open or reverse positions.
Manual signals wait for `paper approve`; `paper open` remains the standalone
manual entry command and no longer accepts `--signal`.

`strategy evaluate` defaults to an unready observation. `--source-ready` is an
explicit fixture-only readiness assertion, enabling offline execution tests; it
does not validate or enable a live feed. `market watch` still does not execute
entries. No daemon or live monitoring is started by this workflow.

`paper consume` reads at most 100 signals by default (1–1000 via `--limit`) for
the fixture instrument, oldest first. Each decision and its optional intent commit
atomically. Each command acquires the existing exclusive paper owner for its
bounded operation, then releases it; concurrent owners fail explicitly. A crash
between signals leaves completed decisions durable and remaining signals queued.
Rerun consumption after restart; pending orders wait for `paper observe`. A new
price is delivered separately, with both observation and availability strictly
after the decision time. Never use the signal's proposed entry as a fill price.
The existing fill model records the actual source, price and latency.

Entry options on `consume`/`approve`: `--quantity risk` (default, sizing against
current equity, stop and scoped policy), `--leverage 1`, `--fee-rate 0.001`, and
`--slippage-bps 0`. Signal stop/target hints are frozen on the intent and validated
against the actual fill price. Linear long/short uses the shared isolated-margin
service; fixture metadata must match the signal instrument exactly.

One signal owns one decision and at most one order. After a lost approval
response, inspect `paper decisions` or retry with the same timestamp, entry
options and metadata; conflicting approvals fail. Accepted means an intent was
admitted: inspect the nested order status for pending, filled or rejected outcome.
A risk rejection is final for that signal and retains its rationale; future
distinct signals remain separately eligible for policy checks. Current account
state, kill switch, instance enabled state, freshness, cooldown and exposure limits
are checked again before fill. Failed writes roll back and remain retryable;
recorded observations, fills, ledger and journal-outbox items retain existing
atomic replay behavior. No StopLore delivery occurs in M4.

For isolated spot fixture replay, saved reports and frozen-input reruns, see the
[M5 historical replay guide](docs/m5-historical-replay-report.md). Start with
`millionaire backtest run --help`; this offline slice does not enable live monitoring.

Trade performance is available through `millionaire backtest trades RUN-ID
--output json` or `millionaire report trades --scope forward-paper --account
ACCOUNT-ID --output json`. Both support immutable strategy version, instrument
and opening-date filters. See [trade metrics and equity](docs/m5-trade-report.md)
for exact metric boundaries, partial exits, ledger reconciliation and unavailable
valuation labels.

Signal behavior and hypothetical success: `millionaire backtest signals RUN-ID --threshold-percent 2 --stop-percent 1 --window-bars 20 --output json`. Forward scope uses `millionaire report signals --scope forward-paper --account ACCOUNT-ID` with the same explicit rule flags. Read saved definitions/results with `millionaire report signals --saved REPORT-ID --output json`. See [signal report semantics and evidence](docs/m5-signal-report.md).

Linear historical runs add `--market linear --leverage 2 --funding-file testdata/backtest/m5-linear-funding.json`; the JSON file declares expected funding intervals and contemporaneous explicit events. Missing/unknown funding stays labeled. Optional partial-target/trailing and frozen approximate-liquidation flags use the common paper services; see [linear replay assumptions and examples](docs/m5-linear-replay-report.md).
