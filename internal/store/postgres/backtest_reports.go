package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

// BacktestTrades reads retained results only; it never accesses execution tables
// or reruns trading. Legacy runs explicitly lack equity history.
func (s *Store) BacktestTrades(ctx context.Context, id string, f analytics.Filter) (analytics.Report, error) {
	saved, err := s.BacktestReport(ctx, id)
	if err != nil {
		return analytics.Report{}, err
	}
	if saved.Status != "completed" {
		return analytics.Report{}, errors.New("trade performance requires a completed backtest")
	}
	if f.AccountID != "" && f.AccountID != saved.AccountID {
		return analytics.Report{}, errors.New("account filter does not match backtest account")
	}
	capital, err := domain.ParseDecimal(saved.Config.InitialCapital)
	if err != nil {
		return analytics.Report{}, ErrSchema
	}
	in, err := s.BacktestInputs(ctx, id)
	if err != nil {
		return analytics.Report{}, err
	}
	r, err := analytics.Trades(analytics.Input{CashBalance: &saved.Balance, Candles: in.Candles, Scope: "backtest", Signals: saved.Signals, Positions: saved.Positions, Orders: saved.Orders, Fills: saved.Fills, Ledger: saved.Ledger, Equity: saved.Equity, InitialCapital: &capital}, f)
	if err != nil {
		return r, err
	}
	r.RunID = id
	r.Warnings = append(r.Warnings, saved.Warnings...)
	return r, nil
}

