# Millionaire technical design

**Status:** M0 reconnaissance complete; accepted storage, execution/journal and candle/replay boundaries are recorded in [ADRs](adr/). Examples, interfaces, package layout, and conceptual tables remain illustrative rather than implemented contracts. See [M0 findings](m0-report.md) for provider and integration limitations.

This document owns implementation proposals and their existing rationale. Product requirements remain in [the specification](spec.md); milestone scope and gates live in [the roadmap](roadmap.md). The [M0 integration map](stoplore-integration.md) verifies external API claims against a pinned source revision. An unverified proposal is not an accepted ADR.

## Storage decision: PostgreSQL

Use **PostgreSQL as the only application database for MVP**. Use `pgx/v5`, versioned SQL migrations and `sqlc` where it reduces boilerplate. Ship `compose.yaml` for local development and deployment instructions for a VPS. Why not SQLite: SQLite is attractive for a single-binary/offline CLI but only allows one writer at a time, requires care with high-volume market writes, and may complicate shared journal persistence; this project anticipates continuous market ingestion, multiple CLI readers, later UI access, and already uses Go/PostgreSQL tooling. That is a trade-off, not a claim that SQLite is inadequate for a small single-process bot.

Keep StopLore in a dedicated schema and pool as accepted in [ADR 0001](adr/0001-postgresql-storage-boundary.md). It accepts a PostgreSQL pool, but owns its transactions and whole-journal JSONB persistence. Maintain a Millionaire-owned journal-sync outbox and correlation table; its API has no external idempotency keys. See the [verified map](stoplore-integration.md) before adapter work.

**Do not build a second storage adapter in the MVP.** Document the repository interfaces sufficiently to permit SQLite later if needed, without creating unnecessary database abstractions.

## Core architecture

Use a modular monolith, ports-and-adapters style, with a single shared domain model, isolated application services and transport-independent business logic.

```text
CLI (Cobra) / private web HTTP interface / future TUI
         |
 Application services / control interface
         |
 MarketData -> Normalizer -> Candle Store -> Event Bus
                                     |
                     Strategy Runtime + Indicator Engine
                                     |
                     Signal Store + Signal Notifications
                                     |
                         Execution Policy
                          /            \
                    Alert only     Manual/Auto paper
                                         |
                                   Risk Manager
                                         |
                                  Paper Execution
                                         |
                             Ledger / Orders / Positions
                                         |
                              Journal Outbox + StopLore
                                         |
                         Analytics / Reports / Backtest

Backtest: Historical Feed + Simulated Clock -> same strategy,
         risk, execution and analytics interfaces.
```

Do not introduce Kafka/Redis/a message broker in MVP. A typed in-process event dispatcher plus durable DB checkpoints/outbox is enough. Do not scatter business logic throughout CLI commands.

### Suggested Go package layout

```text
cmd/millionaire/main.go
internal/app/               # Composition root and service wiring
internal/config/            # YAML, environment overrides, validation
internal/domain/            # Instrument, candle, signal, order, position, account
internal/marketdata/        # Interfaces, normalization, history, stream
internal/marketdata/bybit/  # Public Bybit spot+linear adapter (MVP)
internal/marketdata/replay/ # Historical/fixture feed
internal/candles/           # Interval aggregation, persistence, synchronization
internal/indicator/         # Reusable pure calculations
internal/strategy/          # Engine, registry, config DSL, version management
internal/strategy/builtin/  # Breakout, S/R, price-action examples
internal/signal/            # Signals, correlation IDs and suppression
internal/risk/              # Position sizing, exposure and daily limits
internal/paper/             # Simulated orders, fills, positions, margin
internal/portfolio/         # Accounts, balances, immutable ledger
internal/backtest/          # Replay, virtual clock, reproducible runs
internal/analytics/         # Signal/trade/strategy/backtest metrics
internal/notify/            # Dispatcher, terminal, desktop, Telegram
internal/journal/           # StopLore adapter and durable sync/retry
internal/store/postgres/    # Queries and repositories
internal/control/           # Local process control, optional Unix socket
internal/cli/               # Cobra commands and formatters
internal/web/               # Private HTTP transport and browser UI integration
migrations/
configs/
examples/strategies/
testdata/market/
docs/
deploy/
```

Refine package names based on actual implementation needs; avoid premature 1:1 packages for trivial types. `domain`, `indicator`, risk math and strategy rule evaluation should be independently testable.

### Core ports (illustrative, NOT guessed StopLore APIs)

