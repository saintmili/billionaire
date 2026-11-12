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
	StrategyID  string            `json:"strategy_id,omitempty"`
	SignalID    string            `json:"signal_id,omitempty"`
	StopPrice   string            `json:"stop_price,omitempty"`
	RequestID   string            `json:"request_id"`
	AccountID   string            `json:"account_id"`
	PositionID  string            `json:"position_id,omitempty"`
	Action      string            `json:"action"`
	Instrument  domain.Instrument `json:"instrument"`
	Side        string            `json:"side"`
	Leverage    int               `json:"leverage"`
	Quantity    string            `json:"quantity"`
	At          time.Time         `json:"at"`
	FeeRate     string            `json:"fee_rate"`
	SlippageBPS string            `json:"slippage_bps"`
}

type Order struct {
	ID       string                    `json:"id"`
	Request  OrderRequest              `json:"request"`
	Metadata marketdata.InstrumentInfo `json:"metadata"`
	Status   string                    `json:"status"`
	Reason   string                    `json:"reason,omitempty"`
}

type Observation struct {
	ID          string            `json:"id"`
	Instrument  domain.Instrument `json:"instrument"`
	Source      string            `json:"source"`
	Price       domain.Decimal    `json:"price"`
	ObservedAt  time.Time         `json:"observed_at"`
	AvailableAt time.Time         `json:"available_at"`
	Ready       bool              `json:"ready"`
}

type ExecutionGate struct {
	RiskQuantity string
	Now          time.Time
	StaleAfter   time.Duration
	Enabled      bool
}

type Fill struct {
	ID            string         `json:"id"`
	OrderID       string         `json:"order_id"`
	PositionID    string         `json:"position_id"`
	ObservationID string         `json:"observation_id"`
	Quantity      domain.Decimal `json:"quantity"`
	Price         domain.Decimal `json:"price"`
	Notional      domain.Decimal `json:"notional"`
	Fee           domain.Decimal `json:"fee"`
	RealizedPnL   domain.Decimal `json:"realized_pnl"`
	Source        string         `json:"source"`
	ObservedAt    time.Time      `json:"observed_at"`
	AvailableAt   time.Time      `json:"available_at"`
	ExecutedAt    time.Time      `json:"executed_at"`
	LatencyMillis int64          `json:"latency_ms"`
}

