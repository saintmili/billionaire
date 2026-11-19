# M6 assembled integration review fixes

All three findings from the assembled review of `integration/m6-operation` at
`3583218` are addressed. This report supplements the historical
[M6 review report](m6-review-report.md); it does not replace its offline evidence.
Final assembled verification after these fixes uses the source-matched `./scripts/check --status` record; see the [acceptance report](m6-report.md).

## Standards

### P2 — Refresh execution clocks after blocking financial preparation

The owner previously refreshed its runtime gate once, before protection queries,
pending-order/account locks and risk queries. A price admitted as fresh could
therefore fill an entry or execute protection after waiting beyond its TTL.

The owner now refreshes after each order's locks and again after risk preparation,
immediately before planning its fill. Protection acquires its position/account
locks before refreshing and evaluating each position. Its order insert follows
the decision, so that insert cannot age the clock used for planning the exit.
Each later order/position samples independently. Callers without a refresh callback
retain their explicit virtual/replay clock. Event availability remains immutable.

`TestForegroundPricesExpireWhileFinancialPreparationWaits` exercises `app.Run`
with real PostgreSQL and the public financial read interfaces. An injected exact
clock advances from 10:00 to 10:02 only after PostgreSQL confirms the owner is
blocked; the TTL is one minute. Cases cover pending-order, entry-account,
risk-control, protective-position and protective-account locks, plus a two-account
batch whose first fill blocks before the second order is decided. All checks
inspect fills, positions and the public accepted-price outcome rather than
asserting internal queries. PostgreSQL locks/triggers supply faults only.

Red evidence: before the fix, the entry-account case produced one stale fill;
both protective lock cases closed the position with a second stale fill; the
multi-order case filled both orders instead of retaining only the first fill.
After the fix, all six lock/batch cases pass under the race detector: stale
observations produce no later fill/exit or accepted-price health outcome.

### P3 — Share funding-health derivation

Feed and exposure health previously repeated the same funding-history and missing
interval queries and the same unknown/supplied/missing derivation. `FundingHealth`
and `instrumentFundingHealth` now own that derivation. Each snapshot caches the
result by instrument, including unknown coverage, and supplies the same value to
all of that instrument's feeds and positions. JSON field names remain the same.
The funding/health journey checks equal supplied coverage and settlement clocks
for the feed and open exposure. Existing funding/health coverage remains in the
targeted PostgreSQL race suite. This is the review's requested shared derivation,
not a separate new financial model.

## Spec

### P2 — Advance valid-price health only from the financial owner's outcome

`Observe` now reports whether this invocation newly accepted an ordered, ready,
current financial price. Duplicate deliveries explicitly return false even when
the original stored result accepted the price. The engine carries this outcome
to health. `LastProcessed` still records successful candle delivery processing,
including skipped/duplicate/recovered events; `LastValidObservation` advances
only from an accepted eligible price and excludes recovered history. Existing
exposure without a current valid accepted price retains an explicit protection/
pending-close limitation, as required by recovery/health ticket #33.

`TestForegroundHealthWaitsForPriceAcceptedAfterDelayedFunding` composes the public
funding adapter, `app.Run`, owner persistence and the authoritative control health
view. Exact clocks put candle availability at 10:00 and funding receipt at
10:00:30. Funding is applied first; the owner explicitly skips the earlier
available candle. Before the fix, health incorrectly advanced the valid timestamp
to 10:00 and called the exposure monitored. After the fix, the valid timestamp
stays empty, `LastProcessed` is 10:00:30, and exposure reports
`awaiting_valid_observation` with its limitation. A newly accepted 10:01 price
restores monitored status. Injecting a lost feed acknowledgment after its commit
and restarting proves that replaying its stored accepted outcome does not certify
monitoring in the new session. Recovered observations also cannot establish that
session's valid-price coverage. The lock regressions cover stale price outcomes.

## Verification

A separate disposable Compose project, `billionaire-m6-review-fixes`, provisioned
the repository's pinned PostgreSQL 17 image with an ephemeral loopback port. Each
test created and removed its own database. Tests used
`BILLIONAIRE_REQUIRE_TEST_DATABASE=1` and the disposable DSN, so no required database
coverage silently skipped. No external provider calls or live order APIs were used.

Passed:

- `go test ./internal/app -run TestForegroundPricesExpireWhileFinancialPreparationWaits -count=1`
- `go test ./internal/app -run 'TestForegroundHealthWaitsForPriceAcceptedAfterDelayedFunding|TestForegroundPricesExpireWhileFinancialPreparationWaits' -count=1`
- `go test -race ./internal/app ./internal/store/postgres ./internal/paper ./internal/backtest -count=1`
- After extending restart/stale-outcome assertions: `go test -race ./internal/app -run 'TestForegroundHealthWaitsForPriceAcceptedAfterDelayedFunding|TestForegroundPricesExpireWhileFinancialPreparationWaits|TestHealth' -count=1`
- `go vet ./internal/app ./internal/store/postgres ./internal/paper ./internal/backtest`
- `git diff --check`

Status: both Standards findings and the Spec finding are addressed. Final assembled verification is recorded separately by `./scripts/check --status` for the accepted source.
