# Project documents

Start with [the roadmap](roadmap.md) to find the next milestone, then read the requirements and design sections relevant to that work.

| Document | Owns |
| --- | --- |
| [Specification](spec.md) | Product behavior, constraints, test requirements, and release acceptance |
| [Technical design](design.md) | Proposed implementation details and rationale, pending M0 verification |
| [Roadmap](roadmap.md) | Milestone execution rules, M0–M8 scope, acceptance gates, and progress |
| [Glossary](../GLOSSARY.md) | Established domain terms |
| [Agent configuration](../AGENTS.md) | When to read tracker, triage, domain, and project instructions |

**Current milestone:** M5 offline acceptance is complete. The seven authorized unblocked M6 tickets are implemented and reviewed; see the [offline implementation report](m6-offline-report.md). M2 actual-host validation is accepted for the local workstation as of 2026-11-19; #29 is eligible, followed by #33–#35 in dependency order. Live startup remains disabled pending M6 implementation; M6 combined acceptance and later milestones remain pending.

- [M6 monitored public funding](m6-funding-report.md): public schedule/rate/mark retrieval, settlement ordering, durable coverage and recovery.
- [M6 offline implementation report](m6-offline-report.md): completed unblocked scope, independent review, verification and remaining gates.
- [M6 review evidence](m6-review-report.md): notification cooldown/pagination corrections and shared risk-policy persistence.
- [M6 configured strategy exits](m6-strategy-exits-report.md): action modes, explicit approval, proven exposure ownership and competing close safety.
- [M6 Linux desktop notifications](m6-desktop-report.md): optional notify-send delivery, safe invocation and headless failure behavior.
- [M6 capital adjustments](m6-capital-report.md): exact audited deposits/withdrawals, owner receipts and cash-flow-aware report limitations.
- [M6 owner control slice](m6-control-report.md): private Unix requests, durable receipts, explicit offline setup and recovery.
- [M6 optional Telegram notifications](m6-telegram-report.md): protected environment references, bounded HTTPS delivery and local-only evidence.
- [M6 durable terminal notifications](m6-notifications-report.md): transactional sources, cooldown, bounded delivery and status commands.
- [M6 foreground runtime slice](m6-runtime-report.md): fixture start, configured subscriptions, single-session ownership, recovery and operating limits.

## Reconnaissance evidence

- [StopLore integration map](stoplore-integration.md): pinned public API, persistence, license findings, mapping and reconciliation gaps.
- [M0 report](m0-report.md): bootstrap, provider evidence, commands and remaining gates.
- [M1 report](m1-report.md): CLI/configuration, PostgreSQL migrations, verification and limitations.
- [M2 report](m2-report.md): public market data, offline fixtures, candle persistence, synchronization and freshness.
- [M2 local validation report](m2-local-validation-report.md): ticket #24 local checks and host-discovered stream fix; [repeatable procedure](m2-host-validation.md). Local runtime acceptance is recorded in the report; a future VPS needs its own host validation.
- [M3 report](m3-report.md): immutable strategies, rules, snapshots, emission policies and signal persistence.
- [M4 acceptance report](m4-report.md): combined offline lifecycle, CLI/JSON, exact reconciliation, failure/replay/concurrency evidence and operating limits.
- [M4 account slice report](m4-accounts-report.md): ticket #1 account/ledger, offline ownership and verification.
- [M4 risk slice report](m4-risk-report.md): ticket #3 policy sizing, transactional limits, persistent kill switch and verification.
- [M4 spot slice report](m4-spot-report.md): ticket #2 orders, fills, positions, fixture execution and verification.
- [M4 signal entry slice report](m4-signals-report.md): ticket #5 approval, automatic decisions, durable intents and offline ownership.
- [M4 linear slice report](m4-linear-report.md): ticket #4 long/short isolated collateral, settlement and valuation.
- [M4 protection slice report](m4-protection-report.md): ticket #6 stops, targets, trailing state, candle ordering and atomic exits.
- [M4 funding slice report](m4-funding-report.md): ticket #7 funding provenance/coverage, approximate liquidation, event ordering and verification.
- [M5 acceptance report and operating guide](m5-report.md): combined CLI/PostgreSQL replay, complete isolation, exact analytics, recovery/retention, train/test examples and current operating limits.
- [M5 trade performance slice report](m5-trade-report.md): ticket #20 lifecycle metrics, read-only scopes, persisted equity, exact reconciliation and limits.
- [M5 signal report](m5-signal-report.md): ticket #21 candidate/suppression evidence, independent hypothetical outcomes and immutable report/CLI verification.
- [M5 linear replay slice report](m5-linear-replay-report.md): ticket #19 isolated long/short funding, interval/point phases, protective exits, approximate liquidation and coverage.
- [M5 historical replay slice report](m5-historical-replay-report.md): ticket #18 public/fixture/cache selection, coverage, clocks and causal multi-timeframe replay.
- [M5 spot fixture slice report](m5-spot-backtest-report.md): ticket #17 isolated frozen run/report, clocks, recovery and verification.
- [M5 comparison/export slice report](m5-compare-export-report.md): ticket #22 retained-run comparison, strategy dimensions and exact JSON/CSV schema.
- [Architecture decisions](adr/): accepted PostgreSQL, execution/journal separation and candle/replay decisions.
- [Milestone checklist](milestone-checklist.md): sequenced follow-up work and acceptance gates.
- GitHub Issues: implementation tickets and dependency links, prepared through `/to-tickets` after M0 findings are available.

## Document authority

Maintain requirements in the specification, implementation proposals in technical design, and milestone progress in the roadmap. Record accepted significant decisions in ADRs and update affected proposals to link to them. Surface conflicts before changing an approved requirement.
