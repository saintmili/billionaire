CREATE TABLE billionaire.paper_controls (
 id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK(id), kill_switch BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO billionaire.paper_controls(id) VALUES(TRUE);
CREATE TABLE billionaire.paper_risk_policies (
 account_id TEXT NOT NULL REFERENCES billionaire.paper_accounts(id),
 strategy_id TEXT NOT NULL DEFAULT '',
 policy JSONB NOT NULL,
 PRIMARY KEY(account_id,strategy_id)
);
