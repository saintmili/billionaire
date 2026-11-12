package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
)

// observeProtection runs inside the same owner transaction as pending orders.
// Protection runs first; manual closes subsequently recheck remaining holdings.
func observeProtection(ctx context.Context, tx pgx.Tx, observation paper.Observation, gate paper.ExecutionGate) ([]paper.Order, error) {
	if !observation.Ready || gate.Now.Sub(observation.ObservedAt) > gate.StaleAfter {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT payload FROM millionaire.paper_positions WHERE status='open' ORDER BY id FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	positions, err := decodePaperRows[paper.Position](rows)
	if err != nil {
		return nil, err
	}
	orders := []paper.Order{}
	for _, p := range positions {
		decision, err := paper.EvaluateProtection(p, observation)
		if err != nil {
			return nil, err
		}
		if decision.State.UpdatedAt.IsZero() {
			continue
		}
		// A skipped overlapping bar or old observation leaves the state unchanged.
		if !decision.State.UpdatedAt.After(p.Protection.UpdatedAt) {
			continue
		}
		if decision.Reason == "" {
			changed := decision.State.StopPrice != p.Protection.StopPrice || decision.State.TrailingActive != p.Protection.TrailingActive
			p.Protection = &decision.State
			if err := saveProtectedPosition(ctx, tx, p); err != nil {
				return nil, err
			}
			if changed {
				payload, err := json.Marshal(p)
				if err != nil {
					return nil, err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO millionaire.journal_outbox(event_id,kind,payload) VALUES($1,'paper_protection_adjusted',$2)`, paper.Identity("trailing", p.ID+":"+observation.ID), payload); err != nil {
					return nil, err
				}
			}
			continue
		}
		request := paper.OrderRequest{RequestID: paper.Identity("protect", p.ID+":"+observation.ID), AccountID: p.AccountID, PositionID: p.ID, Action: "close", Instrument: p.Instrument, Side: "long", Leverage: 1, Quantity: decision.Quantity, At: p.Protection.UpdatedAt, FeeRate: p.Protection.FeeRate, SlippageBPS: p.Protection.SlippageBPS}
		order, err := paper.PrepareOrder(request, p.Metadata)
		if err != nil {
			return nil, err
		}
		payload, err := json.Marshal(order)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO millionaire.paper_orders(id,account_id,instrument_key,intent_at,status,payload) VALUES($1,$2,$3,$4,$5,$6)`, order.ID, p.AccountID, instrumentKey(p.Instrument), request.At, order.Status, payload); err != nil {
			return nil, err
		}
		var balanceText string
		if err := tx.QueryRow(ctx, `SELECT balance::text FROM millionaire.paper_accounts WHERE id=$1 FOR UPDATE`, p.AccountID).Scan(&balanceText); err != nil {
			return nil, err
		}
		balance, err := domain.ParseDecimal(balanceText)
		if err != nil {
			return nil, err
		}
		executable := observation
		executable.Bar = nil
		executable.Price = decision.Price
		plan, err := paper.PlanSpotFill(order, executable, gate, balance, &p)
		if err != nil {
			return nil, err
		}
		if plan.Fill != nil {
			plan.Fill.ExitReason = decision.Reason
			plan.Fill.Ambiguous = decision.Ambiguous
			plan.Fill.Gap = decision.Gap
			plan.Position.Protection = &decision.State
		} else {
			// Rejected exits must not consume a partial target or trailing transition.
			plan.Order.Reason = "protective " + decision.Reason + ": " + plan.Order.Reason
		}
		if err := commitSpotPlan(ctx, tx, plan); err != nil {
			return nil, err
		}
		orders = append(orders, plan.Order)
	}
	return orders, nil
}

func saveProtectedPosition(ctx context.Context, tx pgx.Tx, p paper.Position) error {
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE millionaire.paper_positions SET payload=$2 WHERE id=$1`, p.ID, payload)
	return err
}

// AdjustProtection replaces only supplied fields. Empty strings explicitly
// disable a field. Identity and input are durably replayed, even after closure.
func (o *PaperOwner) AdjustProtection(ctx context.Context, r paper.ProtectionAdjustment) (paper.Position, error) {
	var p paper.Position
	if err := r.Validate(); err != nil {
		return p, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return p, errors.New("paper owner is closed")
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return p, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	payload, err := json.Marshal(r)
	if err != nil {
		return p, err
	}
	var previous, result []byte
	err = tx.QueryRow(ctx, `SELECT request,result FROM millionaire.paper_protection_adjustments WHERE id=$1`, r.RequestID).Scan(&previous, &result)
	if err == nil {
		var stored paper.ProtectionAdjustment
		if json.Unmarshal(previous, &stored) != nil || json.Unmarshal(result, &p) != nil {
			return p, ErrSchema
		}
		canonical, _ := json.Marshal(stored)
		if string(canonical) != string(payload) {
			return p, errors.New("adjustment request ID conflicts with recorded inputs")
		}
		return p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return p, safeError(ctx, ErrUnavailable)
	}
	p, err = decodePaperRow[paper.Position](tx.QueryRow(ctx, `SELECT payload FROM millionaire.paper_positions WHERE id=$1 FOR UPDATE`, r.PositionID))
	if err != nil {
		return p, errors.New("paper position unavailable")
	}
	last, err := decodePaperRow[paper.Observation](tx.QueryRow(ctx, `SELECT payload FROM millionaire.paper_price_events WHERE instrument_key=$1 AND accepted ORDER BY sequence DESC LIMIT 1`, instrumentKey(p.Instrument)))
	if err != nil {
		return p, errors.New("a usable reference observation is required before adjusting protection")
	}
	p, err = paper.AdjustProtection(p, r, last)
	if err != nil {
		return p, err
	}
	if err := saveProtectedPosition(ctx, tx, p); err != nil {
		return p, safeError(ctx, ErrUnavailable)
	}
	result, err = json.Marshal(p)
	if err != nil {
		return p, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO millionaire.paper_protection_adjustments(id,position_id,request,result) VALUES($1,$2,$3,$4)`, r.RequestID, p.ID, payload, result); err != nil {
		return p, safeError(ctx, ErrUnavailable)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO millionaire.journal_outbox(event_id,kind,payload) VALUES($1,'paper_protection_adjusted',$2)`, paper.Identity("adjustment", r.RequestID), result); err != nil {
		return p, safeError(ctx, ErrUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return p, safeError(ctx, errors.New("adjustment outcome unknown; retry identical request"))
	}
	return p, nil
}
