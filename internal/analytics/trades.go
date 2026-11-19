// Package analytics provides read-only metrics over retained financial evidence.
package analytics

import (
	"errors"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/shopspring/decimal"
	"sort"
	"time"
)

type Filter struct {
	AccountID       string             `json:"account_id,omitempty"`
	StrategyVersion string             `json:"strategy_version,omitempty"`
	Instrument      *domain.Instrument `json:"instrument,omitempty"`
	From            time.Time          `json:"from,omitempty"`
	To              time.Time          `json:"to,omitempty"`
}

func (f Filter) Validate() error {
	if !f.From.IsZero() && !f.To.IsZero() && !f.From.Before(f.To) {
		return errors.New("report range requires from before to")
	}
	if f.Instrument != nil {
		return f.Instrument.Validate()
	}
	return nil
}

type EquityPoint struct {
	At       time.Time       `json:"at"`
	Equity   *domain.Decimal `json:"equity"`
	Exposure *domain.Decimal `json:"exposure"`
	Leverage *domain.Decimal `json:"leverage"`
	Cash     domain.Decimal  `json:"cash"`
	Reason   string          `json:"reason,omitempty"`
}
type Input struct {
	CashBalance    *domain.Decimal
	Signals        []strategy.Signal
	Scope          string
	Positions      []paper.Position
	Orders         []paper.Order
	Fills          []paper.Fill
	Ledger         []paper.LedgerEntry
	Equity         []EquityPoint
	InitialCapital *domain.Decimal
	Candles        []domain.Candle
}
type Trade struct {
	IsolatedDeficit domain.Decimal    `json:"isolated_deficit"`
	VersionStatus   string            `json:"version_status"`
	PositionID      string            `json:"position_id"`
	AccountID       string            `json:"account_id"`
	StrategyVersion string            `json:"strategy_version,omitempty"`
	Instrument      domain.Instrument `json:"instrument"`
	Status          string            `json:"status"`
	OpenedAt        time.Time         `json:"opened_at"`
	ClosedAt        *time.Time        `json:"closed_at"`
	GrossPnL        domain.Decimal    `json:"gross_pnl"`
	NetPnL          domain.Decimal    `json:"net_pnl"`
	Fees            domain.Decimal    `json:"fees"`
	Funding         domain.Decimal    `json:"funding"`
	PlannedRisk     *domain.Decimal   `json:"planned_risk"`
	PlannedR        *domain.Decimal   `json:"planned_r"`
	RealizedR       *domain.Decimal   `json:"realized_r"`
	HoldingSeconds  *domain.Decimal   `json:"holding_seconds"`
	MAE             *domain.Decimal   `json:"mae"`
	MFE             *domain.Decimal   `json:"mfe"`
	ExcursionBasis  string            `json:"excursion_basis"`
}
type Report struct {
	CapitalDeposits         domain.Decimal  `json:"capital_deposits"`
	CapitalWithdrawals      domain.Decimal  `json:"capital_withdrawals"`
	ReturnStatus            string          `json:"return_status"`
	RunID                   string          `json:"run_id,omitempty"`
	CashBalance             *domain.Decimal `json:"cash_balance"`
	LedgerCashReconciled    *bool           `json:"ledger_cash_reconciled"`
	IsolatedDeficit         domain.Decimal  `json:"isolated_deficit"`
	Scope                   string          `json:"scope"`
	Filter                  Filter          `json:"filter"`
	Trades                  []Trade         `json:"trades"`
	ClosedTrades            int             `json:"closed_trades"`
	OpenPositions           int             `json:"open_positions"`
	Wins                    int             `json:"wins"`
	Losses                  int             `json:"losses"`
	Breakeven               int             `json:"breakeven"`
	WinRate                 *domain.Decimal `json:"win_rate"`
	LossRate                *domain.Decimal `json:"loss_rate"`
	BreakevenRate           *domain.Decimal `json:"breakeven_rate"`
	GrossPnL                domain.Decimal  `json:"gross_pnl"`
	NetPnL                  domain.Decimal  `json:"net_pnl"`
	Fees                    domain.Decimal  `json:"fees"`
	Funding                 domain.Decimal  `json:"funding"`
	AverageWin              *domain.Decimal `json:"average_win"`
	AverageLoss             *domain.Decimal `json:"average_loss"`
	Expectancy              *domain.Decimal `json:"expectancy"`
	ProfitFactor            *domain.Decimal `json:"profit_factor"`
	ProfitFactorStatus      string          `json:"profit_factor_status"`
	AverageHoldingSeconds   *domain.Decimal `json:"average_holding_seconds"`
	Equity                  []EquityPoint   `json:"equity"`
	Return                  *domain.Decimal `json:"return"`
	MaximumDrawdown         *domain.Decimal `json:"maximum_drawdown"`
	MaximumDrawdownFraction *domain.Decimal `json:"maximum_drawdown_fraction"`
	ValuationStatus         string          `json:"valuation_status"`
	LedgerTotal             domain.Decimal  `json:"ledger_total"`
	LedgerFees              domain.Decimal  `json:"ledger_fees"`
	LedgerFunding           domain.Decimal  `json:"ledger_funding"`
	Warnings                []string        `json:"warnings"`
}

