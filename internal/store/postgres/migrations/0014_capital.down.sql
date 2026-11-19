DROP INDEX billionaire.paper_ledger_capital_request;
ALTER TABLE billionaire.paper_ledger_entries DROP COLUMN reason, DROP COLUMN request_id, DROP COLUMN occurred_at;
