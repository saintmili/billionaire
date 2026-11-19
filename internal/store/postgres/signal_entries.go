package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/notify"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

// DecideSignal atomically records an entry or explicit exit decision and, when authorized, its order.
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
	signal, err := decodePaperRow[strategy.Signal](tx.QueryRow(ctx, "SELECT payload FROM billionaire.strategy_signals WHERE id=$1 FOR UPDATE", request.SignalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errors.New("signal not found")
	}
	if err != nil {
		return d, safeError(ctx, ErrUnavailable)
	}
	if info.Instrument != signal.Instrument {
		return d, errors.New("signal instrument does not match metadata")
	}
	previous, err := decodePaperRow[paper.SignalDecision](tx.QueryRow(ctx, "SELECT payload FROM billionaire.signal_decisions WHERE signal_id=$1", signal.ID))
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
			order, e := decodePaperRow[paper.Order](tx.QueryRow(ctx, "SELECT payload FROM billionaire.paper_orders WHERE id=$1", previous.Order.ID))
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
	instance, err := decodePaperRow[strategy.Instance](tx.QueryRow(ctx, "SELECT binding FROM billionaire.strategy_instances WHERE id=$1", signal.InstanceID))
	if err != nil {
		return d, safeError(ctx, ErrUnavailable)
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT enabled FROM billionaire.strategy_instances WHERE id=$1 FOR SHARE", signal.InstanceID).Scan(&active); err != nil {
		return d, safeError(ctx, ErrUnavailable)
	}
	switch {
	case (signal.Kind != "entry" && signal.Kind != "exit") || signal.Action == strategy.AlertOnly:
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
		r := paper.OrderRequest{Liquidation: request.Liquidation, RequestID: "signal-" + signal.ID, SignalID: signal.ID, StrategyID: signal.StrategyID, AccountID: instance.Account, Instrument: signal.Instrument, Side: string(signal.Direction), Action: "open", At: request.At, Quantity: request.Quantity, Leverage: request.Leverage, FeeRate: request.FeeRate, SlippageBPS: request.SlippageBPS}
		if signal.Kind == "exit" {
			position, reason, e := matchingExitPosition(ctx, tx, signal, instance.Account)
			if e != nil {
				return d, e
			}
			if reason != "" {
				d.Status, d.Reason = "rejected", reason
				break
			}
			// Exit sizing never uses an entry-risk calculation. The default closes
			// current remaining exposure; an explicit quantity requests a partial close.
			r.Action, r.PositionID = "close", position.ID
			r.Side, r.Leverage = position.Direction(), position.EffectiveLeverage()
			if r.Quantity == "risk" {
				r.Quantity = position.RemainingQuantity.String()
			}
			if request.Protection != nil || request.Liquidation != nil {
				return d, errors.New("exit approval does not accept protection or liquidation overrides")
			}
			r.Liquidation = nil
		}
		if signal.Stop != nil {
			r.StopPrice = signal.Stop.String()
		}
		if signal.Target != nil {
			r.TargetPrice = signal.Target.String()
		}
		if request.Protection != nil {
			c := *request.Protection
			if c.StopPrice != "" {
				r.StopPrice = c.StopPrice
			}
			if c.TargetPrice != "" {
				r.TargetPrice = c.TargetPrice
			}
			r.TargetQuantity = c.TargetQuantity
			r.TrailActivation = c.TrailActivation
			r.TrailDistance = c.TrailDistance
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
	if _, err := tx.Exec(ctx, `INSERT INTO billionaire.signal_decisions(signal_id,status,order_id,payload) VALUES($1,$2,$3,$4) ON CONFLICT(signal_id) DO UPDATE SET status=EXCLUDED.status,order_id=EXCLUDED.order_id,payload=EXCLUDED.payload`, d.SignalID, d.Status, orderID, payload); err != nil {
		return d, safeError(ctx, ErrUnavailable)
	}
	if d.Status == "rejected" && d.Order == nil {
		e := signalNotification(signal)
		e.ID = "decision:" + d.SignalID
		e.Kind = "order_rejected"
		e.At = request.At
		e.Reason = d.Reason
		if err := enqueueNotification(ctx, tx, e, notify.Policy{}); err != nil {
			return d, err
		}
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
	rows, err := s.pool.Query(ctx, `SELECT s.payload FROM billionaire.strategy_signals s LEFT JOIN billionaire.signal_decisions d ON d.signal_id=s.id WHERE d.signal_id IS NULL AND s.payload->'instrument'=$1::jsonb ORDER BY s.observed_at,s.id LIMIT $2`, jsonInstrument(info.Instrument), limit)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	result, err := decodePaperRows[strategy.Signal](rows)
	if err != nil {
		return nil, safeError(ctx, ErrSchema)
	}
	return result, nil
}

// SignalDecisions returns a bounded interactive listing. Backtest collection
// reads the complete isolated history through signalDecisions instead.
func (s *Store) SignalDecisions(ctx context.Context) ([]paper.SignalDecision, error) {
	return s.signalDecisions(ctx, true)
}

func (s *Store) signalDecisions(ctx context.Context, bounded bool) ([]paper.SignalDecision, error) {
	query := `SELECT d.payload,o.payload FROM billionaire.signal_decisions d LEFT JOIN billionaire.paper_orders o ON o.id=d.order_id ORDER BY d.signal_id`
	if bounded {
		query += " LIMIT 10000"
	}
	rows, err := s.pool.Query(ctx, query)
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

// matchingExitPosition proves ownership from the opening fill/order/signal chain.
// Legacy/manual positions without this provenance are deliberately ineligible.
// The decision schema supports one close intent: pyramided exposure is rejected
// explicitly rather than choosing an arbitrary position or silently closing a subset.
func matchingExitPosition(ctx context.Context, tx pgx.Tx, signal strategy.Signal, account string) (paper.Position, string, error) {
	rows, err := tx.Query(ctx, `SELECT p.payload FROM billionaire.paper_positions p
 JOIN billionaire.paper_fills f ON f.position_id=p.id
 JOIN billionaire.paper_orders o ON o.id=f.order_id
 JOIN billionaire.strategy_signals s ON s.id=o.payload->'request'->>'signal_id'
 WHERE p.account_id=$1 AND p.status='open'
 AND p.payload->'instrument'=$2::jsonb
 AND p.payload->>'strategy_id'=$3
 AND o.status='filled' AND o.payload->'request'->>'action'='open'
 AND s.payload->>'instance_id'=$4 AND s.payload->>'version_id'=$5
 AND s.payload->>'strategy_id'=$3 AND s.payload->'instrument'=$2::jsonb
 AND s.payload->>'kind'='entry' LIMIT 2`, account, jsonInstrument(signal.Instrument), signal.StrategyID, signal.InstanceID, signal.VersionID)
	if err != nil {
		return paper.Position{}, "", safeError(ctx, ErrUnavailable)
	}
	positions, err := decodePaperRows[paper.Position](rows)
	if err != nil {
		return paper.Position{}, "", safeError(ctx, ErrSchema)
	}
	if len(positions) == 0 {
		return paper.Position{}, "no matching strategy exposure", nil
	}
	if len(positions) > 1 {
		return paper.Position{}, "multiple matching positions unsupported; close explicitly by position", nil
	}
	if positions[0].Direction() != string(signal.Direction) {
		return paper.Position{}, "exit direction does not match strategy exposure", nil
	}
	return positions[0], "", nil
}
