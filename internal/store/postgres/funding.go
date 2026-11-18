package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
)

// ApplyFunding consumes an explicit interval before later price observations.
// The owner lock, financial ledger and outcome commit together, including gaps.
func (o *PaperOwner) ApplyFunding(ctx context.Context, e paper.FundingEvent) (paper.FundingResult, error) {
	return o.applyFunding(ctx, e, false)
}

// ApplyReplayFunding permits the funding point after a completed interval phase
// in a private replay namespace, preserving forward strict ordering.
func (o *PaperOwner) ApplyReplayFunding(ctx context.Context, e paper.FundingEvent) (paper.FundingResult, error) {
	if o.namespace == "" {
		return paper.FundingResult{}, errors.New("replay funding requires an isolated backtest execution context")
	}
	return o.applyFunding(ctx, e, true)
}
func (o *PaperOwner) applyFunding(ctx context.Context, e paper.FundingEvent, replay bool) (paper.FundingResult, error) {
	result := paper.FundingResult{Event: e, Applications: []paper.FundingApplication{}, Orders: []paper.Order{}}
	if err := e.Validate(); err != nil {
		return result, err
	}
	e.EffectiveAt = e.EffectiveAt.UTC()
	e.AvailableAt = e.AvailableAt.UTC()
	result.Event = e
	payload, _ := json.Marshal(e)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return result, errors.New("paper owner is closed")
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return result, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	var prior, recorded []byte
	err = tx.QueryRow(ctx, `SELECT payload,result FROM billionaire.paper_funding_events WHERE id=$1`, e.ID).Scan(&prior, &recorded)
	if err == nil {
		var old paper.FundingEvent
		var replay paper.FundingResult
		if json.Unmarshal(prior, &old) != nil || json.Unmarshal(recorded, &replay) != nil {
			return result, ErrSchema
		}
		canonical, _ := json.Marshal(old)
		if string(canonical) != string(payload) {
			return result, errors.New("funding identity conflicts with recorded inputs")
		}
		return replay, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, safeError(ctx, ErrUnavailable)
	}
	key := instrumentKey(e.Instrument)
	last, err := decodePaperRow[paper.Observation](tx.QueryRow(ctx, `SELECT payload FROM billionaire.paper_price_events WHERE instrument_key=$1 AND accepted ORDER BY sequence DESC LIMIT 1`, key))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, safeError(ctx, ErrUnavailable)
	}
	if err == nil && !e.EffectiveAt.After(last.AvailableAt) && !(replay && last.Phase == "completed_interval" && last.Bar != nil && last.Bar.End.Equal(e.EffectiveAt) && last.AvailableAt.Equal(e.EffectiveAt)) {
		return result, errors.New("funding must precede price observations at or after its effective time; retroactive funding is unsupported")
	}
	previous, err := decodePaperRow[paper.FundingEvent](tx.QueryRow(ctx, `SELECT payload FROM billionaire.paper_funding_events WHERE instrument_key=$1 ORDER BY effective_at DESC LIMIT 1`, key))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, safeError(ctx, ErrUnavailable)
	}
	if err == nil && (!e.EffectiveAt.After(previous.EffectiveAt) || e.AvailableAt.Before(previous.AvailableAt)) {
		return result, errors.New("funding intervals must have increasing effective and availability clocks")
	}
	if e.Rate == nil {
		result.Warning = "missing funding data: " + e.MissingReason + "; results are approximate"
	} else {
		rows, err := tx.Query(ctx, `SELECT payload FROM billionaire.paper_positions WHERE status='open' ORDER BY id FOR UPDATE`)
		if err != nil {
			return result, safeError(ctx, ErrUnavailable)
		}
		positions, err := decodePaperRows[paper.Position](rows)
		if err != nil {
			return result, safeError(ctx, ErrUnavailable)
		}
		for _, p := range positions {
			if p.Instrument != e.Instrument || !e.EffectiveAt.After(p.OpenedAt) {
				continue
			}
			if p.Protection != nil && (!e.EffectiveAt.After(p.Protection.UpdatedAt) && !(replay && e.EffectiveAt.Equal(p.Protection.UpdatedAt))) {
				return result, errors.New("funding cannot precede an existing position adjustment or exit-management clock")
			}
			if p.OpenedAt.IsZero() {
				return result, errors.New("legacy position lacks funding eligibility clock")
			}
			payment, err := paper.FundingPayment(p, *e.Rate, e.Price)
			if err != nil {
				return result, safeError(ctx, ErrUnavailable)
			}
			actual := payment
			deficit := domain.Decimal{}
			if p.ReservedMargin.Add(payment).IsNegative() {
				deficit = p.ReservedMargin.Add(payment).Neg()
				actual = p.ReservedMargin.Neg()
			}
			p.ReservedMargin = p.ReservedMargin.Add(actual)
			p.FundingPnL = p.FundingPnL.Add(actual)
			p.RealizedPnL = p.RealizedPnL.Add(actual)
			movements := []paper.Movement{{Kind: "funding", Amount: payment}, {Kind: "funding_collateral_transfer", Amount: actual.Neg()}}
			if deficit.IsPositive() {
				movements = append(movements, paper.Movement{Kind: "isolated_deficit", Amount: deficit})
			}
			for _, m := range movements {
				_, err = tx.Exec(ctx, `INSERT INTO billionaire.paper_ledger_entries(id,account_id,kind,amount) VALUES($1,$2,$3,$4)`, paper.Identity("funding", e.ID+":"+p.ID+":"+m.Kind), p.AccountID, m.Kind, m.Amount.String())
				if err != nil {
					return result, safeError(ctx, ErrUnavailable)
				}
			}
			if err = saveProtectedPosition(ctx, tx, p); err != nil {
				return result, safeError(ctx, ErrUnavailable)
			}
			application := paper.FundingApplication{PositionID: p.ID, Payment: payment, CollateralDelta: actual, IsolatedDeficit: deficit}
			result.Applications = append(result.Applications, application)
			outbox, _ := json.Marshal(struct {
				Event       paper.FundingEvent       `json:"event"`
				Application paper.FundingApplication `json:"application"`
				Position    paper.Position           `json:"position"`
			}{e, application, p})
			if _, err = tx.Exec(ctx, `INSERT INTO billionaire.journal_outbox(event_id,kind,payload) VALUES($1,'paper_funding',$2)`, paper.Identity("funding", e.ID+":"+p.ID), outbox); err != nil {
				return result, safeError(ctx, ErrUnavailable)
			}
		}
		// Funding changes collateral before liquidation at its exact public valuation.
		observation := paper.Observation{ID: paper.Identity("funding-price", e.ID), Instrument: e.Instrument, Source: e.Instrument.Provider, Price: e.Price, ObservedAt: e.EffectiveAt, AvailableAt: e.AvailableAt, Ready: true}
		if replay {
			observation.Phase = "funding_after_interval"
		}
		orders, err := observeProtection(ctx, tx, observation, paper.ExecutionGate{ReplayNextOpen: replay, Now: e.AvailableAt, StaleAfter: e.AvailableAt.Sub(e.EffectiveAt) + time.Nanosecond, Enabled: false})
		if err != nil {
			return result, safeError(ctx, ErrUnavailable)
		}
		result.Orders = append(result.Orders, orders...)
	}
	encoded, _ := json.Marshal(result)
	if _, err = tx.Exec(ctx, `INSERT INTO billionaire.paper_funding_events(id,instrument_key,effective_at,payload,result) VALUES($1,$2,$3,$4,$5)`, e.ID, key, e.EffectiveAt, payload, encoded); err != nil {
		return result, safeError(ctx, ErrUnavailable)
	}
	if err = tx.Commit(ctx); err != nil {
		return result, errors.New("funding outcome unknown; retry identical event")
	}
	return result, nil
}
func (s *Store) PaperFunding(ctx context.Context) ([]paper.FundingResult, error) {
	rows, err := s.pool.Query(ctx, `SELECT result FROM billionaire.paper_funding_events ORDER BY effective_at,id`)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	results, err := decodePaperRows[paper.FundingResult](rows)
	if results == nil {
		results = []paper.FundingResult{}
	}
	if err != nil {
		return nil, safeError(ctx, ErrSchema)
	}
	return results, nil
}
