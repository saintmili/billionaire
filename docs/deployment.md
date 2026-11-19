# Private Linux deployment

The workstation's public-provider host checks were accepted on 2026-11-19.
A future VPS needs its own [host validation](m2-host-validation.md) before live
startup there. All orders remain simulated. Use one engine per logical database;
foreground and system services must not run concurrently against it.

## PostgreSQL and protected configuration

Use PostgreSQL 17 with password authentication (SCRAM), bound to loopback or a
local Unix socket. The normal `compose.yaml` requires a password and binds
loopback; start it with `docker compose up -d --wait`. Never deploy the disposable
`compose.test.yaml` trust-authentication server. Protect backups and database
credentials; do not publish port 5432 or any control endpoint.

Create a separate login role and application database using a PostgreSQL
administrator. Give the application role ownership of its database and public
schema, with permission to create retained backtest schemas. It needs application
DDL/migrations and writes, but no superuser, role creation or database creation.
Use the administrative `psql` `\password` prompt to set its password without
placing the value in SQL history. StopLore delivery and its separate schema are
pending M7. Run schema migrations explicitly while the engine is stopped; they
are deliberately absent from the service's startup commands.

For a dedicated system service, run these installation steps as an administrator
from the source checkout. Adapt the nologin path to the host:

```sh
sudo useradd --system --user-group --home-dir /var/lib/billionaire --shell /usr/bin/nologin billionaire
go build -o /tmp/billionaire-install ./cmd/billionaire
sudo install -o root -g root -m 0755 /tmp/billionaire-install /usr/local/bin/billionaire
sudo install -d -o root -g billionaire -m 0750 /etc/billionaire
sudo install -d -o billionaire -g billionaire -m 0700 /var/lib/billionaire
sudo install -o billionaire -g billionaire -m 0600 configs/example.yaml /etc/billionaire/config.yaml
sudo install -o root -g root -m 0600 /dev/null /etc/billionaire/billionaire.env
sudoedit /etc/billionaire/billionaire.env
```

Put `BILLIONAIRE_DATABASE_URL="postgres://..."` in that environment file using the
actual private credentials. `storage.dsn_env` names that variable in config.
The file uses systemd assignment syntax, with no `export`, shell substitutions or
shell commands. Do not source it as shell code. Keep it root-owned mode 0600;
systemd reads it before switching user. Config contains environment variable names,
not secrets. Set instruments/timeframes, notification targets and engine settings
in config before startup. The working directory is `/var/lib/billionaire`.

For maintenance commands, `systemd-run` can load the same protected environment
without printing credentials or granting an interactive login to the service user:

```sh
sudo systemd-run --wait --pipe --collect -p User=billionaire -p Group=billionaire -p UMask=0077 -p EnvironmentFile=/etc/billionaire/billionaire.env /usr/local/bin/billionaire --config /etc/billionaire/config.yaml config validate
sudo systemd-run --wait --pipe --collect -p User=billionaire -p Group=billionaire -p UMask=0077 -p EnvironmentFile=/etc/billionaire/billionaire.env /usr/local/bin/billionaire --config /etc/billionaire/config.yaml init
```

`config validate` checks configuration offline; `init` checks credentials and
applies migrations. Run `init` only while stopped, including upgrades. An owner
rejects concurrent migration. Database unavailability and failed migrations are
errors to repair before proceeding, never reasons to launch a second owner.

## Foreground workstation

Use the logged-in user's authenticated database, mode-0600 config and a private
socket directory. Read the URL without terminal echo, then export it; never put
credentials in command arguments or a transcript:

```sh
read -rs BILLIONAIRE_DATABASE_URL
export BILLIONAIRE_DATABASE_URL
billionaire --config /absolute/config.yaml config validate
billionaire --config /absolute/config.yaml init
billionaire --config /absolute/config.yaml --control-socket /private/runtime/control.sock --output json start --live
```

Create `/private/runtime` under a directory you own, mode 0700, replacing the
example path everywhere. Choose exactly one source: `--live`, or
`--fixture /absolute/market.json`. Fixtures finish unless `--hold` is added.
For the shipped `testdata/market/m2.json`, set `engine.lookback: 1` and
`engine.queue_capacity: 1` in a test config; do not silently change production
lookback. Ctrl-C or SIGTERM drains cleanup, closes the socket and releases the
PostgreSQL owner. Graceful exit is 0 (currently engine cancellation is consumed)
or 130 if cancellation reaches main; the unit accepts both.

Terminal alerts go to stderr. Optional `notifications.targets: [terminal, desktop]`
uses the logged-in desktop's `notify-send` session. A dedicated headless user
normally uses `[terminal]`; missing desktop access is nonfatal and visible in
notification failures. See [desktop behavior](m6-desktop-report.md).
Optional Telegram uses `[terminal, telegram]` plus the environment variable names
under `notifications.telegram`. Store the actual token/chat ID in the protected
environment file. Delivery has five bounded attempts and may duplicate after a
lost external acknowledgement; inspect `notifications status`. See the
[Telegram guide](m6-telegram-report.md). No live notification is needed for testing.

## System service

Install the shipped unit after the binary, configuration and database are ready:

```sh
sudo systemd-analyze verify deploy/billionaire.service
sudo install -o root -g root -m 0644 deploy/billionaire.service /etc/systemd/system/billionaire.service
sudo systemctl daemon-reload
sudo systemctl start billionaire.service
sudo systemctl status billionaire.service --no-pager
sudo journalctl -u billionaire.service -n 100 --no-pager
```

