# M6 combined operation and acceptance (#35)

The M6 scenarios are implemented against real PostgreSQL and the public runtime,
private owner control, CLI, notification and service boundaries. All native #35
blockers (#34, #27, #31) were confirmed closed on 2026-11-19. Private-web entry #9
remains natively blocked by #35. This slice stops at M6; it changes no production
financial service, database migration, exchange access or ADR.

Final assembled acceptance requires the integration owner's full `./scripts/check`
and two-axis review. Those results must be recorded before closing #35 or marking
M6 complete. Use `./scripts/check --status` for the current source fingerprint,
commit, toolchain and retained `.scratch/checks/<run>/` evidence. The targeted
results below do not substitute for that assembled-source gate.

The local workstation's M2 provider evidence was accepted on 2026-11-19;
[host acceptance](m2-local-validation-report.md#local-runtime-acceptance-2026-11-19)
supersedes the older VPS-only blockers in historical slice reports. Explicit
`start --live` monitors public/read-only data on that validated host. Every order,
fill, balance, funding payment and liquidation remains simulated.

## Combined runtime/control journey

`internal/app/m6_acceptance_test.go: TestM6RuntimeControlJourney` starts one
`app.Run` owner with actual synchronized fixture prices and a health-backed
`app.NewControl` on a private Unix socket. Every operator mutation crosses
`control.Call`; store reads assert the durable public read models. It uses three
10,000-USDT accounts, one immutable rule version, and distinct manual-paper and
auto-paper strategy instances to keep configured exit ownership unambiguous.
The fixture is generated in the test; no internet, credentials or fake SQL are used.

| Phase | Observable result |
| --- | --- |
| Price 100 | Owner health is ready; standalone manual intent stays pending; manual-paper signal awaits explicit approval; auto-paper intent is admitted. None fills at its signal price. |
| Price 102 | All three entries fill quantity 1 at the next price. Set maximum positions 1 with pyramiding enabled on the auto account, submit another entry, and submit a manual partial close of 0.5. |
| Later price 102 | The extra entry rejects with `maximum positions exceeded`. The partial close leaves quantity 0.5. Adjust its stop to 95, pause entries and observe a durable manual entry rejection. Terminal delivers structured JSON; real desktop adapter failure is durable after one attempt. |
| Price 80 while paused | The manual residual closes protectively. Strategy exit signals leave both strategy positions open until a later eligible observation; the manual-paper exit receives explicit socket approval. |
| Price 81 while paused | Approved and automatic configured exits close their matching exposures. Deposit 100.125 with reason/time; identical retry preserves the receipt and conflicting payload reuse rejects. Withdraw 25 while flat. Explicitly resume; replaying the historical pause receipt does not pause again. Submit a new manual entry. |
| Later price 81 | Resumed manual entry fills once; both strategy instances stay enabled. |
| Restart/replay | All original source data is replayed through a new owner. Orders, fills, ledger entries, signals, decisions and financial/signal notification sources retain the same identities and counts. The original manual receipt remains pending after fill and restart. |

Exact manual cash is **9983.125 USDT**: 10000 - 102 + 51 + 40 + 100.125 - 25 - 81.
Every account's immutable ledger sum equals its current cash. Approved configured
exit P&L is -21 USDT with explicit zero fees; auto-paper uses the documented 0.1%
default, charging 0.102 entry and 0.081 exit, for -21.183 USDT. Funding and external
flows remain separate from trading P&L. Cash-flow-aware return/drawdown limitations
are independently exercised by the capital/report tests.

The test initially failed while constructing the scenario: flat candles supplied
invalid stop geometry, and a risk assertion expected rejection before execution.
Corrected fixtures use valid independent stops and a later price for the risk
gate. Receipt assertions compare structured JSON because PostgreSQL JSONB may
reorder keys. No production fix was needed or claimed by these acceptance tests.

## Composed funding and independent failure evidence

The combined gate reuses these focused tests instead of duplicating their crash,
permission and concurrency logic inside one large journey:

- `TestForegroundFundingPrecedesPriceAndRecoversAfterCommittedConsumerFailure`
  uses the same foreground owner boundary with a linear 2-unit long at 100,
  leverage 5. Signed funding -2/+2 changes collateral 40 → 38 → 40; the third
  expected interval is explicitly missing. Failure after funding commit and
  repeated restart cannot double charge; cash and ledger reconcile to 960.
  `TestCLIForegroundFundingAndCoverageRemainInspectableAfterRestart` covers the
  shipped `testdata/market/m6-funding.json` CLI fixture and durable coverage.
- Foreground recovery tests fail after evaluation/before intent and after intent/
  fill/before acknowledgment, then prove a single recovered signal/order/fill
  and notification source. Health/explicit-live tests exercise recovery/backfill,
  current-clock freshness, gaps, owner loss, authoritative status and local
  REST/WebSocket startup. See [recovery evidence](m6-recovery-report.md).
- `TestSocketLostResponseConcurrentRetryAndOwnerRestart` drops a response,
  concurrently retries the same request eight times and restores the original
  receipt after restart. Socket tests cover unsafe modes and paths, malformed
  input and dead owners; CLI controls preserve clean JSON and pending receipts.
- Capital tests cover exact audit/restart, malformed input, unavailable funding/
  stale marks, insufficient free margin, concurrent overspend and transactional
  receipt/outbox rollback. Existing M4 risk tests cover every entry path sharing
  pause, freshness and policy admission/execution gates.
- Notification tests cover source deduplication, suppression independent of signal
  emission, claim recovery/stale acknowledgments, bounded retry, headless failures,
  blocked senders without financial ownership, clean CLI JSON and pagination.
  The combined journey proves control/protection continues after desktop failure.

Telegram is **selected as an available optional configured capability**, backed by
closed ticket #32 and local protocol/rate-limit/restart/exhaustion tests. It is not
enabled in the combined fixture or required for terminal delivery. No remote
Telegram message or secret was needed; see [Telegram evidence](m6-telegram-report.md).

## Verification and deployment

Targeted commands use the pinned PostgreSQL 17 image in `compose.test.yaml`,
Compose project `billionaire-m6-acceptance`, ephemeral loopback port 32773.
`internal/testdb` creates/drops a separate random database per test. The normal
application database and services are untouched. Set `BILLIONAIRE_TEST_DATABASE_URL`
to that disposable server and `BILLIONAIRE_REQUIRE_TEST_DATABASE=1` so PostgreSQL
coverage cannot silently skip.

```sh
go vet ./internal/app
go test ./internal/app -run '^TestM6RuntimeControlJourney$' -count=1 -v
go test -race ./internal/app ./internal/control ./internal/cli ./internal/store/postgres \
  -run 'TestM6RuntimeControlJourney|TestForeground|TestHealthGates|TestOwnerHealth|TestExplicitLive|TestSocket|TestDeployment|TestCapital|TestCLIForegroundFunding|TestNotification|TestDesktop|TestTelegram|TestBlockedNotification|TestAllEntryPaths' -count=1 -v
go test -race ./internal/store/postgres -run '^TestM4EntryPaths' -count=1 -v
git diff --check
# Final assembled integration gate, run by the integration owner:
./scripts/check --status
./scripts/check
```

The standalone combined PostgreSQL journey and the targeted race matrix passed
on 2026-11-19, with no skipped PostgreSQL cases. The matrix completed app (9.647s),
control (1.317s), CLI (17.679s), and PostgreSQL (19.877s). Retained local evidence is
the main checkout’s `.scratch/runs/m6-acceptance/evidence/targeted-race.log`. An additional
combined-journey race run passed (2.381s), covering the final notification-source
replay assertion; see `journey-race.log` in that same evidence directory. Shared entry-gate
race tests passed (9.077s; `entry-gates-race.log`), and app vet and
`git diff --check` passed on the final slice source.
`./scripts/check --status` currently reports no matching assembled-source record.
Final full integration verification and review are pending; the integration owner
records their assembled-source result before acceptance.

[Systemd slice evidence](m6-systemd-report.md) records both static unit verification
and an actual temporary systemd 262 user-manager start/stop/restart journey on this
host. The shipped production unit selects explicit `start --live` and private
same-user control. The [deployment runbook](deployment.md) owns installation,
protected configuration/environment, restricted PostgreSQL, local/SSH control,
logs, bounded restart policy, graceful shutdown and backup/restore procedures.
Deployment CLI tests exercise real subprocess config validation, unavailable DB,
invalid config, single-owner exclusion, 0700 directory/0600 socket, graceful
SIGTERM, restart and receipts. The race suite instruments the test process;
its separately built deployment CLI subprocess is an ordinary Go build.

## Operating limits

- The actual user-service journey uses fixtures and temporary paths/current user;
  it does not install/enable the production system service or prove boot ordering,
  dedicated-user setup, production journald routing, sustained unattended live
  operation, restart-rate exhaustion or forced SIGKILL timeout behavior. These
  limits are retained in the systemd report. A future VPS needs host validation.
- Fixture exhaustion with `--hold` keeps ownership but disconnects feeds and blocks
  entries. It generates no later execution price. Owner availability and market
  readiness are different conditions in authoritative status.
- Protection uses actual available observations. Missing prices cannot guarantee
  exits during outages. Funding is approximate isolated simulation with declared
  schedule/provenance; missing or delayed coverage is explicit, never invented
  zero funding or retroactive settlement. Multiple feeds have no global replay
  clock. See [funding limits](m6-funding-report.md).
- External notification display is bounded at-least-once delivery: a crash after
  display but before durable acknowledgment may repeat it. Actual desktop display
  and remote Telegram delivery were not claimed. Arbitrarily blocked `io.Writer`
  cancellation is not guaranteed.
- Journal outbox records remain local pending work; external StopLore delivery is
  M7. M6W, M7 and M8 implementation is not started or authorized by this report.

## Assembled review follow-up

The assembled review's two Standards findings (execution clocks after financial
waits and duplicated funding-health derivation) and one Spec finding (valid-price
monitoring after a funding-ordered skip) are addressed in the
[integration review evidence](m6-integration-review.md). Exact-clock PostgreSQL
regressions, targeted race tests and vet passed. The final assembled
`./scripts/check` after these fixes remains a separate pending gate.
