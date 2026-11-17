package paper

import (
	"errors"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
)

// LiquidationModel is an approximate isolated model, never a venue risk tier.
// Equity <= current notional * (maintenance + liquidation fee) triggers exit.
type LiquidationModel struct {
	MaintenanceRate string `json:"maintenance_rate"`
	FeeRate         string `json:"fee_rate"`
}

func DefaultLiquidationModel() LiquidationModel { return LiquidationModel{"0.005", "0.005"} }
func (m LiquidationModel) Validate() error {
	for _, s := range []string{m.MaintenanceRate, m.FeeRate} {
		v, err := ExactValue(s, false)
		if err != nil || v.GreaterThan(decimal("1")) {
			return errors.New("liquidation rates must be exact decimals from 0 to 1")
		}
	}
	if decimal(m.MaintenanceRate).Add(decimal(m.FeeRate)).GreaterThanOrEqual(decimal("1")) {
		return errors.New("combined liquidation rates must be below 1")
	}
	return nil
}
func LiquidationRequired(p Position, mark domain.Decimal, m LiquidationModel) bool {
	return p.Status == "open" && p.Instrument.Market == domain.Linear && p.ReservedMargin.Add(p.PricePnL(mark, p.RemainingQuantity)).LessThanOrEqual(mark.Mul(p.RemainingQuantity).Mul(decimal(m.MaintenanceRate).Add(decimal(m.FeeRate))))
}

// FundingPayment returns a signed collateral change, rounded adversely to 18 places.
func FundingPayment(p Position, rate string, mark domain.Decimal) (domain.Decimal, error) {
	r, err := domain.ParseDecimal(rate)
	if err != nil || r.Abs().GreaterThan(decimal("1")) || r.Exponent() < -18 || validateValue(mark, true) != nil {
		return domain.Decimal{}, errors.New("funding requires a signed exact rate between -1 and 1 and positive valuation price")
	}
	amount := mark.Mul(p.RemainingQuantity).Mul(r)
	if p.Direction() == "long" {
		amount = amount.Neg()
	}
	return amount.RoundFloor(18), nil
}

// FundingEvent explicitly records one expected interval, including missing data.
// Price is a public/fixture valuation at EffectiveAt, not a future candle close.
type FundingEvent struct {
	ID            string            `json:"id"`
	Instrument    domain.Instrument `json:"instrument"`
	EffectiveAt   time.Time         `json:"effective_at"`
	AvailableAt   time.Time         `json:"available_at"`
	Source        string            `json:"source"`
	Provenance    string            `json:"provenance"`
	Rate          *string           `json:"rate"`
	Price         domain.Decimal    `json:"price"`
	MissingReason string            `json:"missing_reason,omitempty"`
}
type FundingApplication struct {
	CollateralDelta domain.Decimal `json:"collateral_delta"`
	IsolatedDeficit domain.Decimal `json:"isolated_deficit"`
	PositionID      string         `json:"position_id"`
	Payment         domain.Decimal `json:"payment"`
}
type FundingResult struct {
	Event        FundingEvent         `json:"event"`
	Applications []FundingApplication `json:"applications"`
	Orders       []Order              `json:"orders"`
	Warning      string               `json:"warning,omitempty"`
}

func (e FundingEvent) Validate() error {
	if !requestID.MatchString(e.ID) || e.Instrument.Validate() != nil || e.Instrument.Market != domain.Linear || e.EffectiveAt.IsZero() || e.AvailableAt.Before(e.EffectiveAt) || e.Source == "" || e.Provenance == "" {
		return errors.New("funding requires linear instrument, identity, ordered clocks and explicit public/fixture provenance")
	}
	if e.Rate == nil {
		if e.MissingReason == "" {
			return errors.New("missing funding requires an explicit reason")
		}
		return nil
	}
	if e.MissingReason != "" {
		return errors.New("funding cannot have both a rate and missing reason")
	}
	_, err := FundingPayment(Position{}, *e.Rate, e.Price)
	return err
}

// EvaluateLiquidation prioritizes an already active protective stop if it is
// reached before liquidation. A gap through both liquidates at the observed open.
func EvaluateLiquidation(p Position, o Observation, protection ProtectionDecision) (ProtectionDecision, error) {
	d := ProtectionDecision{}
	if p.Instrument.Market != domain.Linear || p.Status != "open" || p.Protection == nil || !o.Ready || o.Instrument != p.Instrument || !protectionClockEligible(o, p.Protection.UpdatedAt) {
		return d, nil
	}
	if o.Bar != nil && o.Bar.Start.Before(p.Protection.UpdatedAt) {
		return d, nil
	}
	m := DefaultLiquidationModel()
	if p.Liquidation != nil {
		m = *p.Liquidation
	}
	if err := m.Validate(); err != nil {
		return d, err
	}
	open, adverse := o.Price, o.Price
	if o.Bar != nil {
		open = o.Bar.Open
		adverse = o.Bar.Low
		if p.Direction() == "short" {
			adverse = o.Bar.High
		}
	}
	gap := LiquidationRequired(p, open, m)
	if !gap && !LiquidationRequired(p, adverse, m) {
		return d, nil
	}
	if !gap && (protection.Reason == "stop_loss" || protection.Reason == "trailing_stop") && !LiquidationRequired(p, protection.Price, m) {
		return d, nil
	}
	price := open
	if !gap {
		// Find the first adverse instrument tick satisfying the inequality without
		// rounding the threshold optimistically. Division is only a search seed.
		rates := decimal(m.MaintenanceRate).Add(decimal(m.FeeRate))
		numerator := p.EntryPrice.Mul(p.RemainingQuantity).Sub(p.ReservedMargin)
		denominator := p.RemainingQuantity.Mul(decimal("1").Sub(rates))
		if p.Direction() == "short" {
			numerator = p.EntryPrice.Mul(p.RemainingQuantity).Add(p.ReservedMargin)
			denominator = p.RemainingQuantity.Mul(decimal("1").Add(rates))
		}
		raw := numerator.DivRound(denominator, 18)
		steps, rem := raw.QuoRem(p.Metadata.PriceStep, 0)
		if p.Direction() == "short" && !rem.IsZero() {
			steps = steps.Add(decimal("1"))
		}
		price = steps.Mul(p.Metadata.PriceStep)
		if !price.IsPositive() {
			price = adverse
		}
	}
	state := *p.Protection
	state.UpdatedAt = o.AvailableAt
	return ProtectionDecision{State: state, Reason: "liquidation_approximate", Price: price, Quantity: p.RemainingQuantity.String(), Gap: gap, Ambiguous: o.Bar != nil && protection.Reason != ""}, nil
}

// CapIsolatedLoss records uncollectible gap loss explicitly instead of drawing
// on another position or available cash. Fees are included in the collateral cap.
func CapIsolatedLoss(plan *Execution) {
	if plan.Fill == nil || !plan.CashDelta.IsNegative() {
		return
	}
	deficit := plan.CashDelta.Neg()
	plan.CashDelta = domain.Decimal{}
	plan.Movements = append(plan.Movements, Movement{Kind: "isolated_deficit", Amount: deficit})
	plan.Fill.IsolatedDeficit = plan.Fill.IsolatedDeficit.Add(deficit)
	plan.Fill.RealizedPnL = plan.Fill.RealizedPnL.Add(deficit)
	plan.Position.RealizedPnL = plan.Position.RealizedPnL.Add(deficit)
}
