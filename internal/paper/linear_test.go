package paper_test

import (
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"testing"
	"time"
)

func TestLinearMarginAndSettlement(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			info := metadata()
			info.Instrument.Market = domain.Linear
			at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
			r := paper.OrderRequest{RequestID: "entry", AccountID: "a", Action: "open", Instrument: info.Instrument, Side: side, Leverage: 5, Quantity: "2", At: at, FeeRate: "0.001", SlippageBPS: "0"}
			order, err := paper.PrepareOrder(r, info)
			if err != nil {
				t.Fatal(err)
			}
			obs := paper.Observation{ID: "p1", Instrument: info.Instrument, Source: "fixture", Price: amount("100"), ObservedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Ready: true}
			gate := paper.ExecutionGate{Now: obs.AvailableAt, StaleAfter: time.Minute, Enabled: true}
			entry, err := paper.PlanFill(order, obs, gate, amount("1000"), nil)
			if err != nil || entry.Fill == nil {
				t.Fatalf("entry: %+v %v", entry, err)
			}
			if entry.CashDelta.String() != "-40.2" {
				t.Fatalf("margin and fee: %s", entry.CashDelta)
			}
			r.Action = "close"
			r.RequestID = "partial"
			r.PositionID = entry.Position.ID
			r.Quantity = "0.5"
			r.At = at.Add(2 * time.Second)
			closeOrder, err := paper.PrepareOrder(r, info)
			if err != nil {
				t.Fatal(err)
			}
			obs.Price = amount("110")
			obs.ObservedAt = at.Add(3 * time.Second)
			obs.AvailableAt = obs.ObservedAt
			gate.Now = obs.AvailableAt
			partial, err := paper.PlanFill(closeOrder, obs, gate, amount("959.8"), entry.Position)
			wantCash, wantPnL := "14.945", "4.895"
			if side == "short" {
				wantCash, wantPnL = "4.945", "-5.105"
			}
			if err != nil || partial.Fill == nil {
				t.Fatalf("partial: %+v %v", partial, err)
			}
			if partial.CashDelta.String() != wantCash || partial.Fill.RealizedPnL.String() != wantPnL {
				t.Fatalf("partial cash=%s pnl=%s", partial.CashDelta, partial.Fill.RealizedPnL)
			}
			r.RequestID = "final"
			r.Quantity = "1.5"
			r.At = at.Add(4 * time.Second)
			closeOrder, err = paper.PrepareOrder(r, info)
			if err != nil {
				t.Fatal(err)
			}
			obs.Price = amount("90")
			obs.ObservedAt = at.Add(5 * time.Second)
			obs.AvailableAt = obs.ObservedAt
			gate.Now = obs.AvailableAt
			final, err := paper.PlanFill(closeOrder, obs, gate, amount("1000"), partial.Position)
			wantPnL = "-10.39"
			if side == "short" {
				wantPnL = "9.61"
			}
			if err != nil || final.Fill == nil || final.Position.Status != "closed" || !final.Position.RemainingCost.IsZero() || final.Position.RealizedPnL.String() != wantPnL {
				t.Fatalf("final: %+v %v", final, err)
			}
		})
	}
}

func TestLinearShortProtectionAndAdversePrice(t *testing.T) {
	info := metadata()
	info.Instrument.Market = domain.Linear
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	r := paper.OrderRequest{RequestID: "short", AccountID: "a", Action: "open", Instrument: info.Instrument, Side: "short", Leverage: 5, Quantity: "2", At: at, FeeRate: "0", SlippageBPS: "100", StopPrice: "110", TargetPrice: "80", TrailActivation: "90", TrailDistance: "5"}
	order, err := paper.PrepareOrder(r, info)
	if err != nil {
		t.Fatal(err)
	}
	obs := paper.Observation{ID: "p1", Instrument: info.Instrument, Source: "fixture", Price: amount("100.001"), ObservedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Ready: true}
	gate := paper.ExecutionGate{Now: obs.AvailableAt, StaleAfter: time.Minute, Enabled: true}
	entry, err := paper.PlanFill(order, obs, gate, amount("1000"), nil)
	if err != nil || entry.Fill == nil || entry.Fill.Price.String() != "99" {
		t.Fatalf("short entry: %+v %v", entry, err)
	}
	obs.Price = amount("85")
	obs.ObservedAt = at.Add(2 * time.Second)
	obs.AvailableAt = obs.ObservedAt
	d, err := paper.EvaluateProtection(*entry.Position, obs)
	if err != nil || d.State.StopPrice != "90" || !d.State.TrailingActive || d.Reason != "" {
		t.Fatalf("trailing: %+v %v", d, err)
	}
	entry.Position.Protection = &d.State
	obs.Price = amount("95")
	obs.ObservedAt = at.Add(3 * time.Second)
	obs.AvailableAt = obs.ObservedAt
	d, err = paper.EvaluateProtection(*entry.Position, obs)
	if err != nil || d.Reason != "trailing_stop" || d.Price.String() != "95" || !d.Gap {
		t.Fatalf("short stop: %+v %v", d, err)
	}
}

