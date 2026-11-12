# Millionaire

A Go CLI project for public-market monitoring and paper trading. M0 contains a historical-candle reconnaissance spike and verified design notes. The application CLI, persistence, strategies, and simulation engine are later milestones.

Use Go **1.27.2** or a compatible newer toolchain. M0 has no external module dependencies, database, or credentials.

```sh
go run ./cmd/candle-spike --fixture testdata/market/bybit-spot-btcusdt-1m.json
go test ./...
go vet ./...
go build -o /tmp/candle-spike ./cmd/candle-spike
```

The spike prints one JSON candle with exact decimal strings and UTC boundaries. The fixture is synthetic and follows the documented Bybit response shape; it is not captured exchange history. Default tests are offline, including local HTTP test servers.

An explicit online probe requests one fixed BTCUSDT spot 1-minute candle beginning `2022-12-09T16:00:00Z`:

```sh
go run ./cmd/candle-spike
go run ./cmd/candle-spike --base-url https://api.bybit.com
go run ./cmd/candle-spike --help
```

`--fixture` reads only the supplied file. Without it, `--base-url` selects the public REST origin; credentials, paths, query strings, redirects and non-HTTPS remote origins are rejected (loopback HTTP is allowed for tests). Requests time out after 15 seconds. Provider/HTTP errors, malformed responses, unexpected instruments/time ranges, and incomplete candles fail with a nonzero exit and diagnostics on stderr. There is no automatic fixture fallback or live order interface.

REST access returned country-block HTTP 403 from this environment during M0. WebSocket upgrades succeeded, but subscriptions and candle delivery were not tested. Validate connectivity on the actual deployment host before enabling monitoring. See [M0 findings and limitations](docs/m0-report.md), the [StopLore integration map](docs/stoplore-integration.md), and [project documents](docs/README.md).
