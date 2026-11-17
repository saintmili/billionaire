package postgres_test

import (
	"context"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/paper"
	"reflect"
	"testing"
	"time"
)

func TestTradeReportReconcilesPartialExitsAndReadsNeverChangeFinancialState(t *testing.T) {
	ctx := context.Background()
	s, owner, a, _ := newSpotTest(t)
	request := spotRequest(a.ID, "entry")
	o, e := owner.SubmitOrder(ctx, request, spotInfo(), true)
	if e != nil {
		t.Fatal(e)
	}
	obs, gate := spotObservation(1)
	if _, e = owner.Observe(ctx, obs, gate); e != nil {
		t.Fatal(e)
	}
	id := paper.Identity("position", o.ID)
	for _, step := range []struct {
		id, price string
		second    int
	}{{"partial", "110", 2}, {"final", "120", 4}} {
		request.RequestID = step.id
		request.Action = "close"
		request.PositionID = id
		request.Quantity = "1"
		request.At = spotRequest(a.ID, "").At.Add(time.Duration(step.second) * time.Second)
		if _, e = owner.SubmitOrder(ctx, request, spotInfo(), false); e != nil {
			t.Fatal(e)
		}
		obs, gate = spotObservation(step.second + 1)
		obs.Price = spotDecimal(step.price)
		if _, e = owner.Observe(ctx, obs, gate); e != nil {
			t.Fatal(e)
		}
	}
	before, e := s.PaperLedger(ctx, a.ID)
	if e != nil {
		t.Fatal(e)
	}
	for range 2 {
		r, e := s.PaperTrades(ctx, analytics.Filter{AccountID: a.ID}, gate.Now, time.Minute)
		if e != nil {
			t.Fatal(e)
		}
		// 200 initial principal, proceeds 110+120, fees .2+.11+.12 = .43.
		if r.Scope != "forward-paper" || r.ClosedTrades != 1 || r.GrossPnL.String() != "30" || r.NetPnL.String() != "29.57" || r.Fees.String() != "0.43" || r.LedgerTotal.String() != "1029.57" || r.LedgerFees.String() != "0.43" {
			t.Fatalf("report: %+v", r)
		}
		if r.Trades[0].PlannedRisk != nil || r.MaximumDrawdown != nil || r.ValuationStatus != "snapshot_only" {
			t.Fatalf("missing evidence invented: %+v", r)
		}
	}
	after, e := s.PaperLedger(ctx, a.ID)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("report changed ledger", e)
	}
	cash, e := s.PaperAccount(ctx, a.ID)
	if e != nil || cash.Balance.String() != "1029.57" {
		t.Fatalf("cash: %+v %v", cash, e)
	}
}
func TestOpenTradeReportLabelsStaleValuationAndExposure(t *testing.T) {
	ctx := context.Background()
	s, owner, a, _ := newSpotTest(t)
	request := spotRequest(a.ID, "entry")
	if _, e := owner.SubmitOrder(ctx, request, spotInfo(), true); e != nil {
		t.Fatal(e)
	}
	obs, gate := spotObservation(1)
	if _, e := owner.Observe(ctx, obs, gate); e != nil {
		t.Fatal(e)
	}
	r, e := s.PaperTrades(ctx, analytics.Filter{AccountID: a.ID}, gate.Now, time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if r.OpenPositions != 1 || r.ClosedTrades != 0 || r.NetPnL.String() != "-0.2" || r.Equity[0].Equity.String() != "999.8" || r.Equity[0].Exposure.String() != "200" {
		t.Fatalf("open: %+v", r)
	}

	obs, gate = spotObservation(3)
	obs.Bar = &paper.PriceBar{Start: gate.Now.Add(-time.Second), End: gate.Now, Open: spotDecimal("100"), Close: spotDecimal("100"), High: spotDecimal("120"), Low: spotDecimal("90")}
	if _, e = owner.Observe(ctx, obs, gate); e != nil {
		t.Fatal(e)
	}
	observed, e := s.PaperTrades(ctx, analytics.Filter{AccountID: a.ID}, gate.Now, time.Minute)
	if e != nil || observed.Trades[0].MAE == nil || observed.Trades[0].MAE.String() != "-20" || observed.Trades[0].MFE.String() != "40" {
		t.Fatalf("retained forward observation bounds %+v %v", observed, e)
	}
	stale, e := s.PaperTrades(ctx, analytics.Filter{AccountID: a.ID}, gate.Now.Add(2*time.Minute), time.Minute)
	if e != nil || stale.Return != nil || stale.Equity[0].Equity != nil || stale.Equity[0].Exposure != nil || stale.ValuationStatus != "incomplete" {
		t.Fatalf("stale: %+v %v", stale, e)
	}
}
