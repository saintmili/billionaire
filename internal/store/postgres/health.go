package postgres

import (
	"context"
	"github.com/saintmili/billionaire/internal/domain"
	"time"
)

// OperationalCounts is a read-only snapshot. Journal rows are pending M7 work;
// their presence never implies external delivery.
type OperationalCounts struct {
	DatabaseIdentity     string     `json:"database_identity"`
	BacktestsCompleted   int64      `json:"backtests_completed"`
	BacktestsCancelled   int64      `json:"backtests_cancelled"`
	JournalLagSeconds    *float64   `json:"journal_lag_seconds"`
	ActiveInstances      int64      `json:"active_instances"`
	Signals              int64      `json:"signals"`
	OpenPositions        int64      `json:"open_positions"`
	PendingOrders        int64      `json:"pending_orders"`
	BacktestsRunning     int64      `json:"backtests_running"`
	BacktestsFailed      int64      `json:"backtests_failed"`
	NotificationFailures int64      `json:"notification_failures"`
	NotificationsPending int64      `json:"notifications_pending"`
	JournalPending       int64      `json:"journal_pending"`
	JournalOldest        *time.Time `json:"journal_oldest_pending_at"`
}

func (s *Store) OperationalCounts(ctx context.Context) (*OperationalCounts, error) {
	var r OperationalCounts
	err := s.pool.QueryRow(ctx, `SELECT
 md5(current_database() || ':' || (SELECT oid::text FROM pg_database WHERE datname=current_database()) || ':' || COALESCE(inet_server_addr()::text,'local') || ':' || COALESCE(inet_server_port()::text,'local')),
 (SELECT count(*) FROM billionaire.strategy_instances WHERE enabled),
 (SELECT count(*) FROM billionaire.strategy_signals),
 (SELECT count(*) FROM billionaire.paper_positions WHERE status='open'),
 (SELECT count(*) FROM billionaire.paper_orders WHERE status='pending'),
 (SELECT count(*) FROM billionaire.backtest_runs WHERE status='running'),
 (SELECT count(*) FROM billionaire.backtest_runs WHERE status='failed'),
 (SELECT count(*) FROM billionaire.notification_deliveries WHERE state='failed'),
 (SELECT count(*) FROM billionaire.notification_deliveries WHERE state IN ('pending','inflight')),
 (SELECT count(*) FROM billionaire.journal_outbox),
 (SELECT min(created_at) FROM billionaire.journal_outbox),
 (SELECT count(*) FROM billionaire.backtest_runs WHERE status='completed'),
 (SELECT count(*) FROM billionaire.backtest_runs WHERE status='cancelled'),
 (SELECT EXTRACT(EPOCH FROM CURRENT_TIMESTAMP-min(created_at))::float8 FROM billionaire.journal_outbox)`).Scan(&r.DatabaseIdentity, &r.ActiveInstances, &r.Signals, &r.OpenPositions, &r.PendingOrders, &r.BacktestsRunning, &r.BacktestsFailed, &r.NotificationFailures, &r.NotificationsPending, &r.JournalPending, &r.JournalOldest, &r.BacktestsCompleted, &r.BacktestsCancelled, &r.JournalLagSeconds)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return &r, nil
}

// MissingFundingIntervals retains incomplete historical coverage even after a
// later successful settlement; a latest supplied rate cannot repair that gap.
func (s *Store) MissingFundingIntervals(ctx context.Context, i domain.Instrument) (int64, error) {
	var count int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM billionaire.paper_funding_events WHERE instrument_key=$1 AND COALESCE(result->>'warning','')<>''`, instrumentKey(i)).Scan(&count); err != nil {
		return 0, safeError(ctx, ErrUnavailable)
	}
	return count, nil
}
