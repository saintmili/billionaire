CREATE TABLE millionaire.paper_funding_events (
 id TEXT PRIMARY KEY,
 instrument_key TEXT NOT NULL,
 effective_at TIMESTAMPTZ NOT NULL,
 payload JSONB NOT NULL,
 result JSONB NOT NULL,
 UNIQUE(instrument_key,effective_at)
);

-- Recover eligibility clocks from durable entry executions on upgrade. Model
-- defaults describe the new approximate simulator, not historical venue data.
UPDATE millionaire.paper_positions AS p
SET payload = p.payload || jsonb_build_object('opened_at', f.payload->'available_at')
FROM millionaire.paper_fills AS f
JOIN millionaire.paper_orders AS o ON o.id=f.order_id
WHERE f.position_id=p.id AND o.payload->'request'->>'action'='open'
 AND (p.payload->'opened_at' IS NULL OR p.payload->>'opened_at'='0001-01-01T00:00:00Z');
UPDATE millionaire.paper_positions
SET payload = payload || '{"liquidation":{"maintenance_rate":"0.005","fee_rate":"0.005"}}'::jsonb
WHERE payload->'instrument'->>'market'='linear'
 AND (payload->'liquidation' IS NULL OR payload->'liquidation'='null'::jsonb);
