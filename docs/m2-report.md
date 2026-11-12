# M2 market data and historical synchronization

Implemented 2026-11-12 against baseline `d54b0c8`. M2 adds read-only market
observations and durable candles. No strategies, paper entries, daemon, or
StopLore integration were implemented.

## Files and decisions

- `internal/marketdata/bybit/`: public metadata/history adapter, backward REST
  pagination, rate limiting, bounded HTTP-429/provider-limit retries, clock checks,
  and one public WebSocket connection with subscription acknowledgment,
  heartbeat, size/deadline limits, partial/closed decoding and cancellation.
- `internal/marketdata/`: canonical query/range validation, complete-bucket
  aggregation, cache-aware synchronization, bounded reconnect/backfill and feed
  freshness/gap state. Callbacks are synchronous, providing backpressure with no
  queue that can silently lose a closed candle. Consumer/storage errors stop the
  stream; restarting backfills from persisted data.
- `internal/marketdata/fixture/` and `testdata/market/m2.json`: a labeled synthetic
  provider with its own `fixture` identity, 60 one-minute bars for each BTC/ETH
  spot/linear instrument, scripted partial/duplicate/out-of-order updates and a
  virtual observation clock. No network access or captured exchange prices.
- Domain timeframes now support fixed whole minutes/hours/days up to 30d plus UTC
  Monday weeks and calendar months. Unsupported units/counts fail. Canonical
  candle validation enforces matching opening/exclusive-end boundaries.
- `internal/store/postgres/migrations/0002_market_data.{up,down}.sql` and candle
  repository: instrument metadata, exact NUMERIC OHLC/volume, canonical JSON
  payloads, immutable revision history and persisted per-stream feed/checkpoint
  state. Existing migration 1 checksums are unchanged.
- CLI `provider list|test`, `market backfill|watch|status`; updated run instructions.

