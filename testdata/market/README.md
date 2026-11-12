# M0 candle fixture

`bybit-spot-btcusdt-1m.json` is a **synthetic** seven-field Bybit V5 kline response, constructed from the [documented wire contract](https://bybit-exchange.github.io/docs/v5/market/kline), checked on 2026-11-12. Prices and volume are illustrative, not historical evidence. No credentials or real account data are included.

The candle starts at `1670601600000` (2022-12-09 16:00 UTC), ends exclusively at 16:01 UTC, and the fixture provider time is exactly that end. Decimal strings deliberately retain trailing zeros to demonstrate lossless decoding. This single candle is for reconnaissance; M2/M3 must add multi-market history, gaps, duplicates, and breakout fixtures.

`m2.json` is a synthetic version-1 dataset for the explicit `fixture` provider.
It contains one hour of 1m candles for BTC/USDT and ETH/USDT in spot and linear
markets, with exact illustrative prices/volumes and scripted partial, duplicate,
and late-partial updates. The adapter derives other intervals from complete 1m
buckets and advances its observation clock deterministically. All fixture
identities are separate from `bybit`; the data is never passed off as captured
exchange history. It requires no network access or credentials.
