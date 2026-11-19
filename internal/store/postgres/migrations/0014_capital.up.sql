ALTER TABLE billionaire.paper_ledger_entries ADD COLUMN reason TEXT NOT NULL DEFAULT '', ADD COLUMN request_id TEXT, ADD COLUMN occurred_at TIMESTAMPTZ;
CREATE UNIQUE INDEX paper_ledger_capital_request ON billionaire.paper_ledger_entries(request_id) WHERE request_id IS NOT NULL;
