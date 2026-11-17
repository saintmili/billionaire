# M5 historical dataset replay — ticket #18

Ticket #18 extends the [initial spot slice](m5-spot-backtest-report.md) with
public/fixture/cache selection, frozen multi-timeframe inputs, coverage checks
and explicit closed-candle revision clocks. M5 remains in progress; linear
funding, further analytics, comparison/export and combined acceptance remain
separate tickets. M2 actual-host validation is still pending. Monitoring remains
disabled; every financial operation is simulated.

## Commands and acquisition

```sh
# VERSION must select exactly these timeframes in its immutable definition.
millionaire backtest run --strategy VERSION --provider fixture \
  --fixture testdata/market/m3.json --market spot --symbol BTC/USDT \
  --timeframes 1m --from 2026-01-01T00:20:00Z --to 2026-01-01T00:22:00Z \
  --capital 10000 --fee-rate 0.001 --slippage-bps 0 --seed 7 --output json

# The same market-specific identity must already have metadata and candles.
millionaire backtest run --strategy VERSION --provider fixture --cached \
  --timeframes 1m --from 2026-01-01T00:20:00Z --to 2026-01-01T00:22:00Z --output json

# Uses configured public HTTPS REST endpoint and the existing retry/pagination,
# precision, closed-candle and synchronization boundaries. Requires venue access.
millionaire backtest run --strategy VERSION --provider bybit --market spot \
  --symbol BTC/USDT --timeframes 1m,5m \
  --from 2026-01-01T00:20:00Z --to 2026-01-01T01:00:00Z --output json

millionaire backtest report RUN_ID --output json
millionaire backtest run --source-run RUN_ID --output json
```

Public history warms the PostgreSQL cache through the shared synchronizer;
explicit fixture selection reads that file directly and persists its exact
selection, so another file sharing fixture instrument IDs cannot replace it;
`--refresh` explicitly refetches cached ranges. `--cached` constructs no provider,
reads metadata/candles directly, excludes `--fixture`/`--refresh`, and fails on
missing metadata. Fixture runs never fall back to a public source. Public source
endpoint is retained in the configuration; an older cache retains its provider
identity and observation provenance, but its original endpoint is unavailable.

The CLI accepts spot long `on_candle_close` versions and all their declared
timeframes. Every selected timeframe must match the version; duplicate intervals
fail. `on_update` is rejected: neither latest REST history nor fixture History
proves complete intrabar capture. Unsupported recorded/approximate update
models are never silently selected. Risk/capital/fee/slippage flags from #17
remain available and use the same financial services.

## Frozen coverage and revision semantics

`backtest.Queries` includes the required primary lookback before the range, plus
higher-frame lookback evaluated at the first primary close. Calendar intervals
step through UTC calendar boundaries. `backtest.Freeze` validates closed stamped
payloads, preserves exact metadata/candles/source observations and canonicalizes
input/frame order. Exact duplicates do not change dataset hashes or economics.
Ambiguous different revisions at an identical source observation are rejected.

Missing required lookback always fails. Other missing primary or higher-frame
candles fail unless `--acknowledge-gaps` is explicit. The flag, exact missing
interval warnings and execution implications remain in the saved report. No
missing opens or partial higher buckets are invented; pending intents wait for
the next actual primary open. Strategies remain unready while their required
contiguous windows are unavailable. Acknowledgement does not bridge lookback.

The CLI's `--clock-mode close` is an explicit **latest historical snapshot**
model: each selected bar becomes available at its exclusive end. Provider
observations acquired later remain unchanged in frozen inputs; they are not
claimed as historical event arrival times. Unknown historical revisions remain
unavailable. A known sequence of different revisions cannot be silently folded
or backdated by Freeze in this mode.

The service also supports `Config.ClockMode=recorded-close` for an explicitly
supplied closed-revision dataset. Publications occur at the recorded source
observation. A later correction updates as-of history only after that instant;
it does not reevaluate a past primary event, manufacture an open or apply its
protection interval twice. Identical refetches retain first recorded availability.
A returning older revision after a different revision is rejected because the
current content-identity store cannot represent distinct update identities for
that sequence. This mode does not support intrabar updates. CLI acquisition
rejects `recorded-close`, because the shared public/fixture/cache History APIs
return latest snapshots rather than complete recorded revision streams.
Publications after the selected range end remain frozen provenance only and
produce warnings; they cannot create post-range signals, fills or valuations.

