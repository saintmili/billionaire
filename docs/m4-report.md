# M4 offline acceptance report

M4 implements the paper-account lifecycle under the approved offline sequencing
exception. It uses deterministic public/fixture observations and real PostgreSQL.
M2's actual-host REST/stream validation is still pending because this environment
is geographically blocked. Live monitoring remains disabled. Stop at M4; M5,
M6, M6W and M7 have not started.

## Delivered behavior

The slice reports retain implementation and command details:
[accounts](m4-accounts-report.md), [spot execution](m4-spot-report.md),
[risk controls](m4-risk-report.md), [linear execution](m4-linear-report.md),
[signal entries](m4-signals-report.md), [protection](m4-protection-report.md), and
[funding/approximate liquidation](m4-funding-report.md).

Accounts, exact immutable ledger entries, orders, fills, positions, protection
adjustments, funding outcomes and financial journal-outbox enqueue persist in
PostgreSQL through migration 9. An exclusive paper owner serializes bounded
fixture mutations. CLI commands release ownership after each invocation; subsequent
commands and a restarted owner recover durable outcomes. This is offline operation,
not the M6 daemon or control socket. Outbox delivery and real StopLore journaling
remain M7 work.

Standalone orders, explicit approvals and automatic decisions share account and
strategy risk gates. Orders fill only from a later usable observation; signal
prices never become executable prices. Pyramiding defaults off and an opposite
signal cannot implicitly close or reverse exposure. Spot supports long holdings
with leverage 1; linear supports isolated long/short exposure. Kill switch,
disabled instances and disabled markets block entries while exits continue.

Acceptance exposed two joined financial defects and verified their corrections:

- An entry with positive collateral could already meet the inclusive approximate
  liquidation threshold. Execution now checks the prospective position against
  its persisted liquidation model, including entry slippage, before filling.
  Both long/short 100x default 1% and 5x custom 20% thresholds reject without a fill.
- Daily-loss checks ignored funding and counted waived isolated gap losses as
  collectable losses. They now include effective-day funding collateral changes
  and the recorded fill deficit waiver. Funding is counted once, actual debit
  loss blocks entry, credits offset losses, and waived gap losses do not consume
  the daily-loss budget. UTC reset behavior remains covered.

No dependency or migration was added for acceptance.

## Combined acceptance evidence

The approved seams are public paper service methods, PostgreSQL repository
methods, and CLI/JSON commands. Tests inspect public repository reads; SQL triggers
are used only to inject transaction failures.

| Requirement | Evidence |
| --- | --- |
| Approved/automatic × long/short signal-created journeys | `internal/store/postgres/m4_acceptance_test.go`: no fill at signal price, next-observation entry, .5 of 2 target, funding on the remaining 1.5, owner restart, identical request/event replay, final stop with kill switch and disabled instance/market |
| Exact cross-feature reconciliation | Same journeys verify cash, reserved collateral, fees, funding and position P&L against immutable ledger totals; funded gap liquidation beats a genuinely queued manual close and checks exact fee/deficit amounts |
| Joined transaction rollback | Signal-created partial targets and funding-triggered liquidation inject failures at fills, account, position, ledger, journal outbox and durable outcome writes; every public financial/decision snapshot stays unchanged, then identical retry succeeds once |
| Shared entry gates | `m4_risk_acceptance_test.go`: standalone/approved/automatic × account limit, strategy limit, insufficient margin, stale/unready source, disabled market and persistent kill switch; execution revalidation, durable rejection/replay, admission gates and no money movement |
| Conservative protection and restart | `m4_protection_acceptance_test.go`: long/short signal-created ambiguous bars use adverse stop and slippage; trailing activation cannot exit in the same bar, overlapping bars are skipped, restarted owner exits from persisted trailing state |
| Concurrency and no reversal | Same file: approved/automatic entries share a max-position limit; concurrent funded-liquidation replay with pending entry/manual close cannot duplicate exits; opposite signal preserves the original exposure |
| 18-decimal residuals | Same file: a one-atto funding debit and partial margin allocation settle the full remainder exactly, leaving no reserved collateral after closure |
| CLI/JSON lifecycle | `internal/cli/m4_acceptance_test.go`: approved long and automatic short each create account/version/instance/signal, wait for a later fill, partially target, apply/replay funding and finish while paused; short gap liquidation rejects its queued competing close; exact cash/ledger/P&L are checked through JSON |
| Spot restrictions | CLI acceptance rejects naked short and leverage 2 before creating orders, fills, positions or money movements; existing manual CLI tests cover spot/linear partial/full closes |
| Existing slice behavior retained | Account, risk, spot, signal, protection, linear and funding tests cover validation, immutable identities, duplicate intervals, freshness, migration upgrade/down, gap handling, failure/outbox rollback and owner exclusivity |

