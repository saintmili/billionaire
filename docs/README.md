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
- [Architecture decisions](adr/): accepted PostgreSQL, execution/journal separation and candle/replay decisions.
- [Milestone checklist](milestone-checklist.md): sequenced follow-up work and acceptance gates.
- GitHub Issues: implementation tickets and dependency links, prepared through `/to-tickets` after M0 findings are available.

## Document authority

Maintain requirements in the specification, implementation proposals in technical design, and milestone progress in the roadmap. Record accepted significant decisions in ADRs and update affected proposals to link to them. Surface conflicts before changing an approved requirement.
