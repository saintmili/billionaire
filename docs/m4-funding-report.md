# M4 funding and approximate liquidation — ticket #7

This slice adds explicit funding intervals, durable coverage/provenance and an
approximate isolated liquidation model. It uses deterministic fixtures and
PostgreSQL under the approved offline M4 exception. M4 combined acceptance is
still pending; M2 actual-host validation remains required before live monitoring.
No exchange order interface, authentication, M5 replay product or journal delivery
is introduced.

## Implemented boundaries

- `internal/paper`: exact signed funding payments, immutable per-entry liquidation
  assumptions, adverse candle/stop ordering, entry eligibility clocks and isolated
  deficit reporting. Existing linear settlement caps each exit at released
  collateral, preventing a gap from consuming available cash or other positions.
- `internal/store/postgres`: owner-controlled `ApplyFunding`, persisted
  `PaperFunding` outcomes, missing interval warnings, funding/observation clock
  guards and transactional liquidation through the existing execution path.
- Migration 9 stores unique funding identities and instrument/effective intervals;
  it recovers existing positions' entry clocks from their durable entry fills and
  records default approximate liquidation assumptions. Up/down roundtrip remains
  covered; ledger history is never rewritten.
- `internal/cli`: `paper funding apply --fixture` and `paper funding list`, both
  support JSON. Linear `paper open` accepts `--maintenance-rate` and
  `--liquidation-fee-rate`; signal entries inherit model defaults.

The approved test seams were paper service methods, PostgreSQL repository methods
and CLI/JSON commands. Tests exercise behavior using real PostgreSQL and public
read methods. SQL triggers only inject failures at the database boundary.

## Financial assumptions and ordering

An explicit `FundingEvent` supplies identity, linear instrument, effective time,
availability time, source, provenance, signed rate and a positive valuation price.
The service accepts normalized public/fixture events; the CLI accepts fixture
source/provider only. There is no network lookup in this command and no automated
historical funding retrieval. `rate: null` requires `missing_reason`, is persisted,
and returns a visible warning without fabricating a zero rate. Coverage is known
only for explicitly supplied expected intervals; absent intervals are not inferred
and an empty history means unknown coverage. These records preserve inputs for
future M5 manifests without implementing a backtest engine.

Payment = remaining quantity × supplied valuation × signed rate. Positive rates
charge longs and pay shorts; negative rates reverse this. Signed payments round
down to 18 decimal places, conservatively charging fractional debits. Funding is
paid from isolated collateral. An immutable `funding` ledger entry and opposing
`funding_collateral_transfer` keep available cash unchanged while collateral and
realized P&L change. An amount beyond collateral is explicitly waived through an
`isolated_deficit` entry; applications expose full payment, collateral delta and
deficit separately. Funding is recognized in `funding_pnl` and position realized
P&L; subsequent partial/final closes release the adjusted collateral.

Funding must be submitted before price observations or position adjustments at
its effective instant. Exposure opened strictly before that instant is eligible;
entries at the same instant are excluded. Funding cannot be applied retroactively
to exposure already closed or advanced by later prices/adjustments. Funding
intervals must increase in effective time with nonregressing availability clocks.
A following observation cannot regress behind the funding effective/availability
boundary. Identical replay returns the original durable outcome, including after
restart; conflicting identities and a second identity for an existing interval
fail. Missing intervals advance the same instrument clock.

Liquidation defaults are maintenance `0.005` and fee `0.005`, fractions of current
notional, each >= 0 with their sum < 1. Each entry persists its chosen model.
Legacy linear positions receive these explicit approximate defaults on upgrade.
The trigger is inclusive:

```text
reserved collateral + directional gross price P&L
    <= current notional × (maintenance rate + liquidation fee rate)
```

The valuation source is the public/fixture observation, not an exchange mark,
order book or risk tier. Funding adjusts collateral first, then its supplied
valuation is checked for liquidation/protection in the same transaction. For a
completed candle, the adverse extreme determines threshold crossing; liquidation
fills at the first adverse instrument tick across the threshold, with configured
slippage. An existing protective stop reached before liquidation wins. An open
already beyond liquidation closes at that open; a target or later manual close
cannot outrank an ambiguous liquidation. Trailing adjustments still take effect
on the next event. Stale/unready prices do not claim an observed liquidation;
bars overlapping the prior position state are skipped.

Liquidation closes all remaining quantity, with the model liquidation fee replacing
the ordinary exit fee. Risk-reducing linear exits may execute below entry minimum
notional after a collapse. All linear exits cap settlement at released collateral;
`isolated_deficit` on fill/ledger records excess price loss plus costs. This is an
explicit approximate write-off, not venue insurance, cross-margin settlement,
automatic collateral top-up or an exchange-accurate bankruptcy price. Funds,
orders, fills, remaining position/margin, outcome and journal-outbox enqueue share
one owner transaction. A competing protective/manual exit rechecks remaining
holdings, preventing duplicate/oversized closure. Outbox delivery remains M7.

## Commands and deterministic evidence

See the [README funding workflow](../README.md#funding-and-approximate-liquidation-m4-ticket-7).
`testdata/paper/linear-funding.json` is an illustrative 1% ETH linear interval after
the existing entry fixture; `linear-funding-missing.json` records explicit later
missing coverage. The rate is deliberately conspicuous and is not venue data.

Verification uses `BILLIONAIRE_TEST_DATABASE_URL` for an isolated local PostgreSQL
cluster and `GOCACHE=/tmp/m4-go-cache` in this sandbox:

```sh
go test ./...
go test -race ./internal/paper ./internal/store/postgres ./internal/cli
go vet ./...
go build ./...
```

All four commands passed, including PostgreSQL-backed tests and the relevant
race checks. The race check caught and verified the fix for replay decoding that
previously reused an incoming funding-rate pointer; replay now decodes a fresh
result without mutating caller data.

Coverage includes all four sign/direction combinations and final cash/ledger/P&L
reconciliation; inclusive thresholds; funding-triggered liquidation; stop versus
liquidation on candle paths and gaps; long/short exhausted collateral; liquidation
below entry minimum notional; competing manual close; concurrent replay/restart;
missing intervals; immutable input conflicts and regressing clocks; migration
upgrade and up/down; CLI JSON roundtrip; and funding/liquidation outbox failures
that roll back financial state and remain retryable without exposing SQL details.

Funding records are immutable; late replacement/correction of a missing interval
is rejected rather than applied to advanced exposure.

Public-provider funding coverage, inferred funding schedules, venue risk tiers,
mark-price feeds, insurance/bankruptcy settlement and live host validation remain
outside this offline slice. Approximate results must retain those limitations.
