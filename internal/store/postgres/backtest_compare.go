package postgres

import (
	"context"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
)

// BacktestSnapshot reads saved artifacts only, including previously saved signal
// rules. It never generates a signal report or accesses an execution schema.
func (s *Store) BacktestSnapshot(ctx context.Context, id string) (backtest.Snapshot, error) {
	r, err := s.BacktestReport(ctx, id)
	if err != nil {
		return backtest.Snapshot{}, err
	}
	out := backtest.Snapshot{Report: r, EvidenceStatus: "unavailable", SignalStatus: "not_evaluated/no_saved_rule", SignalReports: []analytics.SignalReport{}, Warnings: append([]string{}, r.Warnings...)}
	rows, err := s.pool.Query(ctx, "SELECT report FROM millionaire.signal_reports WHERE run_id=$1 ORDER BY id", id)
	if err != nil {
		return out, safeError(ctx, ErrUnavailable)
	}
	out.SignalReports, err = decodePaperRows[analytics.SignalReport](rows)
	if err != nil {
		return out, ErrSchema
	}
	if len(out.SignalReports) > 0 {
		out.SignalStatus = "saved_rules; each rule/filter/observation clock is independent"
	}
	if r.Status != "completed" {
		out.EvidenceStatus = "run_" + r.Status
		out.Warnings = append(out.Warnings, "Performance unavailable for a non-completed run; partial artifacts are audit evidence only")
		return out, nil
	}
	in, err := s.BacktestInputs(ctx, id)
	if err != nil {
		out.Warnings = append(out.Warnings, "Frozen input evidence unavailable or invalid; retained report remains readable")
		return out, nil
	}
	out.Version = &in.Version
	metrics, err := s.BacktestTrades(ctx, id, analytics.Filter{})
	if err != nil {
		return out, err
	}
	out.Metrics = &metrics
	out.EvidenceStatus = "verified"
	reject, pending, ambiguous := 0, 0, 0
	for _, d := range r.Decisions {
		if d.Status == "rejected" {
			reject++
		}
	}
	for _, o := range r.Orders {
		if o.Status == "pending" {
			pending++
		}
	}
	for _, f := range r.Fills {
		if f.Ambiguous {
			ambiguous++
		}
	}
	out.RejectedDecisions = &reject
	out.PendingOrders = &pending
	out.AmbiguousFills = &ambiguous
	if !r.CandidatesAvailable {
		out.Warnings = append(out.Warnings, "Suppressed candidate evidence unavailable")
	}
	if r.Config.Instrument.Instrument.Market == domain.Linear {
		out.Warnings = append(out.Warnings, "Linear funding coverage and approximate liquidation assumptions are retained in report/config; absent legacy fields are unavailable evidence")
	}
	return out, nil
}
func (s *Store) CompareBacktests(ctx context.Context, left, right string) (backtest.Comparison, error) {
	a, e := s.BacktestSnapshot(ctx, left)
	if e != nil {
		return backtest.Comparison{}, e
	}
	b, e := s.BacktestSnapshot(ctx, right)
	if e != nil {
		return backtest.Comparison{}, e
	}
	return backtest.Compare(a, b), nil
}

type StrategyReportFilter struct {
	RunIDs          []string           `json:"run_ids"`
	AccountID       string             `json:"account_id"`
	StrategyVersion string             `json:"strategy_version"`
	Instrument      *domain.Instrument `json:"instrument"`
	Timeframe       domain.Timeframe   `json:"timeframe"`
	Range           backtest.Period    `json:"range"`
}

// BacktestStrategies returns separate run snapshots, never pooled strategy
// metrics. Dates select runs whose original ranges overlap [from,to).
func (s *Store) BacktestStrategies(ctx context.Context, f StrategyReportFilter) ([]backtest.Snapshot, error) {
	if err := (analytics.Filter{From: f.Range.From, To: f.Range.To, Instrument: f.Instrument}).Validate(); err != nil {
		return nil, err
	}
	if f.Timeframe != "" {
		if _, err := domain.ParseTimeframe(string(f.Timeframe)); err != nil {
			return nil, err
		}
	}
	ids := f.RunIDs
	if len(ids) == 0 {
		rows, e := s.pool.Query(ctx, "SELECT id FROM millionaire.backtest_runs ORDER BY created_at,id")
		if e != nil {
			return nil, safeError(ctx, ErrUnavailable)
		}
		for rows.Next() {
			var id string
			if rows.Scan(&id) != nil {
				rows.Close()
				return nil, ErrSchema
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, safeError(ctx, ErrUnavailable)
		}
	}
	result := []backtest.Snapshot{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		snap, e := s.BacktestSnapshot(ctx, id)
		if e != nil {
			return nil, e
		}
		r := snap.Report
		c := r.Config
		if f.AccountID != "" && r.AccountID != f.AccountID || f.StrategyVersion != "" && c.VersionID != f.StrategyVersion || f.Instrument != nil && c.Instrument.Instrument != *f.Instrument {
			continue
		}
		if !f.Range.From.IsZero() && !c.To.After(f.Range.From) || !f.Range.To.IsZero() && !c.From.Before(f.Range.To) {
			continue
		}
		if f.Timeframe != "" {
			found := false
			for _, tf := range c.Timeframes {
				if tf == f.Timeframe {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		result = append(result, snap)
	}
	return result, nil
}
