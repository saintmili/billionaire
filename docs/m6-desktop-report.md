# M6 Linux desktop notification slice (#31)

Desktop delivery extends the shared durable notification worker and CLI. No
migration or additional service is required. This is offline fixture evidence;
M2 actual-host acceptance remains pending, live monitoring stays disabled and M6
combined acceptance is not claimed.

## Workstation and headless configuration

Install your distribution's `notify-send` utility (usually the libnotify package)
and run Billionaire as your logged-in desktop user with the inherited session
D-Bus environment. Select the optional target in configuration:

```yaml
notifications:
  targets: [terminal, desktop]
  cooldown: 30s
```

Start the authorized offline fixture runtime with
`billionaire start --fixture testdata/market/m3.json --config config.yaml`.
Foreground start captures target and cooldown policy for new source events.
Existing deliveries retain their original targets. Both foreground delivery and
`billionaire notifications dispatch --config config.yaml --output json` use the
same adapter map. Inspect outcomes using
`billionaire notifications status --config config.yaml --output json`.

On headless servers keep `targets: [terminal]`. Selecting desktop without a
usable facility records an inspectable delivery failure and preserves terminal
delivery and financial processing. No display/session is required by tests.
Presence of D-Bus, DISPLAY or WAYLAND_DISPLAY allows an invocation; an invalid or
unreachable session then produces the nonzero-exit category below. The application
does not create sessions or grant access to another user's session.

## Delivery behavior and limits

The adapter resolves `notify-send` from the process's trusted PATH. It invokes the
executable directly, with fixed options, an option terminator and two literal text
arguments; no shell parses event values. The summary identifies the event kind;
the body contains the complete event JSON with markup escaped. Invalid UTF-8,
control characters and any event text field above 4096 bytes are rejected. A
malformed event cannot inject extra options, shell commands or notification markup.
Child stdout and stderr are discarded rather than copied into logs or persistence.

Each invocation has a 1.5-second deadline, inside the shared worker's two-second
send deadline. Failure categories exposed through status are:

| Category | Delivery outcome |
| --- | --- |
| `desktop_missing_binary` | Permanent failure; executable unavailable or unsafe PATH resolution |
| `desktop_session_unavailable` | Permanent failure; no D-Bus/display session environment |
| `desktop_invalid_event` | Permanent failure; invalid/oversized event text |
| `desktop_timeout` | Retry after bounded exponential backoff |
| `desktop_failed` | Retry after bounded exponential backoff; nonzero exit or launch failure |

The common PostgreSQL dispatcher makes at most five attempts, then retains the
failed delivery. Retry timestamps and attempts survive restart. Event/target
uniqueness and shared cooldown prevent duplicate source enqueue and repeated
condition spam. A crash between successful display and acknowledgement can still
duplicate external display, as with terminal delivery. Successful process exit
means notify-send accepted the invocation; it cannot prove a human saw an alert.
After repairing a permanent facility failure, future events can deliver; this
slice does not add a manual retry command for failed historical deliveries.

## Verification

Local executable fixtures cover successful literal argument delivery, markup
escaping, missing binary/session, nonzero exit with sensitive child output,
timeout, NUL/control/UTF-8 rejection and size limits. Real disposable PostgreSQL
fixtures cover duplicate suppression and cooldown through CLI dispatch/status,
durable headless failures, restart between attempts, terminal delivery alongside
desktop failures, five-attempt exhaustion and no later retry. No actual desktop
alert or remote message is sent. Full suite, practical race checks, vet and build
are recorded in the ticket handoff.