type Position struct {
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
	if r.Instrument != info.Instrument || r.Instrument.Market != domain.Spot || r.Side != "long" || r.Leverage != 1 {
		return Order{}, errors.New("spot orders require matching metadata, long holdings and leverage 1; borrowing and shorts are unsupported")
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
	if !requestID.MatchString(o.ID) || o.Instrument.Validate() != nil || o.Instrument.Market != domain.Spot || o.Source != o.Instrument.Provider || o.ObservedAt.IsZero() || o.AvailableAt.Before(o.ObservedAt) {
		return errors.New("observation requires a valid identity, matching source and ordered observation/availability timestamps")
	}
	if err := validateValue(o.Price, true); err != nil {
		return err
	}
	return nil
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

// SpotOrderRejection applies the same admission rules at intent submission and
// again against locked current holdings at execution. Order syntax is prepared first.
func SpotOrderRejection(order Order, enabled bool, position *Position) string {
	if order.Request.Action == "open" {
		if !enabled {
			return "market disabled"
		}
		return ""
	}
	quantity, err := ExactValue(order.Request.Quantity, true)
	if err != nil || position == nil || position.ID != order.Request.PositionID || position.AccountID != order.Request.AccountID || position.Instrument != order.Request.Instrument || position.Status != "open" || quantity.GreaterThan(position.RemainingQuantity) {
		return "close exceeds owned holdings or position identity is invalid"
	}
	return ""
}

func PlanSpotFill(order Order, observation Observation, gate ExecutionGate, balance domain.Decimal, position *Position) (Execution, error) {
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
	if eligible, reason := SpotObservationEligibility(order, observation, gate); !eligible {
		if reason != "" {
			return reject(reason)
		}
		return result, nil
	}
	if reason := SpotOrderRejection(order, gate.Enabled, position); reason != "" {
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
	notional := price.Mul(quantity)
	if order.Request.Action == "open" {
		notional = notional.RoundCeil(18)
	} else {
		notional = notional.RoundFloor(18)
	}
	if _, err := ExactValue(notional.String(), true); err != nil {
		return reject("fill notional exceeds supported bounds")
	}
	if notional.LessThan(order.Metadata.MinNotional) {
		return reject("fill below minimum notional")
	}
	fee := notional.Mul(decimal(order.Request.FeeRate)).RoundCeil(18)
	fill := Fill{ID: Identity("fill", order.ID), OrderID: order.ID, PositionID: Identity("position", order.ID), ObservationID: observation.ID, Quantity: quantity, Price: price, Notional: notional, Fee: fee, Source: observation.Source, ObservedAt: observation.ObservedAt, AvailableAt: observation.AvailableAt, ExecutedAt: gate.Now, LatencyMillis: gate.Now.Sub(order.Request.At).Milliseconds()}
	if order.Request.Action == "open" {
		total := notional.Add(fee)
		if total.GreaterThan(balance) {
			return reject("insufficient USDT funds")
		}
		result.CashDelta = total.Neg()
		result.Position = &Position{StrategyID: order.Request.StrategyID, ID: fill.PositionID, AccountID: order.Request.AccountID, Instrument: order.Request.Instrument, Metadata: order.Metadata, Status: "open", InitialQuantity: quantity, RemainingQuantity: quantity, EntryPrice: price, RemainingCost: total, FeesPaid: fee}
		result.Movements = []Movement{{Kind: "spot_buy", Amount: notional.Neg()}, {Kind: "fee", Amount: fee.Neg()}}
	} else {
		copy := *position
		cost := copy.RemainingCost
		if quantity.LessThan(copy.RemainingQuantity) {
			cost = cost.Mul(quantity).DivRound(copy.RemainingQuantity, 18)
		}
		result.CashDelta = notional.Sub(fee)
		fill.PositionID = copy.ID
		fill.RealizedPnL = result.CashDelta.Sub(cost)
		copy.RemainingQuantity = copy.RemainingQuantity.Sub(quantity)
		copy.RemainingCost = copy.RemainingCost.Sub(cost)
		copy.RealizedPnL = copy.RealizedPnL.Add(fill.RealizedPnL)
		copy.FeesPaid = copy.FeesPaid.Add(fee)
		if copy.RemainingQuantity.IsZero() {
			copy.Status = "closed"
		}
		result.Position = &copy
		result.Movements = []Movement{{Kind: "spot_sell", Amount: notional}, {Kind: "fee", Amount: fee.Neg()}}
	}
	result.Order.Status = "filled"
	result.Fill = &fill
	return result, nil
}

// ExecutablePrice applies adverse slippage and instrument price precision.
func ExecutablePrice(order Order, observed domain.Decimal) domain.Decimal {
	price := observed
	factor := decimal(order.Request.SlippageBPS).Shift(-4)
	if order.Request.Action == "open" {
		price = price.Mul(decimal("1").Add(factor))
	} else {
		price = price.Mul(decimal("1").Sub(factor))
	}
	// Round in the adverse direction to the instrument tick, never improving the modeled fill.
	steps, remainder := price.QuoRem(order.Metadata.PriceStep, 0)
	if order.Request.Action == "open" && !remainder.IsZero() {
		steps = steps.Add(decimal("1"))
	}
	price = steps.Mul(order.Metadata.PriceStep)

	return price
}

// SpotObservationEligibility is shared by risk planning and fill planning.
// A blank reason with eligible=false leaves the intent pending for a later price.
func SpotObservationEligibility(order Order, observation Observation, gate ExecutionGate) (bool, string) {
	if order.Status != "pending" || order.Request.Instrument != observation.Instrument {
		return false, ""
	}
	if !observation.ObservedAt.After(order.Request.At) || !observation.AvailableAt.After(order.Request.At) || observation.AvailableAt.After(gate.Now) {
		return false, ""
	}
	if !observation.Ready || gate.Now.Sub(observation.ObservedAt) > gate.StaleAfter {
		return false, "source stale/unready"
	}
	return true, ""
}
