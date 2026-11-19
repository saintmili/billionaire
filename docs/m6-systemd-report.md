# M6 systemd operation (#34)

Added `deploy/billionaire.service`, the [private deployment runbook](deployment.md),
a README pointer, and public CLI subprocess acceptance. No application protocol,
financial behavior, migration or new dependency is introduced. The production
unit intentionally selects `start --live`, retains a single PostgreSQL owner,
and exposes only its same-user private Unix socket.

The unit supplies dedicated user/group, absolute working/config/binary paths,
mode-0700 runtime/state directories, umask 0077, protected mode-0600 environment
file loading, config validation, SIGTERM cleanup with a 30-second deadline,
journald output, no privilege gain and protected system/home files. Failure
restarts wait ten seconds and stop after three starts in five minutes. Explicit
stop does not restart; explicit init/migration is a stopped-owner operation.
The guide covers authenticated restricted PostgreSQL, private local/SSH controls,
authoritative owner versus feed readiness, desktop/headless/Telegram behavior,
backups, isolated restore drills and repair steps.

## Executed evidence (2026-11-19)

Tests used the pinned PostgreSQL 17 image from `compose.test.yaml` under the
isolated Compose project `billionaire-m6-systemd`, loopback ephemeral port 32772.
Each journey created and dropped a random test database via `internal/testdb`.
Normal deployment databases and existing services were not changed.

`TestDeploymentUnitVerification` first failed because the deployment unit did
not exist, then passed after adding it. `systemd-analyze verify` validates the
shipped unit with only the executable path replaced by `/usr/bin/true`; the
runbook separately requires installed verification with the real binary.

`TestDeploymentFixtureLifecycle` builds the real CLI, then checks offline config
validation and init, rejected invalid-config/database-unavailable startup,
stopped-owner mutation rejection, actual fixture start, authoritative health,
0700 directory/0600 socket, account creation and identical retry, durable outcome
lookup, pause/resume and historical pause retry, rejection of offline mutations
and a competing foreground owner, SIGTERM shutdown, socket removal and released
ownership. A second start restores the same account/receipt, creates a new owner
session and repeats controls without duplication. The test waits until all four
finite fixture feeds disconnect; ownership remains held but entries are unready.
It claims no post-exhaustion entry execution or live-provider service readiness.

Commands completed successfully:

```sh
# Disposable server URL supplied through BILLIONAIRE_TEST_DATABASE_URL.
go test ./internal/cli -run '^TestDeployment' -count=1 -v
go test -race ./internal/cli -run '^TestDeployment' -count=1 -v
BILLIONAIRE_TEST_SYSTEMD=1 go test ./internal/cli -run '^TestDeploymentFixtureLifecycle$' -count=1 -v
go vet ./internal/cli
git diff --check
```

The race command instruments the Go test process; the CLI subprocess is built
with ordinary `go build`. Existing engine/control race coverage and final full
repository verification remain with the integration owner's assembled
`./scripts/check`; this report does not claim that later check has run.

The opt-in run used the actual running systemd 262 user manager via temporary
`systemd-run --user --wait --pipe --collect` units named
`billionaire-fixture-<pid>-<timestamp>.service`. It set the documented restart,
start-rate limit, umask, stop deadline, success status, no-new-privileges and
protected-home/system properties. Initial service execution exposed an inaccessible
fixture in the checkout under `ProtectHome=true`; the corrected test stages its
binary, fixture, config and mode-0600 environment under a private `/tmp`
directory. The corrected service journey passed, including actual manager stop
and new-session start. A post-run listing of `billionaire-fixture-*` showed zero
loaded units; temporary units/files/databases were removed. The disposable
Compose project and volume were removed after verification.

## Exact limits

This is actual temporary user-service evidence, with fixture inputs and the
current OS user. It does not install the dedicated `billionaire` system user,
production system unit or persistent environment/config, enable boot startup,
change the normal database, or demonstrate long-running unattended live feeds.
The user-service test substitutes temporary writable paths for production
runtime/state directories and captures process output through pipes; production
journald routing, dedicated-user directory creation and boot ordering were
statically verified/documented rather than installed. Manager lifecycle journal
entries were inspected during diagnosis. The full three-failures/five-minutes
rate-limit exhaustion and forced timeout/SIGKILL path were not exercised;
the unit configuration is verified and normal graceful stop is exercised.

No external provider or Telegram call was made. The accepted workstation M2
host evidence remains authoritative; a future VPS needs its own host check.
This slice does not accept combined M6 #35, M6W, M7 journal delivery or M8 backup/
release hardening. No ADR conflict or domain vocabulary change was needed.
