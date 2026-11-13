CREATE TABLE millionaire.signal_decisions (
 signal_id TEXT PRIMARY KEY REFERENCES millionaire.strategy_signals(id),
 status TEXT NOT NULL CHECK(status IN ('observed','awaiting_approval','accepted','rejected')),
 order_id TEXT UNIQUE REFERENCES millionaire.paper_orders(id),
 payload JSONB NOT NULL
);
CREATE INDEX signal_decisions_status ON millionaire.signal_decisions(status,signal_id);
