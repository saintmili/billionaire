# M6 optional Telegram delivery (#32)

Telegram is available through the same durable queue and Sender boundary as terminal
alerts. This slice uses local protocol fixtures and real PostgreSQL only. No live
Telegram messages were sent. Live monitoring remains disabled; M6 acceptance and
M6W remain blocked by their existing gates.

## Configuration and operation

Add `telegram` to `notifications.targets` to select it. Configure environment
references, never literal credentials:

```yaml
notifications:
  targets: [terminal, telegram]
  cooldown: 0s
  telegram:
    token_env: BILLIONAIRE_TELEGRAM_BOT_TOKEN
    chat_id_env: BILLIONAIRE_TELEGRAM_CHAT_ID
```

Set those variables in the process environment (for a deployed service, use a
protected mode-0600 environment file). The token must use the Bot API token form;
chat targets support signed numeric IDs or `@username`. Configuration validation
checks reference names without resolving credentials. Values are resolved only
when constructing a selected delivery adapter. Missing/invalid credentials fail
that target permanently as `telegram_unavailable`; terminal and other operation
continue. Removing Telegram from targets disables new queued Telegram deliveries.
Existing queued targets remain recorded and fail as unavailable if no sender is
selected. No token or chat value is included in CLI results or delivery errors.

`billionaire notifications status --output json` shows per-target durable state;
`billionaire notifications dispatch --output json` attempts one bounded batch.
Foreground operation uses the same sender construction and worker. Configuration
is captured at startup; restart after changing environment references or targets.

## Delivery contract

The adapter POSTs JSON to the fixed HTTPS `api.telegram.org` origin, uses no
redirects, and imposes a two-second request timeout. The message is plain text
(JSON event context, without parse mode) capped conservatively at 4096 UTF-8 bytes;
link previews are disabled. Responses are read through a 64-KiB bound. Neither
transport error URLs nor remote descriptions/bodies are returned or persisted.
Only finite Telegram error categories are retained.

HTTP/API 429 responses use `parameters.retry_after` seconds, including delays over
one hour. Conversion saturates at the largest whole-second Go duration to avoid
overflow. Other 4xx failures and redirects are permanent; transport, malformed or
oversized responses, and server failures retry through the durable queue's bounded
backoff and five-attempt ceiling. The sender does not retry in memory. Delivery
happens after queue claim commit, outside financial ownership.

The queue deduplicates event/target identities and applies configured cooldown.
A request can reach Telegram while its response is lost, or the process can exit
before acknowledgement is recorded. Recovery can repeat the alert: Telegram's
sendMessage API provides no client idempotency key in the verified contract.
Delivered means a successful API acknowledgement, not that a human read it.

Contract verified on 2026-11-19 against the official
[request/response contract](https://core.telegram.org/bots/api#making-requests),
[sendMessage](https://core.telegram.org/bots/api#sendmessage), and
[ResponseParameters](https://core.telegram.org/bots/api#responseparameters).

## Evidence

Tests cover local HTTP success/plaintext/truncation, fixed request origin, refused
redirects, bounded responses, cancellation, missing credentials, permanent and
transient failures, rate limits and overflow. Real PostgreSQL exercises duplicate
queue insertion, per-target independence, delays beyond one hour, restart before
retry, and final exhaustion. CLI fixtures verify offline validation secrecy and
missing-credential terminal delivery. No migration or dependency was added.

Verification completed:

- `BILLIONAIRE_TEST_DATABASE_URL=... go test ./...` passed with real PostgreSQL.
- After merging desktop #31, `go test -race ./internal/notify ./internal/cli ./internal/store/postgres -run 'Telegram|Desktop|Notification' -count=1` passed with real PostgreSQL.
- `go vet ./...` and `go build ./cmd/billionaire` passed after that merge.
- The standalone adapter deadline was also exercised with no caller timeout against a slow local server.

One proposed CLI test was rejected by automatic approval review because it would
construct a real Telegram HTTP client while dispatching. The final CLI dispatch
test explicitly clears both credential variables and returns before constructing
an HTTP request. Offline validation alone uses sentinel secret values. All API
requests in tests go through local protocol transports; no live send was attempted.
