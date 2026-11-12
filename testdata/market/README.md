# M0 candle fixture

`bybit-spot-btcusdt-1m.json` is a **synthetic** seven-field Bybit V5 kline response, constructed from the [documented wire contract](https://bybit-exchange.github.io/docs/v5/market/kline), checked on 2026-11-12. Prices and volume are illustrative, not historical evidence. No credentials or real account data are included.

The candle starts at `1670601600000` (2022-12-09 16:00 UTC), ends exclusively at 16:01 UTC, and the fixture provider time is exactly that end. Decimal strings deliberately retain trailing zeros to demonstrate lossless decoding. This single candle is for reconnaissance; M2/M3 must add multi-market history, gaps, duplicates, and breakout fixtures.
