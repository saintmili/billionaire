CREATE TABLE millionaire.paper_accounts (
 id TEXT PRIMARY KEY,
 request_id TEXT NOT NULL UNIQUE,
 name TEXT NOT NULL CHECK(name ~ '^[A-Za-z0-9][A-Za-z0-9 _.-]{0,79}$'),
 currency TEXT NOT NULL CHECK(currency = 'USDT'),
 initial_balance NUMERIC(38,18) NOT NULL CHECK(initial_balance > 0 AND initial_balance <= 1000000000000000000),
 balance NUMERIC(38,18) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE millionaire.paper_ledger_entries (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES millionaire.paper_accounts(id),
 kind TEXT NOT NULL,
 amount NUMERIC(38,18) NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX paper_ledger_account ON millionaire.paper_ledger_entries(account_id,created_at,id);
CREATE FUNCTION millionaire.reject_ledger_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'paper ledger is append-only';
END;
$$;
CREATE TRIGGER paper_ledger_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON millionaire.paper_ledger_entries
 FOR EACH STATEMENT EXECUTE FUNCTION millionaire.reject_ledger_mutation();
CREATE TABLE millionaire.journal_outbox (
 event_id TEXT PRIMARY KEY,
 kind TEXT NOT NULL,
 payload JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
