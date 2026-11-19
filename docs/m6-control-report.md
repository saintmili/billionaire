# M6 owner control slice (#26)

**Current operation (2026-11-19):** This dated slice report retains its original gates and test evidence. The accepted local-host decision and subsequent funding, live recovery/health and systemd implementation supersede earlier startup restrictions; see the [combined M6 report](m6-report.md) for current scope, verification and limits.

This fixture-only slice adds private CLI control of the held foreground owner.
It does not enable live monitoring, complete M6 acceptance, implement balance
adjustments/strategy exits, or waive the pending M2 actual-host gate.

## Operation

Initialize storage, configure `engine.lookback: 1` for the shipped M2 dataset
(the [runtime guide](m6-runtime-report.md#run) shows the YAML), then keep the fixture
owner available in one terminal. Initial account/strategy setup may alternatively
use explicit `--offline` while that owner is stopped:

```sh
billionaire init
billionaire start --fixture testdata/market/m2.json --hold
```

Without `--hold`, deterministic fixture subscriptions finish and release their
socket and ownership as before. Ctrl-C stops the held fixture owner. There is no
TCP control listener. The default endpoint is
`/tmp/billionaire-<current-uid>/control.sock`; `--control-socket /absolute/path`
selects another endpoint for both owner and client. Its immediate directory must
be owned by the current user with mode 0700; the socket must be owned by that user
with mode 0600. The owner creates its directory if absent. Linux peer credentials
are checked in both directions. Symlinks, non-sockets, unexpected owners and
permissive modes are refused. An existing endpoint is never replaced, including a
stale socket: verify that its owner is stopped and remove only that verified stale
socket before starting again. Shutdown removes only the original socket inode.

In another terminal:

```sh
billionaire paper account create --name Main --balance 10000 --request-id main-1
billionaire kill-switch on --request-id pause-1
billionaire kill-switch off --request-id resume-1
billionaire strategy enable breakout-demo --request-id enable-1
billionaire paper risk set --account ACCOUNT --max-positions 2 --request-id risk-1
billionaire paper open --account ACCOUNT --market spot --symbol BTC/USDT \
  --quantity 0.01 --at 2026-11-12T10:00:00Z --request-id entry-1
billionaire paper close POSITION --quantity 0.01 \
  --at 2026-11-12T10:01:00Z --request-id close-1
billionaire paper adjust POSITION --stop-loss 95 \
  --at 2026-11-12T10:01:00Z --request-id adjust-1
billionaire paper approve SIGNAL --quantity 0.01 \
  --at 2026-11-12T10:01:00Z --request-id approve-1
billionaire control outcome entry-1 --output json
billionaire paper orders --account ACCOUNT --output json
```

These are command shapes; accounts, strategy instances, intent clocks and prices
must match the selected fixture. For running-owner entries, the CLI identifies
the fixture instrument while metadata, configured enablement and adjustment
freshness come from the owner's startup snapshot. Approval metadata is resolved
from the persisted signal. Protection, kill-switch and execution risk checks are
the existing shared paper services. Pending orders still require later executable
observations; `--hold` does not invent prices after fixture exhaustion.

Commands default to the socket and never acquire a replacement owner when the
endpoint is missing/dead. Read-only account/order/report queries retain their
PostgreSQL read path. `--offline` explicitly selects the historical fixture/setup
workflow with exclusive ownership for financial writes and strategy setup.
Strategy registration and all CLI risk/enable writes run on that pinned session,
so losing its connection cannot reconnect through the pool. Strategy setup
(`strategy add --offline`) requires the runtime stopped. Explicit observation,
funding, signal consumption and evaluation require `--offline`; `market watch`
also requires a fixture. Its evaluation transactions retain the engine fence.
Backtests retain their isolated namespaces and account state.

## Retry and failure contract

Every owner mutation carries a bounded stable request ID. Migration 0013 stores
the full command JSON and immutable original outcome. Exact JSON payload retries
return the original receipt; another command kind or changed payload under that
ID rejects. Decimal strings remain exact. Intent times must remain identical on
retry. Business rejections are retained; infrastructure failures before commit
leave no receipt. An outcome lookup that finds no receipt is not proof that an
in-flight command will never commit.

The receipt and mutation commit in one transaction on the held PostgreSQL
session. Existing transaction bodies use savepoints inside that transaction;
there is no mutation-then-receipt crash window. A command rejection rolls back
its work before its rejection receipt commits. A failed receipt insert rolls
back the financial mutation. An ambiguous final commit or transport response is
reported as unknown; query the original ID, then retry the identical command.
Never generate a new ID just because the response was lost.

Receipts distinguish accepted control changes, pending paper execution, fills
and rejected decisions. They are immutable historical acknowledgements. A
pending receipt remains pending after the corresponding current order fills;
inspect `paper orders`/`paper fills` for current execution state. Domain-specific
CLI commands retain their normal text/JSON result; `control outcome` exposes the
uniform receipt and authoritative order/account IDs. Empty-result rejections
produce a structured rejection receipt and a nonzero CLI result.

Requests/responses are bounded to 1 MiB, one strict JSON document, no unknown
fields, at most 32 concurrent accepted connections and bounded transport/service
deadlines. The shared `app.Control` service can be reused by the later web
transport. No web listener or browser access has been added.

## Verification

Public seams: CLI Execute, real Unix requests, application services backed by
PostgreSQL. Tests cover absent owner without direct fallback, unsafe directory and
socket permissions, preserving existing endpoint files, dead owner, concurrent
identical requests, response loss, owner/socket restart, conflicting identity,
original pending receipts after a fill, persistent rejection and injected receipt
failure with complete financial rollback. Existing offline CLI tests explicitly
select `--offline`.

Verification completed with `BILLIONAIRE_TEST_DATABASE_URL` pointing to a disposable
local PostgreSQL database: full `go test ./...`, practical `go test -race` across
application/control/CLI lifecycle cases, `go vet ./...`, CLI build and
`git diff --check`. No external delivery or live market access was required.
