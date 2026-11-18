CREATE TABLE billionaire.market_instruments (
 provider TEXT NOT NULL, market TEXT NOT NULL CHECK(market IN ('spot','linear')),
 instrument_id TEXT NOT NULL, base TEXT NOT NULL, quote TEXT NOT NULL CHECK(quote='USDT'),
 metadata JSONB, PRIMARY KEY(provider,market,instrument_id)
);
CREATE TABLE billionaire.market_candles (
 source TEXT NOT NULL, provider TEXT NOT NULL, market TEXT NOT NULL, instrument_id TEXT NOT NULL,
 timeframe TEXT NOT NULL, open_time TIMESTAMPTZ NOT NULL, end_time TIMESTAMPTZ NOT NULL CHECK(end_time>open_time),
 open NUMERIC NOT NULL CHECK(open>0), high NUMERIC NOT NULL CHECK(high>0), low NUMERIC NOT NULL CHECK(low>0),
 close NUMERIC NOT NULL CHECK(close>0), base_volume NUMERIC NOT NULL CHECK(base_volume>=0),
 revision TEXT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL,
 PRIMARY KEY(source,provider,market,instrument_id,timeframe,open_time),
 FOREIGN KEY(provider,market,instrument_id) REFERENCES billionaire.market_instruments,
 CHECK(high>=low AND high>=open AND high>=close AND low<=open AND low<=close)
);
CREATE TABLE billionaire.market_candle_revisions (
 source TEXT NOT NULL, provider TEXT NOT NULL, market TEXT NOT NULL, instrument_id TEXT NOT NULL,
 timeframe TEXT NOT NULL, open_time TIMESTAMPTZ NOT NULL, revision TEXT NOT NULL, payload JSONB NOT NULL,
 PRIMARY KEY(source,provider,market,instrument_id,timeframe,open_time,revision)
);
CREATE TABLE billionaire.market_feed_state (
 provider TEXT NOT NULL, market TEXT NOT NULL, instrument_id TEXT NOT NULL, timeframe TEXT NOT NULL,
 payload JSONB NOT NULL, PRIMARY KEY(provider,market,instrument_id,timeframe)
);
