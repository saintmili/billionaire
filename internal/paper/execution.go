package paper

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
)

type OrderRequest struct {
	Liquidation     *LiquidationModel `json:"liquidation,omitempty"`
	StrategyID      string            `json:"strategy_id,omitempty"`
	SignalID        string            `json:"signal_id,omitempty"`
	TargetPrice     string            `json:"target_price,omitempty"`
	TargetQuantity  string            `json:"target_quantity,omitempty"`
	TrailActivation string            `json:"trail_activation,omitempty"`
	TrailDistance   string            `json:"trail_distance,omitempty"`
	StopPrice       string            `json:"stop_price,omitempty"`
	RequestID       string            `json:"request_id"`
	AccountID       string            `json:"account_id"`
	PositionID      string            `json:"position_id,omitempty"`
	Action          string            `json:"action"`
	Instrument      domain.Instrument `json:"instrument"`
	Side            string            `json:"side"`
	Leverage        int               `json:"leverage"`
	Quantity        string            `json:"quantity"`
	At              time.Time         `json:"at"`
	FeeRate         string            `json:"fee_rate"`
	SlippageBPS     string            `json:"slippage_bps"`
}

type Order struct {
	ID       string                    `json:"id"`
	Request  OrderRequest              `json:"request"`
	Metadata marketdata.InstrumentInfo `json:"metadata"`
	Status   string                    `json:"status"`
	Reason   string                    `json:"reason,omitempty"`
}

type Observation struct {
	// Phase orders replay opening after the previous close at a shared boundary.
	Phase       string            `json:"phase,omitempty"`
	Bar         *PriceBar         `json:"bar,omitempty"`
	ID          string            `json:"id"`
	Instrument  domain.Instrument `json:"instrument"`
	Source      string            `json:"source"`
	Price       domain.Decimal    `json:"price"`
	ObservedAt  time.Time         `json:"observed_at"`
	AvailableAt time.Time         `json:"available_at"`
	Ready       bool              `json:"ready"`
}

type ExecutionGate struct {
	// ReplayNextOpen opts into equal-boundary opens inside an isolated replay only.
	ReplayNextOpen bool
	// ReplayProtectionOnly settles protective exits without consuming pending intents.
	ReplayProtectionOnly bool
	RiskQuantity         string
	Now                  time.Time
	StaleAfter           time.Duration
	Enabled              bool
}

type Fill struct {
	IsolatedDeficit domain.Decimal `json:"isolated_deficit"`
	ExitReason      string         `json:"exit_reason,omitempty"`
	Ambiguous       bool           `json:"ambiguous,omitempty"`
	Gap             bool           `json:"gap,omitempty"`
	ID              string         `json:"id"`
	OrderID         string         `json:"order_id"`
	PositionID      string         `json:"position_id"`
	ObservationID   string         `json:"observation_id"`
	Quantity        domain.Decimal `json:"quantity"`
	Price           domain.Decimal `json:"price"`
	Notional        domain.Decimal `json:"notional"`
	Fee             domain.Decimal `json:"fee"`
	RealizedPnL     domain.Decimal `json:"realized_pnl"`
	Source          string         `json:"source"`
	ObservedAt      time.Time      `json:"observed_at"`
	AvailableAt     time.Time      `json:"available_at"`
	ExecutedAt      time.Time      `json:"executed_at"`
	LatencyMillis   int64          `json:"latency_ms"`
}

type Position struct {
	OpenedAt          time.Time                 `json:"opened_at"`
	Liquidation       *LiquidationModel         `json:"liquidation,omitempty"`
	FundingPnL        domain.Decimal            `json:"funding_pnl"`
	Side              string                    `json:"side,omitempty"`
	Leverage          int                       `json:"leverage,omitempty"`
	ReservedMargin    domain.Decimal            `json:"reserved_margin"`
	Protection        *ProtectionState          `json:"protection,omitempty"`
	StrategyID        string                    `json:"strategy_id,omitempty"`
	ID                string                    `json:"id"`
	AccountID         string                    `json:"account_id"`
	Instrument        domain.Instrument         `json:"instrument"`
	Metadata          marketdata.InstrumentInfo `json:"metadata"`
	Status            string                    `json:"status"`
	InitialQuantity   domain.Decimal            `json:"initial_quantity"`
	RemainingQuantity domain.Decimal            `json:"remaining_quantity"`
	EntryPrice        domain.Decimal            `json:"entry_price"`
	RemainingCost     domain.Decimal            `json:"remaining_cost"`
	RealizedPnL       domain.Decimal            `json:"realized_pnl"`
	FeesPaid          domain.Decimal            `json:"fees_paid"`
}

