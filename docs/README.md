# Project documents

Start with [the roadmap](roadmap.md) to find the next milestone, then read the requirements and design sections relevant to that work.

| Document | Owns |
| --- | --- |
| [Specification](spec.md) | Product behavior, constraints, test requirements, and release acceptance |
| [Technical design](design.md) | Proposed implementation details and rationale, pending M0 verification |
| [Roadmap](roadmap.md) | Milestone execution rules, M0–M8 scope, acceptance gates, and progress |
| [Glossary](../GLOSSARY.md) | Established domain terms |
| [Agent configuration](../AGENTS.md) | When to read tracker, triage, domain, and project instructions |

## Reconnaissance evidence

- [StopLore integration map](stoplore-integration.md): pinned public API, persistence, license findings, mapping and reconciliation gaps.
- [M0 report](m0-report.md): bootstrap, provider evidence, commands and remaining gates.
- [M1 report](m1-report.md): CLI/configuration, PostgreSQL migrations, verification and limitations.
- [M2 report](m2-report.md): public market data, offline fixtures, candle persistence, synchronization and freshness.
- [M3 report](m3-report.md): immutable strategies, rules, snapshots, emission policies and signal persistence.
- [M4 account slice report](m4-accounts-report.md): ticket #1 account/ledger, offline ownership and verification.
- [M4 risk slice report](m4-risk-report.md): ticket #3 policy sizing, transactional limits, persistent kill switch and verification.
- [M4 spot slice report](m4-spot-report.md): ticket #2 orders, fills, positions, fixture execution and verification.
- [M4 protection slice report](m4-protection-report.md): ticket #6 stops, targets, trailing state, candle ordering and atomic exits.
- [Architecture decisions](adr/): accepted PostgreSQL, execution/journal separation and candle/replay decisions.
- [Milestone checklist](milestone-checklist.md): sequenced follow-up work and acceptance gates.
- GitHub Issues: implementation tickets and dependency links, prepared through `/to-tickets` after M0 findings are available.

## Document authority

Maintain requirements in the specification, implementation proposals in technical design, and milestone progress in the roadmap. Record accepted significant decisions in ADRs and update affected proposals to link to them. Surface conflicts before changing an approved requirement.
