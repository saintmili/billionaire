CREATE TABLE billionaire.paper_orders (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES billionaire.paper_accounts(id),
 instrument_key TEXT NOT NULL,
 intent_at TIMESTAMPTZ NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','filled','rejected')),
 payload JSONB NOT NULL
);
CREATE INDEX paper_pending_orders ON billionaire.paper_orders(instrument_key,intent_at,id) WHERE status='pending';
CREATE TABLE billionaire.paper_positions (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES billionaire.paper_accounts(id),
 status TEXT NOT NULL CHECK(status IN ('open','closed')),
 payload JSONB NOT NULL
);
CREATE INDEX paper_positions_account ON billionaire.paper_positions(account_id,id);
CREATE TABLE billionaire.paper_fills (
 id TEXT PRIMARY KEY,
 order_id TEXT NOT NULL UNIQUE REFERENCES billionaire.paper_orders(id),
 position_id TEXT NOT NULL REFERENCES billionaire.paper_positions(id),
 account_id TEXT NOT NULL REFERENCES billionaire.paper_accounts(id),
 payload JSONB NOT NULL
);
ALTER TABLE billionaire.paper_ledger_entries ADD COLUMN fill_id TEXT REFERENCES billionaire.paper_fills(id);
CREATE TABLE billionaire.paper_price_events (
 id TEXT PRIMARY KEY,
 sequence BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE,
 accepted BOOLEAN NOT NULL,
 instrument_key TEXT NOT NULL,
 observed_at TIMESTAMPTZ NOT NULL,
 available_at TIMESTAMPTZ NOT NULL,
 payload JSONB NOT NULL,
 result JSONB NOT NULL
);
CREATE INDEX paper_price_clock ON billionaire.paper_price_events(instrument_key,sequence DESC) WHERE accepted;
