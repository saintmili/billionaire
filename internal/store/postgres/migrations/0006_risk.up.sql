CREATE TABLE millionaire.paper_controls (
 id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(id), kill_switch BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO millionaire.paper_controls(id) VALUES(TRUE);
CREATE TABLE millionaire.paper_risk_policies (
 account_id TEXT NOT NULL REFERENCES millionaire.paper_accounts(id),
 strategy_id TEXT NOT NULL DEFAULT '',
 policy JSONB NOT NULL,
 PRIMARY KEY(account_id,strategy_id)
);
