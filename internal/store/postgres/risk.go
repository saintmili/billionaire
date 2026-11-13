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
		signal, err := decodePaperRow[strategy.Signal](tx.QueryRow(ctx, "SELECT payload FROM millionaire.strategy_signals WHERE id=$1", order.Request.SignalID))
		if errors.Is(err, pgx.ErrNoRows) {
			return "signal unavailable or incompatible with entry", nil
		}
		if err != nil {
			return "", err
		}
		instance, err := decodePaperRow[strategy.Instance](tx.QueryRow(ctx, "SELECT binding FROM millionaire.strategy_instances WHERE id=$1", signal.InstanceID))
		if err != nil {
			return "", err
		}
		if signal.Kind != "entry" || string(signal.Direction) != order.Request.Side || signal.StrategyID != order.Request.StrategyID || instance.Account != order.Request.AccountID || signal.Instrument != order.Request.Instrument || !signal.SourceReady || (instance.Action != strategy.ManualPaper && instance.Action != strategy.AutoPaper) {
			return "signal unavailable or incompatible with entry", nil
		}
		if order.Request.At.Before(signal.ObservedAt) {
			return "intent precedes signal availability", nil
		}
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM millionaire.paper_orders WHERE status='filled' AND payload->'request'->>'signal_id'=$1 AND payload->'request'->>'action'='open')`, signal.ID).Scan(&used); err != nil {
			return "", err
		}
		if used {
			return "signal already filled", nil
		}
	}

	return "", nil
}

type scopedRiskPolicy struct {
	policy         paper.RiskPolicy
	strategyScoped bool
}

func planRiskEntry(ctx context.Context, tx pgx.Tx, order paper.Order, observation paper.Observation, gate paper.ExecutionGate, balance domain.Decimal) (paper.ExecutionGate, string, error) {
	reason, err := entryAdmission(ctx, tx, order)
	if reason != "" || err != nil {
		return gate, reason, err
	}
	rows, err := tx.Query(ctx, `SELECT strategy_id,policy FROM millionaire.paper_risk_policies WHERE account_id=$1 AND (strategy_id='' OR strategy_id=$2) ORDER BY strategy_id`, order.Request.AccountID, order.Request.StrategyID)
	if err != nil {
		return gate, "", err
	}
	policies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (scopedRiskPolicy, error) {
		var scope string
		var data []byte
		var p scopedRiskPolicy
		if err := row.Scan(&scope, &data); err != nil {
			return p, err
		}
		p.strategyScoped = scope != ""
		if err := json.Unmarshal(data, &p.policy); err != nil {
			return p, ErrSchema
		}
		return p, nil
	})
	if err != nil {
		return gate, "", err
	}
	var accountConfigured bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM millionaire.paper_risk_policies WHERE account_id=$1 AND strategy_id='')`, order.Request.AccountID).Scan(&accountConfigured); err != nil {
		return gate, "", err
	}
	if !accountConfigured {
		policies = append([]scopedRiskPolicy{{}}, policies...)
	}
	if order.Request.SignalID != "" {
		signal, e := decodePaperRow[strategy.Signal](tx.QueryRow(ctx, "SELECT payload FROM millionaire.strategy_signals WHERE id=$1", order.Request.SignalID))
		if e != nil {
			return gate, "", e
		}
		policies = append(policies, scopedRiskPolicy{policy: paper.RiskPolicy{PercentRisk: signal.RiskPercent, Pyramiding: true}, strategyScoped: true})
		candleLimited := false
		for _, p := range policies {
			candleLimited = candleLimited || !p.policy.Pyramiding
		}
		if candleLimited {
			var used bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM millionaire.paper_orders o JOIN millionaire.strategy_signals s ON s.id=o.payload->'request'->>'signal_id' WHERE o.account_id=$1 AND o.status='filled' AND o.payload->'request'->>'action'='open' AND s.payload->>'strategy_id'=$2 AND s.payload->'instrument'=$3::jsonb AND s.payload->>'timeframe'=$4 AND s.payload->>'candle_open'=$5)`, order.Request.AccountID, signal.StrategyID, jsonInstrument(signal.Instrument), string(signal.Timeframe), signal.CandleOpen.UTC().Format(time.RFC3339Nano)).Scan(&used)
			if err != nil {
				return gate, "", err
			}
			if used {
				return gate, "entry already filled for signal candle", nil
			}
		}

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
	needsMarks := order.Request.Instrument.Market == domain.Linear
	for _, scoped := range policies {
		p := scoped.policy
		needsMarks = needsMarks || p.PercentRisk != "" || p.MarginPercent != "" || p.SymbolExposure != ""
	}
	for _, p := range positions {
		needsMarks = needsMarks || p.Instrument.Market == domain.Linear
	}
	observations, err := positionMarks(ctx, tx, positions, &observation)
	if err != nil {
		return gate, "", err
	}
	if needsMarks {
		valuation := paper.ValueAccount(balance, positions, observations, gate.Now, gate.StaleAfter)
		if valuation.Reason != "" {
			return gate, valuation.Reason, nil
		}
		equity = *valuation.Equity
		if !equity.IsPositive() || !valuation.FreeMargin.IsPositive() {
			return gate, "insufficient equity/free margin", nil
		}
	}
	for _, p := range positions {
		mark := p.EntryPrice
		if o, ok := observations[p.ID]; ok {
			mark = o.Price
		}
		marks[p.ID] = mark.Mul(p.RemainingQuantity)
		if !needsMarks {
			equity = equity.Add(marks[p.ID])
		}
	}
	entry := paper.ExecutablePrice(order, observation.Price)
	stop, stopErr := paper.ExactValue(order.Request.StopPrice, true)
	if order.Request.StopPrice != "" && (stopErr != nil || ((order.Request.Side == "long" && !entry.GreaterThan(stop)) || (order.Request.Side == "short" && !stop.GreaterThan(entry))) || !stop.Mod(order.Metadata.PriceStep).IsZero()) {
		return gate, "invalid entry/stop distance or stop precision", nil
	}
	quantity, _ := paper.ExactValue(order.Request.Quantity, true)
	if order.Request.Quantity == "risk" {
		sized := false
		for _, scoped := range policies {
			p := scoped.policy
			if p.FixedRisk != "" || p.PercentRisk != "" {
				q, why := paper.SizeDirectionalEntry(p, equity, entry, stop, order.Metadata.QuantityStep, order.Request.Side)
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
	if needsMarks {
		valuation := paper.ValueAccount(balance, positions, observations, gate.Now, gate.StaleAfter)
		capital := entry.Mul(quantity).RoundCeil(18)
		if order.Request.Instrument.Market == domain.Linear {
			capital = paper.RequiredMargin(capital, order.Request.Leverage)
		}
		feeRate, _ := paper.ExactValue(order.Request.FeeRate, false)
		required := capital.Add(entry.Mul(quantity).Mul(feeRate).RoundCeil(18))
		if required.GreaterThan(*valuation.FreeMargin) {
			return gate, "insufficient free margin", nil
		}
	}
	for _, policy := range policies {
		p := policy.policy
		strategyScoped := policy.strategyScoped
		state := paper.RiskState{Equity: equity, Side: order.Request.Side}
		for _, position := range positions {
			if strategyScoped && position.StrategyID != order.Request.StrategyID {
				continue
			}
			state.Positions++
			if position.Instrument.Market == domain.Linear {
				state.Exposure = state.Exposure.Add(position.ReservedMargin)
			} else {
				state.Exposure = state.Exposure.Add(marks[position.ID])
			}
			if position.Instrument == order.Request.Instrument {
				state.SymbolPositions++
				state.SymbolExposure = state.SymbolExposure.Add(marks[position.ID])
			}
		}
		day := gate.Now.UTC().Truncate(24 * time.Hour)
		for _, f := range fills {
			position := byID[f.PositionID]
			if strategyScoped && position.StrategyID != order.Request.StrategyID {
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
				pnl = pnl.Add(position.PricePnL(f.Price, f.Quantity).RoundFloor(18))
			}
			state.DailyPnL = state.DailyPnL.Add(pnl)
		}
		if why := paper.EntryRiskReason(p, state, quantity, entry, stop, order.Request.Leverage, gate.Now); why != "" {
			return gate, why, nil
		}
	}
	return gate, "", nil
}

func jsonInstrument(instrument domain.Instrument) []byte {
	data, _ := json.Marshal(instrument)
	return data
}
