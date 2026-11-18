package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

// AcquireEngineOwner fences both financial execution and strategy evaluation on
// the same non-reconnecting PostgreSQL session. Database loss fails closed.
func (s *Store) AcquireEngineOwner(ctx context.Context) (*PaperOwner, error) {
	o, err := s.AcquirePaperOwner(ctx)
	if err != nil {
		return nil, err
	}
	var acquired bool
	if err = o.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(7341928603)").Scan(&acquired); err != nil {
		o.Close()
		return nil, safeError(ctx, ErrUnavailable)
	}
	if !acquired {
		o.Close()
		return nil, ErrPaperOwned
	}
	return o, nil
}

func (o *PaperOwner) EvaluateStrategyEvent(ctx context.Context, id string, event marketdata.Event, registry *strategy.Registry) (strategy.Evaluation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return strategy.Evaluation{}, errors.New("engine owner is closed")
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return strategy.Evaluation{}, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	return evaluateStrategyEvent(ctx, tx, id, event, registry)
}

// Check detects a lost owner session even while subscriptions are idle.
func (o *PaperOwner) Check(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return errors.New("engine owner is closed")
	}
	if err := o.conn.Ping(ctx); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	return nil
}

// RecordedPaperObservation preserves the original readiness/availability of an
// event redelivered as recovery, rather than rewriting an immutable observation.
func (s *Store) RecordedPaperObservation(ctx context.Context, observation paper.Observation) (*paper.Observation, error) {
	key := paper.Identity("observation", instrumentKey(observation.Instrument)+":"+observation.ID)
	prior, err := decodePaperRow[paper.Observation](s.pool.QueryRow(ctx, "SELECT payload FROM billionaire.paper_price_events WHERE id=$1", key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return &prior, nil
}

// EngineRunning observes the evaluation fence without changing runtime state.
func (s *Store) EngineRunning(ctx context.Context) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	var available bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock_shared(7341928603)").Scan(&available); err != nil {
		return false, safeError(ctx, ErrUnavailable)
	}
	return !available, nil
}
