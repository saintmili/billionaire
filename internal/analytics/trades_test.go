package analytics_test

import (
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
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
