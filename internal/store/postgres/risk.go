package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

func (s *Store) KillSwitch(ctx context.Context) (bool, error) {
	var on bool
	err := s.pool.QueryRow(ctx, "SELECT kill_switch FROM millionaire.paper_controls WHERE id").Scan(&on)
	if err != nil {
		return false, safeError(ctx, ErrUnavailable)
	}
	return on, nil
}

// SetKillSwitch serializes with entry transactions, without requiring engine ownership.
func (s *Store) SetKillSwitch(ctx context.Context, on bool) error {
	_, err := s.pool.Exec(ctx, "UPDATE millionaire.paper_controls SET kill_switch=$1 WHERE id", on)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	return nil
}
func (s *Store) RiskPolicy(ctx context.Context, accountID, strategyID string) (paper.RiskPolicy, error) {
	p, err := decodePaperRow[paper.RiskPolicy](s.pool.QueryRow(ctx, "SELECT policy FROM millionaire.paper_risk_policies WHERE account_id=$1 AND strategy_id=$2", accountID, strategyID))
	if errors.Is(err, pgx.ErrNoRows) {
		return paper.RiskPolicy{}, nil
	}
	if err != nil {
		return p, safeError(ctx, ErrUnavailable)
	}
	return p, nil
}
func (s *Store) SetRiskPolicy(ctx context.Context, accountID, strategyID string, p paper.RiskPolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	var id string
	if err := tx.QueryRow(ctx, "SELECT id FROM millionaire.paper_accounts WHERE id=$1 FOR UPDATE", accountID).Scan(&id); err != nil {
		return errors.New("paper account unavailable")
	}
	if strategyID != "" {
		if err := tx.QueryRow(ctx, "SELECT id FROM millionaire.strategy_definitions WHERE id=$1", strategyID).Scan(&id); err != nil {
			return errors.New("strategy definition unavailable")
		}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO millionaire.paper_risk_policies(account_id,strategy_id,policy) VALUES($1,$2,$3) ON CONFLICT(account_id,strategy_id) DO UPDATE SET policy=EXCLUDED.policy`, accountID, strategyID, data); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	return nil
}
func entryAdmission(ctx context.Context, tx pgx.Tx, order paper.Order) (string, error) {
	if order.Request.Action != "open" {
		return "", nil
	}
	var on bool
	if err := tx.QueryRow(ctx, "SELECT kill_switch FROM millionaire.paper_controls WHERE id FOR SHARE").Scan(&on); err != nil {
		return "", err
	}
	if on {
		return "kill switch active", nil
	}
	if order.Request.StrategyID != "" {
		var id string
		if err := tx.QueryRow(ctx, "SELECT id FROM millionaire.strategy_definitions WHERE id=$1", order.Request.StrategyID).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
			return "strategy unavailable", nil
		} else if err != nil {
			return "", err
		}
	}
	if order.Request.SignalID != "" {
		var valid bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM millionaire.strategy_signals s JOIN millionaire.strategy_instances i ON i.id=s.instance_id WHERE s.id=$1 AND s.kind='entry' AND s.direction='long' AND s.payload->>'strategy_id'=$2 AND i.binding->>'account'=$3 AND s.payload->'instrument'=$4::jsonb AND s.payload->>'source_ready'='true' AND i.binding->>'action' IN ('manual_paper','auto_paper'))`, order.Request.SignalID, order.Request.StrategyID, order.Request.AccountID, mustJSON(order.Request.Instrument)).Scan(&valid)
		if err != nil {
			return "", err
		}
		if !valid {
			return "signal unavailable or incompatible with entry", nil
		}
	}
	return "", nil
}
func mustJSON(value any) []byte { data, _ := json.Marshal(value); return data }