func TestLinearShortRiskSizingAndMarginLimit(t *testing.T) {
	p := paper.RiskPolicy{FixedRisk: "20", MarginPercent: "25", Pyramiding: true}
	state := paper.RiskState{Equity: amount("1000"), Side: "short", Exposure: amount("200")}
	if why := paper.EntryRiskReason(p, state, amount("2"), amount("100"), amount("110"), 5, time.Now()); why != "" {
		t.Fatalf("valid short rejected: %s", why)
	}
	state.Exposure = amount("211")
	if why := paper.EntryRiskReason(p, state, amount("2"), amount("100"), amount("110"), 5, time.Now()); why != "margin usage exceeded" {
		t.Fatalf("margin breach: %s", why)
	}
}

func TestLinearInputsAndShortCloseSlippage(t *testing.T) {
	info := metadata()
	info.Instrument.Market = domain.Linear
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	r := paper.OrderRequest{RequestID: "short", AccountID: "a", Action: "open", Instrument: info.Instrument, Side: "short", Leverage: 5, Quantity: "2", At: at, FeeRate: "0.001", SlippageBPS: "100"}
	for _, change := range []func(*paper.OrderRequest){
		func(r *paper.OrderRequest) { r.Leverage = 0 }, func(r *paper.OrderRequest) { r.Leverage = 101 }, func(r *paper.OrderRequest) { r.Side = "sell" }, func(r *paper.OrderRequest) { r.Quantity = "0.0001" }, func(r *paper.OrderRequest) { r.Quantity = "-1" }, func(r *paper.OrderRequest) { r.Quantity = "NaN" },
	} {
		bad := r
		change(&bad)
		if _, err := paper.PrepareOrder(bad, info); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	order, err := paper.PrepareOrder(r, info)
	if err != nil {
		t.Fatal(err)
	}
	obs := paper.Observation{ID: "entry", Instrument: info.Instrument, Source: "fixture", Price: amount("100"), ObservedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Ready: true}
	gate := paper.ExecutionGate{Now: obs.AvailableAt, StaleAfter: time.Minute, Enabled: true}
	small := order
	small.Request.Quantity = "0.001"
	plan, err := paper.PlanFill(small, obs, gate, amount("1000"), nil)
	if err != nil || plan.Order.Status != "rejected" || plan.Fill != nil {
		t.Fatalf("minimum notional: %+v %v", plan, err)
	}
	entry, err := paper.PlanFill(order, obs, gate, amount("1000"), nil)
	if err != nil || entry.Fill == nil {
		t.Fatalf("entry: %+v %v", entry, err)
	}
	r.Action = "close"
	r.PositionID = entry.Position.ID
	r.RequestID = "close"
	r.At = at.Add(2 * time.Second)
	order, err = paper.PrepareOrder(r, info)
	if err != nil {
		t.Fatal(err)
	}
	obs.Price = amount("100.001")
	obs.ObservedAt = at.Add(3 * time.Second)
	obs.AvailableAt = obs.ObservedAt
	gate.Now = obs.AvailableAt
	plan, err = paper.PlanFill(order, obs, gate, amount("1000"), entry.Position)
	if err != nil || plan.Fill == nil || plan.Fill.Price.String() != "101.01" {
		t.Fatalf("adverse short buy-to-close: %+v %v", plan, err)
	}
}