Worked journey values use price 100, quantity 2, leverage 5 and fee 0.1%.
Long closes .5 at 110, pays 1.5 funding on the remaining exposure and stops 1.5 at
90: final cash `988.11`, realized P&L `-11.89`, fees `.39`. Short closes .5 at
90, receives 1.5 funding and stops 1.5 at 110: final cash `991.09`, realized P&L
`-8.91`, fees `.41`. The CLI short gap variant closes the remaining 1.5 at 150
with approximate liquidation: cash `974.755`, P&L `-25.245`, fee `1.125` for
liquidation, and isolated deficit `44.625`. The write-off protects available cash.

## Verification

All required checks passed on 2026-11-14 using Go `1.27.2-X:nodwarf5`,
`GOCACHE=/tmp/m4-go-cache` and an isolated local PostgreSQL test cluster supplied
through `MILLIONAIRE_TEST_DATABASE_URL` (no credentials recorded here):

```sh
go test ./...
go test -race ./internal/paper ./internal/store/postgres ./internal/cli
go vet ./...
go build ./...
git diff --check
```

The full PostgreSQL package completed in 102.341 seconds; the race run completed
its PostgreSQL package in 131.492 seconds and CLI package in 44.762 seconds.
The PostgreSQL tests ran against real isolated databases, including migrations,
concurrency and trigger-injected rollback. These were executed checks, not
skipped integration tests. Focused red/green runs first reproduced both financial
defects described above, then passed with the minimal fixes. Repository CI's
required test/vet/build commands also passed. M4 offline acceptance is complete;
M2's live-host gate remains pending.

## Integration review

Independent Standards and Spec reviewers assessed
`git diff 9ba1fc7...803ae8a` after the funding and acceptance slices were merged
onto `integration/m4-completion`. Standards found no actionable documented-rule
violations or material baseline smells. Spec found no actionable missing,
incorrect or out-of-scope behavior against tickets #7/#8 and the accepted offline
M4 contract. Both reviewers inspected the changes and accepted the executed test
evidence above; they did not independently rerun PostgreSQL checks.

Review totals: Standards — zero findings; Spec — zero findings.

## Offline operation and failure recovery

The [README](../README.md) provides account, manual order, signal approval,
automatic consumption, protection, funding and liquidation commands. Use
`--output json` for exact decimal strings, durable identities and reasons. Use a
fresh account/instance for independent examples. Order/adjustment IDs and explicit
`--at` timestamps must remain identical on retries. After a lost response, inspect
`paper orders`, `paper decisions`, `paper fills`, `paper positions`, `paper funding
list` and `paper account show` before choosing a new request identity.

Readiness is an explicit fixture assertion, not proof of live connectivity.
Metadata must match the instrument and precise price/quantity ticks. Entry
policies and market/instance state are checked again at fill time; an accepted
intent can later reject. Rejection is durable and does not move money. Invalid
spot short/leverage syntax fails before order persistence. Failed transactions
remain retryable; unknown commit outcomes require identical retries.

Funding effective/availability clocks must increase and funding must precede
observations/adjustments at the same effective instant. Only positions opened
strictly before it qualify. Missing expected intervals require explicit missing
records; absent intervals are not inferred and an empty history is unknown.
Retroactive corrections are rejected after exposure has advanced. Positive
funding charges longs and credits shorts; the signed amount rounds down to 18
places and changes isolated collateral, keeping available cash unchanged until
settlement. Daily loss follows the effective UTC day, includes funding credits
and debits, and excludes explicitly waived isolated deficits.

The liquidation model is approximate, uses fixture/public observed prices and
per-entry maintenance/fee assumptions, and never claims venue risk tiers or mark
prices. An entry already at its model's liquidation threshold is rejected.
Stops reached before liquidation win; an opening gap through liquidation wins
against targets or queued manual closes. Completed ambiguous candles choose the
adverse outcome. Trailing updates take effect on a later event; bars overlapping
previous position state are skipped. Isolated loss caps and deficit write-offs
are explicit approximations, not exchange insurance or collateral top-ups.

No authenticated trading client, exchange order API or exchange credentials are
accepted. Provider funding retrieval/schedules and actual-host validation are
unresolved outside this offline milestone. Financial outbox enqueue is verified;
journal delivery, backtest products, daemon/socket operation, browser operation
and production deployment are not claimed.
