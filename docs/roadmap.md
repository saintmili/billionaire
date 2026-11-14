# Millionaire roadmap

**Current state:** M0 completed through the documented connectivity-limitation/offline-fixture path. M1 is complete with CLI/configuration and verified PostgreSQL migration/status behavior. M2 implementation and offline protocol/fixture/PostgreSQL checks are complete; its actual-host live validation gate remains pending because access is geographically blocked here. M3 offline strategy implementation and acceptance checks are complete under the sequencing exception below. M4 offline paper lifecycle and combined acceptance are complete under its approved sequencing exception; see the [M4 report](m4-report.md). Live monitoring remains disabled pending the M2 gate. See [M0 evidence and limitations](m0-report.md), the [M1 report](m1-report.md), [M2 evidence/limitations](m2-report.md), and the [M3 report](m3-report.md).

Read [the specification](spec.md) for requirements and [technical design](design.md) for the proposed baseline. Track implementation tickets and their dependencies in GitHub Issues using [the tracker conventions](agents/issue-tracker.md).

## Progress

| Milestone | Status |
| --- | --- |
| M0 — Repository reconnaissance and technical design | Complete — [evidence](m0-report.md), [StopLore map](stoplore-integration.md) |
| M1 — Project scaffolding and durable storage | Complete — [evidence](m1-report.md) |
| M2 — Market data and historical synchronization | Implemented; offline checks pass; [live-host gate pending](m2-report.md) |
| M3 — Strategy runtime and reference strategies | Complete under offline sequencing exception — [evidence](m3-report.md) |
| M4 — Paper accounts, orders, risk, and position lifecycle | Complete under offline sequencing exception — [combined acceptance evidence](m4-report.md); M2 live-host gate remains pending |
| M5 — First-class backtesting and analytics | Not started |
| M6 — Notifications and process operation | Not started |
| M6W — Private operational web app | Not started — approved MVP scope addition; after M6 |
| M7 — StopLore journaling | Not started |
| M8 — Production hardening and MVP release | Not started |

Update this table with milestone outcomes and links to evidence as work completes. GitHub Issues own individual ticket status.

## Milestone execution rules

Treat [the specification](spec.md) as the governing product contract and [technical design](design.md) as the proposed technical baseline. **Do not implement everything in one change.** Work through milestones in order. For each milestone: inspect the existing repository, propose exact files and migration changes, implement the smallest coherent vertical slice, add unit/integration tests, run `go test ./...`, document commands/configuration, and summarize deviations and outstanding risks. Stop at the milestone boundary unless explicitly instructed to continue.

**Do not invent StopLore API methods or structures.** Inspect its current `go.mod`, exported packages, concrete interfaces and README/examples. Integrate through a narrowly scoped adapter. If StopLore cannot represent an event, document the gap and implement a safe reconciliation boundary instead of modifying its internals blindly.

**Hard invariant:** The MVP must never submit live orders or request authenticated trading API permissions. Market-data connections are public/read-only only. All orders, fills, balances and liquidations belong to the simulation engine.

### Definition of done for every milestone

- Build and tests succeed (`go test ./...`; race tests for concurrent modules where practical).
- Configuration and failure behavior documented.
- Tests cover happy path, edge cases, duplicate events and failures.
- No credentials in logs, fixtures or source code.
- Dependencies introduced only with justification.
- A short milestone report: changed packages, commands run, outcomes, unresolved issues.

## Sequencing exception: offline M3 work

On 2026-11-12, the user requested the next implementation milestone after M2's
live-host limitation was reported. Proceed with M3 strategy work using verified
M2 fixture/protocol and PostgreSQL boundaries. This work does not depend on a live
Bybit connection. M2's actual-host REST/stream validation remains pending and must
pass before forward monitoring is enabled; this exception does not waive it or
mark M2 fully accepted. Stop at the M3 boundary.

## Implementation milestones

### Approved scope addition: private web in MVP