Gorilla WebSocket **v1.5.3** is the only new direct dependency. Its
[documented concurrency contract](https://pkg.go.dev/github.com/gorilla/websocket@v1.5.3)
supports one reader/one writer; the adapter writes the initial subscription before
starting its sole heartbeat writer. It accepts no exchange credentials and calls
no authenticated endpoint. HTTP redirect following is disabled.

## Provider contract verification

Primary references checked during M2:
[REST klines](https://bybit-exchange.github.io/docs/v5/market/kline),
[instrument metadata](https://bybit-exchange.github.io/docs/v5/market/instrument),
[public kline updates](https://bybit-exchange.github.io/docs/v5/websocket/public/kline),
and [socket connection/heartbeat](https://bybit-exchange.github.io/docs/v5/ws/connect).
Spot uses base precision/minimum order amount; deprecated spot minimum quantity
is not treated as a current venue constraint. Linear instruments must be trading
USDT-settled linear perpetuals. Financial values remain exact decimal strings.

REST newest-first rows become ascending closed candles; forming rows never enter
storage. Stream `confirm` and inclusive millisecond ends normalize to the same
canonical boundaries. REST and socket data share source `bybit`, instrument,
timeframe and open time. Content hashes omit observation time, so identical
closes do not become new revisions simply because they were fetched again.
Different content retains a revision; older observations cannot replace a newer
canonical value. Partial events retain distinct observation timestamps for later
strategy evaluation but are not persisted as closed candles.

Native Bybit intervals are preferred. Other supported intervals fetch 1m history
and aggregate only complete contiguous target buckets. Derived live streams
use valid 1m observations for freshness, repair missing source closes, and emit completed aggregates; they do not fabricate partial
target updates. Missing source candles or entire buckets cause explicit errors.
Queries are capped at 100,000 candles, including the 1m fallback query; split
larger requests. Calendar months are never represented as a fixed duration.

## Synchronization and failure behavior

Backfill uses cached candles and fetches only missing contiguous ranges unless
`--refresh` is selected. Remaining missing candles produce counts and a nonzero
exit; valid data already received remains durable. Watch warms up history before
subscription and again before every reconnect. It repairs a detected jump in
closed candles before emitting the later close. Reconnect attempts are bounded
per watch invocation, with exponential backoff/jitter and cancellation.

Feed state contains connection status, last valid observation, contiguous closed
end, gap count and failure reason. `AllowsEntries(now, ttl)` requires a connected,
fresh, complete source without clock drift and with the latest expected close.
This is a market-data prerequisite for M3/M4, not permission to place an order.
It is evaluated against the current clock at read time, so a stalled/crashed
source cannot remain healthy indefinitely. Graceful stop clears connection state.
Partial history does not establish a live observation. A completed fixture is
explicitly disconnected.

A provider timestamp more than 30 seconds from the supplied clock is rejected.
Requests and socket frames are bounded; invalid metadata, decimals, boundaries,
subscription rejection and HTTP/provider failures are surfaced without raw
response bodies or credentials. A long-running watch is canceled by the command
context rather than M1's short database setup timeout. M6 will add engine
ownership and process restart supervision; M2's watcher only writes market data.

## Verification

User-confirmed test seams: provider history/stream through local protocol
servers, aggregation/synchronization/freshness, PostgreSQL repositories and CLI.
Tests ran throughout red/green slices and regular build/vet checks. Evidence:

- HTTP protocol tests paginate BTC/USDT and ETH/USDT in both spot/linear categories
  at 1m and 15m; validate metadata, ordering and exact decimals without auth.
- Real WebSocket tests verify topic subscription, acknowledgment, heartbeat,
  partial/closed flags and exclusive ends. A forced socket interruption advances
  the virtual clock; reconnect fetches the missing closed candles through REST
  before resuming. Real PostgreSQL reads confirm the complete sequence.
- Repository tests cover distinct markets/symbols/timeframes, duplicate writes,
  exact precision, correction revisions and migration up/down/reopen. Fixture
  tests cover history, cache reuse during provider failure, partial/duplicate/
  late-partial suppression, durable checkpoints and disconnected completion.
- Aggregation tests cover incomplete and entirely missing buckets and calendar
  month boundaries. Freshness tests reject stale, gapped and disconnected feeds.
- CLI acceptance backfills every BTC/ETH × spot/linear × 1m/15m combination,
  watches the scripted fixture, and reads persisted health with no network.
- Failure tests cover geographic HTTP 403, malformed JSON/rows, provider errors,
  excessive clock drift, bounded rate-limit retries and forming candles.

`go test ./...` passed with PostgreSQL integration enabled. `go test -race
./internal/marketdata/... ./internal/store/postgres ./internal/cli`, `go vet ./...`,
`go build ./...`, `go mod verify`, local Markdown target checks and
`git diff --check` passed. The built CLI also passed `provider test` using the
explicit fixture. CI includes the new offline protocol and database tests;
a hosted GitHub run has not been triggered locally. Independent review follows
the implementation commit.

## Limitations and milestone boundary

A fresh direct public REST probe still returned HTTP 403 with a CloudFront
country-block response from this environment. The actual-host live validation gate remains pending; M2 is not marked fully accepted. No live historical fetch or stream
subscription is claimed here. The complete adapter path is verified through local
HTTP/WebSocket servers and synthetic fixtures; verify both REST and streaming
from the actual intended host before forward monitoring. There is no silent
fallback, region routing workaround, or claim that another provider is validated.

Only closed candles and feed state are durable by default. Native partial update
capture/replay is later scope, as required for faithful `on_update` backtests.
Derived streams emit closes only. Cached history is not revalidated until an
explicit refresh or newer provider observation supplies a correction. A database
or consumer failure stops watch explicitly; supervision and single-engine
ownership belong to M6. StopLore licensing and the Go patch-refresh release gate
remain as documented in M0/M1. M3 was not started.
