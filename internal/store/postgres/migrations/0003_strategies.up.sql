CREATE TABLE millionaire.strategy_definitions(id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE millionaire.strategy_versions(
 id TEXT PRIMARY KEY CHECK(id ~ '^[0-9a-f]{64}$'), strategy_id TEXT NOT NULL REFERENCES millionaire.strategy_definitions,
 number INTEGER NOT NULL CHECK(number>0), definition JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(strategy_id,number)
);
CREATE FUNCTION millionaire.reject_strategy_version_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'strategy versions are immutable'; END;
$$;
CREATE TRIGGER strategy_version_immutable BEFORE UPDATE OR DELETE ON millionaire.strategy_versions
 FOR EACH ROW EXECUTE FUNCTION millionaire.reject_strategy_version_change();
CREATE TABLE millionaire.strategy_instances(
 id TEXT PRIMARY KEY, version_id TEXT NOT NULL REFERENCES millionaire.strategy_versions,
 binding JSONB NOT NULL, enabled BOOLEAN NOT NULL DEFAULT FALSE, runtime_state JSONB NOT NULL DEFAULT '{}'
);
CREATE FUNCTION millionaire.reject_strategy_binding_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.version_id IS DISTINCT FROM OLD.version_id OR NEW.binding IS DISTINCT FROM OLD.binding THEN
 RAISE EXCEPTION 'strategy instance bindings are immutable; create another instance'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER strategy_binding_immutable BEFORE UPDATE ON millionaire.strategy_instances
 FOR EACH ROW EXECUTE FUNCTION millionaire.reject_strategy_binding_change();
CREATE TABLE millionaire.strategy_evaluations(
 instance_id TEXT NOT NULL REFERENCES millionaire.strategy_instances, event_id TEXT NOT NULL,
 observed_at TIMESTAMPTZ NOT NULL, event JSONB NOT NULL, result JSONB NOT NULL,
 PRIMARY KEY(instance_id,event_id)
);
CREATE TABLE millionaire.strategy_signals(
 id TEXT PRIMARY KEY, instance_id TEXT NOT NULL, event_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('entry','exit')), direction TEXT NOT NULL CHECK(direction IN ('long','short')),
 observed_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL,
 FOREIGN KEY(instance_id,event_id) REFERENCES millionaire.strategy_evaluations,
 UNIQUE(instance_id,event_id,kind,direction)
);
CREATE INDEX strategy_signals_by_instance_time ON millionaire.strategy_signals(instance_id,observed_at,id);
