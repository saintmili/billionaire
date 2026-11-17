package paper

import (
	"errors"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
)

// PriceBar is a completed, explicitly approximate OHLC observation. Start is
// inclusive and End exclusive; no path within this interval is implied.
type PriceBar struct {
	Start time.Time      `json:"start"`
	End   time.Time      `json:"end"`
	Open  domain.Decimal `json:"open"`
	High  domain.Decimal `json:"high"`
	Low   domain.Decimal `json:"low"`
	Close domain.Decimal `json:"close"`
}

type ProtectionConfig struct {
	StopPrice       string `json:"stop_price,omitempty"`
	TargetPrice     string `json:"target_price,omitempty"`
	TargetQuantity  string `json:"target_quantity,omitempty"`
	TrailActivation string `json:"trail_activation,omitempty"`
	TrailDistance   string `json:"trail_distance,omitempty"`
}

type ProtectionState struct {
	ProtectionConfig
	UpdatedAt      time.Time `json:"updated_at"`
	HighWater      string    `json:"high_water,omitempty"`
	TrailingActive bool      `json:"trailing_active"`
	TargetDone     bool      `json:"target_done"`
	FeeRate        string    `json:"fee_rate"`
	SlippageBPS    string    `json:"slippage_bps"`
}

type ProtectionDecision struct {
	State     ProtectionState
	Reason    string
	Price     domain.Decimal
	Quantity  string
	Ambiguous bool
	Gap       bool
}

// ValidateProtection checks exact ticks and directional geometry. A zero reference
// permits syntax validation before an entry's actual execution price is known.
func ValidateProtection(c ProtectionConfig, p Position, reference domain.Decimal) error {
	if err := p.Metadata.Validate(); err != nil {
		return err
	}
	for _, s := range []string{c.StopPrice, c.TargetPrice, c.TrailActivation, c.TrailDistance} {
		if s == "" {
			continue
		}
		v, err := ExactValue(s, true)
		if err != nil || !v.Mod(p.Metadata.PriceStep).IsZero() {
			return errors.New("protection prices and trailing distance must be positive exact price ticks")
		}
	}
	if (c.TrailActivation == "") != (c.TrailDistance == "") {
		return errors.New("trailing activation and distance must be configured together")
	}
	if c.TrailDistance != "" && !decimal(c.TrailDistance).LessThan(decimal(c.TrailActivation)) {
		return errors.New("trailing distance must be below activation price")
	}
	if c.StopPrice != "" && c.TargetPrice != "" && !favorable(p, decimal(c.TargetPrice), decimal(c.StopPrice)) {
		return errors.New("stop and target have invalid directional geometry")
	}
	if reference.IsPositive() {
		if c.StopPrice != "" && !favorable(p, reference, decimal(c.StopPrice)) {
			return errors.New("stop must be adverse to reference price")
		}
		if c.TargetPrice != "" && !favorable(p, decimal(c.TargetPrice), reference) {
			return errors.New("target must be favorable to reference price")
		}
	}
	if c.TargetQuantity != "" {
		q, err := ExactValue(c.TargetQuantity, true)
		if err != nil || c.TargetPrice == "" || !q.Mod(p.Metadata.QuantityStep).IsZero() || q.LessThan(p.Metadata.MinQuantity) || (p.RemainingQuantity.IsPositive() && q.GreaterThan(p.RemainingQuantity)) {
			return errors.New("target quantity requires a target and must fit owned holdings, step and minimum")
		}
	}
	return nil
}

func (o Observation) validateBar() error {
	b := o.Bar
	if b == nil {
		return nil
	}
	if b.Start.IsZero() || !b.End.After(b.Start) || o.ObservedAt.Before(b.End) || !o.Price.Equal(b.Close) {
		return errors.New("bar requires ordered boundaries, completed availability and matching close price")
	}
	for _, v := range []domain.Decimal{b.Open, b.High, b.Low, b.Close} {
		if err := validateValue(v, true); err != nil {
			return err
		}
	}
	if b.Low.GreaterThan(b.High) || b.High.LessThan(b.Open) || b.High.LessThan(b.Close) || b.Low.GreaterThan(b.Open) || b.Low.GreaterThan(b.Close) {
		return errors.New("invalid OHLC bounds")
	}
	return nil
}