type Movement struct {
	Kind   string
	Amount domain.Decimal
}
type Execution struct {
	Order     Order
	Fill      *Fill
	Position  *Position
	CashDelta domain.Decimal
	Movements []Movement
}

type ObservationResult struct {
	ObservationID string  `json:"observation_id"`
	Orders        []Order `json:"orders"`
	Reason        string  `json:"reason,omitempty"`
}

// ExactValue bounds paper quantities/prices and all intermediate money values.
// Reject excess precision rather than allowing PostgreSQL or decimal division to round silently.
func ExactValue(text string, positive bool) (domain.Decimal, error) {
	value, err := domain.ParseDecimal(text)
	limit, _ := domain.ParseDecimal("1000000000000000000")
	if err != nil || value.IsNegative() || (positive && !value.IsPositive()) || value.Exponent() < -18 || value.GreaterThan(limit) {
		return domain.Decimal{}, errors.New("paper value must be a nonnegative plain decimal at most 10^18 with at most 18 fractional digits (positive where required)")
	}
	return value, nil
}

func validateValue(value domain.Decimal, positive bool) error {
	if value.Exponent() < -18 || value.Exponent() > 18 || value.Coefficient().BitLen() > 128 {
		return errors.New("paper decimal exceeds supported precision or magnitude")
	}
	_, err := ExactValue(value.String(), positive)
	return err
}

func PrepareOrder(r OrderRequest, info marketdata.InstrumentInfo) (Order, error) {
	if r.Instrument.Market == domain.Linear && r.Liquidation == nil {
		m := DefaultLiquidationModel()
		r.Liquidation = &m
	}
	if r.Liquidation != nil {
		if err := r.Liquidation.Validate(); err != nil {
			return Order{}, err
		}
	}
	if !requestID.MatchString(r.RequestID) || r.AccountID == "" || len(r.AccountID) > 128 || r.At.IsZero() {
		return Order{}, errors.New("valid request ID, account and intent timestamp are required")
	}
	if r.Action != "open" && r.Action != "close" {
		return Order{}, errors.New("order action must be open or close")
	}
	if r.Action == "close" && (r.PositionID == "" || len(r.PositionID) > 128) {
		return Order{}, errors.New("close requires an existing position ID")
	}
	if r.Action == "open" && r.PositionID != "" {
		return Order{}, errors.New("open cannot reverse or modify an existing position")
	}
	if err := info.Validate(); err != nil {
		return Order{}, err
	}
	if r.Instrument != info.Instrument || (r.Instrument.Market == domain.Spot && (r.Side != "long" || r.Leverage != 1)) {
		return Order{}, errors.New("spot orders require matching metadata, long holdings and leverage 1; borrowing and shorts are unsupported")
	}
	if r.Instrument.Market == domain.Linear && ((r.Side != "long" && r.Side != "short") || r.Leverage < 1 || r.Leverage > 100) {
		return Order{}, errors.New("linear orders require long/short direction and integer leverage from 1 to 100")
	}
	for _, v := range []domain.Decimal{info.PriceStep, info.QuantityStep, info.MinQuantity, info.MinNotional} {
		if err := validateValue(v, false); err != nil {
			return Order{}, errors.New("instrument metadata exceeds supported decimal bounds")
		}
	}
	quantityText := r.Quantity
	if quantityText == "risk" && r.Action == "open" {
		quantityText = info.QuantityStep.String()
	}
	quantity, err := ExactValue(quantityText, true)
	if err != nil {
		return Order{}, err
	}
	if r.Quantity != "risk" && (!quantity.Mod(info.QuantityStep).IsZero() || quantity.LessThan(info.MinQuantity)) {
		return Order{}, errors.New("quantity violates instrument step or minimum")
	}
	fee, err := ExactValue(r.FeeRate, false)
	if err != nil || fee.GreaterThan(decimal("1")) {
		return Order{}, errors.New("fee rate must be between 0 and 1")
	}
	slippage, err := ExactValue(r.SlippageBPS, false)
	if err != nil || !slippage.LessThan(decimal("10000")) {
		return Order{}, errors.New("slippage must be at least 0 and below 10000 basis points")
	}
	if r.Quantity != "risk" {
		r.Quantity = quantity.String()
	}
	r.FeeRate = fee.String()
	r.SlippageBPS = slippage.String()
	r.At = r.At.UTC()
	return Order{ID: Identity("order", r.RequestID), Request: r, Metadata: info, Status: "pending"}, nil
}

