package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"time"
)

// BacktestSignals uses retained frozen observations only. Generating a report
// stores its explicit hypothetical definition; it never reruns trading.
func (s *Store) BacktestSignals(ctx context.Context, id string, f analytics.SignalFilter, rule analytics.SuccessRule) (analytics.SignalReport, error) {
	saved, err := s.BacktestReport(ctx, id)
	if err != nil {
		return analytics.SignalReport{}, err
	}
	if saved.Status != "completed" {
		return analytics.SignalReport{}, errors.New("signal reporting requires a completed backtest")
	}
	if f.AccountID != "" && f.AccountID != saved.AccountID {
		return analytics.SignalReport{}, errors.New("account filter does not match backtest account")
	}
	frozen, err := s.BacktestInputs(ctx, id)
	if err != nil {
		return analytics.SignalReport{}, err
	}
	if saved.Config.ClockMode == "recorded-close" {
		bars := []domain.Candle{}
		for _, c := range frozen.Candles {
			if !c.ObservedAt.After(saved.Config.To) {
				bars = append(bars, c)
			}
		}
		frozen.Candles = bars
	}
	in := analytics.SignalInput{Scope: "backtest", Signals: saved.Signals, Candidates: saved.Candidates, CandidatesAvailable: saved.CandidatesAvailable, Candles: frozen.Candles, Through: saved.Config.To, Fills: saved.Fills, Orders: saved.Orders}
	r, err := analytics.Signals(in, f, rule)
	if err != nil {
		return r, err
	}
	r.RunID = id
	r.Through = in.Through
	r.EvidenceHash = backtest.Hash(in)
	r.Warnings = append(r.Warnings, saved.Warnings...)
	return s.saveSignalReport(ctx, r)
}
func (s *Store) saveSignalReport(ctx context.Context, r analytics.SignalReport) (analytics.SignalReport, error) {
	r.ID = backtest.Hash(r)
	payload, err := json.Marshal(r)
	if err != nil {
		return r, ErrSchema
	}
	var run any
	if r.RunID != "" {
		run = r.RunID
	}
	if _, err = s.pool.Exec(ctx, "INSERT INTO billionaire.signal_reports(id,scope,run_id,report) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING", r.ID, r.Scope, run, payload); err != nil {
		return r, safeError(ctx, ErrUnavailable)
	}
	return r, nil
}
func (s *Store) SignalReport(ctx context.Context, id string) (analytics.SignalReport, error) {
	var r analytics.SignalReport
	var b []byte
	err := s.pool.QueryRow(ctx, "SELECT report FROM billionaire.signal_reports WHERE id=$1", id).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, errors.New("signal report not found")
	}
	if err != nil {
		return r, safeError(ctx, ErrUnavailable)
	}
	if json.Unmarshal(b, &r) != nil {
		return r, ErrSchema
	}
	return r, nil
}

// PaperSignals captures one consistent forward-paper snapshot and persists the
// rule and resulting evidence hash. Account selection is explicit.
func (s *Store) PaperSignals(ctx context.Context, f analytics.SignalFilter, rule analytics.SuccessRule, through time.Time) (analytics.SignalReport, error) {
	if err := f.Validate(); err != nil {
		return analytics.SignalReport{}, err
	}
	if err := rule.Validate(); err != nil {
		return analytics.SignalReport{}, err
	}
	if f.AccountID == "" || through.IsZero() {
		return analytics.SignalReport{}, errors.New("forward-paper signals require account and observation clock")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return analytics.SignalReport{}, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM billionaire.paper_accounts WHERE id=$1)", f.AccountID).Scan(&exists); err != nil {
		return analytics.SignalReport{}, safeError(ctx, ErrUnavailable)
	}
	if !exists {
		return analytics.SignalReport{}, errors.New("paper account not found")
	}
	rows, err := tx.Query(ctx, "SELECT s.payload FROM billionaire.strategy_signals s JOIN billionaire.strategy_instances i ON i.id=s.instance_id WHERE i.binding->>'account'=$1 AND s.observed_at<=$2 ORDER BY s.observed_at,s.id", f.AccountID, through)
	if err != nil {
		return analytics.SignalReport{}, safeError(ctx, ErrUnavailable)
	}
	signals, err := decodePaperRows[strategy.Signal](rows)
	if err != nil {
		return analytics.SignalReport{}, err
	}
	rows, err = tx.Query(ctx, "SELECT e.result FROM billionaire.strategy_evaluations e JOIN billionaire.strategy_instances i ON i.id=e.instance_id WHERE i.binding->>'account'=$1 AND e.observed_at<=$2 ORDER BY e.observed_at,e.instance_id,e.event_id", f.AccountID, through)
	if err != nil {
		return analytics.SignalReport{}, safeError(ctx, ErrUnavailable)
	}
	evaluations, err := decodePaperRows[strategy.Evaluation](rows)
	if err != nil {
		return analytics.SignalReport{}, err
	}
	available := true
	candidates := []strategy.Candidate{}
	for _, e := range evaluations {
		if e.Candidates == nil {
			available = false
		}
		candidates = append(candidates, e.Candidates...)
	}
	rows, err = tx.Query(ctx, "SELECT payload FROM billionaire.market_candles WHERE end_time<=$1 AND observed_at<=$1 AND source=provider ORDER BY provider,market,instrument_id,timeframe,open_time,revision", through)
	if err != nil {
		return analytics.SignalReport{}, safeError(ctx, ErrUnavailable)
	}
	candles, err := decodePaperRows[domain.Candle](rows)
	if err != nil {
		return analytics.SignalReport{}, err
	}
	rows, err = tx.Query(ctx, "SELECT payload FROM billionaire.paper_orders WHERE account_id=$1 ORDER BY id", f.AccountID)
	if err != nil {
		return analytics.SignalReport{}, safeError(ctx, ErrUnavailable)
	}
	orders, err := decodePaperRows[paper.Order](rows)
	if err != nil {
		return analytics.SignalReport{}, err
	}
	rows, err = tx.Query(ctx, "SELECT payload FROM billionaire.paper_fills WHERE account_id=$1 ORDER BY id", f.AccountID)
	if err != nil {
		return analytics.SignalReport{}, safeError(ctx, ErrUnavailable)
	}
	fills, err := decodePaperRows[paper.Fill](rows)
	if err != nil {
		return analytics.SignalReport{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return analytics.SignalReport{}, safeError(ctx, ErrUnavailable)
	}
	in := analytics.SignalInput{Scope: "forward-paper", Signals: signals, Candidates: candidates, CandidatesAvailable: available, Candles: candles, Through: through, Orders: orders, Fills: fills}
	r, err := analytics.Signals(in, f, rule)
	if err != nil {
		return r, err
	}
	r.Through = through.UTC()
	r.EvidenceHash = backtest.Hash(in)
	r.Warnings = append(r.Warnings, "Forward observations use the retained current candle revisions; report is a frozen retrospective snapshot, not a reconstruction of historical revision availability")
	return s.saveSignalReport(ctx, r)
}