func ptr(d domain.Decimal) *domain.Decimal { return &d }
func integer(n int) domain.Decimal         { return decimal.NewFromInt(int64(n)) }

// Trades counts each closed position lifecycle once. Partial exits remain part
// of that lifecycle. Date filters select opening instants, never truncate exits.
func Trades(in Input, f Filter) (Report, error) {
	r := Report{Scope: in.Scope, Filter: f, Trades: []Trade{}, Equity: []EquityPoint{}, Warnings: []string{}, ProfitFactorStatus: "no_trades", ValuationStatus: "unavailable"}
	if err := f.Validate(); err != nil {
		return r, err
	}
	if in.Scope != "backtest" && in.Scope != "forward-paper" {
		return r, errors.New("explicit backtest or forward-paper scope required")
	}
	orders := map[string]paper.Order{}
	for _, o := range in.Orders {
		orders[o.ID] = o
	}
	holdingKnown := 0
	win, loss, holding := domain.Decimal{}, domain.Decimal{}, domain.Decimal{}
	signals := map[string]string{}
	for _, signal := range in.Signals {
		signals[signal.ID] = signal.VersionID
	}
	for _, p := range in.Positions {
		version := ""
		for _, fill := range in.Fills {
			if fill.PositionID == p.ID {
				o := orders[fill.OrderID]
				if o.Request.Action == "open" {
					version = signals[o.Request.SignalID]
				}
			}
		}
		if f.AccountID != "" && p.AccountID != f.AccountID || f.StrategyVersion != "" && version != f.StrategyVersion || f.Instrument != nil && p.Instrument != *f.Instrument || !f.From.IsZero() && p.OpenedAt.Before(f.From) || !f.To.IsZero() && !p.OpenedAt.Before(f.To) {
			continue
		}
		t := Trade{PositionID: p.ID, AccountID: p.AccountID, StrategyVersion: version, Instrument: p.Instrument, Status: p.Status, OpenedAt: p.OpenedAt, Fees: p.FeesPaid, Funding: p.FundingPnL, ExcursionBasis: "unavailable: no complete price observation evidence"}
		t.VersionStatus = "unavailable: standalone order or missing signal evidence"
		if version != "" {
			t.VersionStatus = "immutable_signal_version"
		}
		t.NetPnL = p.RealizedPnL
		// Open-position realized P&L does not yet allocate remaining entry fees.
		remainingFee := p.RemainingCost
		if p.Instrument.Market == domain.Spot {
			remainingFee = remainingFee.Sub(remainingSpotPrincipal(p, in.Fills, orders))
		}
		if p.Status == "open" {
			t.NetPnL = t.NetPnL.Sub(remainingFee)
		}
		t.GrossPnL = t.NetPnL.Add(t.Fees).Sub(t.Funding)
		var last time.Time
		for _, fill := range in.Fills {
			if fill.PositionID != p.ID {
				continue
			}
			t.IsolatedDeficit = t.IsolatedDeficit.Add(fill.IsolatedDeficit)
			if fill.ExecutedAt.After(last) {
				last = fill.ExecutedAt
			}
			o := orders[fill.OrderID]
			if o.Request.Action == "open" && o.Request.StopPrice != "" {
				stop, e := domain.ParseDecimal(o.Request.StopPrice)
				if e == nil {
					risk := p.EntryPrice.Sub(stop).Abs().Mul(p.InitialQuantity)
					if risk.IsPositive() {
						t.PlannedRisk = ptr(risk)
						if target, e := domain.ParseDecimal(o.Request.TargetPrice); e == nil {
							t.PlannedR = ptr(target.Sub(p.EntryPrice).Abs().Mul(p.InitialQuantity).Div(risk))
						}
					}
				}
			}
		}
		t.GrossPnL = t.GrossPnL.Sub(t.IsolatedDeficit)
		if p.Status == "closed" {
			r.ClosedTrades++
			if !last.IsZero() {
				t.ClosedAt = &last
			}
			if !last.IsZero() && !p.OpenedAt.IsZero() && !last.Before(p.OpenedAt) {
				t.HoldingSeconds = ptr(decimal.NewFromInt(last.Sub(p.OpenedAt).Nanoseconds()).Shift(-9))
				holding = holding.Add(*t.HoldingSeconds)
				holdingKnown++
			}
			if t.PlannedRisk != nil {
				t.RealizedR = ptr(t.NetPnL.Div(*t.PlannedRisk))
			}
			if t.NetPnL.IsPositive() {
				r.Wins++
				win = win.Add(t.NetPnL)
			} else if t.NetPnL.IsNegative() {
				r.Losses++
				loss = loss.Sub(t.NetPnL)
			} else {
				r.Breakeven++
			}
		} else {
			r.OpenPositions++
		}
		r.IsolatedDeficit = r.IsolatedDeficit.Add(t.IsolatedDeficit)
		r.GrossPnL = r.GrossPnL.Add(t.GrossPnL)
		r.NetPnL = r.NetPnL.Add(t.NetPnL)
		r.Fees = r.Fees.Add(t.Fees)
		r.Funding = r.Funding.Add(t.Funding)
		excursions(&t, p, in.Candles)
		r.Trades = append(r.Trades, t)
	}
	sort.Slice(r.Trades, func(i, j int) bool {
		if r.Trades[i].OpenedAt.Equal(r.Trades[j].OpenedAt) {
			return r.Trades[i].PositionID < r.Trades[j].PositionID
		}
		return r.Trades[i].OpenedAt.Before(r.Trades[j].OpenedAt)
	})
	if r.ClosedTrades > 0 {
		n := integer(r.ClosedTrades)
		r.WinRate = ptr(integer(r.Wins).Div(n))
		r.LossRate = ptr(integer(r.Losses).Div(n))
		r.BreakevenRate = ptr(integer(r.Breakeven).Div(n))
		r.Expectancy = ptr(win.Sub(loss).Div(n))
		if holdingKnown == r.ClosedTrades {
			r.AverageHoldingSeconds = ptr(holding.Div(n))
		}
		r.ProfitFactorStatus = "no_losses"
		if loss.IsPositive() {
			r.ProfitFactor = ptr(win.Div(loss))
			r.ProfitFactorStatus = "defined"
		} else if win.IsZero() {
			r.ProfitFactorStatus = "no_wins_or_losses"
		}
	}
	if r.Wins > 0 {
		r.AverageWin = ptr(win.Div(integer(r.Wins)))
	}
	if r.Losses > 0 {
		r.AverageLoss = ptr(loss.Neg().Div(integer(r.Losses)))
	}
	for _, l := range in.Ledger {
		r.LedgerTotal = r.LedgerTotal.Add(l.Amount)
		if l.Kind == "capital_deposit" {
			r.CapitalDeposits = r.CapitalDeposits.Add(l.Amount)
		}
		if l.Kind == "capital_withdraw" {
			r.CapitalWithdrawals = r.CapitalWithdrawals.Sub(l.Amount)
		}
		if l.Kind == "fee" {
			r.LedgerFees = r.LedgerFees.Sub(l.Amount)
		}
		if l.Kind == "funding" {
			r.LedgerFunding = r.LedgerFunding.Add(l.Amount)
		}
	}
	r.CashBalance = in.CashBalance
	if in.CashBalance != nil {
		same := r.LedgerTotal.Equal(*in.CashBalance)
		r.LedgerCashReconciled = &same
	}
	if f.StrategyVersion != "" || f.Instrument != nil || !f.From.IsZero() || !f.To.IsZero() {
		r.Warnings = append(r.Warnings, "Lifecycle filters select position opening times; account equity and ledger are unavailable for a filtered subset")
	} else {
		r.Equity = append(r.Equity, in.Equity...)
		equityMetrics(&r, in.InitialCapital)
	}
	r.ReturnStatus = "available_without_external_flows"
	if r.Return == nil {
		r.ReturnStatus = "unavailable"
	}
	if !r.CapitalDeposits.IsZero() || !r.CapitalWithdrawals.IsZero() {
		r.Return = nil
		r.MaximumDrawdown = nil
		r.MaximumDrawdownFraction = nil
		r.ReturnStatus = "unavailable_external_capital_flows"
		r.Warnings = append(r.Warnings, "External simulated capital flows are reported separately from trading P&L; return and drawdown are unavailable without cash-flow-timed equity history")
	}
	r.Warnings = append(r.Warnings, "Gross/net totals include realized portions and charged entry costs of open positions; outcome rates and expectancy use closed lifecycles only", "Ledger totals are account-wide cash movements including opening capital and collateral, not trade P&L")
	return r, nil
}
func equityMetrics(r *Report, capital *domain.Decimal) {
	if len(r.Equity) == 0 {
		return
	}
	peak, dd, fraction := domain.Decimal{}, domain.Decimal{}, domain.Decimal{}
	complete := true
	for _, p := range r.Equity {
		if p.Equity == nil {
			complete = false
			continue
		}
		if p.Equity.GreaterThan(peak) {
			peak = *p.Equity
		}
		dip := peak.Sub(*p.Equity)
		if dip.GreaterThan(dd) {
			dd = dip
		}
		if peak.IsPositive() {
			v := dip.Div(peak)
			if v.GreaterThan(fraction) {
				fraction = v
			}
		}
	}
	if !complete {
		r.ValuationStatus = "incomplete"
		r.Warnings = append(r.Warnings, "Unavailable/stale equity points prevent authoritative return and maximum drawdown")
		return
	}
	if len(r.Equity) == 1 {
		r.ValuationStatus = "snapshot_only"
		if capital != nil && capital.IsPositive() {
			r.Return = ptr(r.Equity[0].Equity.Sub(*capital).Div(*capital))
		}
		return
	}
	r.ValuationStatus = "complete"
	r.MaximumDrawdown = ptr(dd)
	r.MaximumDrawdownFraction = ptr(fraction)
	if capital != nil && capital.IsPositive() {
		r.Return = ptr(r.Equity[len(r.Equity)-1].Equity.Sub(*capital).Div(*capital))
	}
}

