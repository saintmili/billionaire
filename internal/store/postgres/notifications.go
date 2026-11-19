package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/notify"
)

func enqueueNotification(ctx context.Context, tx pgx.Tx, e notify.Event, p notify.Policy) error {
	if e.ID == "" || len(e.ID) > 512 || e.Kind == "" || e.At.IsZero() || len(e.Reason) > 2048 {
		return errors.New("invalid notification event")
	}
	e.At = e.At.UTC()
	if e.Level == "" {
		e.Level = "info"
	}
	payload, err := json.Marshal(e)
	if err != nil || len(payload) > 12000 {
		return errors.New("invalid notification payload")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO billionaire.notification_outbox(id,payload,created_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, e.ID, payload, e.At)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if len(p.Targets) == 0 && p.Key == "" && p.Cooldown == 0 {
		var targets []byte
		var seconds int
		if err = tx.QueryRow(ctx, `SELECT targets,cooldown_seconds FROM billionaire.notification_policy WHERE id=true`).Scan(&targets, &seconds); err != nil {
			return safeError(ctx, ErrUnavailable)
		}
		if json.Unmarshal(targets, &p.Targets) != nil {
			return ErrSchema
		}
		if e.Kind == "signal" {
			p.Cooldown = time.Duration(seconds) * time.Second
			p.Key = "signal:" + e.StrategyID + ":" + e.VersionID + ":" + e.Instrument + ":" + e.Timeframe
		}
	}
	state := "pending"
	if p.Cooldown < 0 || p.Cooldown > 24*time.Hour {
		return errors.New("notification cooldown must be between zero and 24h")
	}
	if p.Key != "" && p.Cooldown > 0 {
		var accepted string
		err = tx.QueryRow(ctx, `INSERT INTO billionaire.notification_cooldowns(key,last_at) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET last_at=EXCLUDED.last_at WHERE billionaire.notification_cooldowns.last_at <= $2-$3::interval RETURNING key`, p.Key, e.At, fmt.Sprintf("%f seconds", p.Cooldown.Seconds())).Scan(&accepted)
		if errors.Is(err, pgx.ErrNoRows) {
			state = "suppressed"
		} else if err != nil {
			return safeError(ctx, ErrUnavailable)
		}
	}
	targets := p.Targets
	if len(targets) == 0 {
		targets = []string{"terminal"}
	}
	if len(targets) > 3 {
		return errors.New("too many notification targets")
	}
	for _, target := range targets {
		if target != "terminal" && target != "desktop" && target != "telegram" {
			return errors.New("invalid notification target")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO billionaire.notification_deliveries(event_id,target,state,next_attempt_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, e.ID, target, state, e.At); err != nil {
			return safeError(ctx, ErrUnavailable)
		}
	}
	return nil
}
func (s *Store) RecordNotification(ctx context.Context, e notify.Event, p notify.Policy) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	if err = enqueueNotification(ctx, tx, e, p); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	return nil
}
func (s *Store) Notifications(ctx context.Context, limit int) ([]notify.Delivery, error) {
	return s.NotificationPage(ctx, limit, 0)
}

// NotificationPage lists retained outcomes in stable creation order, without a history cap.
func (s *Store) NotificationPage(ctx context.Context, limit, offset int) ([]notify.Delivery, error) {
	if offset < 0 {
		return nil, errors.New("notification offset must be nonnegative")
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("notification limit must be 1-1000")
	}
	rows, err := s.pool.Query(ctx, `SELECT o.payload,d.target,d.state,d.attempts,d.error,d.next_attempt_at,d.delivered_at,d.lease_until FROM billionaire.notification_deliveries d JOIN billionaire.notification_outbox o ON o.id=d.event_id ORDER BY o.created_at,o.id,d.target LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rows.Close()
	result := []notify.Delivery{}
	for rows.Next() {
		var d notify.Delivery
		var data []byte
		if err = rows.Scan(&data, &d.Target, &d.State, &d.Attempts, &d.Error, &d.NextAttemptAt, &d.DeliveredAt, &d.LeaseUntil); err != nil {
			return nil, ErrSchema
		}
		if json.Unmarshal(data, &d.Event) != nil {
			return nil, ErrSchema
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
func (s *Store) ClaimNotifications(ctx context.Context, now time.Time, limit int) ([]notify.Delivery, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("notification batch must be 1-100")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, `WITH selected AS (SELECT event_id,target FROM billionaire.notification_deliveries WHERE ((state='pending' AND next_attempt_at<=$1) OR (state='inflight' AND lease_until<=$1)) AND NOT EXISTS (SELECT 1 FROM billionaire.notification_target_cooldowns c WHERE c.target=notification_deliveries.target AND c.until_at>$1) ORDER BY next_attempt_at,event_id,target LIMIT $2 FOR UPDATE SKIP LOCKED) UPDATE billionaire.notification_deliveries d SET state='inflight',attempts=d.attempts+1,lease_until=$1+interval '5 minutes',claim_token=md5(random()::text || clock_timestamp()::text) FROM selected s,billionaire.notification_outbox o WHERE d.event_id=s.event_id AND d.target=s.target AND o.id=d.event_id RETURNING o.payload,d.target,d.attempts,d.claim_token`, now, limit)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	result := []notify.Delivery{}
	for rows.Next() {
		var d notify.Delivery
		var data []byte
		if err = rows.Scan(&data, &d.Target, &d.Attempts, &d.Token); err != nil {
			rows.Close()
			return nil, ErrSchema
		}
		if json.Unmarshal(data, &d.Event) != nil {
			rows.Close()
			return nil, ErrSchema
		}
		result = append(result, d)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return result, nil
}

// PrepareNotification admits a claimed send or returns it without consuming retry quota.
// Requests admitted before a rate-limit acknowledgement may already be in flight;
// the durable gate prevents subsequent admissions, including other workers/restarts.
func (s *Store) PrepareNotification(ctx context.Context, d notify.Delivery, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `INSERT INTO billionaire.notification_target_cooldowns(target,until_at) VALUES($1,'1970-01-01') ON CONFLICT DO NOTHING`, d.Target); err != nil {
		return false, safeError(ctx, ErrUnavailable)
	}
	var until time.Time
	if err = tx.QueryRow(ctx, `SELECT until_at FROM billionaire.notification_target_cooldowns WHERE target=$1 FOR UPDATE`, d.Target).Scan(&until); err != nil {
		return false, safeError(ctx, ErrUnavailable)
	}
	ready := !until.After(now)
	if !ready {
		_, err = tx.Exec(ctx, `UPDATE billionaire.notification_deliveries SET state='pending',attempts=attempts-1,next_attempt_at=$4,lease_until=NULL,claim_token=NULL WHERE event_id=$1 AND target=$2 AND claim_token=$3 AND state='inflight'`, d.Event.ID, d.Target, d.Token, until)
		if err != nil {
			return false, safeError(ctx, ErrUnavailable)
		}
	} else {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billionaire.notification_deliveries WHERE event_id=$1 AND target=$2 AND claim_token=$3 AND state='inflight' AND lease_until>$4)`, d.Event.ID, d.Target, d.Token, now).Scan(&valid); err != nil {
			return false, safeError(ctx, ErrUnavailable)
		}
		ready = valid
	}
	if err = tx.Commit(ctx); err != nil {
		return false, safeError(ctx, ErrUnavailable)
	}
	return ready, nil
}
func (s *Store) FinishNotification(ctx context.Context, d notify.Delivery, state, category string, next time.Time) error {
	// Store only a finite, safe category: never persist external error messages/URLs.
	switch category {
	case "desktop_missing_binary", "desktop_session_unavailable", "desktop_timeout", "desktop_failed", "desktop_invalid_event", "telegram_unavailable", "telegram_request", "telegram_transport", "telegram_response", "telegram_rate_limited", "telegram_redirect", "telegram_rejected", "telegram_server", "", "delivery_failed", "target_unavailable", "terminal_unavailable", "rate_limited", "temporary", "permanent", "timeout", "unavailable", "attempts_exhausted":
	default:
		category = "delivery_failed"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	// The gate lock serializes send admission with a rate-limit acknowledgement.
	if _, err = tx.Exec(ctx, `INSERT INTO billionaire.notification_target_cooldowns(target,until_at) VALUES($1,'1970-01-01') ON CONFLICT DO NOTHING`, d.Target); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	var until time.Time
	if err = tx.QueryRow(ctx, `SELECT until_at FROM billionaire.notification_target_cooldowns WHERE target=$1 FOR UPDATE`, d.Target).Scan(&until); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	tag, err := tx.Exec(ctx, `UPDATE billionaire.notification_deliveries SET state=$4,error=$5,next_attempt_at=$6,lease_until=NULL,claim_token=NULL,delivered_at=CASE WHEN $4='delivered' THEN $6::timestamptz ELSE NULL END WHERE event_id=$1 AND target=$2 AND claim_token=$3 AND state='inflight'`, d.Event.ID, d.Target, d.Token, state, category, next)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	if d.RetryAfter > 0 && tag.RowsAffected() == 1 {
		if _, err = tx.Exec(ctx, `UPDATE billionaire.notification_target_cooldowns SET until_at=GREATEST(until_at,$2) WHERE target=$1`, d.Target, next); err != nil {
			return safeError(ctx, ErrUnavailable)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	return nil
}

// ObserveFreshness persists only readiness transitions, including timer observations.
func (s *Store) ObserveFreshness(ctx context.Context, key, instrument, timeframe string, ready bool, at time.Time, reason string) error {
	if key == "" || at.IsZero() {
		return errors.New("invalid freshness observation")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `INSERT INTO billionaire.notification_freshness(key,ready,sequence,observed_at) VALUES($1,$2,0,$3) ON CONFLICT DO NOTHING`, key, ready, at); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	var sequence int64
	var priorReady bool
	var priorAt time.Time
	if err = tx.QueryRow(ctx, `SELECT ready,sequence,observed_at FROM billionaire.notification_freshness WHERE key=$1 FOR UPDATE`, key).Scan(&priorReady, &sequence, &priorAt); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	if at.Before(priorAt) {
		return nil
	}
	changed := sequence == 0 || priorReady != ready
	if changed {
		sequence++
	}
	if _, err = tx.Exec(ctx, `UPDATE billionaire.notification_freshness SET ready=$2,sequence=$3,observed_at=$4 WHERE key=$1`, key, ready, sequence, at); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	if !changed {
		if err := tx.Commit(ctx); err != nil {
			return safeError(ctx, ErrUnavailable)
		}
		return nil
	}
	// First ready observation establishes a baseline without announcing recovery.
	if sequence > 1 || !ready {
		kind := "data_stale"
		if ready {
			kind = "data_recovered"
		}
		err = enqueueNotification(ctx, tx, notify.Event{ID: fmt.Sprintf("freshness:%s:%d", key, sequence), Kind: kind, CorrelationID: key, Instrument: instrument, Timeframe: timeframe, At: at, Reason: reason}, notify.Policy{})
		if err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	return nil
}
func (s *Store) RecordEngineFailure(ctx context.Context, id string, at time.Time, reason string) error {
	return s.RecordNotification(ctx, notify.Event{ID: "engine:" + id, Kind: "engine_failure", Level: "error", CorrelationID: id, At: at, Reason: reason}, notify.Policy{})
}

// SetNotificationPolicy captures target selection for subsequently committed events.
func (s *Store) SetNotificationPolicy(ctx context.Context, p notify.Policy) error {
	if p.Cooldown < 0 || p.Cooldown > 24*time.Hour || len(p.Targets) > 3 {
		return errors.New("invalid notification policy")
	}
	if len(p.Targets) == 0 {
		p.Targets = []string{"terminal"}
	}
	for _, v := range p.Targets {
		if v != "terminal" && v != "desktop" && v != "telegram" {
			return errors.New("invalid notification target")
		}
	}
	data, _ := json.Marshal(p.Targets)
	_, err := s.pool.Exec(ctx, `UPDATE billionaire.notification_policy SET targets=$1,cooldown_seconds=$2 WHERE id=true`, data, int(p.Cooldown.Seconds()))
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	return nil
}
