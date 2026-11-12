-- Position protection and candle observations extend the existing JSON payloads;
-- immutable adjustment requests retain replay outcomes and audit history.
CREATE TABLE millionaire.paper_protection_adjustments (
 id TEXT PRIMARY KEY,
 position_id TEXT NOT NULL REFERENCES millionaire.paper_positions(id),
 request JSONB NOT NULL,
 result JSONB NOT NULL
);

-- Existing positions retain the entry's fee/slippage assumptions, but the old
-- sizing-only stop is not silently activated as a protective stop on upgrade.
UPDATE millionaire.paper_positions AS p
SET payload = p.payload || jsonb_build_object('protection', jsonb_build_object(
 'updated_at', f.payload->'available_at',
 'fee_rate', o.payload->'request'->'fee_rate',
 'slippage_bps', o.payload->'request'->'slippage_bps',
 'trailing_active', false,
 'target_done', false
))
FROM millionaire.paper_fills AS f
JOIN millionaire.paper_orders AS o ON o.id=f.order_id
WHERE f.position_id=p.id AND o.payload->'request'->>'action'='open'
 AND (p.payload->'protection' IS NULL OR p.payload->'protection'='null'::jsonb);
