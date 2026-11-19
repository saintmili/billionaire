# M6 offline implementation review

Review baseline: `2a6c87ec631bf6332874295551a7d3a69974abd9` through
integration `a1ad14d`. Scope: unblocked #25/#26/#27/#28/#30/#31/#32.
The actual-host gate #24, funding #29, downstream #33/#34/#35, M6 acceptance
and M6W remain pending; live monitoring remains disabled.

## Standards

No hard documented-standard violations were identified. PostgreSQL ownership,
exact financial state, immutable receipts and financial/outbox atomicity follow
ADRs 0001/0002. The offline sequencing exception remains explicit.

One low-severity duplication finding: the owner risk control repeated policy
validation, account locking, strategy lookup and policy upsert from
`Store.SetRiskPolicy`. Both paths are active, so future changes could diverge.
The review fix extracts `setRiskPolicy(ctx, tx, ...)` and calls it from each
transaction boundary. Pooled setup/backtests keep their transaction; owner
controls retain the pinned connection, nested savepoint and atomic receipt.
Existing public risk/control integration tests verify behavior after extraction.

## Spec

Two P2 findings were identified and fixed:

1. A Telegram 429 delayed only its delivery, while queued events and concurrent
   workers could keep sending to the same target. Deadlines started at batch
   claim time. Migration 0015 now stores a target deadline. Each send admission
   and rate-limit acknowledgement locks the same target row. Response deadlines
   include elapsed processing/send time in the dispatch clock. Deferred preclaims
   return to pending without spending retry quota. Cooldown persists across
   restart and remains active even if the originating event exhausts attempts.
   Other channels proceed. Requests admitted before acknowledgement may already
   be in flight; later successful acknowledgements cannot clear the cooldown.
2. Status returned only the oldest 1,000 deliveries, hiding later failures.
   `notifications status --offset N --limit L` now reaches every retained delivery
   in stable creation/event/target order. Default behavior and the JSON array
   shape remain compatible. The per-page bound remains 1–1,000; history has no
   hidden total cap. The operating report documents paging until an empty array.

The Telegram report drops irrelevant rejected-test conversation history while
retaining explicit local-only evidence. Paper command help now reflects owner
routing and explicit offline setup.

## Validation

Regression tests ran red before implementation: three rate-limited target sends
instead of one; CLI rejected `--offset`. Both now pass using public dispatcher,
store and CLI JSON seams backed by disposable PostgreSQL. Coverage includes
slow responses, same-batch deferral, retry quota, concurrent in-flight dispatch,
reopened stores, final-attempt cooldown, unrelated terminal progress and a failed
delivery after 1,000 historical records. Telegram HTTP requests use local fixtures.
No live message, push or issue mutation was performed.

Checks passed with `BILLIONAIRE_TEST_DATABASE_URL` pointing at the disposable
loopback PostgreSQL instance:

- `go test ./internal/cli ./internal/store/postgres -run 'Notification|Telegram|Risk|Control' -count=1 -timeout=120s`
- `go test -race ./internal/notify ./internal/cli ./internal/store/postgres -run 'Notification|Telegram|Risk|Control' -count=1 -timeout=180s`
- `go test ./internal/store/postgres -run 'Migration|NotificationTargetCooldown|NotificationCooldown|Telegram' -count=1 -timeout=60s`
- `go vet ./...` and `go build -o /tmp/billionaire-m6-review-binary ./cmd/billionaire`
- Built binary `paper --help` and `notifications status --help` smoke checks.

The root integration workflow records the final full-suite validation separately.

Independent confirmation found no remaining actionable Standards or Spec findings
after `d2ec04d`. The shared risk-policy operation preserves both transaction
boundaries; durable cooldown, response/worker clocks, retry quota and status
pagination satisfy the original findings. The full final PostgreSQL race suite,
vet and build passed on integration `19c6079`; see the
[combined offline report](m6-offline-report.md).

### Worker clock follow-up

Independent confirmation found that the foreground worker forwarded a buffered
ticker timestamp after a slow batch. Although Dispatch included elapsed send time,
the stale starting timestamp could still shorten or expire the next response's
cooldown. `Run` now uses ticks only to wake up and reads the current UTC time
immediately before calling Dispatch; explicit caller clocks remain supported.

A public `notify.Run`/store regression with real PostgreSQL queues a later event
while two local sends each take 1.2 seconds. The next batch returns a one-second
rate limit. Before the fix its durable deadline preceded the actual response by
roughly half a second. After the fix the response retains the full delay and a
fresh concurrent dispatcher cannot bypass it. No live requests are made.

Follow-up checks passed with the disposable PostgreSQL instance:
`go test ./internal/store/postgres -run 'Notification|Telegram' -count=1 -timeout=60s`
and `go test -race ./internal/notify ./internal/store/postgres -run
'Notification|Telegram' -count=1 -timeout=90s`.