On 2026-11-13, the user confirmed a private, single-user operational web app as an MVP requirement, accepting the added delivery time. See the [web contract](spec.md#web-contract) and [ADR 0005](adr/0005-private-web-in-mvp.md). Add M6W after M6 and before M7, preserving the existing milestone identifiers. Execution order is M4 → M5 → M6 → M6W → M7 → M8; this does not authorize starting a later milestone or waive pending acceptance gates. Light/dark themes and responsive desktop/mobile workflows are included; multiple languages and RTL layouts are deferred, with English text prepared for later translation.

### Sequencing exception: offline M4 work

On 2026-11-12, the user approved the eight-ticket M4 breakdown and its offline
sequencing exception. Proceed with fixture-driven paper-account and execution
work against the verified M2/M3 PostgreSQL and event boundaries. Record acceptance
per ticket; the combined offline acceptance gate passed on 2026-11-14. See the
[M4 report](m4-report.md).
M2's actual-host REST/stream validation remains pending and must pass before live
monitoring is enabled. This exception does not waive that gate. Stop at M4.

### M0 — Repository reconnaissance and technical design

**Tasks:** Inspect existing Millionaire repo (if any), StopLore API/storage and license, pin supported Go release and dependencies, verify Bybit public spot/linear endpoints and regional connectivity for intended runtime, document final ports and MVP constraints, note StopLore capability gaps. Produce `docs/adr/` decisions covering PostgreSQL, execution separation and candle/replay semantics.

**Acceptance:** Source-backed StopLore integration map; working technical spike reading one sample historical candle (or a fully documented connectivity limitation + offline fixture); no guessed StopLore methods; codebase bootstraps.

### M1 — Project scaffolding and durable storage

**Tasks:** `go.mod`, CLI root, config loader/validator, structured logging, context cancellation, PostgreSQL Compose, migration runner, core domain types, decimal handling, health/status, CI (`go test`, `go vet`), `README`.

**Acceptance:** `millionaire init`, `config validate`, `status` work; database migrates up/down in clean test environment; invalid config rejected; local install documented.

### M2 — Market data and historical synchronization

**Tasks:** Normalized instrument/candle types, public Bybit adapter, paginated historical fetching, WebSocket subscription, candle close flag, reconnect/backfill, gap/stale detection, persistence and timeframe aggregation; deterministic fixture adapter.

**Acceptance:** Fetch and persist BTC/USDT and ETH/USDT candles in spot/linear categories, including 2 timeframes; reconnect+gap test; duplicates don't double insert; stale source prevents new auto entries (integrated later). Offline fixtures run without network.

### M3 — Strategy runtime and reference strategies

**Tasks:** Registry, YAML schema, built-in predicates, version store, strategy instances, multi-timeframe snapshot, partial/closed evaluation, signal emission policies and dedup, reference breakout + S/R + engulfing strategies.

**Acceptance:** 20-bar breakout triggers exactly according to spec; repeated `while_true` events are persisted distinctly, identical provider event replay is not duplicated; no future candle leakage; invalid rules fail fast; versions are immutable.

### M4 — Paper accounts, orders, risk, and position lifecycle

**Tasks:** Account ledger, market fill model, instrument precision, long spot / long-short linear, isolated margin and leverage, funding model, risk gates, manual/auto approval, SL/TP, trailing stop, partial exit, simplified liquidation, manual close, kill switch.

**Acceptance:** Ledger balances reconcile after entry, partial exits and closure; reprocessing event does not duplicate fills; insufficient margin, disabled market/short spot, risk breaches and stale data cause explicit rejection; no reverse order submitted; tests cover ambiguous SL/TP candles.

### Sequencing exception: offline M5 work

On 2026-11-14, the user invoked `/implement-spec` after M4 offline acceptance
and publication of the approved M5 tickets (#17–#23). Proceed with historical
and fixture-driven backtesting and analytics using the verified M2–M4
boundaries. This work does not require a live market connection. M2's
actual-host REST/stream validation remains pending and must pass before live
monitoring is enabled. This exception does not waive that gate. Stop at M5.

### M5 — First-class backtesting and analytics

**Tasks:** Historical replay service with virtual clock, reproducibility manifests, exact version/data hash, independent simulated accounts, report metrics, run comparison, CSV/JSON export, signal-success definitions, missed-data warnings.

**Acceptance:** Fixed dataset+parameters+seed produces identical trades and metrics on rerun; no look-ahead on multi-timeframe; a backtest cannot mutate live paper accounts; differences in datasets/assumptions appear in compare output; smoke backtest on fixture succeeds.

### M6 — Notifications and process operation

**Tasks:** Terminal, Linux desktop and optional Telegram channels, outbox/retry/suppression, application daemon lifecycle, Unix control socket, safe CLI commands, graceful shutdown, single-engine ownership, systemd deployment unit and VPS runbook.

**Acceptance:** Terminal works in foreground; desktop failure on headless server is nonfatal; Telegram retry is bounded; duplicate event does not spam; engine restart restores state and continues after backfill; control socket restricted to current user.

### M6W — Private operational web app

**Tasks:** After M6 acceptance, add the private HTTP/browser interface over the single-owner application/control boundary. Implement signal/account/position/order/risk/freshness tables and detail views; explicit signal approval and standalone manual paper entry; partial/full closes and stop/target/trailing adjustments; pause-new-entries/resume controls. Add request identity and outcome lookup for duplicate/lost-response handling, visible disconnection/staleness, private access configuration and browser request protections. Support responsive desktop/mobile forms and system light/dark themes with a remembered browser override. Organize English UI text for later translation. Choose the frontend stack and refresh transport during milestone planning; no framework is prescribed by the scope decision.

**Acceptance:** Deterministic fixtures exercise browser approval/manual entry through the shared risk gate, rejection reasons, position adjustments and partial/full exits. Pause blocks new manual/automatic entries while monitoring and exit management continue; resume is explicit. Repeated submissions and a lost response do not duplicate actions; unknown outcomes remain visible until resolved. Verify disconnected/stale states, private access and mutation protection, desktop/narrow-screen usability, system theme changes, remembered overrides and return-to-system. Document the run/access procedure and verification evidence. No charts, strategy editor, backtest configuration UI, translations, language selector or RTL layout are required. Web acceptance is mandatory before MVP release.

### M7 — StopLore journaling

**Tasks:** After M6W acceptance, adapter according to M0 findings; strategy/setup linkage, trade open/adjust/partial close/final close event mapping; persistent correlation and outbox; journal reports and reconciliation commands.

**Acceptance:** Verified real StopLore integration tests; journaling outage never erases financial events; replay/restart does not double-journal; journal status and reconciliation detect missing/failed links; unsupported concepts are documented, not fabricated.

### M8 — Production hardening and MVP release

**Tasks:** End-to-end scenario tests, `go test -race`, provider failure injection, financial invariant tests, performance tests for configured streams/backtest sizes, backup/restore, secrets redaction, release binaries, systemd/Compose docs, operational observability and release notes.

**Acceptance:** End-to-end: market fixture → breakout signal → auto/manual paper position → risk management → exit → journal → report; no exchange credentials anywhere; restart and outage simulations pass; `millionaire --help` documents runnable MVP; M6W browser workflows, themes and responsive checks pass under the specification's release scenario; private web access documented; no blocking P0 defects.

**Milestone gate:** Don't begin the next milestone in the documented execution order if tests or core acceptance for its predecessor fail; M6W sits between M6 and M7. If a dependency forces changing the sequence, document why and update this roadmap first.

## Original M0 prompt (completed; retained for provenance)

> Read `docs/spec.md`, `docs/design.md`, and `docs/roadmap.md`; use them as the product contract, proposed technical baseline, and milestone plan respectively. Implement **M0 only**. First inspect the current repository and `github.com/saintmili/stoplore` directly; inventory StopLore's actual exported API and persistence contract without guessing. Verify public market data choices and propose any necessary adjustments. Produce `docs/stoplore-integration.md`, the architecture/ADR notes and a milestone-by-milestone task checklist. Do not implement real trading or proceed to M1 until M0 findings and acceptance criteria are presented. Run any available checks and report results.

---

**M0 outcome:** StopLore source was inspected at a pinned commit; see the [integration map](stoplore-integration.md). Licensing remains unresolved and its API lacks external idempotency keys. Public REST returned geographic HTTP 403 while WebSocket handshakes succeeded; the offline fixture spike satisfies the permitted M0 fallback. Validate actual-host connectivity before monitoring and use explicit reconciliation for uncertain journal writes. M1 outcomes are recorded in the [M1 report](m1-report.md).
