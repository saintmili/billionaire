# Millionaire product specification

**Status:** Approved product requirements; technical defaults remain subject to M0 inspection.
**Product:** `millionaire`, a Go CLI, with TUI and web interfaces planned later.

This document owns product behavior, constraints, test requirements, and release acceptance. See [the roadmap](roadmap.md) for milestone order and progress, [technical design](design.md) for proposed implementation details, and [the glossary](../GLOSSARY.md) for domain terms.

## Product requirements

### Included in MVP

1. Continuous public-market monitoring, initially BTC/USDT and ETH/USDT.
2. Spot and USDT-margined linear perpetual futures; long and short on perpetuals, long-only unleveraged spot. Build abstractions for broader future markets, but do not imply margin shorting on spot is supported.
3. Multiple configurable timeframes per strategy, including aggregation of lower-timeframe candles where possible.
4. Strategies authored as versioned YAML configurations for built-ins and simple composable conditions; Go interfaces for complex strategies compiled into the application. No unsafe arbitrary-code execution from YAML.
5. Initial example strategies: 20-candle breakout, swing-based support/resistance, and a documented candlestick/price-action pattern (e.g. bullish/bearish engulfing). More sophisticated concepts (FVG, order blocks, etc.) must fit the extension interface later.
6. Signal evaluation on closed candles or on market updates, selected per strategy. Repeated signals while conditions hold are supported, with separate deduplication/cooldown/execution protection.
7. Both alert-only/manual-approval and automatic **paper** entries; configurable per strategy or instance.
8. Separate entry and exit rules, configurable strategy parameters, immutable versions.
9. Paper accounts (dedicated per strategy or shared), adjustable balances, market orders, simulated fees/slippage, P&L, margin, leverage, funding, liquidation, SL/TP, trailing stops and partial exits. Do not automatically reverse a position.
10. Backtesting as part of the first MVP. Backtests must be reproducible and use the same decision/risk/execution code where appropriate.
11. Risk controls: fixed/percent risk sizing, account and strategy limits, daily loss, max concurrent positions, validation before every paper order.
12. Analytics: signals, positions/trades, strategy comparisons and backtests.
13. Terminal alerts and Linux desktop notifications; Telegram optional. More channels later.
14. CLI control, local workstation and VPS operation, persistence and recovery across restarts.
15. Journaling integration with StopLore, through its verified public API.

### Explicitly excluded from MVP

- Real-money order placement, exchange API keys, broker account sync, authenticated balance endpoints.
- Native TUI and web UI (architecture should not block them).
- Portfolio optimization/AI predictions, arbitrary user script execution, distributed market-data clusters.
- Production-perfect venue matching, order-book matching, order-book depth-aware slippage, full cross-margin/inverse-contract modeling.
- Spot margin trading/shorting and coin-margined contracts.
- Automated position reversal.

### Nonfunctional requirements

- Linux-first; local or VPS, single active engine process per logical deployment.
- Resilient reconnect and REST gap backfill, observable error conditions, graceful shutdown.
- UTC timestamps and explicit candle open/close boundaries; exact decimal arithmetic for money, price and quantity (no `float64` for financial ledger arithmetic).
- All financial state changes are transactional and auditable.
- No look-ahead bias in backtests.
- Idempotent event processing and journal sync.
- Stable commands, machine-readable `--output json` where applicable.
- Config validation; errors should identify bad fields, market capability limitations and data gaps.

## Market-data contract

### Initial provider

Implement one end-to-end read-only public provider supporting **spot and USDT linear perpetual** historical candles and streaming candle updates. Bybit V5 public market endpoints are a practical first choice because public WebSockets distinguish spot from linear markets and include a candle-closed flag; historical candles are available through its public market API. This is a technical default, not an availability guarantee in any region. Ensure provider selection and URLs can be configured. Other providers, including Binance public spot data, should be addable later.

References for implementation verification:
- https://bybit-exchange.github.io/docs/v5/ws/connect
- https://bybit-exchange.github.io/docs/v5/websocket/public/kline
- https://bybit-exchange.github.io/docs/v5/market/kline
- https://github.com/binance/binance-spot-api-docs/blob/master/faqs/market_data_only.md

### Instrument and candle identity

Canonical instrument identity: `(provider, market_type, base, quote, instrument_id)`; do not collapse spot BTC/USDT and perpetual BTC/USDT into one instrument. Timeframes are strongly typed; provider-native intervals preferred, otherwise aggregate from an exact lower interval aligned to UTC/calendar boundaries. Arbitrary timeframes are accepted only when an available data source permits correct aggregation; return an actionable error otherwise.

