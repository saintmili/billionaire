package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/notify"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

func signalNotification(s strategy.Signal) notify.Event {
	return notify.Event{ID: "signal:" + s.ID, Kind: "signal", CorrelationID: s.ID, StrategyID: s.StrategyID, VersionID: s.VersionID, Instrument: instrumentKey(s.Instrument), Timeframe: string(s.Timeframe), Price: s.ProposedEntry.String(), At: s.ObservedAt, Reason: s.Reason}
}
func orderNotification(ctx context.Context, tx pgx.Tx, o paper.Order, f *paper.Fill) error {
	e := notify.Event{ID: "order:" + o.ID + ":" + o.Status, Kind: "order_rejected", CorrelationID: o.ID, StrategyID: o.Request.StrategyID, Instrument: instrumentKey(o.Request.Instrument), At: o.Request.At, Reason: o.Reason}
	signalID := o.Request.SignalID
	if signalID == "" && o.Request.PositionID != "" {
		err := tx.QueryRow(ctx, `SELECT COALESCE(o.payload->'request'->>'signal_id','') FROM billionaire.paper_fills f JOIN billionaire.paper_orders o ON o.id=f.order_id WHERE f.position_id=$1 AND o.payload->'request'->>'action'='open' ORDER BY o.intent_at LIMIT 1`, o.Request.PositionID).Scan(&signalID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if signalID != "" {
		s, err := decodePaperRow[strategy.Signal](tx.QueryRow(ctx, `SELECT payload FROM billionaire.strategy_signals WHERE id=$1`, signalID))
		if err != nil {
			return err
		}
		e.Price = s.ProposedEntry.String()
		e.VersionID = s.VersionID
		e.StrategyID = s.StrategyID
		e.Timeframe = string(s.Timeframe)
	}
	if f != nil {
		e.ID = "fill:" + f.ID
		e.At = f.ExecutedAt
		e.Price = f.Price.String()
		e.Kind = "order_opened"
		e.Reason = "paper order filled"
		if o.Request.Action == "close" {
			e.Kind = "position_closed"
		}
		if f.ExitReason != "" {
			e.Kind = f.ExitReason
			e.Reason = f.ExitReason
		}
	}
	return enqueueNotification(ctx, tx, e, notify.Policy{})
}
