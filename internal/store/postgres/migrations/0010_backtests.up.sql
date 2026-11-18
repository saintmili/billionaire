CREATE TABLE billionaire.backtest_runs (
 id TEXT PRIMARY KEY,
 execution_schema TEXT NOT NULL UNIQUE,
 status TEXT NOT NULL CHECK(status IN ('running','completed','failed','cancelled')),
 inputs JSONB NOT NULL,
 manifest JSONB NOT NULL,
 report JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE FUNCTION billionaire.freeze_backtest_inputs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.inputs IS DISTINCT FROM OLD.inputs OR NEW.manifest IS DISTINCT FROM OLD.manifest
    OR NEW.execution_schema IS DISTINCT FROM OLD.execution_schema OR NEW.id IS DISTINCT FROM OLD.id THEN
   RAISE EXCEPTION 'backtest inputs and manifest are immutable';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER backtest_inputs_immutable BEFORE UPDATE ON billionaire.backtest_runs
 FOR EACH ROW EXECUTE FUNCTION billionaire.freeze_backtest_inputs();
