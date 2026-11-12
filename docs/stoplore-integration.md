# StopLore integration map

Inspected 2026-11-12 at [`8064d0e89842a80aa7a2691d32d254466d3a701d`](https://github.com/saintmili/stoplore/tree/8064d0e89842a80aa7a2691d32d254466d3a701d), the then-current `master` tree. This is a source inspection, not a claim of executed StopLore integration tests. Millionaire does not import or vendor StopLore in M0.

## Package, dependencies, and license

The [module](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/go.mod) is `github.com/saintmili/stoplore`, Go 1.25.1, with one root library package and an executable example. Direct dependencies are `google/uuid v1.6.0`, `jackc/pgx/v5 v5.10.0`, and `shopspring/decimal v1.4.0`. These are verified upstream versions, not dependencies added to Millionaire.

The [complete tree](https://api.github.com/repos/saintmili/stoplore/git/trees/8064d0e89842a80aa7a2691d32d254466d3a701d?recursive=1) has no license file; [repository metadata](https://api.github.com/repos/saintmili/stoplore) reported `license: null`. No reuse/distribution license was verified. Resolve permission before introducing the dependency or distributing an integrated binary; public visibility alone does not establish a license.

## Verified exported API inventory

All names below are in the root `stoplore` package. Linked files are pinned to the inspected commit. Request/result structures live beside the operations. This inventory separates the modern decimal journal from the older float-based position API.

| Surface | Actual exported operations and types | Integration use |
| --- | --- | --- |
| [Construction](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/stoplore.go) | `NewWithPostgres(*pgxpool.Pool, []byte) *Service`, `NewWithCSV(string) *Service`, `(*Service).Init() error`; `Service.Journal`, `Service.Exchange` | Initialize once in a separate storage boundary. Caller owns the pool. |
| [Exchanges](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/exchange.go) | `ExchangeService.Add`, `Edit`, `List`, `GetByID`, `GetByName`, `DeleteByID`, `DeleteByName`; `Exchange`, `AddExchangeRequest`, `EditExchangeRequest` | A paper-only journal exchange; explicit entry/exit percentage defaults. |
| [Accounts](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/accounts.go) | `Service.CreateAccount`, `GetAccount`, `ListAccounts`; `Account`, `CreateAccountRequest` | Correlate paper account to journal account; no balances or ledger. |
| [Setups](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/setup.go) | `CreateSetup`, `ReviseSetup`, `GetSetup`, `GetSetupVersion`, `ListSetups`; `Setup`, `SetupVersion`, request types | Link exact strategy version to immutable journal setup version. |
| [Trade and entry](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/trade.go) | `RecordTrade`, `GetTrade`, `ListTrades`; `Trade`, `RecordTradeRequest`, `ExecutionRequest`, `Execution`, `ExecutionFee`, `TradeDirection` (`Long`, `Short`), `ExecutionKind` (`Entry`, `Exit`), `CostBasis` (`RecordedCosts`) | One Millionaire position maps to one trade; first filled entry required. |
| [Scaling](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/scale_trade.go) and [exit](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/close_trade.go) | `RecordEntry(RecordEntryRequest)`, `RecordExit(RecordExitRequest)`, `CloseTrade(CloseTradeRequest)` (all also take context) | Subsequent fills, partial exits, and full exit; do not create a trade per fill. |
| [Funding](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/funding.go) | `RecordFundingPayment`, `RecordFundingPaymentRequest`, `FundingPayment` | Signed settlement amount, positive received / negative paid. |
| [Plans](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/plan.go) | `ReviseTradePlan`, `CorrectTradePlan`; `TradePlan`, `PlanRevision`, `PlanCorrection`, request types | Initial SL/TP in `OriginalPlan`; dated stop/target revisions. No order execution. |
| [Corrections](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/correction.go) | `CorrectExecution`, `CorrectExecutionFee`, `CorrectFundingPayment`, `CorrectTradeContext`, matching request/history types | Audited factual corrections; never substitute these for a new fill. |
| [Review](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/review.go) | `SetTradeReview`; tags, `RuleAdherence`, `TradeReview`, `ReviewCorrection`, request type | Optional human context; tags are mutable, not unique correlation keys. |
| [Performance](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/report.go) and [risk](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/risk.go) | `ReportPerformance`, `ReportFilter`, `PerformanceReport`, `SetupPerformance`, `CompletedTradeResult`, `CorrectOriginalPlannedRisk` and request/history types | Eligible completed trades by setup version/currency; optional original-risk R multiple. |
| [Migration](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/migration.go) and [PostgreSQL import](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/postgres_migration.go) | `ImportCSV`, `ImportPostgres`, `ReviewMigration`, `ConfirmMigration`; requests, `MigrationEvidence`, `MigrationExecution` | Explicit legacy migration, not the adapter's runtime ingestion path. |
| [Decimal](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/decimal.go) | `Decimal`, `ParseDecimal`, `MustDecimal` | Parse exact strings; avoid panic-based constructor for external input. |
| [Legacy contracts](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/contracts.go), [journal](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/journal.go), [models](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/models.go) | `ServiceContract`, `StorageContract`, `JournalContract`, `ExchangeContract`; `Journal.OpenPosition`, `ClosePosition`, `GetPosition`, `ListPositions`, `GetAnalytics`; `Position`, open/close requests, validation/conversion methods, `PositionFilter`, `Side`, `Analytics` | Float-based compatibility surface; excluded from financial mapping. These interfaces do not describe modern trade persistence. |
| [Legacy storage](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/postgres.go) and [CSV](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/csv_position.go) | `PostgresStorage`, `CSVStorage`: position CRUD/Init; CSV also exports exchange CRUD; `Position.GetOrderValue`, fee/quantity/exit/P&L helpers | Do not call or implement to persist modern trades. |
| [Errors](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/errors.go) and [crypto](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/crypto.go) | `ErrNotFound`, `ErrInvalidRequest`, `ErrConflict`, `ErrUnavailable`, legacy nil-request/not-found/already-closed errors; `Encrypt`, `Decrypt` | Classify known rejections; crypto helpers do not establish encryption of modern journal documents. |

The [README](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/README.md) and [example](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/examples/main.go) exercise modern service methods, not `Journal.OpenPosition`.

## Persistence contract

Verified from [service initialization](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/stoplore.go), [document storage](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/journal_state.go), [legacy PostgreSQL storage](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/postgres.go), and the upstream [decimal/storage contract](https://github.com/saintmili/stoplore/blob/8064d0e89842a80aa7a2691d32d254466d3a701d/docs/decimal-and-storage.md):

- Modern PostgreSQL operations read/replace a single versioned JSONB document in `mtl_journal_state`, row `id=1`. Mutations lock the row and commit their own transaction. There is no exported transaction injection or modern storage interface.
- `Service.Init()` initializes both this document store and legacy storage (`trades` with float financial columns on PostgreSQL). Use a dedicated pre-created schema selected by a separate pool's search path. No Millionaire migrations in M0; provision the schema/permissions when M7 integration is implemented.
- CSV modern state is `mtl_journal.csv`, with a version marker and JSON document. Writes take a filesystem lock, sync a temporary file, and rename it. This does not make CSV an application database choice.
- Each operation rereads state. The whole-journal row lock serializes modern writes; budget for this in M7 and test contention. No cross-store atomic commit with Millionaire is exposed.
- New financial values use exact decimals. Optional pointers distinguish unknown from known zero. Entry/exit fee rates use percentage units (`0.04` means 0.04 percent); actual fees are settlement amounts and may be negative rebates. Pass simulated actual fees explicitly, converting bps without floats. Division rounds to 18 places, ties away from zero; verify reconciliation tolerances against Millionaire rounding.
- The `key` passed to `NewWithPostgres` serves legacy notes encryption. Modern document writes serialize JSON directly; do not assume modern notes, tags, or financial context are encrypted by this key.

## Mapping and capability gaps

| Millionaire concept/event | Verified mapping | Remaining ownership or gap |
| --- | --- | --- |
| Paper account | `CreateAccount` under a paper-labeled exchange | Balances, reservations, ledger, risk, and liquidation stay in Millionaire. |
| Strategy definition/version | `CreateSetup` / `ReviseSetup`, `SetupVersionID` on trade | Preserve exact version/hash/YAML and instance correlation in Millionaire; `Rules` is text, not an executable strategy. |
| Signal / rejected order / unfilled order | No dedicated modern record | Store in Millionaire. No journal trade until a real simulated fill. |
| First fill / position open | `RecordTrade` with account, setup version, symbol, direction, settlement, `FirstEntry` | Caller IDs cannot be supplied. Market/provider/timeframe are not typed trade fields; retain canonical instrument externally and use a descriptive symbol. |
| Further entry fill | `RecordEntry` with trade ID | Serialized nondecreasing timestamps; generated execution ID, no source-fill key. |
| Partial exit / final exit | `RecordExit` / `CloseTrade` | Close requires exact remaining quantity; completed trades reject further fills. Record each fill once. |
| Simulated execution fees | `ExecutionRequest.ActualFee` | Supply exact actual charge, including known zero; omitted defaults can reject fills. |
| Funding | `RecordFundingPayment` | Record paid/received sign; zero recorded funding is not proof of full funding coverage. |
| Initial/adjusted SL/TP | `OriginalPlan` / `ReviseTradePlan` | Plan snapshots describe decisions; trailing-stop mechanics, order triggers, and exit reason remain external. |
| Trade results | `ReportPerformance`, `OriginalPlannedRisk` | No signal metrics, equity/drawdown series, MAE/MFE, backtest manifests or live account balance engine. |

## Safe reconciliation boundary

This is a Millionaire adapter design, not an existing StopLore feature. The inspected create/append requests have no external IDs, caller-selected UUIDs, or idempotency tokens. `RecordTrade`, entry/exit/funding append, setup creation, and plan revisions generate fresh IDs. A tag added after creation cannot close the crash window.

M7 must serialize journal delivery per destination and position, and persist source event ID, payload hash, expected previous journal state, operation type, attempt state, returned journal IDs, and errors in Millionaire-owned outbox/correlation tables. Persist an in-flight state **before** calling the library. After success, atomically save correlations and acknowledge the item locally. On restart, in-flight items and ambiguous transport/commit errors become `needs_reconciliation`; block dependent items for that position. Retry automatically only when the operation is known not to have applied.

Use `ListTrades`, `GetTrade`, `ListSetups`, and account/exchange reads to compare stored expectations with observed journal state. Identical fills can legitimately exist: matching timestamps/amounts is evidence, not unique proof. Require explicit resolution when ambiguous. Do not blindly reissue an uncertain `RecordTrade`, append fill, or funding payment; do not fix uncertainty by writing StopLore tables. Exactly-once automatic delivery needs an upstream public idempotency contract before it can be promised. Financial processing continues independently and journal status must expose pending/uncertain items.

M7 acceptance must run the actual library against isolated PostgreSQL schemas and test open → partial exit → close, actual fees/funding, plan changes, restart, commit-before-ack loss, concurrent delivery, outage, and reconciliation through public reads. Reinspect/pin the reviewed source after license resolution; no StopLore implementation changes are part of M0.