Canonical candle: UTC open_time, exclusive end_time, OHLC, base_volume, closed flag, source, revision/update sequence. Unique closed-candle key: `(source, instrument, timeframe, open_time)`. Closed candles must not be duplicated by WebSocket reconnects or historical backfills. Preserve provenance/corrections where required.

### Live-data behavior

- Historical REST warm-up before activating strategies; validate required lookback for each timeframe.
- Subscribe to public WebSocket updates, distinguish partial versus closed candles, ping/pong, reconnect with exponential backoff + jitter and bounded retries.
- Backfill missed candles after reconnect; detect gaps, stale feeds and clock drift.
- Pause new **automatic** paper entries for affected instruments when data is stale/inconsistent; existing positions may continue to be managed using the best available valid data or be explicitly marked as unmonitored. Never silently claim a stop or fill was observed when it was not.
- Bounded queues and explicit overflow policy; never silently drop a closed-candle event needed for strategy evaluation.
- Backtest history pagination, cache reuse, rate limiting and data-quality checks.
- Persist closed candles by default. Partial updates may be ephemeral unless captured explicitly for replay.

### Timeframes and intrabar caveat

`on_candle_close` works from OHLCV history. `on_update` evaluates on each received market/candle update (not necessarily every individual trade tick). **Historical OHLCV alone cannot reproduce intrabar update sequences.** For `on_update` strategies, allow backtesting only on recorded update/tick streams or a clearly labeled approximate model, never claim equivalence with live execution.

## Strategies and signals

### Strategy definitions

Strategy *definition* has stable ID and name; *versions* are immutable snapshots containing schema version, implementation type, parameters, entry rules, exit rules, risk settings, evaluation mode, signal policy and content hash. Strategy *instances* bind a version to an instrument/timeframe set/account and can be enabled or disabled independently. Editing creates a new version. Backtests refer to the exact version and data snapshot.

Use YAML for built-in parameters and simple declarative predicates (AND/OR, comparisons of fields or calculated indicators, crossover, previous values). Validate rule types and parameters; do not execute arbitrary expressions or dynamic Go code from config. Advanced strategies implement the Go `Strategy` interface and are registered at build time; plugin loading is future scope.

### Evaluation, repetition and suppression

- Evaluation modes: `on_candle_close`, `on_update`.
- Emission modes: `on_transition`, `once_per_candle`, `while_true`.
- An evaluation yields normalized `Signal` records (side, direction, strategy/version/instance, instrument, timeframe, observation timestamp, proposed entry, risk hints, reason/evidence).
- Every **distinct incoming event** has a deterministic ID; replaying the same event must not create a second signal, alert or trade.
- `while_true` means a new signal may occur on a **new distinct evaluation event** while the rule remains satisfied, not repeatedly on duplicate messages or a busy loop.
- Separate cooldowns for alert delivery and paper execution. By default: no duplicate paper entry when an account already has the same strategy/instrument/direction exposure, and at most one new entry per candle unless explicitly allowed (pyramiding).
- Multi-timeframe snapshots use only candles/events available at the evaluation timestamp; higher-timeframe pending candles must not leak future closes.

### Reference breakout strategy

Long breakout: on close of candle `t`, calculate maximum `high` among the **20 fully completed candles preceding `t`**. Signal when close(t) is greater than the preceding 20-bar high; default `on_transition` or one signal per closed candle (configurable). Breakout candle's low minus configurable offset is initial SL; target = entry + 2 × (entry - stop). Ignore invalid SL/entry geometries. For backtests, a close-confirmed signal fills **at the next available candle open** (plus modeled slippage), not the already-known signal-candle close. For live paper trading, fill using the next valid observable price after signal; record latency and source. Add symmetric short variant for linear perpetuals only.

The reference strategy should be usable on BTC/USDT and ETH/USDT. Provide deterministic fixtures that include a valid breakout and a false breakout.

### Other initial strategies

- S/R: documented deterministic pivot-high/pivot-low algorithm with lookback, swing confirmation lag, zone tolerance, touch/rejection/break detection; no future-confirmed swing points before confirmation time.
- Price action: bullish/bearish engulfing with clearly specified body/wick and candle-completion rules; optional trend filter in a second timeframe.
- Indicators/rule primitives support moving averages, OHLC, volume and candle patterns as extensions, with tested warm-up handling.

## Trading policy and paper execution

### Signal action modes

An instance is `alert_only`, `manual_paper` or `auto_paper`; all can produce persistent signals and alerts. Manual approval creates an order intent only upon explicit user action. Allow an explicitly initiated manual paper order without a preceding signal, subject to identical account/risk rules. Auto mode calls the same risk gate. Rejected signals persist with rationale; rejection is not a filled trade.

