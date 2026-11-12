# Domain Docs

This repo uses a single-context layout: root `GLOSSARY.md` and `docs/adr/`.

## Before exploring the codebase

- Read `GLOSSARY.md` at the repo root.
- Read ADRs in `docs/adr/` that touch the area you are about to work in.

If these files do not exist, proceed silently. Domain modeling creates them lazily when terms or decisions get resolved.

## Use the glossary's vocabulary

When naming a domain concept in an issue title, refactor proposal, hypothesis, or test name, use the term defined in `GLOSSARY.md`. Respect any synonyms the glossary explicitly avoids.

If a concept is missing, reconsider whether the project uses it; note real gaps for domain modeling.

## Flag ADR conflicts

If your output contradicts an existing ADR, explicitly identify the ADR and explain why the decision should be reopened.
