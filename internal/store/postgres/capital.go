package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
)

// AdjustCapital runs on Control's child owner so cash, ledger, financial outbox
// and the immutable control receipt share the pinned session and transaction.
func (o *PaperOwner) AdjustCapital(ctx context.Context, id string, r paper.CapitalAdjustment, staleAfter time.Duration) (paper.Account, error) {
	amount, err := r.Validate()
	if err != nil {
		return paper.Account{}, err
	}
	if !controlID.MatchString(id) {
		return paper.Account{}, errors.New("invalid capital request ID")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return paper.Account{}, ErrUnavailable
	}
	if o.conn.transaction == nil {
		return paper.Account{}, errors.New("capital adjustments require the atomic control receipt boundary")
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return paper.Account{}, ErrUnavailable
	}
	defer rollback(tx)
	a, err := scanPaperAccount(tx.QueryRow(ctx, "SELECT id,name,currency,balance::text,created_at FROM billionaire.paper_accounts WHERE id=$1 FOR UPDATE", r.AccountID))
	if err != nil {
		return a, errors.New("paper account unavailable")
	}
	if r.Direction == "withdraw" {
		rows, e := tx.Query(ctx, "SELECT payload FROM billionaire.paper_positions WHERE account_id=$1", a.ID)
		if e != nil {
			return a, ErrUnavailable
		}
		positions, e := decodePaperRows[paper.Position](rows)
		if e != nil {
			return a, ErrSchema
		}
		marks, e := positionMarks(ctx, tx, positions, nil)
		if e != nil {
			return a, ErrUnavailable
		}
		// The request timestamp is audit metadata, not permission to reuse stale marks.
		v := paper.ValueAccount(a.Balance, positions, marks, time.Now().UTC(), staleAfter)
		if v.FreeMargin == nil {
			return a, errors.New("withdrawal requires fresh ready position marks")
		}
		if amount.GreaterThan(a.Balance) || amount.GreaterThan(*v.FreeMargin) {
			return a, errors.New("withdrawal exceeds available funds or free margin")
		}
		amount = amount.Neg()
	}
	next := a.Balance.Add(amount)
	maximum, _ := domain.ParseDecimal("1000000000000000000")
	if next.IsNegative() || next.GreaterThan(maximum) {
		return a, errors.New("capital adjustment would produce an invalid balance")
	}
	if _, err = tx.Exec(ctx, "UPDATE billionaire.paper_accounts SET balance=$2 WHERE id=$1", a.ID, next.String()); err != nil {
		return a, ErrUnavailable
	}
	kind := "capital_" + r.Direction
	if _, err = tx.Exec(ctx, "INSERT INTO billionaire.paper_ledger_entries(id,account_id,kind,amount,reason,request_id,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7)", "capital:"+id, a.ID, kind, amount.String(), r.Reason, id, r.At.UTC()); err != nil {
		return a, ErrUnavailable
	}
	a.Balance = next
	raw, err := json.Marshal(struct {
		RequestID string                  `json:"request_id"`
		Request   paper.CapitalAdjustment `json:"request"`
		Account   paper.Account           `json:"account"`
	}{id, r, a})
	if err != nil {
		return a, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO billionaire.journal_outbox(event_id,kind,payload) VALUES($1,$2,$3)", "capital:"+id, kind, raw); err != nil {
		return a, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return a, ErrUnavailable
	}
	return a, nil
}