### Accounts and exposure

- Account balances in USDT for initial instruments; `initial_balance` default example **10,000 USDT** (illustrative only), configurable.
- Multiple accounts; each strategy instance can use a dedicated account or share a selected account.
- Track available balance, reserved margin, realized/unrealized P&L, equity, free margin and immutable ledger entries.
- Risk limits evaluated at account and strategy scopes; concurrent entry attempts must serialize/lock relevant account state transactionally.
- Risk parameters: fixed nominal risk, percentage risk (illustrative default **1% of equity**), max daily loss, max total positions, max per-symbol exposure, max leverage, max margin usage, cooldown, pyramiding (off by default).
- For perpetuals in MVP: **isolated margin only**, USDT linear contracts, configurable leverage, funding and a clearly labeled simplified maintenance-margin/liquidation model. Avoid representing estimated liquidation levels as exchange-accurate.
- Spot: buy/sell existing holdings only, no borrowing, leverage or naked shorting.

### Orders and fills

- MVP order entry: market; architecture for limit orders later. Optional resting limit execution is a follow-up milestone, not a prerequisite for the base MVP.
- Use fill model with configurable maker/taker fee assumptions and slippage, instrument quantity/price steps and minimum notional validation.
- Support SL/TP, adjustable stops, trailing stops, partial close, and explicit manual close.
- No automated reversal: a signal in the opposite direction never implicitly closes and opens the reverse position; a separate explicit close action is required.
- Maintain order, fill, position and ledger records independently; all fills are idempotent and money movements atomic.
- For candle-only simulations where SL and TP both lie within the same candle, use the **adverse-first** conservative assumption by default, mark the event as ambiguous, and expose the choice in backtest metadata. Handle gaps by filling at next tradable observed price plus slippage rather than promising impossible stop prices.
- Funding events require historical/public funding rates where available. When not available, backtests must explicitly record the missing funding model; do not quietly treat the result as venue-realistic.
- Realized P&L, fees, funding and liquidation costs must reconcile to account ledger balance.

### Safety

- Strict paper-only execution mode; no authenticated exchange API interface or secrets accepted in MVP.
- `kill-switch`: stop new paper entries while preserving read-only monitoring, exit management and reporting; clear user-facing state.
- Data stale, margin deficit, validation failure or risk limit exceeded => reject new paper order with reason.

## Backtesting and deterministic replay

- CLI accepts strategy ID/version, provider+instrument+market, date range, timeframes, seed, account capital, fees/slippage/risk model.
- Fetch historical candles and persist them with provenance. Reject runs missing required lookback periods or with unacknowledged data gaps.
- Replay using a virtual clock and deterministic total ordering of historical events. Never access later candles in strategy decisions, indicator warm-ups, stop updates or sizing.
- Run same strategy and risk services as live paper mode; use a replay-specific market-data source and execution-price adapter. The backtest output must never update live paper accounts.
- Persist run manifest: code/build version, strategy version/hash, config hash, provider, market type, dataset identity/range, model assumptions, seed and metrics.
- Allow report, compare, export JSON/CSV; later UI can consume persisted results.
- Separate train/test date ranges in example reports, flag sample-size and survivorship/look-ahead hazards. Do not optimize strategies automatically in MVP.

## Analytics contract

### Signal metrics

Counts of **distinct generated signals**, per symbol/timeframe/version, suppression rates, signal-to-entry conversion, interval between signals, and success under a user-selected hypothetical threshold/window (e.g. TP before SL in 20 bars). Do not define a signal as successful merely because a paper trade was profitable. Store success-rule definition with report.

### Trade metrics

Trade count, win/loss/breakeven rate, net/gross P&L, fees/funding, profit factor (handle zero gross loss), average win/loss, expectancy, planned versus realized R, MAE/MFE where resolution permits, average holding time, equity curve, peak-to-trough maximum drawdown, exposure and leverage usage.

### Strategy comparisons

Compare by immutable strategy version, symbols, timeframes, accounts and date range. Display matched market period and assumptions; do not present different periods/capital models as directly comparable without warnings. Include sample size and drawdown alongside return.

### Backtest reporting

Metrics plus input data quality, execution assumptions, rejected signals, missed fills, ambiguous stop/TP bars, funding-data coverage and reproducibility hash. Separate backtest returns from forward paper results.

## CLI contract

Use Cobra (or a similar justified framework), with subcommands rather than a monolithic interactive UI. All commands have `--help`, meaningful exit codes and `--output json` when returning structured data.

