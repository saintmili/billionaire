# M0 reconnaissance and technical design

Evidence collected 2026-11-12. M0 is complete through its documented connectivity-limitation/offline-fixture acceptance path. This does not validate production connectivity or implement M1. See [follow-up checklist](milestone-checklist.md).

## Repository and implementation scope

Baseline `861682c` contained only AGENTS.md, glossary, specification/design/roadmap and agent guidance. No Go module, application code, migrations, tests, or accepted ADRs existed. There was no existing application to preserve or database to migrate.

Added `go.mod`, `cmd/candle-spike/{main.go,main_test.go}`, `testdata/market/`, root run instructions, this report, the [StopLore map](stoplore-integration.md), [milestone checklist](milestone-checklist.md), and three ADRs. Updated document navigation and roadmap evidence. No schema changes, external dependencies, exchange authentication, or trading code.

## Runtime and dependency baseline

- Pin `go 1.27.2` in `go.mod`, matching the installed `go1.27.2-X:nodwarf5 linux/amd64`. The [official release policy](https://go.dev/doc/devel/release) supports the latest two major release lines. The [download listing](https://go.dev/dl/?mode=json) reported 1.27.4, but fetching `https://go.dev/dl/go1.27.4.linux-amd64.tar.gz` returned HTTP 404 here. Tests therefore establish compatibility with the available 1.27.2 toolchain, not a downloaded 1.27.4 binary. Refresh to an obtainable current security patch before release.
- M0 uses only the standard library: HTTP/JSON, flags, timestamps, and decimal strings without financial calculations. No `go.sum` is needed.
- For M1, the source-verified candidate pins are `pgx/v5 v5.10.0` (PostgreSQL driver) and `shopspring/decimal v1.4.0` (exact financial arithmetic), matching StopLore's [module](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/go.mod). Revalidate compatibility/security before installing. CLI/YAML/migration tooling selection belongs to M1; add exact versions and checksums only when actually used. StopLore is pinned for inspection by commit, not included as a dependency pending license resolution.

## Market-data verification

The [REST contract](https://bybit-exchange.github.io/docs/v5/market/kline) supports public `GET /v5/market/kline` with explicit `category=spot|linear`, uppercase symbols, interval, millisecond bounds and limit. Results are reverse chronological; each row contains timestamp, OHLC, volume and turnover as strings. A forming candle's close is its latest trade price. M2 must paginate backward, normalize ascending, validate closure and deduplicate; M0 requests a single fixed historical minute.

The [connection contract](https://bybit-exchange.github.io/docs/v5/ws/connect) defines public spot and linear sockets. The [streaming kline contract](https://bybit-exchange.github.io/docs/v5/websocket/public/kline) uses `kline.{interval}.{symbol}` and `confirm` to distinguish closure. Its sample end is the last included millisecond: normalize to exclusive end by adding one millisecond, with interval-boundary checks. M2 must implement heartbeat/reconnect/backfill; M0 does not implement streaming.

`linear` also covers products outside the MVP. M2 must validate [instrument metadata](https://bybit-exchange.github.io/docs/v5/market/instrument): USDT quote/settlement and perpetual contract type, trading status, price/quantity precision and minima. Do not equate every linear symbol with an eligible USDT perpetual.

### Connectivity observations

| Probe from this execution environment | Observed result | Limit of evidence |
| --- | --- | --- |
| Default sandbox request to public server time | Local proxy connection refused | Environment transport failure; not a Bybit response. |
| Direct public REST spot BTCUSDT historical minute | HTTP 403, CloudFront country-block response | No spot historical candle retrieved. |
| Direct public REST linear ETHUSDT historical minute | Same HTTP 403 | No linear historical candle retrieved. |
| Direct public spot WebSocket upgrade | HTTP 101 with matching accept header | Handshake only; no subscription or updates tested. |
| Direct public linear WebSocket upgrade | HTTP 101 with matching accept header | Handshake only; no subscription or updates tested. |
| Go spike with synthetic offline fixture | One normalized closed candle | Decoder/command behavior only, not actual historical prices. |

Reproduce REST probes (replace `spot/BTCUSDT` with `linear/ETHUSDT` for the second):

```sh
curl --noproxy '*' --max-time 20 -sS -w '\nHTTP %{http_code}\n' \
  'https://api.bybit.com/v5/market/kline?category=spot&symbol=BTCUSDT&interval=1&start=1670601600000&end=1670601659999&limit=1'
```

Reproduce a handshake for each market by changing the last path component:

```sh
curl --noproxy '*' --http1.1 --max-time 3 -i \
  -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  https://stream.bybit.com/v5/public/spot
```

Curl exits 28 after the upgraded connection stays open without a subscription; the recorded 101 is handshake evidence, not successful streaming. These direct probes remove the unavailable local proxy; they do not alter the source region. The observed egress location does not establish the user's location or future VPS availability.

Bybit [integration guidance](https://bybit-exchange.github.io/docs/v5/guide) documents geographic 403s. Its [service restrictions](https://www.bybit.com/en/help-center/article/Service-Restricted-Countries) include Iran among excluded jurisdictions. A timezone is not proof of residence. Retain configurable provider origins and an offline mode; verify suitability and REST plus streaming availability on the intended runtime. A different provider requires its own capability verification; M0 does not claim Binance or another venue is a validated substitute. Production monitoring remains gated on this check.

## Ports and accepted decisions

These are Billionaire-owned boundaries to implement incrementally, not StopLore interfaces or new packages created by the spike:

| Port/boundary | Responsibility and failure contract |
| --- | --- |
| Market data | Query instrument capabilities/history; stream normalized market events and explicit source/gap/stale errors. Public data only. |
| Strategy | Validate immutable parameters; evaluate an as-of snapshot to signals with deterministic event identity. No access to future candles or account mutation. |
| Paper execution/risk | Validate every intent, serialize account changes, atomically commit fills/positions/ledger/outbox; explicit rejection reason. No exchange order client. |
| Replay clock/feed/pricing | Deterministic available-event order and simulated execution prices with isolated account state. Live decision services remain reusable. |
| Journal sink | Translate committed events to verified modern StopLore methods, expose capability gaps and uncertain sync, never alter ledger state. |
| Notifications/control | Outbox-backed delivery; single engine owns mutations through a private Unix socket. A notification failure cannot erase an event. |

Accepted ADRs: [PostgreSQL boundary](adr/0001-postgresql-storage-boundary.md), [execution and journal separation](adr/0002-paper-execution-and-journal-boundaries.md), [candle/replay semantics](adr/0003-candle-and-replay-semantics.md). Exact Go method signatures evolve with the first vertical slice; illustrative design interfaces are not verified external APIs.

MVP constraints remain as specified: public data, paper-only orders, long-only unleveraged spot, isolated USDT linear perpetual simulation, no automatic reversal, exact ledger arithmetic, and reproducible backtesting. Journaling cannot promise automatic exactly-once delivery with the inspected API; uncertainty must be visible and reconciled.

## Verification and remaining gates

Confirmed command seam: offline JSON output and invalid/provider-error handling, plus a local HTTP server for public request/HTTP-failure behavior. TDD first failed on the absent command, then passed with fixture decoding; rejection cases first exposed invalid acceptance/panics; HTTP request behavior first failed on the missing base-url option. Build/type checks and focused test runs accompanied implementation.

Validation: `go test ./...` and `go test -race ./...` passed; focused command tests and `go build -o /tmp/billionaire-candle-spike ./cmd/candle-spike` passed during implementation. Local Markdown targets and `git diff --check` passed. `go vet ./...` and the final command build passed. Independent Standards and Spec review follows the implementation commit, using baseline `861682c`. The spike intentionally supports only one fixed spot BTCUSDT minute. It is not a configurable history adapter, decimal ledger, migration runner, application CLI, or strategy runtime.

Remaining gates: resolve StopLore licensing before adding the dependency; verify provider availability on the actual host before live monitoring; test StopLore's real PostgreSQL behavior and uncertain-write recovery in M7. Those are explicit later acceptance gates, not fabricated M0 successes.

## Independent code review

Reviewed implementation commit `732db1b` against user-approved baseline `861682c` using `git diff 861682c...HEAD`. The Standards and Spec axes ran in separate fresh-context agents. This report addition records their results without changing code.

### Standards

No findings. The diff follows documented M0 scope, glossary vocabulary, document ownership, and ADR boundaries. Deterministic fixtures/local HTTP servers, exact decimal strings, UTC exclusive ends, and public unauthenticated requests conform to project requirements. No actionable baseline smells. Total: 0 documented-standard breaches; 0 smell findings.

### Spec

No findings. Reconnaissance, the source-backed StopLore map, ADRs, checklist, and bootstrap satisfy M0 through its permitted documented-connectivity-limitation/offline-fixture path. No M1 implementation or real trading was introduced. Licensing, connectivity, and uncertain journal writes are explicit later gates. Total: 0 findings.
