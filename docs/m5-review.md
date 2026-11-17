# M5 independent review and resolutions

The independent review examined `ed00c7f93602dc82276699cacfc1decf3521a2ac...9090197`
(the completed M5 integration tree). The two axes below retain the original
reviewers' findings separately. The reviewers did not rerun broad acceptance
tests; the subsequent checks in this document were run while implementing fixes.

## Standards

Totals: 0 documented-standard breaches; 1 nonblocking judgment call (possible duplicated code); worst: nonblocking.

Documented-standard breaches: none found in `git diff ed00c7f93602dc82276699cacfc1decf3521a2ac...HEAD` (HEAD `9090197`). The PostgreSQL namespace adapter follows ADR 0006; the isolated replay exceptions follow ADR 0007 and retain the shared financial owner required by ADR 0002. No conflicting ADR or new dependency was found.

Judgment calls:

- **Possible Duplicated Code (nonblocking):** `internal/cli/trades.go:40–68`, `internal/cli/signals_report.go:48–84`, and `internal/cli/compare.go:72–102` repeat report selection parsing/validation. Trades and signals both contain `if scope == "forward-paper" && (run != "" || f.AccountID == "")`, with the same error, then repeat `time.Parse(time.RFC3339, from)` / the corresponding `to` block and `(marketFlags{provider: provider, market: market, symbol: symbol, frame: "1m"}).identity(provider)`. Strategy comparison repeats the date and instrument parsing. This makes a change to shared report flag semantics require edits in three handlers. Extract small shared helpers for report scope/run selection and optional date/instrument parsing, while keeping each report's distinct filtering semantics in its handler. This is a smell-baseline heuristic, not a documented rule violation.

Review was read-only; no broad test rerun was needed for this standards finding. Existing M5 acceptance evidence was not independently rerun.

## Spec

Totals: 1 confirmed finding (P2); worst: P2.

- **P2 — Persist all signal decisions before marking a historical run complete.** At `internal/store/postgres/backtest.go:299`, the new result collector calls `SignalDecisions`, whose query unconditionally uses `LIMIT 10000` (`internal/store/postgres/signal_entries.go:170`). A supported run can span up to 100,000 candles; a `while_true` strategy emitting more than 10,000 signals therefore finishes as `completed` with all signals/orders but only the first 10,000 decisions, ordered by hashed signal ID rather than event time. Rejected decisions outside that arbitrary subset disappear from saved report/compare/export evidence; `BacktestSnapshot` still labels the artifacts `verified` and computes `rejected_decisions` from the truncated list. Ticket #17 requires “Expose run status, signals/decisions, orders/fills, final balances and a minimal report through CLI and JSON”; #22 requires “Surface data-quality warnings, rejected signals, missed fills […] Unsupported or missing evidence is explicit rather than silently omitted as a favorable zero.” Remove the bounded interactive-list limit for result collection, paginate completely, or explicitly reject/label incomplete evidence. Verified by tracing the exact collector/query and supported 100,000-candle validation limit; no broad test rerun was needed.

No other confirmed Spec findings. Review covered tickets #17–#23, the M5 requirements/ADRs, replay causality and end policies, frozen-input isolation, financial analytics and retained comparison/export. Documented model limits were assessed as explicit capability boundaries rather than automatically treated as violations.

## Resolution

- `7a2bfcf32572a0990eb751e3a4d6f5855beddc1a` removes the interactive
  decision-list cap from isolated backtest result collection. Completed runs save
  the entire decision history; snapshots and exports retain rejection evidence
  beyond the former 10,000-record boundary. The interactive `SignalDecisions`
  listing remains bounded at 10,000. `PaperOrders` and `PaperFills` were inspected:
  neither has a collection limit, so no corresponding change was needed.
- `392e885a35b53f2ddc5d265795a326ebd16eb456` shares CLI scope/run/account
  validation and optional date/instrument parsing. Trade dates still select
  lifecycle opening instants, signal dates still select signals, and strategy
  dates still select overlapping whole runs. Saved signal-rule selection,
  timeframe parsing and each report's filter validation remain in their handlers.

Previously retained reports are immutable and are not repaired automatically.
Rerun affected pre-fix runs from their frozen source to obtain complete retained
artifacts; this change does not recover omitted decisions into old saved reports.
M2 actual-host validation remains pending, live monitoring remains disabled, and
work stops at M5.

## Evidence

The public PostgreSQL regression
`TestBacktestRetainsDecisionsBeyondInteractiveLimit` first failed with exactly
`status=completed decisions=10000` where 10,001 decisions were required. After the
collector fix it passed against real PostgreSQL in 1.768 seconds. The short
fixture replay bulk seeds 10,000 additional decision records through a disposable
database trigger, including a rejected decision sorted beyond the old boundary;
SQL is fixture setup only. Assertions use `RunBacktest`, `BacktestReport`,
`BacktestSnapshot` and JSON/CSV `WriteExport`, proving complete saved collection
and preservation of late rejection evidence. This avoids replaying thousands of
candles merely to populate the result set. The fixture's event trigger requires
a disposable PostgreSQL superuser; application execution privileges are unchanged.

Existing CLI boundary tests passed after the parsing refactor. Verification uses
`GOCACHE=/tmp/billionaire-go-cache`; PostgreSQL checks use the disposable local
server at `127.0.0.1:55435`, with a fresh isolated database per integration test.

```sh
MILLIONAIRE_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55435/postgres?sslmode=disable' \
  GOCACHE=/tmp/billionaire-go-cache go test ./internal/store/postgres \
  -run TestBacktestRetainsDecisionsBeyondInteractiveLimit -count=1
GOCACHE=/tmp/billionaire-go-cache go test ./internal/cli
MILLIONAIRE_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55435/postgres?sslmode=disable' \
  GOCACHE=/tmp/billionaire-go-cache go test ./...
GOCACHE=/tmp/billionaire-go-cache go vet ./...
GOCACHE=/tmp/billionaire-go-cache go build ./...
MILLIONAIRE_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55435/postgres?sslmode=disable' \
  GOCACHE=/tmp/billionaire-go-cache go test -race ./internal/store/postgres \
  ./internal/cli ./internal/analytics ./internal/backtest \
  -run 'TestBacktestRetainsDecisionsBeyondInteractiveLimit|TestBacktestRunPersistsCausalSpotResultsAndIsolatesPaper|Test.*(Report|Compare|Export)' -count=1
```

Verification outcome: all checks passed. The full real-PostgreSQL suite passed
(PostgreSQL package: 74.240 seconds; CLI: 21.284 seconds). Practical focused race
checks passed (PostgreSQL: 21.517 seconds; CLI: 6.946 seconds; analytics: 1.018
seconds; backtest: 1.027 seconds). Vet, build and `git diff --check` passed. These
are fix-author checks, not a claim that the original reviewers reran acceptance.
