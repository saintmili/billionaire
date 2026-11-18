# M4 paper accounts — ticket #1

Implemented 2026-11-12 against baseline `344ef8e`, for GitHub issue
[#1](https://github.com/saintmili/billionaire/issues/1). This completes the account
slice only; M4's orders, risk, position lifecycle and combined acceptance remain
pending. The roadmap records the user-approved offline M4 sequencing exception.
M2 actual-host validation remains pending and live monitoring remains gated.

## Changes and operation

- `internal/paper` defines account inputs, exact balance validation and output types.
- Migration `0004_paper_accounts` adds accounts, immutable opening ledger entries
  and financial journal-outbox events. Creation commits all three together.
- PostgreSQL account methods provide creation, listing, lookup and ledger inspection.
  Required request IDs give stable account identities, identical retries do not
  credit funds twice, and changed inputs under the same ID are rejected.
- A dedicated PostgreSQL session holds a database-wide advisory ownership lock and
  performs every mutation. Competing processes fail promptly. Session loss prevents
  further writes through that owner; closing/disconnecting releases ownership.
  This is the ownership boundary for offline commands, preceding M6's engine and
  private Unix socket. Owners must be closed explicitly by API callers.
- CLI: `paper account create --name demo --balance 10000 --request-id demo-opening`,
  `paper accounts`, and `paper account show <account-id>`. Each supports text/JSON.
  `init` applies migration 4; account creation never initializes storage implicitly.

See the README's paper-account section for request identity, name rules, numeric
bounds, configuration defaults, error behavior and safe retries. Bounds are
positive USDT up to 10^18 with at most 18 fractional digits; unsupported precision
is rejected. Account names are nonunique display labels. No new dependency was
introduced. The local outbox's `account_opened` event makes no claim about StopLore
support and is not delivered; M7 owns mapping/reconciliation.

## Verification

Test boundaries were confirmed by the user: CLI and PostgreSQL account/ownership
APIs. Focused red/green checks covered account persistence, the absent CLI lifecycle,
and unsupported configuration precision. PostgreSQL 17 ran in an isolated disposable
container; the repository's test helper created and removed dedicated test databases.

Passed:

- Focused account CLI and PostgreSQL tests, including exact opening balance/ledger
  reconciliation, restart, retry identity, invalid inputs, injected ledger/outbox
  insert failures, append-only UPDATE/DELETE/TRUNCATE guards, competing processes,
  owner process death, forced backend termination, closed owners and cancellation.
- `go test ./...` with `BILLIONAIRE_TEST_DATABASE_URL` enabled, including migration
  up/down/repeat/reopen and all existing M1–M3 tests.
- `go vet ./...`, `go build ./...`, and `git diff --check`.

- `go test -race ./internal/cli ./internal/store/postgres` with PostgreSQL enabled.
- Focused race checks for rollback/session loss after the test-only review change.

## Standards review

Independent review found no hard breaches of documented repository standards.
One P3 testing judgement identified session-loss coverage coupled to the private
advisory-lock key. The test now identifies the new database session without that
key, verifies exactly one backend was terminated, and asserts failure through the
owner API. A direct outbox-count assertion was also removed; injected outbox failure
still proves atomic rollback through the public account API. Re-review confirmed
both changes and no remaining standards findings.

## Spec review

Independent review found no actionable missing/partial requirements, scope creep,
or incorrect implementations for issue #1. It verified dedicated-session ownership,
exact balances, request retry semantics and atomic account/ledger/outbox writes.
M4's remaining behavior and M2's outstanding live-host gate stay explicit.

Review totals: Standards — 1 finding addressed, 0 remaining; Spec — 0 findings.

## Remaining gates

No order/fill/position behavior or daemon is implemented by this slice. Account
inspection currently exposes opening cash and its ledger, not future exposure or
margin metrics. Financial outbox delivery is deferred to M7; no StopLore code or
exchange trading API is introduced. M2 live-host validation, StopLore licensing
and the documented Go patch-refresh release gate remain open.
