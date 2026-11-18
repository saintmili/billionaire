CREATE TABLE billionaire.signal_reports (
 id TEXT PRIMARY KEY,
 scope TEXT NOT NULL CHECK(scope IN ('backtest','forward-paper')),
 run_id TEXT REFERENCES billionaire.backtest_runs(id),
 report JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE FUNCTION billionaire.reject_signal_report_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'signal reports and success definitions are immutable'; END;
$$;
CREATE TRIGGER signal_report_immutable BEFORE UPDATE OR DELETE ON billionaire.signal_reports
 FOR EACH ROW EXECUTE FUNCTION billionaire.reject_signal_report_change();
