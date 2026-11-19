# M6 durable terminal notification slice (#30)

**Current operation (2026-11-19):** This dated slice report retains its original gates and test evidence. The accepted local-host decision and subsequent funding, live recovery/health and systemd implementation supersede earlier startup restrictions; see the [combined M6 report](m6-report.md) for current scope, verification and limits.

This slice implements terminal delivery using PostgreSQL and deterministic local
fixtures. Live monitoring remains disabled; M2 actual-host acceptance and the
blocked #29/#33/#34/#35 work remain pending. This is not combined M6 acceptance.

## Operation

After `billionaire init`, `billionaire start --fixture testdata/market/m3.json`
starts a separate notification worker. Optional configuration:

```yaml
notifications:
  targets: [terminal]
  cooldown: 30s
```

Foreground start captures this policy durably for subsequent events. The default
is terminal delivery with no cooldown. Cooldown applies to strategy/version/
instrument/timeframe signal alerts, independently of signal generation, candidate
reports and order admission. Distinct while_true signals retain distinct outbox
identities even when their delivery state is `suppressed`. Existing queued events
retain their captured targets across configuration changes.

`billionaire notifications dispatch --limit 16 --output json` processes one bounded
batch outside the foreground engine. `billionaire notifications status --limit 100
--output json` exposes event context, target, attempts, state, next attempt, lease,
delivery time and safe error category. Neither command changes financial ownership.
Terminal alerts are JSON lines on stderr; command results remain on stdout.

## Sources and guarantees

Strategy signals, order admission/settlement rejection, filled opens, manual
closes, take-profit/stop/trailing exits and approximate liquidation enqueue in the
same transaction as their source state. Signal decision rejection without an order
also enqueues. Financial provenance resolves the immutable signal and recovers it
from the opening order for protective/manual closes; standalone manual orders
explicitly have empty strategy/version/timeframe fields. Backtest completion and
its saved report commit together in the main schema. Internal replay alerts stay
inside the isolated run schema and are never polled by the forward worker.

The foreground application records readiness transitions on delivered feed events
and its periodic freshness observation, using each feed's configured clock. Stable
checkpoints advance the observation clock without creating alerts; older
observations cannot reverse readiness. First ready observation establishes a
baseline. An observed stale baseline alerts once. No observation alone is not an
engine/data failure. Foreground failures after acquiring ownership attempt a
bounded durable failure record with a fixed safe reason; the command's stderr
also reports the failure. An unavailable database cannot durably record its own
outage. The failure record is best effort in that case.

The database is the backlog; no unbounded memory queue exists. Each worker claims
at most 16 deliveries per tick (manual dispatch maximum 100), releases its database
transaction, sends outside financial locks, and acknowledges using a claim token.
Concurrent workers skip locked claims. A five-minute lease permits restart recovery;
an old claimant cannot acknowledge a replacement claim. Each send has a two-second
context deadline; adapters must honor cancellation. Retries use exponential delays,
a maximum of five claims/attempts, and safe error categories. Permanent failures
and exhaustion remain visible. A blocked terminal device can stall its worker
without holding financial locks; cancellation of arbitrary supplied io.Writer
implementations is not guaranteed by Go's writer interface.

Delivery is at least once within the retry budget. A crash after external display
and before acknowledgement can duplicate display. Terminal delivery cannot promise
exactly-once display. Event/target uniqueness prevents duplicate enqueue on source
replay. Unknown adapter error text is discarded; JSON encoding escapes terminal
control characters, and the CLI synchronizes shared diagnostic writes. No network
notification channel is introduced in this slice.

## Verification

Real disposable PostgreSQL tests cover repeated signals and cooldown, source
transaction rollback (including protective exits), backtest completion/isolation,
restart, lease recovery and stale acknowledgement, retry exhaustion/redaction,
freshness transitions/out-of-order checkpoints, and blocked sender independence
from financial admission. CLI fixtures verify parseable command JSON with escaped
terminal alerts on diagnostics. Migration up/down checks include the new head.
Full Go tests, vet/build and practical notification/application/CLI race checks are
run before committing; results are reported with the ticket handoff.

## Review follow-up

Status supports `--offset` (zero by default) and `--limit` (1–1000 per page).
For example, `billionaire notifications status --offset 1000 --limit 1000
--output json` reads the second page in creation/event/target order. Increase the
offset by the page size until an empty array is returned; all retained failures
are reachable and the JSON remains an array of delivery records.

Migration 0015 persists rate-limit deadlines per captured target. Dispatch checks
the durable target gate before each claimed delivery and restores the attempt
quota of unsent deliveries deferred by a cooldown. Retry deadlines include elapsed
claim/send time through receipt of the response, rather than starting at batch
claim time. Target acknowledgement and subsequent send admission serialize through
the same row across workers and restart. Requests admitted before a rate-limit
acknowledgement may already be in flight and cannot be recalled; their later
acknowledgement cannot clear or shorten the cooldown. Other channels proceed.
