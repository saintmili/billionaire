CREATE TABLE billionaire.notification_outbox (
 id text PRIMARY KEY, payload jsonb NOT NULL CHECK (octet_length(payload::text)<=16384), created_at timestamptz NOT NULL
);
CREATE TABLE billionaire.notification_deliveries (
 event_id text NOT NULL REFERENCES billionaire.notification_outbox(id), target text NOT NULL,
 state text NOT NULL CHECK(state IN ('pending','inflight','delivered','failed','suppressed')),
 attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL,
 lease_until timestamptz, claim_token text, error text NOT NULL DEFAULT '', delivered_at timestamptz,
 PRIMARY KEY(event_id,target)
);
CREATE INDEX notification_pending ON billionaire.notification_deliveries(next_attempt_at) WHERE state IN ('pending','inflight');
CREATE TABLE billionaire.notification_cooldowns (key text PRIMARY KEY, last_at timestamptz NOT NULL);
CREATE TABLE billionaire.notification_freshness (key text PRIMARY KEY, ready boolean NOT NULL, sequence bigint NOT NULL, observed_at timestamptz NOT NULL);
CREATE TABLE billionaire.notification_policy (id boolean PRIMARY KEY DEFAULT true CHECK(id), targets jsonb NOT NULL, cooldown_seconds integer NOT NULL);
INSERT INTO billionaire.notification_policy VALUES(true,'["terminal"]',0);