func Identity(kind, key string) string { return fmt.Sprintf("%s_%x", kind, sha256.Sum256([]byte(key))) }
func decimal(s string) domain.Decimal  { v, _ := domain.ParseDecimal(s); return v }

func (o Observation) Validate() error {
	if o.Phase == "next_open" && o.Bar != nil {
		return errors.New("replay opening observation cannot contain a completed bar")
	}
	if o.Phase != "" && o.Phase != "next_open" {
		return errors.New("unsupported observation phase")
	}
	if !requestID.MatchString(o.ID) || o.Instrument.Validate() != nil || o.Source != o.Instrument.Provider || o.ObservedAt.IsZero() || o.AvailableAt.Before(o.ObservedAt) {
		return errors.New("observation requires a valid identity, matching source and ordered observation/availability timestamps")
	}
	if err := validateValue(o.Price, true); err != nil {
		return err
	}
	return o.validateBar()
}

// SameOrder compares only immutable intent and metadata, ignoring fill/rejection state.
func SameOrder(a, b Order) bool {
	a.Status = ""
	a.Reason = ""
	b.Status = ""
	b.Reason = ""
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// OrderRejection applies the same admission rules at intent submission and
// again against locked current holdings at execution. Order syntax is prepared first.
func OrderRejection(order Order, enabled bool, position *Position) string {
	if order.Request.Action == "open" {
		if !enabled {
			return "market disabled"
		}
		return ""
	}
	quantity, err := ExactValue(order.Request.Quantity, true)
	if err != nil || position == nil || position.ID != order.Request.PositionID || position.AccountID != order.Request.AccountID || position.Instrument != order.Request.Instrument || position.Status != "open" || quantity.GreaterThan(position.RemainingQuantity) || order.Request.Side != position.Direction() || order.Request.Leverage != position.EffectiveLeverage() {
		return "close exceeds owned holdings or position identity is invalid"
	}
	return ""
}

func PlanFill(order Order, observation Observation, gate ExecutionGate, balance domain.Decimal, position *Position) (Execution, error) {
	result := Execution{Order: order}
	if _, err := PrepareOrder(order.Request, order.Metadata); err != nil {
		return result, err
	}
	if err := observation.Validate(); err != nil {
		return result, err
	}
	if gate.Now.IsZero() || gate.StaleAfter <= 0 {
		return result, errors.New("execution clock and positive stale threshold are required")
	}
	reject := func(reason string) (Execution, error) {
		result.Order.Status = "rejected"
		result.Order.Reason = reason
		return result, nil
	}
	if eligible, reason := ObservationEligibility(order, observation, gate); !eligible {
		if reason != "" {
			return reject(reason)
		}
		return result, nil
	}
	if reason := OrderRejection(order, gate.Enabled, position); reason != "" {
		return reject(reason)
	}
	quantity := decimal(order.Request.Quantity)
	if gate.RiskQuantity != "" && order.Request.Action == "open" {
		quantity = decimal(gate.RiskQuantity)
	}
	if !quantity.IsPositive() || !quantity.Mod(order.Metadata.QuantityStep).IsZero() || quantity.LessThan(order.Metadata.MinQuantity) {
		return reject("invalid risk-sized quantity")
	}
	price := ExecutablePrice(order, observation.Price)
	if _, err := ExactValue(price.String(), true); err != nil {
		return reject("invalid modeled fill price")
	}
	amounts, err := CalculateFillAmounts(order, price, quantity)
	if err != nil {
		return reject(err.Error())
	}
	notional, fee := amounts.Notional, amounts.Fee
	fill := Fill{ID: Identity("fill", order.ID), OrderID: order.ID, PositionID: Identity("position", order.ID), ObservationID: observation.ID, Quantity: quantity, Price: price, Notional: notional, Fee: fee, Source: observation.Source, ObservedAt: observation.ObservedAt, AvailableAt: observation.AvailableAt, ExecutedAt: gate.Now, LatencyMillis: gate.Now.Sub(order.Request.At).Milliseconds()}
	if order.Request.Action == "open" {
		config := order.Request.ProtectionConfig()
		if err := ValidateProtection(config, Position{Side: order.Request.Side, Metadata: order.Metadata, RemainingQuantity: quantity}, price); err != nil {
			return reject(err.Error())
		}
		capital := amounts.Capital
		total := capital.Add(fee)
		if total.GreaterThan(balance) {
			if order.Request.Instrument.Market == domain.Linear {
				return reject("insufficient isolated margin and fees")
			}
			return reject("insufficient USDT funds")
		}
		result.CashDelta = total.Neg()
		result.Position = &Position{OpenedAt: observation.AvailableAt, Side: order.Request.Side, Leverage: order.Request.Leverage, StrategyID: order.Request.StrategyID, ID: fill.PositionID, AccountID: order.Request.AccountID, Instrument: order.Request.Instrument, Metadata: order.Metadata, Status: "open", InitialQuantity: quantity, RemainingQuantity: quantity, EntryPrice: price, RemainingCost: total, FeesPaid: fee}
		result.Position.Protection = &ProtectionState{ProtectionConfig: config, UpdatedAt: observation.AvailableAt, FeeRate: order.Request.FeeRate, SlippageBPS: order.Request.SlippageBPS}
		result.Movements = []Movement{{Kind: "spot_buy", Amount: capital.Neg()}, {Kind: "fee", Amount: fee.Neg()}}
		if order.Request.Instrument.Market == domain.Linear {
			result.Position.Liquidation = order.Request.Liquidation
			result.Position.ReservedMargin = capital
			result.Movements[0].Kind = "margin_reserve"
			result.Position.RemainingCost = fee
		}
	} else {
		copy := *position
		cost := copy.RemainingCost
		if quantity.LessThan(copy.RemainingQuantity) {
			cost = cost.Mul(quantity).DivRound(copy.RemainingQuantity, 18)
		}
		result.CashDelta = notional.Sub(fee)
		fill.PositionID = copy.ID
		fill.RealizedPnL = result.CashDelta.Sub(cost)
		if copy.Instrument.Market == domain.Linear {
			released := copy.ReservedMargin
			if quantity.LessThan(copy.RemainingQuantity) {
				released = released.Mul(quantity).DivRound(copy.RemainingQuantity, 18)
			}
			gross := copy.PricePnL(price, quantity).RoundFloor(18)
			result.CashDelta = released.Add(gross).Sub(fee)
			fill.RealizedPnL = gross.Sub(fee).Sub(cost)
			copy.ReservedMargin = copy.ReservedMargin.Sub(released)
			result.Movements = []Movement{{Kind: "margin_release", Amount: released}, {Kind: "linear_pnl", Amount: gross}, {Kind: "fee", Amount: fee.Neg()}}
		}
		copy.RemainingQuantity = copy.RemainingQuantity.Sub(quantity)
		copy.RemainingCost = copy.RemainingCost.Sub(cost)
		copy.RealizedPnL = copy.RealizedPnL.Add(fill.RealizedPnL)
		copy.FeesPaid = copy.FeesPaid.Add(fee)
		if copy.RemainingQuantity.IsZero() {
			copy.Status = "closed"
		}
		result.Position = &copy
		if copy.Instrument.Market == domain.Spot {
			result.Movements = []Movement{{Kind: "spot_sell", Amount: notional}, {Kind: "fee", Amount: fee.Neg()}}
		}
	}
	result.Order.Status = "filled"
	result.Fill = &fill
	if result.Position.Instrument.Market == domain.Linear && order.Request.Action == "close" {
		CapIsolatedLoss(&result)
	}
	return result, nil
}

// ExecutablePrice applies adverse slippage and instrument price precision.
func ExecutablePrice(order Order, observed domain.Decimal) domain.Decimal {
	price := observed
	factor := decimal(order.Request.SlippageBPS).Shift(-4)
	if order.IsBuy() {
		price = price.Mul(decimal("1").Add(factor))
	} else {
		price = price.Mul(decimal("1").Sub(factor))
	}
	// Round in the adverse direction to the instrument tick, never improving the modeled fill.
	steps, remainder := price.QuoRem(order.Metadata.PriceStep, 0)
	if order.IsBuy() && !remainder.IsZero() {
		steps = steps.Add(decimal("1"))
	}
	price = steps.Mul(order.Metadata.PriceStep)

	return price
}

// ObservationEligibility is shared by risk planning and fill planning.
// A blank reason with eligible=false leaves the intent pending for a later price.
func ObservationEligibility(order Order, observation Observation, gate ExecutionGate) (bool, string) {
	if order.Status != "pending" || order.Request.Instrument != observation.Instrument {
		return false, ""
	}
	afterIntent := observation.ObservedAt.After(order.Request.At) && observation.AvailableAt.After(order.Request.At)
	if observation.Phase == "next_open" && gate.ReplayNextOpen {
		afterIntent = !observation.ObservedAt.Before(order.Request.At) && !observation.AvailableAt.Before(order.Request.At)
	}
	if !afterIntent || observation.AvailableAt.After(gate.Now) {
		return false, ""
	}
	if !observation.Ready || gate.Now.Sub(observation.ObservedAt) > gate.StaleAfter {
		return false, "source stale/unready"
	}
	return true, ""
}

func (r OrderRequest) ProtectionConfig() ProtectionConfig {
	return ProtectionConfig{StopPrice: r.StopPrice, TargetPrice: r.TargetPrice, TargetQuantity: r.TargetQuantity, TrailActivation: r.TrailActivation, TrailDistance: r.TrailDistance}
}

// Direction and EffectiveLeverage preserve existing spot records without rewriting ledger history.
func (p Position) Direction() string {
	if p.Side == "" {
		return "long"
	}
	return p.Side
}
func (p Position) EffectiveLeverage() int {
	if p.Leverage == 0 {
		return 1
	}
	return p.Leverage
}
func (p Position) PricePnL(mark, quantity domain.Decimal) domain.Decimal {
	pnl := mark.Sub(p.EntryPrice).Mul(quantity)
	if p.Direction() == "short" {
		return pnl.Neg()
	}
	return pnl
}
func (o Order) IsBuy() bool { return (o.Request.Action == "open") == (o.Request.Side == "long") }

// RequiredMargin rounds collateral upward, never understating a reservation.
func RequiredMargin(notional domain.Decimal, leverage int) domain.Decimal {
	divisor := decimal(fmt.Sprint(leverage))
	steps, remainder := notional.Shift(18).QuoRem(divisor, 0)
	if !remainder.IsZero() {
		steps = steps.Add(decimal("1"))
	}
	return steps.Shift(-18)
}

// FillAmounts is the shared monetary quote used for risk admission and settlement.
type FillAmounts struct{ Notional, Fee, Capital domain.Decimal }

func CalculateFillAmounts(order Order, price, quantity domain.Decimal) (FillAmounts, error) {
	amounts := FillAmounts{Notional: roundedNotional(price, quantity, order.IsBuy())}
	if _, err := ExactValue(amounts.Notional.String(), true); err != nil {
		return amounts, errors.New("fill notional exceeds supported bounds")
	}
	// A risk-reducing linear close must remain possible after a price collapse.
	if amounts.Notional.LessThan(order.Metadata.MinNotional) && !(order.Request.Instrument.Market == domain.Linear && order.Request.Action == "close") {
		return amounts, errors.New("fill below minimum notional")
	}
	amounts.Fee = amounts.Notional.Mul(decimal(order.Request.FeeRate)).RoundCeil(18)
	amounts.Capital = amounts.Notional
	if order.Request.Instrument.Market == domain.Linear {
		if order.Request.Leverage < 1 {
			return amounts, errors.New("invalid leverage")
		}
		amounts.Capital = RequiredMargin(amounts.Notional, order.Request.Leverage)
	}
	return amounts, nil
}

func roundedNotional(price, quantity domain.Decimal, buy bool) domain.Decimal {
	n := price.Mul(quantity)
	if buy {
		return n.RoundCeil(18)
	}
	return n.RoundFloor(18)
}
