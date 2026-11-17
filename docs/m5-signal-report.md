# M5 signal reports — ticket #21

Signal reports persist a user-selected hypothetical rule independently of paper
profitability. All execution remains simulated and isolated. The pending M2
live-host gate remains in force; this slice enables no monitoring or later
milestone feature.

## Commands

```sh
millionaire backtest signals RUN-ID --threshold-percent 2 --stop-percent 1 --window-bars 20 --output json
millionaire report signals --scope backtest --run RUN-ID --threshold-percent 2 --stop-percent 1 --window-bars 20 --output json
millionaire report signals --scope forward-paper --account ACCOUNT-ID --threshold-percent 2 --stop-percent 1 --window-bars 20 --output json
millionaire report signals --saved REPORT-ID --output json
```

Optional selectors: `--account`, `--strategy-version`, `--provider`, `--market`,
`--symbol`, `--timeframe`, `--from`, `--to`. Provider/market/symbol form a complete
instrument identity. Date ranges select signal observation instants `[from,to)`;
subsequent observations may extend past that filter's `to`. They cannot extend
past the backtest range end or forward snapshot clock. Backtests must be
completed. Invalid/missing scopes, invalid rules, account mismatch, absent runs
and reports fail with nonzero command status. `--saved` reads the immutable
report without recalculating outcomes and excludes changed rule/filter flags.
`backtest signals RUN-ID --saved REPORT-ID` also verifies run ownership.

## Counts and metrics

Reports concern entry signals. Groups always distinguish provider and market,
instrument, primary timeframe and immutable strategy version. Exit-rule signals
are excluded from entry-conversion and success denominators.

* Generated count: distinct persisted signal IDs, including rejected/untraded
  entries and repeated genuine `while_true` events.
* Satisfied-candidate denominator: distinct ready evaluation events whose entry
  rule is true, before emission policy and cooldown. False conditions, disabled,
  unready and skipped/out-of-order evaluations are excluded. The candidate ID is
  the same deterministic instance/event/kind/direction identity used for signals.
* Suppressed count: candidates withheld by emission policy/cooldown. Suppression
  rate is suppressed / satisfied candidates; zero denominator yields null.
* Old evaluations/runs lacking candidate evidence retain an explicit unknown
  suppression rate and warning; old signals cannot reconstruct suppression.
* Conversion: distinct generated signals with an actual filled opening order /
  generated signals. Approval/pending orders and rejection are not fills. Multiple
  fills of one entry cannot inflate conversion. Empty denominator yields null.
* Intervals: exact decimal seconds between chronological generated signals within
  each group, including zero for distinct signals at one availability instant.

The existing transaction stores evaluation JSON, policy state and signals
atomically. Candidate evidence is now part of that JSON. Replaying a provider
identity returns no new signal or candidate, including concurrent retries.
New backtest reports capture candidates plus an evidence-availability marker.
No strategy predicate, emission policy or causal snapshot rule changes.

## Hypothetical outcomes

The stored `success_rule` requires a positive favorable `threshold_percent`, an
adverse `stop_percent` greater than zero and less than 100, a 1–100000
`window_bars`, and `observation_basis=subsequent-closed-ohlcv`.

The reference price is the signal's proposed entry, independently of its actual
entry, rejection or trade profitability. Long target/stop are reference times
`1 + threshold/100` and `1 - stop/100`; short directions reverse favorable and
adverse price movement. Start with the first full bar at/after signal availability
and after the signal candle. Partial signal bars are excluded. The window counts
contiguous timeframe boundaries, rather than skipping missing bars. Each outcome
includes the observed bar count.

* `success`: favorable threshold reached first within the window.
* `failure`: adverse threshold reached first, or a complete window expires without
  reaching either boundary.
* `ambiguous`: one OHLCV bar contains both boundaries; no intrabar path is invented.
* `missing-data`: a required closed observation is absent or reference price is
  unavailable. A later threshold cannot repair an earlier missing observation.
* `incomplete`: the requested observation window reaches beyond the retained
  range/snapshot clock before a boundary resolves it.

Early resolved outcomes remain resolved even if later bars are missing. Missing,
incomplete and ambiguous results are never converted to failures or successes.
No paper-trade P&L enters this calculation. Backtests use their exact frozen
candles, not the mutable market cache. Recorded-close provenance arriving after
range end is excluded. Forward reports use retained current candle revisions
available through the report clock and label this as a retrospective snapshot,
not historical revision reconstruction. OHLCV update-mode backtests remain
rejected by the existing capability boundary.

Migration 11 adds immutable `signal_reports`; it does not change financial
records. Each saved report includes scope, run ID where applicable, filters,
observation clock, complete rule, evidence hash, grouped counts/rates, outcomes
and warnings. Its deterministic content hash is its ID. A repeated identical
snapshot/rule returns the same ID; a changed snapshot/rule produces another
immutable report. Report reads never run strategy generation or trading. Report
generation writes only this report table. Retain execution schemas and frozen
backtest inputs with the main registry as required by ADR 0006.

## Verification

Independent hand examples cover a 100 reference price, 10% target and 5% stop:
111/96 is success, a 94 low before a later 111 high is failure, and a same-bar
111/89 target/stop touch is ambiguous. A third-bar target is ignored by a two-bar
window. Short favorable movement, missing bars, incomplete windows and timeout
are explicit. Two signals three minutes apart with one entry order split into two
fills yield conversion 0.5 and interval 180 seconds.

Real PostgreSQL forward reporting exercises three genuine satisfied updates,
2s cooldown, two emitted signals, one suppression (1/3), replay duplicates,
rejected entries eligible for success, persisted definitions, deterministic
snapshot/report IDs and unchanged paper cash. CLI fixture reporting verifies both
backtest command forms, exact version/timeframe selection, an incomplete window,
conversion, persisted rule/readback and empty forward scope. Future-price traps
verify generation stays causal while retrospective observation uses later bars.

Checks run against local PostgreSQL with `MILLIONAIRE_TEST_DATABASE_URL`:
`go test ./... -count=1`, `go vet ./...`, and focused analytics/strategy/PostgreSQL/
CLI race checks. The full suite, vet and focused race checks passed. No live data access, tracker changes, push,
PR or later milestone work is included.
