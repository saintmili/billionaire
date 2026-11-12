# Millionaire

Millionaire monitors markets, evaluates strategies, simulates trading, and reports results. All trading in the MVP is paper trading.

## Language

**Instrument**:
A market-specific asset or contract identified with its provider and market type. Spot BTC/USDT and perpetual BTC/USDT are distinct instruments.

**Candle**:
A time-bounded summary of market prices and volume. A partial candle is still forming; a closed candle has completed its interval.

**Strategy definition**:
The stable identity and name of a trading strategy across its versions.

**Strategy version**:
An immutable snapshot of a strategy's parameters, rules, risk settings, and evaluation and signal policies.

**Strategy instance**:
A binding of one strategy version to instruments, timeframes, and a selected account, with an independent enabled state.

**Signal**:
A recorded strategy observation containing direction, market context, and supporting evidence. A signal is distinct from an order or fill.

**Paper account**:
A simulated trading account with balances, exposure, and a history of financial changes.

**Order**:
A request to execute a paper trade, subject to validation and risk rules.

**Fill**:
A simulated execution of an order at a specified price and quantity.

**Position**:
An open market exposure in a paper account, with an entry history and subsequent adjustments or exits.

**Ledger entry**:
An immutable record of a financial change in a paper account, including trade proceeds, fees, or funding.

**Backtest**:
A historical replay of a specific strategy version with recorded data and simulation assumptions, isolated from ongoing paper accounts.

**Journal**:
The StopLore trade record associated with simulated trading activity. The exact mapping of position and fill events to journal records remains to be verified in M0.