func planRiskEntry(ctx context.Context, tx pgx.Tx, order paper.Order, observation paper.Observation, gate paper.ExecutionGate, balance domain.Decimal) (paper.ExecutionGate, string, error) {
	reason, err := entryAdmission(ctx, tx, order)
	if reason != "" || err != nil {
		return gate, reason, err
	}
	rows, err := tx.Query(ctx, `SELECT policy FROM millionaire.paper_risk_policies WHERE account_id=$1 AND (strategy_id='' OR strategy_id=$2) ORDER BY strategy_id`, order.Request.AccountID, order.Request.StrategyID)
	if err != nil {
		return gate, "", err
	}
	policies, err := decodePaperRows[paper.RiskPolicy](rows)
	if err != nil {
		return gate, "", err
	}
	var accountConfigured bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM millionaire.paper_risk_policies WHERE account_id=$1 AND strategy_id='')`, order.Request.AccountID).Scan(&accountConfigured); err != nil {
		return gate, "", err
	}
	if !accountConfigured {
		policies = append([]paper.RiskPolicy{{}}, policies...)
	}
	if order.Request.SignalID != "" {
		signal, e := decodePaperRow[strategy.Signal](tx.QueryRow(ctx, "SELECT payload FROM millionaire.strategy_signals WHERE id=$1", order.Request.SignalID))
		if e != nil {
			return gate, "", e
		}
		policies = append(policies, paper.RiskPolicy{PercentRisk: signal.RiskPercent, Pyramiding: true})
	}
	positionsRows, err := tx.Query(ctx, "SELECT payload FROM millionaire.paper_positions WHERE account_id=$1 AND status='open'", order.Request.AccountID)
	if err != nil {
		return gate, "", err
	}
	positions, err := decodePaperRows[paper.Position](positionsRows)
	if err != nil {
		return gate, "", err
	}
	fillsRows, err := tx.Query(ctx, `SELECT f.payload FROM millionaire.paper_fills f WHERE f.account_id=$1`, order.Request.AccountID)
	if err != nil {
		return gate, "", err
	}
	fills, err := decodePaperRows[paper.Fill](fillsRows)
	if err != nil {
		return gate, "", err
	}
	allPositionsRows, err := tx.Query(ctx, "SELECT payload FROM millionaire.paper_positions WHERE account_id=$1", order.Request.AccountID)
	if err != nil {
		return gate, "", err
	}
	allPositions, err := decodePaperRows[paper.Position](allPositionsRows)
	if err != nil {
		return gate, "", err
	}
	byID := map[string]paper.Position{}
	for _, p := range allPositions {
		byID[p.ID] = p
	}
	equity := balance
	marks := map[string]domain.Decimal{}
	needsMarks := false
	for _, p := range policies {
		needsMarks = needsMarks || p.PercentRisk != "" || p.MarginPercent != "" || p.SymbolExposure != ""
	}
	for _, p := range positions {
		mark := p.EntryPrice
		if p.Instrument == observation.Instrument {
			mark = observation.Price
		} else if needsMarks {
			last, e := decodePaperRow[paper.Observation](tx.QueryRow(ctx, `SELECT payload FROM millionaire.paper_price_events WHERE instrument_key=$1 AND accepted ORDER BY sequence DESC LIMIT 1`, instrumentKey(p.Instrument)))
			if errors.Is(e, pgx.ErrNoRows) {
				return gate, "valuation source unavailable", nil
			}
			if e != nil {
				return gate, "", e
			}
			if !last.Ready || last.AvailableAt.After(gate.Now) || gate.Now.Sub(last.ObservedAt) > gate.StaleAfter {
				return gate, "valuation source stale/unready", nil
			}
			mark = last.Price
		}
		marks[p.ID] = mark.Mul(p.RemainingQuantity)
		equity = equity.Add(marks[p.ID])
	}
	entry := paper.ExecutablePrice(order, observation.Price)
	stop, stopErr := paper.ExactValue(order.Request.StopPrice, true)
	if order.Request.StopPrice != "" && (stopErr != nil || !entry.GreaterThan(stop) || !stop.Mod(order.Metadata.PriceStep).IsZero()) {
		return gate, "invalid entry/stop distance or stop precision", nil
	}
	quantity, _ := paper.ExactValue(order.Request.Quantity, true)
	if order.Request.Quantity == "risk" {
		sized := false
		for _, p := range policies {
			if p.FixedRisk != "" || p.PercentRisk != "" {
				q, why := paper.SizeEntry(p, equity, entry, stop, order.Metadata.QuantityStep)
				if why != "" {
					return gate, why, nil
				}
				if !sized || q.LessThan(quantity) {
					quantity = q
				}
				sized = true
			}
		}
		if !sized {
			return gate, "risk sizing policy unavailable", nil
		}
		gate.RiskQuantity = quantity.String()
	}
	for index, p := range policies {
		scoped := index > 0
		state := paper.RiskState{Equity: equity}
		for _, position := range positions {
			if scoped && position.StrategyID != order.Request.StrategyID {
				continue
			}
			state.Positions++
			state.Exposure = state.Exposure.Add(marks[position.ID])
			if position.Instrument == order.Request.Instrument {
				state.SymbolPositions++
				state.SymbolExposure = state.SymbolExposure.Add(marks[position.ID])
			}
		}
		day := gate.Now.UTC().Truncate(24 * time.Hour)
		for _, f := range fills {
			position := byID[f.PositionID]
			if scoped && position.StrategyID != order.Request.StrategyID {
				continue
			}
			opening := position.ID == paper.Identity("position", f.OrderID)
			if opening && (state.LastEntry.IsZero() || f.ExecutedAt.After(state.LastEntry)) {
				state.LastEntry = f.ExecutedAt
			}
			if f.ExecutedAt.Before(day) || f.ExecutedAt.After(gate.Now) {
				continue
			}
			pnl := f.Fee.Neg()
			if !opening {
				pnl = pnl.Add(f.Notional.Sub(position.EntryPrice.Mul(f.Quantity)))
			}
			state.DailyPnL = state.DailyPnL.Add(pnl)
		}
		if why := paper.EntryRiskReason(p, state, quantity, entry, stop, order.Request.Leverage, gate.Now); why != "" {
			return gate, why, nil
		}
	}
	return gate, "", nil
}
