package paper

import (
	"github.com/saintmili/billionaire/internal/domain"
	"time"
)

// AccountValuation separates cash and reserved collateral from mark-dependent
// values. A nil value means unavailable, never an estimate from entry cost.
type AccountValuation struct {
	AvailableBalance domain.Decimal  `json:"available_balance"`
	ReservedMargin   domain.Decimal  `json:"reserved_margin"`
	RealizedPnL      domain.Decimal  `json:"realized_pnl"`
	UnrealizedPnL    *domain.Decimal `json:"unrealized_pnl"`
	Equity           *domain.Decimal `json:"equity"`
	FreeMargin       *domain.Decimal `json:"free_margin"`
	At               time.Time       `json:"at"`
	Reason           string          `json:"reason,omitempty"`
}

func ValueAccount(balance domain.Decimal, positions []Position, marks map[string]Observation, now time.Time, staleAfter time.Duration) AccountValuation {
	v := AccountValuation{AvailableBalance: balance, At: now.UTC()}
	equity, free, unrealized := balance, balance, domain.Decimal{}
	for _, p := range positions {
		v.RealizedPnL = v.RealizedPnL.Add(p.RealizedPnL)
		if p.Status != "open" {
			continue
		}
		entryFees := p.RemainingCost
		if p.Instrument.Market == domain.Spot {
			entryFees = entryFees.Sub(p.EntryPrice.Mul(p.RemainingQuantity))
		}
		v.RealizedPnL = v.RealizedPnL.Sub(entryFees)
		v.ReservedMargin = v.ReservedMargin.Add(p.ReservedMargin)
		mark, ok := marks[p.ID]
		if !ok || mark.Instrument != p.Instrument || mark.Validate() != nil || !mark.Ready || now.IsZero() || staleAfter <= 0 || mark.AvailableAt.After(now) || now.Sub(mark.ObservedAt) > staleAfter {
			v.Reason = "valuation source stale/unready"
			if !ok {
				v.Reason = "valuation source unavailable"
			}
			continue
		}
		pnl := p.PricePnL(mark.Price, p.RemainingQuantity)
		unrealized = unrealized.Add(pnl)
		if p.Instrument.Market == domain.Linear {
			equity = equity.Add(p.ReservedMargin).Add(pnl)
			free = free.Add(pnl)
		} else {
			equity = equity.Add(mark.Price.Mul(p.RemainingQuantity))
		}
	}
	if v.Reason == "" {
		v.Equity = &equity
		v.FreeMargin = &free
		v.UnrealizedPnL = &unrealized
	}
	return v
}