// EvaluateProtection checks the stop in force before the event, then target,
// then ratchets trailing for the NEXT event. Stops always close all remaining
// holdings; a configured partial target fires once, including across restarts.
func EvaluateProtection(p Position, o Observation) (ProtectionDecision, error) {
	d := ProtectionDecision{}
	if err := o.Validate(); err != nil {
		return d, err
	}
	if p.Protection == nil || p.Status != "open" || p.Instrument != o.Instrument || !o.Ready {
		return d, nil
	}
	d.State = *p.Protection
	if !protectionClockEligible(o, d.State.UpdatedAt) {
		return d, nil
	}
	open, high, low := o.Price, o.Price, o.Price
	if o.Bar != nil {
		if o.Bar.Start.Before(d.State.UpdatedAt) {
			return d, nil
		}
		open, high, low = o.Bar.Open, o.Bar.High, o.Bar.Low
	}
	adverse, best := low, high
	if p.Direction() == "short" {
		adverse, best = high, low
	}
	stopHit := d.State.StopPrice != "" && !favorable(p, adverse, decimal(d.State.StopPrice))
	targetHit := !d.State.TargetDone && d.State.TargetPrice != "" && !favorable(p, decimal(d.State.TargetPrice), best)
	d.Ambiguous = stopHit && targetHit
	if stopHit {
		d.Reason = "stop_loss"
		if d.State.TrailingActive {
			d.Reason = "trailing_stop"
		}
		d.Price = decimal(d.State.StopPrice)
		if favorable(p, d.Price, open) {
			d.Price = open
			d.Gap = true
		}
		d.Quantity = p.RemainingQuantity.String()
	} else if targetHit {
		d.Reason = "take_profit"
		d.Price = decimal(d.State.TargetPrice)
		if favorable(p, open, d.Price) {
			d.Price = open
			d.Gap = true
		}
		d.Quantity = p.RemainingQuantity.String()
		if d.State.TargetQuantity != "" && decimal(d.State.TargetQuantity).LessThan(p.RemainingQuantity) {
			d.Quantity = d.State.TargetQuantity
		}
		d.State.TargetDone = true
	}
	if d.State.TrailActivation != "" && !favorable(p, decimal(d.State.TrailActivation), best) {
		if d.State.HighWater == "" || favorable(p, best, decimal(d.State.HighWater)) {
			d.State.HighWater = best.String()
		}
		distance := decimal(d.State.TrailDistance)
		if p.Direction() == "short" {
			distance = distance.Neg()
		}
		candidate := decimal(d.State.HighWater).Sub(distance)
		steps, remainder := candidate.QuoRem(p.Metadata.PriceStep, 0)
		if p.Direction() == "short" && !remainder.IsZero() {
			steps = steps.Add(decimal("1"))
		}
		candidate = steps.Mul(p.Metadata.PriceStep)
		if d.State.StopPrice == "" || favorable(p, candidate, decimal(d.State.StopPrice)) {
			d.State.StopPrice = candidate.String()
			d.State.TrailingActive = true
		}
	}
	d.State.UpdatedAt = o.AvailableAt
	return d, nil
}

// ProtectionAdjustment is a patch: nil preserves a field; empty disables it.
type ProtectionAdjustment struct {
	RequestID       string        `json:"request_id"`
	PositionID      string        `json:"position_id"`
	At              time.Time     `json:"at"`
	StaleAfter      time.Duration `json:"stale_after"`
	StopPrice       *string       `json:"stop_price,omitempty"`
	TargetPrice     *string       `json:"target_price,omitempty"`
	TargetQuantity  *string       `json:"target_quantity,omitempty"`
	TrailActivation *string       `json:"trail_activation,omitempty"`
	TrailDistance   *string       `json:"trail_distance,omitempty"`
}

func (r ProtectionAdjustment) Validate() error {
	if !requestID.MatchString(r.RequestID) || r.PositionID == "" || r.At.IsZero() || r.StaleAfter <= 0 {
		return errors.New("adjustment requires request ID, position, timestamp and positive stale threshold")
	}
	if r.StopPrice == nil && r.TargetPrice == nil && r.TargetQuantity == nil && r.TrailActivation == nil && r.TrailDistance == nil {
		return errors.New("provide at least one protection adjustment")
	}
	return nil
}

func AdjustProtection(p Position, r ProtectionAdjustment, reference Observation) (Position, error) {
	if err := r.Validate(); err != nil {
		return p, err
	}
	if p.ID != r.PositionID || p.Status != "open" || p.Protection == nil {
		return p, errors.New("adjustment requires an open position with execution assumptions")
	}
	if err := reference.Validate(); err != nil {
		return p, err
	}
	if reference.Instrument != p.Instrument || !reference.Ready || reference.AvailableAt.After(r.At) || r.At.Sub(reference.ObservedAt) > r.StaleAfter || r.At.Before(p.Protection.UpdatedAt) {
		return p, errors.New("adjustment requires a fresh usable reference and cannot precede position state")
	}
	state := *p.Protection
	for _, field := range []struct {
		value  *string
		target *string
	}{{r.StopPrice, &state.StopPrice}, {r.TargetPrice, &state.TargetPrice}, {r.TargetQuantity, &state.TargetQuantity}, {r.TrailActivation, &state.TrailActivation}, {r.TrailDistance, &state.TrailDistance}} {
		if field.value != nil {
			*field.target = *field.value
		}
	}
	if r.TargetPrice != nil || r.TargetQuantity != nil {
		state.TargetDone = false
	}
	config := state.ProtectionConfig
	if state.TargetDone {
		config.TargetPrice = ""
		config.TargetQuantity = ""
	}
	if err := ValidateProtection(config, p, reference.Price); err != nil {
		return p, err
	}
	if r.TrailActivation != nil || r.TrailDistance != nil || r.StopPrice != nil {
		state.HighWater = ""
		state.TrailingActive = false
	}
	state.UpdatedAt = r.At.UTC()
	p.Protection = &state
	return p, nil
}

// favorable reports whether a price is strictly better for the position direction.
func favorable(p Position, price, reference domain.Decimal) bool {
	if p.Direction() == "short" {
		return price.LessThan(reference)
	}
	return price.GreaterThan(reference)
}

// protectionClockEligible permits explicit funding/open point phases to follow
// an already settled interval at its exclusive end. Ordinary observations retain
// strict monotonic clocks and every bar retains its overlap check.
func protectionClockEligible(o Observation, updated time.Time) bool {
	if o.Phase == "funding_after_interval" || o.Phase == "next_open" {
		return !o.ObservedAt.Before(updated) && !o.AvailableAt.Before(updated)
	}
	return o.ObservedAt.After(updated) && o.AvailableAt.After(updated)
}