```go
type MarketDataProvider interface {
    Capabilities(ctx context.Context) (Capabilities, error)
    HistoricalCandles(ctx context.Context, q CandleQuery) ([]Candle, error)
    Stream(ctx context.Context, req StreamRequest) (<-chan MarketEvent, <-chan error)
}

type Strategy interface {
    ID() string
    Validate(params map[string]any) error
    Evaluate(ctx context.Context, snapshot MarketSnapshot, state StrategyState) (Evaluation, error)
}

type ExecutionEngine interface {
    Submit(ctx context.Context, intent OrderIntent) (ExecutionResult, error)
    Adjust(ctx context.Context, req PositionAdjustment) error
}

type JournalSink interface {
    Record(ctx context.Context, event JournalEvent) error
}
```

The exact API should evolve with real code, including injectable clocks and deterministic event IDs. Market feeds, analytics, and StopLore never directly mutate paper trading state. Do not force the paper engine's internal event types onto StopLore; translate at the adapter boundary.

## Notification architecture

`Notification` contains event type, level, correlation ID, strategy/version, instrument, timeframe, price, UTC timestamp, reason, delivery targets and delivery state. Events: strategy signal, order opened, order rejected, SL/TP exit, liquidation, data stale/recovered, engine failures, backtest complete.

MVP channels:
- Terminal structured output; write logs to stderr to keep `--output json` clean.
- Linux desktop via `notify-send` adapter (optional, no crash/headless failure).
- Optional Telegram Bot API with chat ID and token read from environment or protected configuration; no token logging.

Bounded retry with backoff for transient delivery failures, persistence and idempotent notification keys. Senders must not block strategy processing. Webhooks/email later, behind interface. Alert frequency policy is separate from signal emission and trade-entry policy.

## Persistence model

Initial conceptual tables (final migration design may consolidate):

- `instruments`, `market_candles`, `market_sync_checkpoints`
- `strategy_definitions`, `strategy_versions`, `strategy_instances`, `strategy_runtime_state`
- `signals`, `signal_decisions`, `notification_outbox`, `notification_deliveries`
- `paper_accounts`, `paper_orders`, `paper_fills`, `paper_positions`, `paper_position_events`, `paper_ledger_entries`
- `risk_rejections`, `funding_rates`, `backtest_runs`, `backtest_trades`, `backtest_metrics`
- `journal_outbox`, `journal_correlations`, `engine_checkpoints`

Key invariants:

1. Market candle uniqueness `(provider, market_type, instrument, timeframe, open_time)`.
2. Strategy versions immutable after creation; instance references one version.
3. Signals unique on deterministic `(strategy_instance, event_id, rule_id, signal_kind)`; distinguish genuinely new repeated-event signals.
4. Paper fills unique per simulated execution ID; ledger entries append-only.
5. Single transaction for fill + position + account ledger + journal-outbox enqueue.
6. Outbox processors retry idempotently and expose permanently failed events for inspection.
7. Strategy runtime checkpoints allow restart with no duplicate entry; gaps/backfills replay consistently.
8. Appropriate indices for candle time-range lookups, signals by strategy/time, positions by account/status, and outbox delivery status.
9. Configured retention for high-frequency market updates; candles/backtest manifests must not disappear while referenced by reproducible runs without explicit warning.

## StopLore integration

**M0 source verification complete.** The [integration map](stoplore-integration.md) pins the inspected commit and distinguishes modern decimal service methods from the legacy float API. Resolve the missing license before introducing the dependency. Real PostgreSQL integration tests remain an M7 gate. Uncertain writes require reconciliation; do not claim automatic exactly-once retry with this API.

Integration steps:

1. Inspect `go.mod`, README/examples, storage interfaces, account/setups/plans, trade lifecycle, executions, fees/funding, analytics and transaction semantics.
2. Write `docs/stoplore-integration.md`: mapping of Millionaire `PaperAccount`, `StrategyVersion`, `Signal`, `Order`, `Fill`, `Position`, fees, funding, partial close and SL/TP to **verified** StopLore types/methods.
3. Decide exactly what constitutes a StopLore journal entry (e.g. opening a position vs recording a closing fill); avoid double counting individual fills as independent trades.
4. Implement a narrow `JournalSink` adapter that uses the public API. Store source IDs/versions, journal IDs, sync state and failure metadata. Do not write directly to StopLore tables.
5. Persist journal outbox event and financial transaction atomically in Millionaire; deliver asynchronously. Follow the integration map's reconciliation boundary for uncertain writes, retry only known-unapplied operations, and provide `journal sync`, `journal status`, `journal reconcile`.
6. Integration-test opening, partial exits, final exit, fees/funding, restart/retry and failed journal delivery. If a StopLore feature is unsupported, implement a documented partial mapping and hold the feature behind a capability check.
7. Prefer reusing StopLore's analytics for its supported trade metrics, and implement Millionaire-only signal/backtest metrics separately. Cross-check calculations to avoid disagreement.

## Configuration contract (M1)