// CaptureBacktestEquity records an actual event-time snapshot inside the isolated
// namespace. Repeated timestamps preserve deterministic close/open phase order.
func (s *Store) CaptureBacktestEquity(ctx context.Context, accountID string, at time.Time) (analytics.EquityPoint, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return analytics.EquityPoint{}, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	point, _, _, err := accountEquity(ctx, tx, accountID, at, 24*time.Hour)
	if err != nil {
		return point, err
	}
	if err = tx.Commit(ctx); err != nil {
		return point, safeError(ctx, ErrUnavailable)
	}
	return point, nil
}
func accountEquity(ctx context.Context, tx pgx.Tx, accountID string, at time.Time, stale time.Duration) (analytics.EquityPoint, []paper.Position, domain.Decimal, error) {
	point := analytics.EquityPoint{At: at.UTC()}
	if at.IsZero() || stale <= 0 {
		return point, nil, domain.Decimal{}, errors.New("valuation requires a clock and positive stale threshold")
	}
	a, err := scanPaperAccount(tx.QueryRow(ctx, "SELECT id,name,currency,balance::text,created_at FROM millionaire.paper_accounts WHERE id=$1", accountID))
	if err != nil {
		return point, nil, domain.Decimal{}, errors.New("paper account unavailable")
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM millionaire.paper_positions WHERE account_id=$1", accountID)
	if err != nil {
		return point, nil, a.Balance, safeError(ctx, ErrUnavailable)
	}
	positions, err := decodePaperRows[paper.Position](rows)
	if err != nil {
		return point, nil, a.Balance, ErrSchema
	}
	marks, err := positionMarks(ctx, tx, positions, nil)
	if err != nil {
		return point, nil, a.Balance, safeError(ctx, ErrUnavailable)
	}
	v := paper.ValueAccount(a.Balance, positions, marks, at, stale)
	point.Equity = v.Equity
	point.Cash = a.Balance
	point.Reason = v.Reason
	if v.Equity != nil {
		exposure := domain.Decimal{}
		for _, p := range positions {
			if p.Status == "open" {
				exposure = exposure.Add(marks[p.ID].Price.Mul(p.RemainingQuantity).Abs())
			}
		}
		point.Exposure = &exposure
		if v.Equity.IsPositive() {
			leverage := exposure.Div(*v.Equity)
			point.Leverage = &leverage
		}
	}
	return point, positions, a.Balance, nil
}

// PaperTrades reads one repeatable-read snapshot and never writes financial state.
// Current valuation is exposed separately from unavailable historical equity.
func (s *Store) PaperTrades(ctx context.Context, f analytics.Filter, at time.Time, stale time.Duration) (analytics.Report, error) {
	if err := f.Validate(); err != nil {
		return analytics.Report{}, err
	}
	if f.AccountID == "" {
		return analytics.Report{}, errors.New("forward-paper trade reporting requires --account")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return analytics.Report{}, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	point, positions, cash, err := accountEquity(ctx, tx, f.AccountID, at, stale)
	if err != nil {
		return analytics.Report{}, err
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM millionaire.paper_orders WHERE account_id=$1", f.AccountID)
	if err != nil {
		return analytics.Report{}, safeError(ctx, ErrUnavailable)
	}
	orders, err := decodePaperRows[paper.Order](rows)
	if err != nil {
		return analytics.Report{}, ErrSchema
	}
	rows, err = tx.Query(ctx, "SELECT payload FROM millionaire.paper_fills WHERE account_id=$1", f.AccountID)
	if err != nil {
		return analytics.Report{}, safeError(ctx, ErrUnavailable)
	}
	fills, err := decodePaperRows[paper.Fill](rows)
	if err != nil {
		return analytics.Report{}, ErrSchema
	}
	rows, err = tx.Query(ctx, "SELECT id,account_id,kind,amount::text,created_at FROM millionaire.paper_ledger_entries WHERE account_id=$1 ORDER BY created_at,id", f.AccountID)
	if err != nil {
		return analytics.Report{}, safeError(ctx, ErrUnavailable)
	}
	ledger := []paper.LedgerEntry{}
	capital := domain.Decimal{}
	for rows.Next() {
		var l paper.LedgerEntry
		var amount string
		if err = rows.Scan(&l.ID, &l.AccountID, &l.Kind, &amount, &l.CreatedAt); err != nil {
			rows.Close()
			return analytics.Report{}, ErrSchema
		}
		l.Amount, err = domain.ParseDecimal(amount)
		if err != nil {
			rows.Close()
			return analytics.Report{}, ErrSchema
		}
		ledger = append(ledger, l)
		if l.Kind == "opening" {
			capital = capital.Add(l.Amount)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return analytics.Report{}, safeError(ctx, ErrUnavailable)
	}
	rows, err = tx.Query(ctx, "SELECT payload FROM millionaire.strategy_signals WHERE id IN (SELECT payload->'request'->>'signal_id' FROM millionaire.paper_orders WHERE account_id=$1)", f.AccountID)
	if err != nil {
		return analytics.Report{}, safeError(ctx, ErrUnavailable)
	}
	signals, err := decodePaperRows[strategy.Signal](rows)
	if err != nil {
		return analytics.Report{}, ErrSchema
	}

	rows, err = tx.Query(ctx, "SELECT payload FROM millionaire.paper_price_events WHERE accepted AND available_at<=$1 AND instrument_key IN (SELECT instrument_key FROM millionaire.paper_orders WHERE account_id=$2) ORDER BY sequence", at, f.AccountID)
	if err != nil {
		return analytics.Report{}, safeError(ctx, ErrUnavailable)
	}
	observations, err := decodePaperRows[paper.Observation](rows)
	if err != nil {
		return analytics.Report{}, ErrSchema
	}
	bars := []domain.Candle{}
	for _, observation := range observations {
		b := observation.Bar
		if b == nil || b.End.After(at) {
			continue
		}
		bars = append(bars, domain.Candle{Instrument: observation.Instrument, OpenTime: b.Start, EndTime: b.End, High: b.High, Low: b.Low, Open: b.Open, Close: b.Close})
	}
	r, err := analytics.Trades(analytics.Input{Candles: bars, CashBalance: &cash, Signals: signals, Scope: "forward-paper", Positions: positions, Orders: orders, Fills: fills, Ledger: ledger, Equity: []analytics.EquityPoint{point}, InitialCapital: &capital}, f)
	if err != nil {
		return r, err
	}
	r.Warnings = append(r.Warnings, "Historical forward-paper equity was not retained; only the current read-time valuation is available")
	if err = tx.Commit(ctx); err != nil {
		return r, safeError(ctx, ErrUnavailable)
	}
	return r, nil
}
