# M1 scaffolding and durable storage

Implemented 2026-11-12. Scope is CLI/configuration, structured logging and cancellation,
PostgreSQL initialization/migrations/health, core decimal/domain types, Compose,
and CI. Market ingestion, strategies, accounts, execution, and journaling remain
later milestones.

## Files and migration plan

- `cmd/millionaire/main.go`: signal-aware process entrypoint.
- `internal/cli/`: Cobra commands and text/JSON output.
- `internal/config/`: strict schema-versioned YAML, paper-only validation and environment overrides/references.
- `internal/domain/`: exact decimal parsing and shared instrument/candle identity types.
- `internal/store/postgres/`: connection lifecycle, transactional migration runner and read-only health/status.
- `internal/store/postgres/migrations/0001_bootstrap.{up,down}.sql`: a dedicated `millionaire` schema, application metadata, and schema version/checksum tracking; no market/trading tables yet.
- `configs/example.yaml`: embedded starting configuration, no DSN values or exchange credentials.
- `compose.yaml`, `.github/workflows/ci.yaml`, README and this report: installation and repeatable checks.

## Dependencies

Pinned Cobra v1.10.2 for subcommands/help, pgx/v5 v5.10.0 for PostgreSQL,
shopspring/decimal v1.4.0 for exact financial values, and go.yaml.in/yaml/v3
v3.0.4 for strict YAML decoding. Standard `log/slog` supplies structured logs;
there is no logging framework, broker, ORM, or StopLore dependency. The migration
runner is limited to application-owned transactional SQL. sqlc is deferred until
there are actual domain queries to generate.

API references: [Cobra](https://pkg.go.dev/github.com/spf13/cobra@v1.10.2),
[pgx](https://pkg.go.dev/github.com/jackc/pgx/v5@v5.10.0),
[YAML](https://pkg.go.dev/go.yaml.in/yaml/v3@v3.0.4), and
[decimal](https://pkg.go.dev/github.com/shopspring/decimal@v1.4.0).

## Behavior and boundaries

`millionaire init` creates an embedded example config with `0600` permissions,
preserves existing files, and migrates PostgreSQL. `config validate` checks the
versioned YAML offline. `status --output json` is read-only and reports paper
mode, storage schema/readiness, and `engine: not_implemented`. The explicit
`database migrate up|down` command provides the migration acceptance seam.

The first migration creates paper-mode application metadata. Ordered versions
and checksums live in `millionaire.schema_migrations`. All pending up migrations
(or the last down migration) run within one transaction under an advisory lock.
SQL failures roll back; unknown/mismatched versions fail. The tracking table/schema
remain after down at version zero. An uncertain commit acknowledgment tells the
caller to check status rather than claim rollback.

Config validates before opening PostgreSQL. It rejects live mode, exchange-key
fields, duplicates/unknown fields, multiple YAML documents, unsupported market,
quote, provider and interval selections, credentials in provider URLs, private
socket paths, invalid durations and nonpositive/nonfinite financial values.
Environment overrides are validated with the same rules. DSNs are resolved only
from the configured environment reference. Errors avoid DSN values and raw
server messages. Standard slog emits JSON with UTC times on stderr; stdout is
reserved for command results. SIGINT/SIGTERM cancel the command context; connection
and migration waits also obey the configured timeout.

Shared domain types establish distinct spot/linear instrument identities, typed
native timeframes, and canonical candle fields with UTC exclusive bounds,
provenance and exact decimal OHLC/volume. Decimal inputs accept bounded plain
base-ten strings; no financial value passes through floats. Market ingestion,
aggregation and provider metadata validation remain M2. Paper account/ledger,
order/fill/position persistence and execution remain M4.

## Verification

User-confirmed seams: CLI commands, config loader/validator, domain decimal
constructors, PostgreSQL migration/status API. Review baseline: `54997ba`.

Focused tests were run throughout implementation. Initial tests failed on the
missing decimal/config/domain interfaces and CLI commands. Rejection tests
exposed missing validation, including a private WebSocket path, before those
checks were added.

- The shipped Compose file passed validation and started on a random loopback
  port with a generated password. The built binary passed init → config validate
  → status → down → up. Only that temporary project's container/volume were
  removed afterward.
- `go test ./...` passed with `MILLIONAIRE_TEST_DATABASE_URL` set. PostgreSQL tests
  actually ran in uniquely created/disposed databases: repeat/up/down/reopen,
  injected SQL failure rollback, checksum mismatch, concurrent migrations,
  canceled lock waits, and CLI init/status across command instances.
- `go test -race ./internal/store/postgres ./internal/cli` passed with PostgreSQL tests enabled.
- `go vet ./...`, `go build ./...`, `go mod verify`, local Markdown target checks,
  and `git diff --check` passed.

## Deviations and remaining gates

No ADR conflicts. The illustrative config was narrowed to implemented M1 fields
rather than accepting dormant strategy/risk/notification/journal behavior.
The custom migration runner is small and transactional; no third-party migration
framework or sqlc-generated queries are needed for bootstrap metadata. Future
migrations must extend the ordered list and preserve checksum history.

The default unit suite skips database tests without an explicit test URL; CI
sets it against an ephemeral service. CI YAML was added, but a hosted GitHub run
has not been triggered from this local session. PostgreSQL 17 is pinned to the
locally verified image digest; update deliberately for maintenance releases.
Go remains at the available M0-pinned 1.27.2 toolchain; the M0 security-patch
refresh gate still applies before release. Bybit actual-host availability and
StopLore licensing remain their documented later gates. M2 was not started.

## Independent review

The implementation at `40a7820` was reviewed against user-approved baseline
`54997ba` by independent Standards and Spec agents. Standards reported zero
findings. Spec reported one P3: generic YAML decoding errors omitted the offending
field/line required by the configuration error contract. The fix extracts only
location metadata, maps it to a field path, and omits decoder text containing
submitted values. A regression test first failed, then passed for unknown keys,
duplicate keys, sequence-vs-scalar errors and malformed field types. The full
suite, vet and build were rerun successfully after the fix. Final review of the
fix is recorded below.