`Store.RunFrozenBacktest` uses the retained strategy definition directly and
never resolves the upstream strategy version. `--source-run` rejects all changed
input flags and reads only the retained inputs. Original fixture removal,
upstream version unavailability or cache corrections cannot alter a frozen run.
`BacktestInputs` checks retained input hashes before returning them. Retain and
back up the registry plus per-run schemas as described in ADR 0006; no automatic
delete/retention policy is introduced.

## Causal execution and audit clocks

Execution model `spot-ohlcv-multiframe-next-open-phase-v2` orders events by virtual
availability, phase, timeframe, candle open and revision identity. At each clock:

1. Publish every available completed timeframe into isolated as-of history.
2. Settle a primary completed interval through shared adverse-first protection.
3. Evaluate only the primary event and create decisions through existing services.
4. Observe the next actual primary open, containing only its opening price.

Higher candles closing at the evaluation boundary are visible; later closes and
corrections are absent. Only the primary timeframe drives execution, preventing
multiple protection applications from overlapping higher bars. Warm-up is
published history only, with no signals or execution observations.

`ReplayProtectionOnly` prevents the close/protection phase from consuming pending
orders. This matters when a recorded publication arrives after an intervening
open: the pending intent stays pending until another actual open, including at
the end of the range. Both replay gates require an isolated owner namespace.
Forward-paper strict-after observation semantics remain unchanged.

Exact original observations/revisions are in `BacktestInputs.Candles`, joined by
candle identity/revision to modeled event IDs. Signals' `source_observed_at`
reflects the replay candle view in close-clock mode; the report explicitly warns
to use frozen inputs for original provider observation provenance. Manifest clock
and model fields describe the mapping. No future completed High/Low/Close is
exposed at the opening-price observation.

The #20 equity helper is called initially and after each primary close and open.
Equal-time phase snapshots are retained, preserving entry-fee drawdown. This
provides historical marks to analytics without changing its ownership of metrics.
Open positions and pending intents remain at range end; final cash alone is not
equity. No synthetic liquidation or final tradable price is added.

## Verification

Approved seams: backtest service, public PostgreSQL repository APIs, CLI and
analytics APIs. Vertical red/green evidence includes a missing Freeze API,
cache-selection API, original frozen-version rerun API, first-recorded-availability
regression, unavailable returning update identity, and a real PostgreSQL delayed
close that incorrectly filled an entry at the final close before the narrow
protection-only gate was introduced.

Regression coverage includes duplicate/shuffled input hashes, timeframe ordering,
missing warm-up despite acknowledgement, missing cached metadata, public HTTPS
selection and cached replay after server shutdown, fixture deletion, immutable
versions unavailable upstream, a higher close becoming visible exactly at the
primary boundary, late corrections, post-range publications, pending final
intents, rejected update-mode, ordinary risk/protection outcomes and unchanged
forward ownership, balances, orders, signals and kill switch.

Final checks are recorded below after execution. This slice does not validate a
live venue, supply linear funding, claim intrabar equivalence or close M5.

Verified 2026-11-17 against the provided disposable PostgreSQL server, using
`MILLIONAIRE_TEST_DATABASE_URL` and `GOCACHE=/tmp/billionaire-go-cache`:

- `go test ./...` — passed after the final fixture-selection correction;
  PostgreSQL package 48.187s, CLI package 13.125s.
- `go test -race ./internal/backtest ./internal/store/postgres ./internal/cli -run 'TestBacktest|TestFreeze|TestRecorded' -count=1`
  — passed (1.058s / 6.865s / 6.765s).
- `go vet ./...` — passed.
- `go build -o /tmp/billionaire-ticket18 ./cmd/millionaire`, followed by
  `backtest run --help` — passed; selection, cache, gaps, clock and risk flags
  are present.
- `git diff --check` — passed. The assigned branch descends from and was merged
  with the current `integration/m5-backtesting-analytics` tip before handoff.

No live venue request, tracker mutation, push, PR, daemon or journal delivery was
performed. Shared analytics helper commits from #20 were incorporated with its
owner's agreement; #20 retains ownership of metrics and reporting commands.
