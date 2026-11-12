# Project documents

Start with [the roadmap](roadmap.md) to find the next milestone, then read the requirements and design sections relevant to that work.

| Document | Owns |
| --- | --- |
| [Specification](spec.md) | Product behavior, constraints, test requirements, and release acceptance |
| [Technical design](design.md) | Proposed implementation details and rationale, pending M0 verification |
| [Roadmap](roadmap.md) | Milestone execution rules, M0–M8 scope, acceptance gates, and progress |
| [Glossary](../GLOSSARY.md) | Established domain terms |
| [Agent configuration](../AGENTS.md) | When to read tracker, triage, domain, and project instructions |

## Documents created as findings become available

- `docs/stoplore-integration.md`: M0 must produce a source-backed map of StopLore's actual public API, persistence contract, license, and capability gaps. It does not exist yet.
- `docs/adr/`: record significant settled architectural trade-offs during M0 and later work. No accepted ADRs have been created by this reorganization.
- GitHub Issues: implementation tickets and dependency links, prepared through `/to-tickets` after M0 findings are available.

## Document authority

Maintain requirements in the specification, implementation proposals in technical design, and milestone progress in the roadmap. Record accepted significant decisions in ADRs and update affected proposals to link to them. Surface conflicts before changing an approved requirement.