The implemented schema and defaults live in [configs/example.yaml](../configs/example.yaml), embedded into the binary. See [README](../README.md#commands-and-configuration) for strict validation, supported environment overrides, secret references, and failure behavior. M1 intentionally includes only app, storage, market-data selection, and initial paper-capital settings; strategy/risk/notification/journal settings are added with their implementing milestones. Configuration rejects live mode and exchange credential fields. Provider URLs are configurable but public/read-only; M1 status checks storage rather than provider availability.

The M3 strategy contract is implemented in [examples/strategies/](../examples/strategies/) and documented in the [M3 report](m3-report.md). Configuration hashes produce immutable numbered versions; instances pin their instrument/account/action and version. The registry exposes compiled Go validation/evaluation/warm-up interfaces, and declarative rules use a bounded typed schema. PostgreSQL commits evaluation identity, policy state and signals together. Order/risk execution is M4. See [ADR 0004](adr/0004-preserve-event-availability-clock.md) for recorded availability clocks and retry semantics.

## Private MVP web interface

[ADR 0005](adr/0005-private-web-in-mvp.md) brings a limited operational web app into MVP. Deliver it after M6 establishes daemon ownership and control services. Preserve [ADR 0002](adr/0002-paper-execution-and-journal-boundaries.md): HTTP handlers delegate mutations to the single owning engine through the shared application/control boundary. They must not create another evaluator, write financial state directly, or bypass risk checks. UI validation assists entry; application services remain authoritative.

Serve the UI and its HTTP transport on a private origin, defaulting to loopback for SSH forwarding. Private-network binding requires explicit configuration. Validate browser origins/hosts and protect mutation requests against cross-site requests; keep reads free of side effects. Public hosting and multi-user identity are not part of this design. Document the chosen private-access setup during the web milestone.

Give each mutation a stable request identity and retain its outcome across retries/restarts as needed. Reusing an identity must return the original outcome without repeating the action; reject conflicting payload reuse. After a lost response the browser queries the original outcome before retrying. Disabling a submit button alone cannot guarantee this behavior. Distinguish transport uncertainty, accepted/pending execution, fills and risk rejection.

Read models expose engine connectivity and market-data freshness separately, with observation times. Reconnect refreshes authoritative state before further trading actions; background updates must not erase an in-progress form. Polling versus streaming and the frontend stack remain implementation proposals to settle when planning the web milestone.

Use responsive tables/detail views and forms for the specified daily workflows; charts, strategy editing and backtest configuration are deferred. Use semantic theme colors for light/dark, system preference when no override exists, and browser-local persistence with a return-to-system option. Organize English UI copy in a translation-ready message catalog, avoiding sentence fragments assembled in code. Do not translate instrument identities or user content. Translations, language selection and RTL layouts are deferred; no localization framework is mandated now.

## Baseline summary

This table summarizes the baseline for navigation, including the approved web scope change; the detailed sections in this document and [the specification](spec.md) govern each topic. Unverified technical proposals still require implementation-time verification.

| Topic | Requirement or proposed MVP default |
|---|---|
| Product | Millionaire Go application with CLI and private web UI |
| Architecture | Modular monolith, single active runtime |
| Database | PostgreSQL only; SQLite postponed |
| Market connectivity | Public data APIs only, no authenticated exchange integration |
| Default provider | Bybit V5 public spot + linear (subject to validation) |
| Symbols | BTC/USDT and ETH/USDT configured manually |
| Markets | Spot and USDT linear perpetual first; future markets extendable |
| Strategy authoring | YAML common rules + compiled Go advanced strategies |
| Initial examples | Breakout-20, swing S/R, engulfing price action |
| Signal frequency | Both close and update evaluation; configurable repeated signals |
| Trade action | Alert-only, manual paper, automatic paper |
| Account model | Shared or strategy-specific paper account |
| Trading constraints | No automated reversal; default pyramiding disabled |
| Orders | Market execution MVP, limit order later |
| Perpetual margin | Isolated, simplified and explicitly labeled |
| Backtesting | MVP requirement; deterministic OHLCV close-model initially |
| Analytics | Signal, trading, strategy and backtest categories |
| Notifications | Terminal, Linux desktop, optional Telegram |
| Journal | StopLore public API adapter after verification |
| UI roadmap | CLI + private operational web in MVP; native TUI later |
| Web appearance | Responsive desktop/mobile, system light/dark with remembered override |
| UI languages | English MVP; translation preparation now, multiple languages/RTL later |
| Future live trading | Not implemented and disabled in MVP |

## Backtest execution isolation

The M5 run/report implementation reuses existing PostgreSQL strategy, risk and execution services inside a retained per-run schema. Main-schema records retain frozen inputs/manifests and results. [ADR 0006](adr/0006-backtest-execution-isolation.md) records the isolation tradeoff, privilege/retention consequences and SQL namespace boundary; [ticket #17 evidence](m5-spot-backtest-report.md) documents the initial spot fixture model.