```text
millionaire init
millionaire config validate
millionaire provider list
millionaire provider test --provider bybit
millionaire market watch --market spot --symbol BTC/USDT --timeframe 1m
millionaire market backfill --market linear --symbol ETH/USDT --timeframe 15m --from ... --to ...
millionaire strategy list
millionaire strategy validate examples/strategies/breakout.yaml
millionaire strategy add examples/strategies/breakout.yaml
millionaire strategy enable <instance-id>
millionaire strategy disable <instance-id>
millionaire start
millionaire status
millionaire signals list --strategy <id>
millionaire paper account create --name demo --balance 10000
millionaire paper accounts
millionaire paper positions
millionaire paper approve <signal-id>
millionaire paper open --account <id> --market spot --symbol BTC/USDT --side long --quantity <qty>
millionaire paper close <position-id> --quantity <qty>
millionaire paper adjust <position-id> --stop-loss <price>
millionaire paper kill-switch on|off
millionaire backtest run --strategy <version-id> --symbol BTC/USDT --market spot --from ... --to ...
millionaire backtest report <run-id>
millionaire backtest compare <run-id> <run-id>
millionaire report signals|trades|strategies
millionaire journal status
millionaire journal sync
millionaire journal reconcile
```

For commands affecting the running engine, provide safe single-owner coordination. Preferred first step: daemon maintains the only active runtime; CLI uses a Unix-domain control socket for mutations. Separate CLI processes must not independently evaluate/process live events against the same account. VPS access via SSH rather than publicly exposed control endpoints. Reading reports can use read-only database queries or the same control API, but keep write semantics consistent.

## Required automated test matrix

1. **Market:** REST pagination, WebSocket out-of-order, duplicated partial/closed candles, interrupted connection, gap reconciliation, stale feed, timeframe aggregation boundaries and market-type separation.
2. **Strategy:** insufficient warmup, boundary prices, crossover/equality, repeated events, cooldown, version immutability, intra-candle versus close, 4h filter not seeing an unfinished future close, confirmed-pivot lag.
3. **Paper:** zero/negative/NaN-equivalent invalid inputs, precision ticks, fees and slippage, long/short P&L, spot restrictions, leverage, isolated margin, funding, liquidation, partial exits, trailing stop, SL/TP collision, daily loss limit, exposure locks.
4. **Backtest:** future-data trap fixtures, reproducibility, missing candles, identical replay and offline live-event sequence where semantics match, backtest isolation.
5. **Journal:** unsupported StopLore feature, transient DB failure, outbox retry, idempotent mapping, multiple fills per position, reconcile after restart.
6. **Notification:** duplicate event, repeated-condition alert policy, Telegram errors, headless desktop, secrets not logged.
7. **Operations:** process restart, corrupt/invalid config, failed migrations, missing PostgreSQL, single-instance lock, graceful stop and recovery.

Use deterministic fixture data for core tests; external-provider network tests must be opt-in so CI is reliable.

## Deployment and operations

- Local: PostgreSQL via Compose or installed service, `millionaire start` foreground initially; desktop notifications optional; no publicly exposed control API.
- VPS: dedicated OS user, env file mode 0600, systemd unit for engine, PostgreSQL with authentication and backup schedule, journal file/report retention, logs through journald or structured files; Telegram for off-device notifications.
- Do not place PostgreSQL or the control socket on a public internet interface.
- Minimum visibility: last successful market update per instrument, current provider state, gap count, active strategy instances, signals emitted, active paper positions, backtest status, journal outbox lag and notification delivery failures.
- Backup: PostgreSQL logical backup plus strategy YAML and application version metadata; document restore drill.

## MVP release acceptance scenario

With a seeded `10,000 USDT` demo account and deterministic sample market data:

1. Configure BTC/USDT spot and ETH/USDT linear perpetual monitoring.
2. Register and enable a specific immutable version of breakout-20.
3. Backfill 20+ candles and produce a qualifying breakout only after its closing candle becomes available.
4. Emit and persist a unique signal, with reason/price/strategy version.
5. Deliver terminal alert; optional desktop/Telegram delivery respects policy.
6. In manual mode, a position waits for explicit approval. In auto mode, risk validation submits a paper order and receives a simulated fill.
7. Position receives stop and 2R target; subsequent market events mark equity/P&L and close the position under the documented fill model.
8. A partial close, trailing-stop update, fee or funding adjustment produces correct account ledger and position history as applicable.
9. StopLore receives the correctly mapped journal events or an auditable pending outbox entry; retry/reconcile is idempotent.
10. Run the same strategy version on historical fixtures and obtain deterministic backtest analytics, equity curve and export.
11. Restart the daemon during monitoring; perform backfill and continue without duplicate signals, orders or journals.
12. Verify all exchange access was public market data. No real exchange account, API key or order submitted.
