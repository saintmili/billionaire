package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
)

// PaperAccountValuation reads cash, positions and marks from one read-only snapshot.
func (s *Store) PaperAccountValuation(ctx context.Context, accountID string, now time.Time, staleAfter time.Duration) (paper.AccountValuation, error) {
	var result paper.AccountValuation
	if now.IsZero() || staleAfter <= 0 {
		return result, errors.New("valuation requires a clock and positive stale threshold")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	var balance string
	if err := tx.QueryRow(ctx, "SELECT balance::text FROM billionaire.paper_accounts WHERE id=$1", accountID).Scan(&balance); err != nil {
		return result, errors.New("paper account unavailable")
	}
	cash, err := domain.ParseDecimal(balance)
	if err != nil {
		return result, ErrSchema
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM billionaire.paper_positions WHERE account_id=$1", accountID)
	if err != nil {
		return result, safeError(ctx, ErrUnavailable)
	}
	positions, err := decodePaperRows[paper.Position](rows)
	if err != nil {
		return result, ErrSchema
	}
	marks, err := positionMarks(ctx, tx, positions, nil)
	if err != nil {
		return result, safeError(ctx, ErrUnavailable)
	}
	result = paper.ValueAccount(cash, positions, marks, now, staleAfter)
	if err := tx.Commit(ctx); err != nil {
		return result, safeError(ctx, ErrUnavailable)
	}
	return result, nil
}

func positionMarks(ctx context.Context, tx pgx.Tx, positions []paper.Position, current *paper.Observation) (map[string]paper.Observation, error) {
	marks := map[string]paper.Observation{}
	for _, p := range positions {
		if p.Status != "open" {
			continue
		}
		if current != nil && current.Instrument == p.Instrument {
			marks[p.ID] = *current
			continue
		}
		last, err := decodePaperRow[paper.Observation](tx.QueryRow(ctx, `SELECT payload FROM billionaire.paper_price_events WHERE instrument_key=$1 AND accepted ORDER BY sequence DESC LIMIT 1`, instrumentKey(p.Instrument)))
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		marks[p.ID] = last
	}
	return marks, nil
}
