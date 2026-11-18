# M2 local host validation — ticket #24

Date: 2026-11-18. This workstation is the user's local system; Billionaire's
intended runtime is a separate VPS. This report records preliminary local evidence,
not M2 actual-host acceptance. Ticket #24 remains open and live monitoring remains
disabled. M6 and later milestones have not started.

## Scope and procedure

Baseline: `949aa92ad6abd5a33f5e71b69cc46de3984b3dcd`. Toolchain:
`go1.27.2-X:nodwarf5 linux/amd64`. The validation uses public Bybit REST and
WebSocket endpoints, eight BTC/ETH × spot/linear × 1m/3m combinations, and an
isolated PostgreSQL 17 container bound to loopback. No exchange credentials,
strategy evaluation, paper-account changes or live orders are involved.

See the [repeatable host procedure](m2-host-validation.md). Commands used:

```sh
go build -o /tmp/billionaire24-validator ./cmd/billionaire
/tmp/billionaire24-validator init --config /tmp/billionaire24.yaml --output json
/tmp/billionaire24-validator market validate-host --allow-network \
  --host-role local --duration 9m --config /tmp/billionaire24.yaml --output json
```

The database URL was supplied through the configured environment variable, with
the password held in a temporary mode-0600 file and omitted from evidence.
The validation database was separate from existing application databases.

## Failure discovered through the local run

The first run started at `2026-11-18T14:45:36Z`. All eight metadata/history paths
worked, but linear streams failed before emitting a candle. A minimal public
WebSocket capture showed a matching `kline.1.BTCUSDT` snapshot arriving **before**
the successful subscription acknowledgment. The adapter previously rejected any
candle before acknowledgment as `unexpected public candle message`.

An offline protocol regression reproduced the failure. The adapter now validates
and buffers at most 64 early candles, publishes them in arrival order only after
a positive acknowledgment, and retains the acknowledgment deadline. Rejection,
missing acknowledgment and buffer overflow fail without publishing unacknowledged
candles. Existing topic, clock, boundary and numeric validation remains in force.

The initial run was stopped gracefully at `2026-11-18T14:49:39Z` to rebuild and
repeat with the fix; its interrupted 3m spot checks are not claimed as passed.

## Corrected local provider run

**All eight checks passed**, exit status 0. The run started at
`2026-11-18T14:49:49Z` and finished at `2026-11-18T14:54:09Z`.
The [structured local evidence](evidence/m2-local-2026-11-18.json) retains exact
history/stream samples, host identity, UTC timestamps, public endpoint selection,
per-check stages, counters and validation results.

| Market | Instrument | Frame | History candles | Partial updates | Live closes | Recovered closes | Connections |
| --- | --- | --- | ---: | ---: | ---: | ---: | ---: |
| spot | BTC/USDT | 1m | 3 | 4 | 1 | 3 | 2 |
| spot | BTC/USDT | 3m | 3 | 62 | 1 | 1 | 2 |
| spot | ETH/USDT | 1m | 3 | 2 | 1 | 3 | 2 |
| spot | ETH/USDT | 3m | 3 | 60 | 1 | 1 | 2 |
| linear | BTC/USDT | 1m | 3 | 60 | 1 | 6 | 2 |
| linear | BTC/USDT | 3m | 3 | 63 | 1 | 2 | 2 |
| linear | ETH/USDT | 1m | 3 | 1 | 1 | 5 | 2 |
| linear | ETH/USDT | 3m | 3 | 64 | 1 | 2 | 2 |

Every combination verified unchanged duplicate writes, persisted streamed closes,
cleared pending closed-event state, and disconnected-state entry rejection.
Recovered counts include replayable durable events from the interrupted initial
run; they are not counts of uniquely missed candle intervals. Each check also
created a real interval-long interruption and performed REST recovery before its
second subscription. Stale/gap rejection flags are explicitly synthetic probes of
an observed healthy feed, not claims of naturally occurring provider failures.
The earlier geographic HTTP 403 was not reproduced locally.

The report explicitly retains `m2_actual_host_gate: pending` and
`live_monitoring_enabled: false`. Temporary database containers were removed after
evidence collection and verification.

## Repository verification

The completed implementation is commit `2b1bc7f`, merged into
`integration/24-host-validation` as `5d5af4c`. Required checks passed:

```sh
# BILLIONAIRE_TEST_DATABASE_URL selected a separate disposable PostgreSQL 17 server.
go test ./... -count=1
go test -race ./internal/marketdata/... ./internal/cli -count=1
go vet ./...
go build ./...
git diff --check
```

The test service matches CI's trust authentication and is separate from the
password-protected host-validation database. An initial password-authenticated
test run exposed the existing restricted-role test's requirement for passwordless
temporary login roles; it passed when rerun with CI's disposable service setup.
No existing user database authentication was changed. Ordinary tests remain
independent of external provider connectivity; database cases require explicit
test database configuration.

### Standards

Independent review of `949aa92...5d5af4c`: no documented-standard violations,
actionable optional smells, or concrete correctness findings.

### Spec

Independent review against #24 and the user's local-only scope: no actionable
findings. VPS acceptance remains pending by design. The reviewers inspected the
implementation and passing test logs; final local provider evidence is recorded
above and was subsequently verified against the structured results.

## Acceptance boundary

Repeat the same procedure on the VPS with `--host-role intended-runtime`, retain
the checkout/toolchain and sanitized evidence, and review all eight results before
accepting M2 or closing #24. Local connectivity does not establish VPS connectivity.
No report or host-role flag automatically enables monitoring. M6 must implement
the documented fail-closed startup prerequisite and ongoing freshness checks.