// excursions uses only fully held candles before the final-exit bar. Values
// describe initial-size price excursions, not cash P&L after partial exits.
func excursions(t *Trade, p paper.Position, bars []domain.Candle) {
	mae, mfe := domain.Decimal{}, domain.Decimal{}
	count := 0
	for _, bar := range bars {
		if bar.Instrument != p.Instrument || bar.OpenTime.Before(p.OpenedAt) || t.ClosedAt != nil && !bar.EndTime.Before(*t.ClosedAt) {
			continue
		}
		if !bar.High.IsPositive() || !bar.Low.IsPositive() {
			continue
		}
		count++
		low, high := p.PricePnL(bar.Low, p.InitialQuantity), p.PricePnL(bar.High, p.InitialQuantity)
		if low.GreaterThan(high) {
			low, high = high, low
		}
		if low.LessThan(mae) {
			mae = low
		}
		if high.GreaterThan(mfe) {
			mfe = high
		}
	}
	if count > 0 {
		t.MAE = ptr(mae)
		t.MFE = ptr(mfe)
		t.ExcursionBasis = "OHLC fully-held-bar bounds; initial quantity, excluding exit bar; partial-size/path/order unknown"
	}
}

// remainingSpotPrincipal follows the engine's proportional 18-place cost
// allocation over settled notionals. Recomputing price*remaining quantity would
// count unsettled sub-atto products as fees or gains.
func remainingSpotPrincipal(p paper.Position, fills []paper.Fill, orders map[string]paper.Order) domain.Decimal {
	principal := p.EntryPrice.Mul(p.RemainingQuantity).RoundFloor(18)
	ordered := []paper.Fill{}
	for _, f := range fills {
		if f.PositionID == p.ID {
			ordered = append(ordered, f)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ExecutedAt.Before(ordered[j].ExecutedAt) })
	quantity := domain.Decimal{}
	found := false
	for _, f := range ordered {
		action := orders[f.OrderID].Request.Action
		if action == "open" && f.Notional.IsPositive() && f.Quantity.IsPositive() {
			principal = f.Notional
			quantity = f.Quantity
			found = true
			continue
		}
		if !found || action != "close" {
			continue
		}
		if f.Quantity.Equal(quantity) {
			principal = domain.Decimal{}
			quantity = domain.Decimal{}
			continue
		}
		if quantity.IsPositive() {
			allocated := principal.Mul(f.Quantity).DivRound(quantity, 18)
			principal = principal.Sub(allocated)
			quantity = quantity.Sub(f.Quantity)
		}
	}
	return principal
}
