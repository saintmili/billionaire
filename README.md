# Millionaire

A Linux-first Go CLI for public-market monitoring and paper trading. M2 provides
configuration, PostgreSQL storage, public Bybit candles, historical backfill,
streaming recovery and explicit offline fixtures. Strategies, accounts, execution,
and the daemon are later milestones.

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
No StopLore tables are touched. No account/ledger tables exist yet; later migrations
must preserve exact numeric values and transactional ledger invariants.

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
closed candles and health/checkpoint state are durable. Synchronous callbacks
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
See the [M0 report](docs/m0-report.md), [M1 report](docs/m1-report.md), [M2 report](docs/m2-report.md),
[StopLore map](docs/stoplore-integration.md), and [project documents](docs/README.md).
