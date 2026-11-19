# M6 configured strategy exits — ticket #28

**Current operation (2026-11-19):** This dated slice report retains its original gates and test evidence. The accepted local-host decision and subsequent funding, live recovery/health and systemd implementation supersede earlier startup restrictions; see the [combined M6 report](m6-report.md) for current scope, verification and limits.

Explicit `exit` signals now use the existing signal-decision, paper close-order,
observation and financial-owner services. No separate execution engine, migration,
exchange access or additional configuration is introduced. The foreground owner
consumes these signals through its existing durable pending-signal queue.

## Behavior and operation

- `alert_only` records an observation and creates no intent.
- `manual_paper` records `awaiting_approval`; `paper approve <signal-id>` creates
  a close intent only after explicit approval, through the running owner's socket
  by default. Offline fixtures require `--offline`.
- `auto_paper` records its decision and close intent atomically on the owner.
- Exposure must match the selected account, instrument, strategy definition,
  opening strategy instance and immutable version. Ownership is proven through
  the opening fill → order → entry signal. A standalone/manual position carrying
  the same strategy label does not supply that provenance.
- The current decision schema holds one order. No exposure produces a durable
  `no matching strategy exposure` rejection; multiple matching open positions
  produce `multiple matching positions unsupported; close explicitly by position`.
  Neither case chooses an arbitrary position or opens new exposure.
- `--quantity risk` (the default) means the matched position's full remaining
  quantity for an exit. An explicit quantity requests that partial close and is
  validated against instrument precision and remaining exposure. Fees and slippage
  use the existing flags. Direction and leverage come from the owned position.
  Entry protection/liquidation overrides are rejected on exit approval.
- Intents remain pending at the signal/approval timestamp and fill only on a later
  eligible executable observation. Stale/unready observations retain the existing
  durable rejected-order semantics; inspect `paper decisions` and `paper orders`
  for the reason. A rejected signal/order is never silently resubmitted.
- Pause new entries (`kill-switch on`) permits valid close admission/execution.
  Protective/liquidation/manual/strategy closes share the existing locked position
  settlement. A competing close can become rejected after another close reduces
  or consumes exposure; it cannot over-close it.
- An entry signal, including an opposite-direction entry, remains an entry and
  never invokes this close workflow. Reversal is not introduced.
- Decisions already recorded as `observed` by an earlier release stay terminal;
  upgrading does not retroactively execute historical exits.

Example explicit partial approval:

```sh
billionaire paper approve SIGNAL_ID --at 2026-11-12T10:00:02Z \
  --quantity 0.1 --request-id approve-exit-1 --output json
billionaire control outcome approve-exit-1 --output json
billionaire paper decisions --output json
```

Use the same request identity/inputs after a lost response, or query its immutable
control outcome. A control receipt describes the original accepted/pending result;
`paper decisions` joins the current order status after subsequent execution. Exact
signal-decision retries also return that current order. Resume entries explicitly
with `kill-switch off` when appropriate.

## Verification

Tests use the pre-agreed CLI and application-service seams backed by disposable
real PostgreSQL databases. The first action-mode test failed with exit signals
incorrectly returning `observed`, then passed after implementation. Coverage includes
all action modes, long/short linear settlement, next-observation timing, restart/duplicate processing, pause,
opening-instance provenance, standalone positions lacking provenance, explicit
partial quantities, full remaining exposure after an earlier manual partial close,
multiple-match rejection, excessive sizing, stale observations, historical consumed
decisions, opposite entries retaining existing exposure, atomic rollback when receipt persistence fails, immutable owner-control receipts after response loss, and concurrent
manual/strategy closes competing with protective stops and linear liquidation.
CLI fixtures exercise entry → exit evaluation → consume/approve → observe in all
three modes, including repeated approval and a manual partial exit.

With `BILLIONAIRE_TEST_DATABASE_URL` pointing to the disposable loopback test PostgreSQL:

- `go test ./...` — passed, including all database integration packages.
- `go test ./internal/store/postgres -run TestStrategyExit -count=1` — passed.
- `go test ./internal/cli -run TestSignalEntryFixtureCLI -count=1` — passed.
- `go test -race ./internal/store/postgres -run TestStrategyExit -count=1` — passed.

After merging integration tip `5f43e15`, `go test ./...` passed again, as did
`go vet ./...`, `go build ./cmd/billionaire` and the following practical race checks:

```sh
go test -race ./internal/store/postgres ./internal/cli ./internal/app ./internal/control \
  -run 'TestStrategyExit|TestSignalEntryFixtureCLI|TestControl' -count=1
go test -race ./internal/store/postgres -run TestStrategyExit -count=1
go test -race ./internal/control
```

The final exit race run includes an additional linear-short settlement test and
asserts that the competing stop/liquidation path supplied the actual exit reason.
After merging ticket #27's integration tip `55bbb9e` (migration 0014), the focused
PostgreSQL/CLI/application checks passed again with the race detector:

```sh
go test -race ./internal/store/postgres ./internal/cli ./internal/app \
  -run 'TestStrategyExit|TestSignalEntryFixtureCLI|TestControl' -count=1
```

Network delivery was never exercised against live recipients.

## Limits

This is offline M6 slice acceptance only. Multiple matching positions require
explicit position closes. The M2 actual-host gate remains pending, live monitoring
stays disabled, and this work does not implement #29/#33/#34/#35, claim combined M6
acceptance, or begin M6W.
