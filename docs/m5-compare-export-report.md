# M5 comparison and exact exports — ticket #22

Compare retained backtest runs and inspect strategy results through read-only
PostgreSQL APIs and CLI commands. Every result keeps its own immutable version,
instrument metadata, account, primary timeframe (verified retained version),
timeframe set, original range, manifest, configuration, artifacts and warnings.
No strategy metrics are pooled across accounts, models or datasets. This slice
does not optimize strategies or enable monitoring; the M2 actual-host gate remains.

## Commands

```sh
millionaire backtest compare LEFT_RUN RIGHT_RUN --output json
millionaire report strategies --scope backtest --output json
millionaire report strategies --scope backtest --run RUN_A,RUN_B \
  --strategy-version VERSION --provider fixture --market spot --symbol BTC/USDT \
  --timeframe 1m --from 2026-01-01T00:20:00Z --to 2026-01-02T00:00:00Z \
  --output json
millionaire backtest export RUN_ID --kind report --format json
millionaire backtest export RUN_ID --kind trades --format csv > trades.csv
millionaire backtest export RUN_ID --kind equity --format csv > equity.csv
```

`--account` selects an isolated account identity. Repeated/comma-separated `--run`
selects retained runs; without it, selection enumerates the registry in creation
order with ID as tie break. Duplicated run selections appear once. Strategy,
instrument and timeframe filters select whole runs; date filters select overlapping
original ranges `[from,to)`. They never trim a position lifecycle or recalculate
return on the intersection. Forward-paper strategy attribution is explicitly
unsupported here; use `report trades --scope forward-paper --account ACCOUNT`
for its separate account-wide boundary. Missing IDs and invalid options exit nonzero.

Comparison schema version `1` contains `left`, `right`, `matched_period` (nullable
intersection), `directly_comparable`, ordered `differences` with field paths and
original left/right values, and warnings. Configuration/model differences include
capital, fee/slippage, risk, funding, seed, clock, range and instrument metadata;
manifest comparisons retain all dataset/configuration/strategy/reproducibility/
build/execution/availability/end-policy fields. Numerically equal decimal strings
and timeframe set order alone do not create configuration-field differences;
original hash differences are still shown. Generated run/account identities remain
visible context rather than different financial assumptions. Saved signal reports
are compared explicitly, including different rules, filters and observation clocks.

Metrics are full-run trade analytics: return is accompanied by maximum drawdown
and closed-lifecycle sample size, open positions, valuation status, exact P&L/costs,
profit-factor labels and the retained equity curve. A positive return alone does
not establish superiority. Small samples, survivorship and selection/look-ahead
bias limit inference. `matched_period` never represents newly calculated matched-
period performance. Different dataset hashes may reflect revisions, lookback,
coverage or provenance even when requested dates match. Unknown build provenance
prevents authoritative direct comparability. Read the preserved warnings even
when the same run is compared with itself.

Snapshots preserve full decisions/orders/fills, including rejection reasons,
pending end-of-range intents, ambiguous stop/target flags, price gaps and isolated
deficits. Counts are available only for completed runs with verified frozen
inputs. Pending orders are retained unfilled intents, not proof of all hypothetical
missed fills. Coverage/model and exact funding source evidence remain in the full
configuration/report. Legacy missing fields do not establish complete funding or
absence of ambiguity. Old missing candidate evidence is explicitly warned.

Failed/running/cancelled runs retain status/error and partial audit artifacts but
have null metrics and counts. Invalid/missing frozen inputs keep the saved report
readable with unavailable evidence and null analytics. Completed empty runs can
have zero trades and defined return/drawdown when retained equity supports them;
missing/stale marks yield null authoritative return/drawdown. No saved signal rule
is `not_evaluated/no_saved_rule`, never an implicit success definition. All existing
saved signal reports are included independently with ID, complete success rule,
filter, observation clock, evidence hash, outcomes and warnings. Reads never create
or regenerate those reports, choose a hidden rule or rerun trading.

## Export schema version 1

`backtest export` writes to stdout; `--format` is independent of the root display
option. JSON is `{schema_version:"1",kind,context,rows}`. `context` is the complete
snapshot described above; report rows contain that snapshot, trade rows follow
`analytics.Trade`, and equity rows follow `analytics.EquityPoint`. Empty/unavailable
collections are `rows:[]`; context distinguishes their reason.

CSV has exactly these fixed columns, in order:

```text
schema_version,kind,row_type,sequence,context_json,row_json
```

Every data row uses `schema_version=1`, `kind=report|trades|equity`,
`row_type=data`, zero-based decimal `sequence`, compact JSON of the full snapshot
in `context_json`, and compact typed row JSON in `row_json`. These JSON cells are
intentional: they retain nested provenance and evolving financial artifacts
without lossy flattening. Parse CSV first with a conforming CSV reader, then parse
the two JSON cells. For no rows, one `row_type=context` record has empty sequence,
the full context and literal `null` in `row_json`. A header-only file is never used
to represent unavailable/empty performance.

Exact decimals are JSON strings, including values beyond float64 precision; null
is unavailable, not zero. No rounding or float conversion occurs. Times use UTC
RFC3339 with nanosecond precision as needed. Ratios are fractions; holding time
is seconds. Equity sequence preserves retained order, including equal-time close/
open phases; JSON array order provides the same sequence. Nested commas, quotes,
CRLF and newlines are escaped by JSON and `encoding/csv`. Output errors propagate
as command failures. The complete context is repeated per CSV record, trading file
size for standalone interpretability and lossless context/rule retention.

Persist and back up registry inputs/results/signal reports together with isolated
schemas per ADR 0006. Export and comparison read saved reports and verified frozen
inputs, never current market prices or current strategy definitions. Source cache
changes cannot rewrite exported results. These commands work without execution
schemas; missing frozen inputs explicitly reduce evidence. No migration is added.

## Verification

Approved seams: backtest service, PostgreSQL public repository, analytics and CLI.
The first comparison and export cycles failed for missing public APIs, then passed
with the implementation. Independent fixture expectations include return
`-0.01179`, drawdown `117.9`, one closed lifecycle and changed fees exposing
`config.fee_rate`. Actual CLI exercises persisted signal rules, strategy filters
and all three JSON/CSV exports alongside existing causal/isolation checks.

Additional cases cover boundary-touch/disjoint periods, partial overlap,
capital changes, numeric-format/frame-order equivalence, empty completed runs,
incomplete equity, corrupt frozen inputs and failed/running/cancelled reports.
Artifacts with nonexistent execution schemas remain readable. Comparison/list
succeed with PostgreSQL `default_transaction_read_only=on`, proving no report-
generation or trading writes. Tests retain huge/18-place decimals, negative/null
values, escaped JSON/CSV, nanosecond times, equal-time phase order, empty context
rows and writer errors.

Final full-suite, vet, race and CLI build/help outcomes are recorded after execution.
