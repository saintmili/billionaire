CREATE TABLE billionaire.control_outcomes (
 request_id TEXT PRIMARY KEY,
 payload JSONB NOT NULL,
 outcome JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER control_outcomes_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON billionaire.control_outcomes
 FOR EACH STATEMENT EXECUTE FUNCTION billionaire.reject_ledger_mutation();
