package paper

import (
	"errors"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
)

// RiskPolicy limits are optional; account and strategy policies are both enforced.
// Monetary limits are USDT; percent fields use 0..100, cooldown uses seconds.
type RiskPolicy struct {
	FixedRisk       string `json:"fixed_risk,omitempty"`
	PercentRisk     string `json:"percent_risk,omitempty"`
	DailyLoss       string `json:"daily_loss,omitempty"`
	MaxPositions    int    `json:"max_positions,omitempty"`
	SymbolExposure  string `json:"symbol_exposure,omitempty"`
	MaxLeverage     int    `json:"max_leverage,omitempty"`
	MarginPercent   string `json:"margin_percent,omitempty"`
	CooldownSeconds int    `json:"cooldown_seconds,omitempty"`
	Pyramiding      bool   `json:"pyramiding"`
}

func (p RiskPolicy) Validate() error {
	if p.MaxPositions < 0 || p.MaxLeverage < 0 || p.CooldownSeconds < 0 || int64(p.CooldownSeconds) > int64((1<<63-1)/int64(time.Second)) {
		return errors.New("risk integer limits must be nonnegative")
	}
	for _, s := range []string{p.FixedRisk, p.PercentRisk, p.DailyLoss, p.SymbolExposure, p.MarginPercent} {
		if s != "" {
			if _, err := ExactValue(s, true); err != nil {
				return errors.New("risk limits must be positive exact decimals")
			}
		}
	}
	for _, s := range []string{p.PercentRisk, p.MarginPercent} {
		if s != "" && decimal(s).GreaterThan(decimal("100")) {
			return errors.New("risk percentages must be at most 100")
		}
	}
	return nil
}
func SizeEntry(p RiskPolicy, equity, entry, stop, step domain.Decimal) (domain.Decimal, string) {
	return SizeDirectionalEntry(p, equity, entry, stop, step, "long")
}

func SizeDirectionalEntry(p RiskPolicy, equity, entry, stop, step domain.Decimal, side string) (domain.Decimal, string) {
	if p.Validate() != nil {
		return domain.Decimal{}, "invalid risk policy"
	}
	if !entry.IsPositive() || !stop.IsPositive() || !favorable(Position{Side: side}, entry, stop) || !step.IsPositive() {
		return domain.Decimal{}, "invalid entry/stop distance"
	}
	budget := domain.Decimal{}
	if p.FixedRisk != "" {
		budget = decimal(p.FixedRisk)
	}
	if p.PercentRisk != "" {
		if !equity.IsPositive() {
			return domain.Decimal{}, "equity unavailable for risk sizing"
		}
		percent := equity.Mul(decimal(p.PercentRisk)).Shift(-2)
		if budget.IsZero() || percent.LessThan(budget) {
			budget = percent
		}
	}
	if !budget.IsPositive() {
		return domain.Decimal{}, "risk sizing policy unavailable"
	}
	steps, _ := budget.QuoRem(entry.Sub(stop).Abs().Mul(step), 0)
	q := steps.Mul(step)
	if validateValue(q, true) != nil {
		return domain.Decimal{}, "risk size below precision or outside supported bounds"
	}
	return q, ""
}

type RiskState struct {
	Side            string
	Equity          domain.Decimal
	Exposure        domain.Decimal
	SymbolExposure  domain.Decimal
	DailyPnL        domain.Decimal
	Positions       int
	SymbolPositions int
	LastEntry       time.Time
}

// EntryRiskReason checks a prospective fill against current scoped state.
func EntryRiskReason(p RiskPolicy, state RiskState, quantity, entry, stop domain.Decimal, leverage int, now time.Time) string {
	if p.Validate() != nil {
		return "invalid risk policy"
	}
	if !p.Pyramiding && state.SymbolPositions > 0 {
		return "pyramiding disabled"
	}
	if p.MaxPositions > 0 && state.Positions >= p.MaxPositions {
		return "maximum positions exceeded"
	}
	if p.MaxLeverage > 0 && leverage > p.MaxLeverage {
		return "maximum leverage exceeded"
	}
	if p.DailyLoss != "" && state.DailyPnL.Neg().GreaterThanOrEqual(decimal(p.DailyLoss)) {
		return "daily loss limit reached"
	}
	if p.CooldownSeconds > 0 && !state.LastEntry.IsZero() && now.Sub(state.LastEntry) < time.Duration(p.CooldownSeconds)*time.Second {
		return "entry cooldown active"
	}
	notional := entry.Mul(quantity)
	if p.SymbolExposure != "" && state.SymbolExposure.Add(notional).GreaterThan(decimal(p.SymbolExposure)) {
		return "per-symbol exposure exceeded"
	}
	if p.MarginPercent != "" {
		if !state.Equity.IsPositive() {
			return "equity unavailable for margin limit"
		}
		if leverage < 1 {
			return "invalid leverage"
		}
		capital := RequiredMargin(notional, leverage)
		if state.Exposure.Add(capital).GreaterThan(state.Equity.Mul(decimal(p.MarginPercent)).Shift(-2)) {
			return "margin usage exceeded"
		}
	}
	if p.FixedRisk != "" || p.PercentRisk != "" {
		max, reason := SizeDirectionalEntry(p, state.Equity, entry, stop, decimal("0.000000000000000001"), state.Side)
		if reason != "" {
			return reason
		}
		if quantity.GreaterThan(max) {
			return "entry risk budget exceeded"
		}
	}
	return ""
}