The unit selects explicit `start --live`; use it only on a validated host.
It creates owner-only `/run/billionaire` and `/var/lib/billionaire`, uses umask
0077, root-owned executable, protected system/home files, and no privilege gain.
The control socket is `/run/billionaire/control.sock` mode 0600 with same-UID peer
checks; membership in the group alone is insufficient. Maintenance clients run
as `billionaire`, locally or via an authorized SSH/sudo session. For example:

```sh
sudo systemd-run --wait --pipe --collect -p User=billionaire -p Group=billionaire -p EnvironmentFile=/etc/billionaire/billionaire.env /usr/local/bin/billionaire --config /etc/billionaire/config.yaml --control-socket /run/billionaire/control.sock --output json status
```

Replace `status` with `kill-switch on --request-id operator-pause-1` for a control
round-trip, then `control outcome operator-pause-1` to inspect its receipt.
A request ID identifies one immutable request: reuse it only for the identical
retry. Resume with `kill-switch off --request-id operator-resume-1`.

`Type=simple` active means the process started, not that entries are ready.
Require authoritative `engine_connectivity: connected`, ownership `held`, database
ready and suitable `health.feeds`/`health.exposure`. `entries_ready` must be false
while recovering, disconnected, stale or missing required coverage. Inspect last
successful processing timestamps, gaps and counts, including pending notification
and journal work. A held finite fixture is disconnected after exhaustion, and
never proves live monitoring. See [health semantics](m6-recovery-report.md).

```sh
sudo systemctl restart billionaire.service
sudo systemctl stop billionaire.service
sudo systemctl enable billionaire.service
```

Restart sends SIGTERM, waits up to 30 seconds for cleanup, then starts a new
session. Stop does not trigger automatic restart. Verify stopped ownership and
socket removal using `status`; restart must show a new health session and retain
control receipts. Enable is an explicit operator choice for boot persistence.
The unit restarts failures after 10 seconds, with at most three starts in five
minutes. This bounds a repeated startup failure loop, not lifetime restarts.
After repairing a failure use `systemctl reset-failed billionaire.service` then
start. Startup validates config but does not run migrations. Start authenticated
PostgreSQL separately: Docker Compose has no universal `postgresql.service`
dependency, while installed PostgreSQL needs its actual host unit checked.

Stdout with `--output json` is NDJSON; stderr contains diagnostics and terminal
alerts with separate formats. Journald captures both. Configure host journal
size/time retention and review notification failures; never include credentials
in shared logs. No public TCP control listener is provided.

## Repeatable fixture acceptance

The automated journey builds the actual binary, provisions a uniquely named
database on an explicitly disposable test server, creates private mode-0600
config and service environment, and runs start/status/control/stop/restart. It
checks socket permissions, competing ownership, historical request retry,
account uniqueness after restart, SIGTERM cleanup and missing-owner rejection.
It never queries financial tables behind the CLI. Static verification substitutes
`/usr/bin/true` only for the executable path; installed verification above uses
the real executable.

```sh
# URL must name a disposable test server with database-creation permission.
go test ./internal/cli -run '^TestDeployment' -count=1 -v
# Explicit opt-in: temporary isolated user-manager units; no persistent install.
BILLIONAIRE_TEST_SYSTEMD=1 go test ./internal/cli -run '^TestDeploymentFixtureLifecycle$' -count=1 -v
```

Set `BILLIONAIRE_TEST_DATABASE_URL` securely first; without it the integration
journey skips (the repository `scripts/check` requires it and provisions it).
User mode needs `systemd-run`, an accessible user bus and a functioning manager.
It uses the same fixture and CLI assertions plus bounded restart, success exit,
private umask, protected system/home and stop timeout properties. It deliberately
substitutes temporary paths and the current user for the production dedicated
user/runtime/state directories. It does not install or enable the system unit.
All inputs are copied outside home because `ProtectHome=true` hides home files.
For a dedicated fixture install, use an isolated database/config and a temporary
drop-in clearing `ExecStart=` then setting the full absolute command with
`start --fixture /var/lib/billionaire/market.json --hold`. Remove that drop-in
before live operation. Do not test against a normal paper database.

## Repair and retention

Invalid configuration: run offline validation and fix before restart. Missing
schema/authentication/database: check authenticated PostgreSQL, explicit init and
role grants. Competing owner: stop the intended engine first; never bypass its
lock with offline mutations. Missing/stale socket: inspect ownership and current
process first. Never blindly unlink an endpoint or recursively remove a runtime
directory; remove only an individually proven stale, test-owned socket while no
engine owns the deployment. A stale or disconnected feed needs provider/backfill
repair; restarting alone does not prove readiness. Headless desktop failure and
Telegram failure appear in durable notification status and remain nonfatal.

Retain PostgreSQL logical backups, strategy YAML, configuration without secrets,
and exact binary/source version metadata in protected storage. With the URL in
your private environment, an authenticated backup can avoid a credential argument:

```sh
umask 077
PGDATABASE="$BILLIONAIRE_DATABASE_URL" pg_dump --format=custom --file billionaire.dump
```

Restore drills use a separate empty authenticated database, `pg_restore` from the
protected archive, then matching binary/config validation, stopped-owner schema
inspection and fixture status/reconciliation. Never restore over the running
application database. Keep original backups until the drill reconciles. Host
backup scheduling/retention and full disaster/release acceptance remain M8;
external journal delivery remains M7 and private web operation remains M6W.
