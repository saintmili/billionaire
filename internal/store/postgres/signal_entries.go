package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

// DecideSignal atomically records a decision and, when authorized, its order.
// Signals remain immutable observations. The signal identity owns exactly one decision.
func (o *PaperOwner) DecideSignal(ctx context.Context, request paper.SignalEntryRequest, info marketdata.InstrumentInfo, enabled bool) (paper.SignalDecision, error) {
	request = request.Normalize()
	d := paper.SignalDecision{SignalID: request.SignalID, Request: request}
	if request.SignalID == "" || request.At.IsZero() {
		return d, errors.New("signal and explicit decision timestamp are required")
	}
	if err := info.Validate(); err != nil {
		return d, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return d, errors.New("paper owner is closed")
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return d, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	signal, err := decodePaperRow[strategy.Signal](tx.QueryRow(ctx, "SELECT payload FROM millionaire.strategy_signals WHERE id=$1 FOR UPDATE", request.SignalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errors.New("signal not found")
	}
	if err != nil {
		return d, safeError(ctx, ErrUnavailable)
	}
	if info.Instrument != signal.Instrument {
		return d, errors.New("signal instrument does not match metadata")
	}
	previous, err := decodePaperRow[paper.SignalDecision](tx.QueryRow(ctx, "SELECT payload FROM millionaire.signal_decisions WHERE signal_id=$1", signal.ID))
	if err == nil && !(previous.Status == "awaiting_approval" && request.Approve) {
		// An exact approval retry returns the current order outcome, including after a lost response.
		if request.Approve && previous.Status != "observed" {
			a, _ := json.Marshal(previous.Request)
			b, _ := json.Marshal(request)
			if string(a) != string(b) {
				return d, errors.New("signal decision already recorded with different approval inputs")
			}
		}
		if previous.Order != nil {
			order, e := decodePaperRow[paper.Order](tx.QueryRow(ctx, "SELECT payload FROM millionaire.paper_orders WHERE id=$1", previous.Order.ID))
			if e != nil {
				return d, safeError(ctx, ErrUnavailable)
			}
			if request.Approve {
				retry := order
				retry.Metadata = info
				if !paper.SameOrder(order, retry) {
					return d, errors.New("signal approval metadata conflicts with recorded order")
				}
			}
			previous.Order = &order
		}
		return previous, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return d, safeError(ctx, ErrUnavailable)
	}
	if request.At.Before(signal.ObservedAt) {
		return d, errors.New("decision precedes signal availability")
	}
	instance, err := decodePaperRow[strategy.Instance](tx.QueryRow(ctx, "SELECT binding FROM millionaire.strategy_instances WHERE id=$1", signal.InstanceID))
	if err != nil {
		return d, safeError(ctx, ErrUnavailable)
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT enabled FROM millionaire.strategy_instances WHERE id=$1 FOR SHARE", signal.InstanceID).Scan(&active); err != nil {
		return d, safeError(ctx, ErrUnavailable)
	}
	switch {
	case signal.Kind != "entry" || signal.Action == strategy.AlertOnly:
		d.Status = "observed"
		d.Reason = "signal is observation only"
	case signal.Action == strategy.ManualPaper && !request.Approve:
		d.Status = "awaiting_approval"
		d.Reason = "explicit paper approval required"
	case request.Approve && signal.Action != strategy.ManualPaper:
		return d, errors.New("only manual_paper signals accept explicit approval")
	case !active || instance.Account == "" || instance.Action != signal.Action:
		d.Status = "rejected"
		d.Reason = "strategy instance disabled or incompatible"
	case !signal.SourceReady:
		d.Status = "rejected"
		d.Reason = "signal source stale/unready"
	default:
		r := paper.OrderRequest{RequestID: "signal-" + signal.ID, SignalID: signal.ID, StrategyID: signal.StrategyID, AccountID: instance.Account, Instrument: signal.Instrument, Side: string(signal.Direction), Action: "open", At: request.At, Quantity: request.Quantity, Leverage: request.Leverage, FeeRate: request.FeeRate, SlippageBPS: request.SlippageBPS}
		if signal.Stop != nil {
			r.StopPrice = signal.Stop.String()
		}
		if signal.Target != nil {
			r.TargetPrice = signal.Target.String()
		}
		order, e := paper.PrepareOrder(r, info)
		if e != nil {
			return d, e
		}
		order, e = submitOrder(ctx, tx, order, enabled)
		if e != nil {
			return d, e
		}
		d.Order = &order
		d.Status = "accepted"
		if order.Status == "rejected" {
			d.Status = "rejected"
			d.Reason = order.Reason
		}
	}
	payload, err := json.Marshal(d)
	if err != nil {
		return d, err
	}
	var orderID *string
	if d.Order != nil {
		orderID = &d.Order.ID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO millionaire.signal_decisions(signal_id,status,order_id,payload) VALUES($1,$2,$3,$4) ON CONFLICT(signal_id) DO UPDATE SET status=EXCLUDED.status,order_id=EXCLUDED.order_id,payload=EXCLUDED.payload`, d.SignalID, d.Status, orderID, payload); err != nil {
		return d, safeError(ctx, ErrUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return d, errors.New("decision commit outcome unknown; retry identical inputs")
	}
	return d, nil
}

// PendingSignalEntries returns a bounded, durable work queue for one instrument.
// Awaiting manual approval is already consumed and remains visible in decisions.
func (s *Store) PendingSignalEntries(ctx context.Context, info marketdata.InstrumentInfo, limit int) ([]strategy.Signal, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("signal consumption limit must be 1-1000")
	}
	rows, err := s.pool.Query(ctx, `SELECT s.payload FROM millionaire.strategy_signals s LEFT JOIN millionaire.signal_decisions d ON d.signal_id=s.id WHERE d.signal_id IS NULL AND s.payload->'instrument'=$1::jsonb ORDER BY s.observed_at,s.id LIMIT $2`, jsonInstrument(info.Instrument), limit)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	result, err := decodePaperRows[strategy.Signal](rows)
	if err != nil {
		return nil, safeError(ctx, ErrSchema)
	}
	return result, nil
}

func (s *Store) SignalDecisions(ctx context.Context) ([]paper.SignalDecision, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.payload,o.payload FROM millionaire.signal_decisions d LEFT JOIN millionaire.paper_orders o ON o.id=d.order_id ORDER BY d.signal_id LIMIT 10000`)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rows.Close()
	result := []paper.SignalDecision{}
	for rows.Next() {
		var data, order []byte
		var d paper.SignalDecision
		if rows.Scan(&data, &order) != nil || json.Unmarshal(data, &d) != nil {
			return nil, ErrSchema
		}
		if order != nil {
			var o paper.Order
			if json.Unmarshal(order, &o) != nil {
				return nil, ErrSchema
			}
			d.Order = &o
		}
		result = append(result, d)
	}
	if rows.Err() != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return result, nil
}
