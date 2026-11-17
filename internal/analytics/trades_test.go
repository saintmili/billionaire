package analytics_test

import (
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"testing"
	"time"
)

func d(s string) domain.Decimal {
	v, e := domain.ParseDecimal(s)
	if e != nil {
		panic(e)
	}
	return v
}
func TestPartialExitsAreOneTradeWithExactFeesAndFunding(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Two units at 100; one exits 110, one 120. Fees 2+1.1+1.2; funding -3.
	// Gross=30, net=22.7, initial stop risk=20, realized R=1.135.
	p := paper.Position{ID: "p", AccountID: "a", Status: "closed", OpenedAt: at, InitialQuantity: d("2"), EntryPrice: d("100"), RealizedPnL: d("22.7"), FeesPaid: d("4.3"), FundingPnL: d("-3")}
	in := analytics.Input{Scope: "backtest", Positions: []paper.Position{p}, Orders: []paper.Order{{ID: "entry", Request: paper.OrderRequest{Action: "open", StopPrice: "90", TargetPrice: "120"}}}, Fills: []paper.Fill{{OrderID: "entry", PositionID: "p", ExecutedAt: at}, {PositionID: "p", ExecutedAt: at.Add(time.Hour)}, {PositionID: "p", ExecutedAt: at.Add(2 * time.Hour)}}}
	r, e := analytics.Trades(in, analytics.Filter{})
	if e != nil {
		t.Fatal(e)
	}
	if r.ClosedTrades != 1 || r.Wins != 1 || r.NetPnL.String() != "22.7" || r.GrossPnL.String() != "30" || r.Fees.String() != "4.3" || r.Funding.String() != "-3" {
		t.Fatalf("metrics: %+v", r)
	}
	if r.ProfitFactor != nil || r.ProfitFactorStatus != "no_losses" || r.Trades[0].RealizedR == nil || r.Trades[0].RealizedR.String() != "1.135" || r.AverageHoldingSeconds.String() != "7200" {
		t.Fatalf("boundaries: %+v", r)
	}
}
func TestEquityUsesObservedPeaksAndRejectsIncompleteValuations(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	capital := d("1000")
	a, b, c := d("1000"), d("1200"), d("900")
	in := analytics.Input{Scope: "backtest", InitialCapital: &capital, Equity: []analytics.EquityPoint{{At: at, Equity: &a}, {At: at.Add(time.Hour), Equity: &b}, {At: at.Add(2 * time.Hour), Equity: &c}}}
	r, e := analytics.Trades(in, analytics.Filter{})
	if e != nil {
		t.Fatal(e)
	}
	if r.Return == nil || r.Return.String() != "-0.1" || r.MaximumDrawdown.String() != "300" || r.MaximumDrawdownFraction.String() != "0.25" {
		t.Fatalf("equity: %+v", r)
	}
	in.Equity[1].Equity = nil
	in.Equity[1].Reason = "stale"
	r, e = analytics.Trades(in, analytics.Filter{})
	if e != nil || r.Return != nil || r.MaximumDrawdown != nil || r.ValuationStatus != "incomplete" {
		t.Fatalf("stale: %+v %v", r, e)
	}
}
func TestNoTradesAndMissingRiskRemainUndefined(t *testing.T) {
	r, e := analytics.Trades(analytics.Input{Scope: "forward-paper"}, analytics.Filter{})
	if e != nil || r.Expectancy != nil || r.WinRate != nil || r.ProfitFactor != nil || r.ValuationStatus != "unavailable" {
		t.Fatalf("empty: %+v %v", r, e)
	}
}
func TestVersionFilterUsesImmutableSignalAndCandleExcursionsAreBounds(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := paper.Position{ID: "p", StrategyID: "stable", Status: "closed", OpenedAt: at, EntryPrice: d("100"), InitialQuantity: d("2")}
	in := analytics.Input{Scope: "backtest", Positions: []paper.Position{p}, Signals: []strategy.Signal{{ID: "s", VersionID: "v1"}}, Orders: []paper.Order{{ID: "o", Request: paper.OrderRequest{SignalID: "s", Action: "open"}}}, Fills: []paper.Fill{{OrderID: "o", PositionID: "p", ExecutedAt: at}, {PositionID: "p", ExecutedAt: at.Add(2 * time.Minute)}}, Candles: []domain.Candle{{OpenTime: at, EndTime: at.Add(time.Minute), High: d("120"), Low: d("90")}, {OpenTime: at.Add(time.Minute), EndTime: at.Add(2 * time.Minute), High: d("999"), Low: d("1")}}}
	r, e := analytics.Trades(in, analytics.Filter{StrategyVersion: "v1"})
	if e != nil || r.ClosedTrades != 1 {
		t.Fatalf("version: %+v %v", r, e)
	}
	trade := r.Trades[0]
	if trade.MAE == nil || trade.MAE.String() != "-20" || trade.MFE.String() != "40" || trade.StrategyVersion != "v1" {
		t.Fatalf("bounds: %+v", trade)
	}
}
func TestLossBreakevenAndIsolatedWriteoffHaveDefinedDenominators(t *testing.T) {
	in := analytics.Input{Scope: "backtest", Positions: []paper.Position{{ID: "win", Status: "closed", RealizedPnL: d("20")}, {ID: "loss", Status: "closed", RealizedPnL: d("-10")}, {ID: "flat", Status: "closed"}}}
	r, e := analytics.Trades(in, analytics.Filter{})
	if e != nil {
		t.Fatal(e)
	}
	if r.ClosedTrades != 3 || r.Wins != 1 || r.Losses != 1 || r.Breakeven != 1 || r.WinRate.String() != "0.3333333333333333" || r.ProfitFactor.String() != "2" || r.AverageWin.String() != "20" || r.AverageLoss.String() != "-10" || r.Expectancy.String() != "3.3333333333333333" || r.AverageHoldingSeconds != nil {
		t.Fatalf("denominators %+v", r)
	}
	in.Positions = []paper.Position{{ID: "gap", Status: "closed", RealizedPnL: d("-25.245"), FeesPaid: d("1.37"), FundingPnL: d("1.5")}}
	in.Fills = []paper.Fill{{PositionID: "gap", IsolatedDeficit: d("44.625")}}
	r, e = analytics.Trades(in, analytics.Filter{})
	if e != nil || r.GrossPnL.String() != "-70" || r.NetPnL.String() != "-25.245" || r.IsolatedDeficit.String() != "44.625" {
		t.Fatalf("writeoff %+v %v", r, e)
	}
}
func TestOpenEntryCostsUseSettledNotionalAtFinancialPrecision(t *testing.T) {
	// Price*quantity has 36 places but the engine settles notional to 18.
	// The unsettled fraction is not an offset to the charged .001 fee.
	p := paper.Position{ID: "p", Status: "open", Instrument: domain.Instrument{Market: domain.Spot}, EntryPrice: d("0.333333333333333333"), InitialQuantity: d("0.333333333333333333"), RemainingQuantity: d("0.333333333333333333"), RemainingCost: d("0.11211111111111111"), FeesPaid: d("0.001")}
	in := analytics.Input{Scope: "backtest", Positions: []paper.Position{p}, Orders: []paper.Order{{ID: "o", Request: paper.OrderRequest{Action: "open"}}}, Fills: []paper.Fill{{OrderID: "o", PositionID: "p", Quantity: d("0.333333333333333333"), Notional: d("0.11111111111111111"), Fee: d("0.001")}}}
	r, e := analytics.Trades(in, analytics.Filter{})
	if e != nil || r.NetPnL.String() != "-0.001" || r.GrossPnL.String() != "0" {
		t.Fatalf("settled costs %+v %v", r, e)
	}
}
