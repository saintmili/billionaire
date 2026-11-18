# M2 opt-in host validation

Ticket #24 adds a bounded evidence command. This workstation is local; the intended
runtime is a separate VPS. Local checks cannot accept the actual-host gate. M2
acceptance and #24 remain pending, live monitoring remains disabled, and M6 has
not started.

## Run locally, then repeat on the intended VPS

Use a dedicated disposable PostgreSQL database, with the normal configuration's
`storage.dsn_env` naming its secret environment variable. Do not run another
`market watch` or validation process against that database: M2 feed checkpoints
are shared by provider/instrument/timeframe and this command deliberately changes
connection state. It does not create accounts, evaluate strategies, or execute
paper entries. No exchange credentials are needed or supported.

```sh
go build -o bin/billionaire ./cmd/billionaire
./bin/billionaire init --config billionaire.yaml
umask 077
./bin/billionaire market validate-host --allow-network \
  --host-role local --duration 12m --config billionaire.yaml --output json \
  > host-validation.json 2> host-validation.log
```

On the VPS, repeat with `--host-role intended-runtime`. Keep the command's exit
status alongside stdout and diagnostics; record the checkout's `git rev-parse HEAD`
and `go version` separately. The evidence includes UTC start/end, machine hostname,
role, effective public endpoints, reproducible validation flags, and eight separate
BTC/USDT and ETH/USDT × spot/linear × 1m/3m results. The config path, DSN and environment
are omitted. Review hostname/public endpoint disclosure before sharing evidence.
Do not publish the configuration or secret environment file.

The command requires explicit network opt-in; ordinary `go test ./...` never calls
Bybit. The duration is a positive total deadline capped at 30 minutes, including
database setup and all parallel checks. Twelve minutes normally permits the 3m
close/outage/reconnect sequence; a shorter run can fail with incomplete evidence.
After cancellation each started watcher has a bounded two-second feed-state cleanup;
final persistence verification has a five-second bound, so shutdown can exceed the
main deadline by this small cleanup allowance. Provider failures are never replaced
by fixture data or another endpoint.

## Evidence and failure behavior

For every combination the check:

1. Reads and validates public instrument metadata and market-specific identity.
2. Refreshes three completed historical candles through the synchronizer, reads
   them back through PostgreSQL, and writes the identical normalized candles again
   to verify zero changed candles. A normalized history sample preserves exact
   decimal strings, closed boundaries and provider provenance.
3. Runs the existing public stream/synchronizer path. It records partial and live
   closed updates and representative normalized samples. After the first live
   close, it intentionally closes that subscription and waits one complete frame
   plus three seconds before returning the injected transport interruption. This
   creates at least one missed close for real REST recovery before resubscription.
4. Requires a second subscription, recovered closed candles, and a live partial
   after recovery. Closed-event callbacks and checkpoints finish before successful
   cancellation. It reads a live closed candle back through the repository, requires
   no unacknowledged closed event, and verifies the stopped feed denies entries.
5. Applies synthetic stale-time and gap-count changes to an observed healthy feed
   snapshot and verifies `AllowsEntries` rejects both. These flags are explicitly
   labeled synthetic; the deliberate disconnect/backfill uses the real provider.

A passed check means the observed paths worked for this run and host. It does not
claim a naturally occurring outage, historical partial-update recording, continuous
service availability, or engine startup acceptance. History retrieval also enforces
gap detection; unfilled gaps fail explicitly. Duplicate validation replays exact
stored provider candles, rather than assuming a refreshed candle cannot legitimately
receive a provider correction.

Per-check `stage`, counters and allowlisted error strings distinguish metadata,
history/persistence, duplicate, stream/reconnect and final verification failures.
HTTP status and numeric Bybit error codes survive sanitization; raw response bodies,
headers, database errors and arbitrary transport diagnostics do not. Failure or
incomplete evidence exits nonzero and leaves the gate pending. Setup/configuration
errors can occur before a report exists; configuration errors identify fields
without echoing supplied values. A successfully loaded configuration followed by
unavailable storage produces a structured failed report. Every emitted report says
`m2_actual_host_gate: pending` and `live_monitoring_enabled: false`, including runs
labeled intended-runtime. Review and record actual VPS evidence before changing
milestone acceptance; the flag is a provenance label, not approval.

## Later engine startup prerequisite

M6 must add an explicit, fail-closed startup prerequisite against reviewed,
successful evidence for the intended runtime host and provider/endpoints. Merely
finding candles, a healthy feed, a local report, or passing `--host-role
intended-runtime` must not enable live monitoring. It must retain a distinct runtime
freshness gate after actual-host acceptance: `FeedState.AllowsEntries` blocks stale,
gapped and disconnected feeds. This ticket does not implement the M6 engine,
startup policy, deployment, or automatic acceptance storage.

## Offline verification

The existing M2 protocol-server tests cover partial/closed decoding, deterministic
forced interruption and REST recovery, duplicates, gap/stale rejection, persistence
and market separation. New CLI checks cover explicit opt-in and bounds; an isolated
PostgreSQL plus local TLS server records repeated geographic HTTP 403 failures for
all eight identities, retains pending/disabled state and suppresses raw provider
bodies. Set `BILLIONAIRE_TEST_DATABASE_URL` only to a disposable test PostgreSQL server
to include database tests. These tests remain independent of external connectivity.

## Host-discovered protocol ordering

The first local run exposed a valid Bybit linear ordering that prior fixtures did
not exercise: an initial candle snapshot can arrive before the positive subscribe
acknowledgment. The adapter now validates and buffers at most 64 pre-ack candles,
then delivers them in order only after a successful acknowledgment. Negative or
missing acknowledgment, invalid candles and buffer overflow fail explicitly;
there is no silent close loss. The existing ten-second acknowledgment deadline
still bounds streams that keep sending pre-ack data. A minimized local WebSocket
regression failed with `unexpected public candle message` before the fix and
passed afterward; negative/missing acknowledgment and overflow tests also pass.
