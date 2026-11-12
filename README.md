# Millionaire

A Linux-first Go CLI for public-market monitoring and paper trading. M1 provides
configuration, PostgreSQL initialization/migrations and storage status. Market
feeds, strategies, accounts, execution, and the daemon are later milestones.

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
and documented native intervals are accepted. Prices/balances use plain decimal
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

The database user must be able to create the application schema and its objects.
No StopLore tables are touched. No financial tables exist yet; later migrations
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
See the [M0 report](docs/m0-report.md), [M1 report](docs/m1-report.md),
[StopLore map](docs/stoplore-integration.md), and [project documents](docs/README.md).
