# M6 capital adjustments (#27)

This offline slice adds explicit simulated USDT deposits and withdrawals to an
existing paper account. Live monitoring remains disabled; this does not complete
M6 acceptance or waive actual-host validation.

## Operating path

With the owning foreground engine running:

```sh
billionaire paper account adjust ACCOUNT-ID --direction deposit \
  --amount 100.125 --currency USDT --reason 'Additional simulated capital' \
  --at 2026-11-19T12:00:00Z --request-id capital-20261119-1 --output json
billionaire paper account adjust ACCOUNT-ID --direction withdraw \
  --amount 25 --reason 'Reduce simulated capital' \
  --at 2026-11-19T12:05:00Z --request-id capital-20261119-2 --output json
billionaire control outcome capital-20261119-1 --output json
billionaire paper account show ACCOUNT-ID --output json
billionaire report trades --scope forward-paper --account ACCOUNT-ID --output json
```

Default mutations use the private owner socket. Explicit `--offline` uses the
same shared application service and immutable receipt under exclusive paper
ownership. It fails while another owner holds the session; socket failure never
falls back to an offline writer. After an uncertain response, reuse the same
request ID and identical command inputs, or query its outcome on the running
owner. A receipt preserves the original result even after later balance changes.
Changing any command inputs under that identity is an error, including changing
the amount's textual representation. Rejected outcomes remain rejected; submit a
new identity for a new attempt after correcting the cause.

Amount must be a positive plain decimal, no more than 18 fractional digits and
at most 1000000000000000000 USDT. Resulting cash must remain between zero and that
maximum. Currency is USDT only. Account, direction, audit reason, explicit RFC3339
timestamp and stable request identity are required. Reason is limited to 500 bytes
and cannot contain line breaks. `--at` records the caller's audit timestamp, while
ledger `created_at` records the database transaction clock. Withdrawal freshness
uses the current UTC clock, so a caller cannot revive an old mark with `--at`.

## Financial guarantees

The pinned owner mutex serializes adjustments with execution and controls; the
account row is also locked. Migration 0014 adds immutable ledger audit reason,
request identity and occurrence timestamp, plus a unique request identity index.
The existing owner/control savepoint boundary atomically commits cash, the signed
`capital_deposit`/`capital_withdraw` ledger movement, a financial journal-outbox
event with request and resulting account, and the immutable control receipt.
There is no StopLore delivery in this milestone. The financial outbox records
external capital flows rather than inventing journal trade API calls.

Withdrawals cannot exceed either cash or fresh valued free margin. Existing
linear collateral is already excluded from cash and is never subtracted twice.
Unrealized linear losses reduce free margin; unrealized profits cannot authorize
withdrawing more cash than exists. Missing, unready or stale marks block a
withdrawal whenever exposure requires valuation. Deposits remain available when
marks are unavailable, including accounts with open positions. Capital changes
do not create orders, close positions, or alter position collateral.

## Reporting semantics

Trade lifecycle gross/net P&L, costs and realized valuation P&L exclude external
capital flows. Account ledger totals still reconcile exactly to cash. Reports
expose account-wide `capital_deposits` and positive `capital_withdrawals` totals
separately. Opening capital remains the original opening ledger entry.

If any external flow exists, `return`, `maximum_drawdown` and
`maximum_drawdown_fraction` are null, and `return_status` is
`unavailable_external_capital_flows`. Current equity remains visible when fresh.
Raw equity jumps caused by capital must not be described as trading performance;
flow-timed equity sufficient for a defensible return/drawdown model is not
retained. No time-weighted return is estimated. With no external flows existing
return behavior is preserved. These totals, like ledger reconciliation, are
account-wide even when lifecycle filters select a subset of trades.

## Verification

Red tests observed `unsupported control command` at the shared application seam
and `unknown flag: --direction` at the CLI seam before their implementations.
Tests use disposable real PostgreSQL and the confirmed public seams:

- Exact fractional deposit, original outcome recovery after owner restart,
  duplicate retry, conflict rejection, signed ledger audit and zero trade P&L.
- Offline CLI deposit/withdraw/retry and exact 18-place socket request to a held
  owner; explicit offline attempts cannot bypass ownership.
- Concurrent 600-USDT withdrawals from 1000 accept exactly one and leave 400.
- Open isolated linear positions retain 40 collateral: withdraw 959.8 cash at a
  flat mark or 949.8 when a 10-USDT unrealized loss must remain covered. Greater
  amounts fail. Stale marks reject withdrawals, while deposits still succeed.
- Injected failures at ledger, financial outbox and receipt writes roll back
  cash, ledger and receipt; retry succeeds without conflicting orphan events.
- Invalid amounts, precision, currency, direction, missing time/reason/account,
  overflow and unknown JSON fields cannot change cash or ledger.

Validation passed with the disposable PostgreSQL DSN: `go test ./...`,
`go test -race ./internal/app ./internal/cli ./internal/store/postgres -run TestCapital -count=1`,
`go vet ./...`, and `go build ./cmd/billionaire`.
