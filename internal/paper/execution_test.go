package paper_test

import (
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
)

func amount(s string) domain.Decimal {
	v, err := domain.ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return v
}
func metadata() marketdata.InstrumentInfo {
	return marketdata.InstrumentInfo{Instrument: domain.Instrument{Provider: "fixture", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}, PriceStep: amount("0.01"), QuantityStep: amount("0.001"), MinQuantity: amount("0.001"), MinNotional: amount("5")}
}

func TestSpotEntryWaitsForLaterPriceAndUsesConservativeCosts(t *testing.T) {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	info := metadata()
	order, err := paper.PrepareOrder(paper.OrderRequest{RequestID: "entry", AccountID: "account", Action: "open", Instrument: info.Instrument, Side: "long", Leverage: 1, Quantity: "2", At: at, FeeRate: "0.001", SlippageBPS: "100"}, info)
	if err != nil {
		t.Fatal(err)
	}
	observation := paper.Observation{ID: "price-1", Instrument: info.Instrument, Source: "fixture", Price: amount("100"), ObservedAt: at, AvailableAt: at, Ready: true}
	gate := paper.ExecutionGate{Now: at, StaleAfter: time.Minute, Enabled: true}
	plan, err := paper.PlanFill(order, observation, gate, amount("1000"), nil)
	if err != nil || plan.Order.Status != "pending" || plan.Fill != nil {
		t.Fatalf("already-known price filled: %+v %v", plan, err)
	}
	observation.ObservedAt = at.Add(time.Second)
	observation.AvailableAt = observation.ObservedAt
	gate.Now = observation.AvailableAt
	plan, err = paper.PlanFill(order, observation, gate, amount("1000"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Order.Status != "filled" || plan.Fill.Price.String() != "101" || plan.Fill.Fee.String() != "0.202" || plan.CashDelta.String() != "-202.202" || plan.Position.RemainingCost.String() != "202.202" || plan.Fill.LatencyMillis != 1000 {
		t.Fatalf("entry: %+v", plan)
	}
}

func TestSpotCloseAllocatesCostAndReconcilesFees(t *testing.T) {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	info := metadata()
	request := paper.OrderRequest{RequestID: "buy", AccountID: "a", Action: "open", Instrument: info.Instrument, Side: "long", Leverage: 1, Quantity: "2", At: at, FeeRate: "0.001", SlippageBPS: "0"}
	order, _ := paper.PrepareOrder(request, info)
	observation := paper.Observation{ID: "p1", Instrument: info.Instrument, Source: "fixture", Price: amount("100"), ObservedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Ready: true}
	gate := paper.ExecutionGate{Now: observation.AvailableAt, StaleAfter: time.Minute, Enabled: true}
	entry, err := paper.PlanFill(order, observation, gate, amount("1000"), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.RequestID = "partial"
	request.Action = "close"
	request.PositionID = entry.Position.ID
	request.Quantity = "0.5"
	request.At = at.Add(2 * time.Second)
	closeOrder, err := paper.PrepareOrder(request, info)
	if err != nil {
		t.Fatal(err)
	}
	observation.ID = "p2"
	observation.Price = amount("110")
	observation.ObservedAt = at.Add(3 * time.Second)
	observation.AvailableAt = observation.ObservedAt
	gate.Now = observation.AvailableAt
	partial, err := paper.PlanFill(closeOrder, observation, gate, amount("799.8"), entry.Position)
	if err != nil || partial.Fill.RealizedPnL.String() != "4.895" || partial.Position.RemainingQuantity.String() != "1.5" || partial.Position.RemainingCost.String() != "150.15" || partial.CashDelta.String() != "54.945" {
		t.Fatalf("partial: %+v %v", partial, err)
	}
	request.RequestID = "final"
	request.Quantity = "1.5"
	request.At = at.Add(4 * time.Second)
	closeOrder, err = paper.PrepareOrder(request, info)
	if err != nil {
		t.Fatal(err)
	}
	observation.ID = "p3"
	observation.Price = amount("90")
	observation.ObservedAt = at.Add(5 * time.Second)
	observation.AvailableAt = observation.ObservedAt
	gate.Now = observation.AvailableAt
	final, err := paper.PlanFill(closeOrder, observation, gate, amount("854.745"), partial.Position)
	if err != nil || final.Position.Status != "closed" || !final.Position.RemainingQuantity.IsZero() || !final.Position.RemainingCost.IsZero() || final.Position.RealizedPnL.String() != "-10.39" || final.Position.FeesPaid.String() != "0.39" || final.CashDelta.String() != "134.865" {
		t.Fatalf("close: %+v %v", final, err)
	}
}

func TestSpotPolicyRejectsUnsafeInputsAndExecution(t *testing.T) {
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	info := metadata()
	request := paper.OrderRequest{RequestID: "entry", AccountID: "a", Action: "open", Instrument: info.Instrument, Side: "long", Leverage: 1, Quantity: "2", At: at, FeeRate: "0.001", SlippageBPS: "0"}
	for _, change := range []func(*paper.OrderRequest){
		func(r *paper.OrderRequest) { r.Side = "short" }, func(r *paper.OrderRequest) { r.Leverage = 2 },
		func(r *paper.OrderRequest) { r.Quantity = "0" }, func(r *paper.OrderRequest) { r.Quantity = "NaN" }, func(r *paper.OrderRequest) { r.Quantity = "0.0001" },
		func(r *paper.OrderRequest) { r.FeeRate = "-1" }, func(r *paper.OrderRequest) { r.SlippageBPS = "10000" },
		func(r *paper.OrderRequest) { r.Instrument.Market = domain.Linear },
	} {
		invalid := request
		change(&invalid)
		if _, err := paper.PrepareOrder(invalid, info); err == nil {
			t.Fatalf("accepted invalid request %+v", invalid)
		}
	}
	missing := info
	missing.PriceStep = amount("0")
	if _, err := paper.PrepareOrder(request, missing); err == nil {
		t.Fatal("missing precision accepted")
	}
	order, _ := paper.PrepareOrder(request, info)
	observation := paper.Observation{ID: "next", Instrument: info.Instrument, Source: "fixture", Price: amount("100"), ObservedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Ready: true}
	gate := paper.ExecutionGate{Now: observation.AvailableAt, StaleAfter: time.Minute, Enabled: true}
	for _, test := range []struct {
		name           string
		balance        string
		enabled, ready bool
		delay          time.Duration
	}{
		{"funds", "200", true, true, 0}, {"disabled", "1000", false, true, 0}, {"unready", "1000", true, false, 0}, {"stale", "1000", true, true, 2 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			g := gate
			g.Enabled = test.enabled
			g.Now = g.Now.Add(test.delay)
			o := observation
			o.Ready = test.ready
			plan, err := paper.PlanFill(order, o, g, amount(test.balance), nil)
			if err != nil || plan.Order.Status != "rejected" || plan.Order.Reason == "" || plan.Fill != nil || !plan.CashDelta.IsZero() {
				t.Fatalf("unsafe execution: %+v %v", plan, err)
			}
		})
	}
	request.Quantity = "0.001"
	small, _ := paper.PrepareOrder(request, info)
	plan, err := paper.PlanFill(small, observation, gate, amount("1000"), nil)
	if err != nil || plan.Order.Status != "rejected" {
		t.Fatalf("minimum notional: %+v %v", plan, err)
	}
	observation.Price = amount("100.001")
	plan, err = paper.PlanFill(order, observation, gate, amount("1000"), nil)
	if err != nil || plan.Fill.Price.String() != "100.01" {
		t.Fatalf("buy tick rounding: %+v %v", plan, err)
	}
	gate.Now = gate.Now.Add(2 * time.Second)
	plan, err = paper.PlanFill(order, observation, gate, amount("1000"), nil)
	if err != nil || plan.Fill.LatencyMillis != 3000 {
		t.Fatalf("processing latency: %+v %v", plan, err)
	}
}
