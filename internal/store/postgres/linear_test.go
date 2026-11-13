package postgres_test

import (
	"context"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"sync"
	"testing"
	"time"
)

func TestLinearExecutionPersistsMarginAndReconciles(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			info := spotInfo()
			info.Instrument.Market = domain.Linear
			request := spotRequest(account.ID, "entry")
			request.Instrument = info.Instrument
			request.Side = side
			request.Leverage = 5
			order, err := owner.SubmitOrder(ctx, request, info, true)
			if err != nil || order.Status != "pending" {
				t.Fatalf("submit: %+v %v", order, err)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			for range 2 {
				if _, err := owner.Observe(ctx, obs, gate); err != nil {
					t.Fatal(err)
				}
			}
			owner.Close()
			owner, err = store.AcquirePaperOwner(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			position, err := store.PaperPosition(ctx, paper.Identity("position", order.ID))
			if err != nil {
				t.Fatal(err)
			}
			if position.ReservedMargin.String() != "40" || position.Direction() != side || position.EffectiveLeverage() != 5 {
				t.Fatalf("position: %+v", position)
			}
			saved, err := store.PaperAccount(ctx, account.ID)
			if err != nil || saved.Balance.String() != "959.8" {
				t.Fatalf("cash: %+v %v", saved, err)
			}
			for _, step := range []struct {
				id, quantity, price string
				second              int
			}{{"partial", "0.5", "110", 2}, {"final", "1.5", "90", 4}} {
				request.RequestID = step.id
				request.Action = "close"
				request.PositionID = position.ID
				request.Quantity = step.quantity
				request.At = spotRequest(account.ID, "unused").At.Add(time.Duration(step.second) * time.Second)
				if _, err := owner.SubmitOrder(ctx, request, info, false); err != nil {
					t.Fatal(err)
				}
				obs, gate = spotObservation(step.second + 1)
				obs.Instrument = info.Instrument
				obs.Price = spotDecimal(step.price)
				gate.Enabled = false
				for range 2 {
					result, err := owner.Observe(ctx, obs, gate)
					if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
						t.Fatalf("exit: %+v %v", result, err)
					}
				}
			}
			position, err = store.PaperPosition(ctx, position.ID)
			if err != nil || position.Status != "closed" || !position.ReservedMargin.IsZero() {
				t.Fatalf("closed: %+v %v", position, err)
			}
			want := "989.61"
			if side == "short" {
				want = "1009.61"
			}
			saved, err = store.PaperAccount(ctx, account.ID)
			if err != nil || saved.Balance.String() != want {
				t.Fatalf("settlement: %+v %v", saved, err)
			}
			ledger, err := store.PaperLedger(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			total := spotDecimal("0")
			for _, e := range ledger {
				total = total.Add(e.Amount)
			}
			if !total.Equal(saved.Balance) {
				t.Fatalf("ledger=%s cash=%s", total, saved.Balance)
			}
		})
	}
}

func TestLinearAccountValuationAndFreshness(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	r := spotRequest(account.ID, "short")
	r.Instrument = info.Instrument
	r.Side = "short"
	r.Leverage = 5
	if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	obs, gate = spotObservation(2)
	obs.Instrument = info.Instrument
	obs.Price = spotDecimal("90")
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	v, err := store.PaperAccountValuation(ctx, account.ID, gate.Now, time.Minute)
	if err != nil || v.Equity == nil || v.Equity.String() != "1019.8" || v.ReservedMargin.String() != "40" || v.FreeMargin.String() != "979.8" || v.UnrealizedPnL.String() != "20" || v.RealizedPnL.String() != "-0.2" {
		t.Fatalf("valuation: %+v %v", v, err)
	}
	v, err = store.PaperAccountValuation(ctx, account.ID, gate.Now.Add(2*time.Minute), time.Minute)
	if err != nil || v.Equity != nil || v.FreeMargin != nil || v.UnrealizedPnL != nil || v.Reason == "" {
		t.Fatalf("stale valuation: %+v %v", v, err)
	}
}

func TestLinearConcurrentEntriesCannotOversubscribeCollateral(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{Pyramiding: true}); err != nil {
		t.Fatal(err)
	}
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		wg.Go(func() {
			r := spotRequest(account.ID, id)
			r.Instrument = info.Instrument
			r.Leverage = 5
			r.Quantity = "30"
			_, err := owner.SubmitOrder(ctx, r, info, true)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, o := range result.Orders {
		counts[o.Status]++
	}
	if counts["filled"] != 1 || counts["rejected"] != 1 {
		t.Fatalf("collateral oversubscribed: %+v", result)
	}
	saved, err := store.PaperAccount(ctx, account.ID)
	if err != nil || saved.Balance.String() != "397" {
		t.Fatalf("cash: %+v %v", saved, err)
	}
}

func TestLinearPolicyLimitsAndExecutionTimeSizing(t *testing.T) {
	for _, test := range []struct {
		name     string
		policy   paper.RiskPolicy
		quantity string
		reason   string
	}{
		{"max leverage", paper.RiskPolicy{MaxLeverage: 4}, "2", "maximum leverage exceeded"},
		{"margin boundary", paper.RiskPolicy{MarginPercent: "4"}, "2", ""},
		{"margin breach", paper.RiskPolicy{MarginPercent: "3.99"}, "2", "margin usage exceeded"},
		{"sizing", paper.RiskPolicy{FixedRisk: "20"}, "risk", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			if err := store.SetRiskPolicy(ctx, account.ID, "", test.policy); err != nil {
				t.Fatal(err)
			}
			info := spotInfo()
			info.Instrument.Market = domain.Linear
			r := spotRequest(account.ID, "entry")
			r.Instrument = info.Instrument
			r.Side = "short"
			r.Leverage = 5
			r.StopPrice = "110"
			r.FeeRate = "0"
			r.Quantity = test.quantity
			if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			result, err := owner.Observe(ctx, obs, gate)
			if err != nil || len(result.Orders) != 1 {
				t.Fatalf("observe: %+v %v", result, err)
			}
			order := result.Orders[0]
			if order.Reason != test.reason {
				t.Fatalf("reason=%q want=%q", order.Reason, test.reason)
			}
			if test.reason == "" {
				positions, err := store.PaperPositions(ctx, account.ID)
				if err != nil || len(positions) != 1 || positions[0].RemainingQuantity.String() != "2" {
					t.Fatalf("positions: %+v %v", positions, err)
				}
			} else {
				fills, err := store.PaperFills(ctx, account.ID)
				if err != nil || len(fills) != 0 {
					t.Fatalf("rejection filled: %+v %v", fills, err)
				}
			}
		})
	}
}

func TestLinearShortProtectiveStopWinsAmbiguousBarWhilePaused(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	r := spotRequest(account.ID, "entry")
	r.Instrument = info.Instrument
	r.Side = "short"
	r.Leverage = 5
	r.StopPrice = "110"
	r.TargetPrice = "90"
	if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	if err := store.SetKillSwitch(ctx, true); err != nil {
		t.Fatal(err)
	}
	obs, gate = spotObservation(3)
	obs.Instrument = info.Instrument
	gate.Enabled = false
	obs.Bar = &paper.PriceBar{Start: obs.ObservedAt.Add(-time.Second), End: obs.ObservedAt, Open: spotDecimal("100"), High: spotDecimal("115"), Low: spotDecimal("85"), Close: obs.Price}
	for range 2 {
		result, err := owner.Observe(ctx, obs, gate)
		if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
			t.Fatalf("protection: %+v %v", result, err)
		}
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(positions) != 1 || positions[0].Status != "closed" || positions[0].RealizedPnL.String() != "-20.42" || !positions[0].ReservedMargin.IsZero() {
		t.Fatalf("position: %+v %v", positions, err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 2 {
		t.Fatal(err)
	}
	found := false
	for _, f := range fills {
		found = found || (f.ExitReason == "stop_loss" && f.Ambiguous && f.Price.String() == "110")
	}
	if !found {
		t.Fatalf("fills: %+v", fills)
	}
	saved, err := store.PaperAccount(ctx, account.ID)
	if err != nil || saved.Balance.String() != "979.58" {
		t.Fatalf("cash: %+v %v", saved, err)
	}
}

func TestSpotAndLinearPricesStayDistinctAndStaleCollateralRejectsEntry(t *testing.T) {
	ctx := context.Background()
	_, owner, account, _ := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	r := spotRequest(account.ID, "linear")
	r.Instrument = info.Instrument
	r.Leverage = 5
	if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 0 {
		t.Fatalf("spot price filled linear order: %+v %v", result, err)
	}
	obs.Instrument = info.Instrument
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	r = spotRequest(account.ID, "spot")
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate = spotObservation(120)
	result, err = owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "rejected" || result.Orders[0].Reason != "valuation source stale/unready" {
		t.Fatalf("stale collateral: %+v %v", result, err)
	}
}

func TestLinearShortExactCollateralUsesSameRoundingAsFill(t *testing.T) {
	ctx := context.Background()
	_, owner, _, _ := newSpotTest(t)
	account, err := owner.CreateAccount(ctx, paper.CreateAccount{RequestID: "exact", Name: "exact", Currency: "USDT", InitialBalance: "5.005"})
	if err != nil {
		t.Fatal(err)
	}
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	info.PriceStep = spotDecimal("0.000000000000000001")
	r := spotRequest(account.ID, "exact-short")
	r.Instrument = info.Instrument
	r.Side = "short"
	r.Quantity = "0.001"
	if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	obs.Price = spotDecimal("5000.000000000000000001")
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
		t.Fatalf("exact short rejected: %+v %v", result, err)
	}
}

func TestLinearEntryRejectsImmediateSlippageMarginDeficit(t *testing.T) {
	for _, test := range []struct {
		name, side, quantity, limit, reason string
		leverage                            int
	}{
		{"long free margin", "long", "90", "100", "insufficient projected free margin", 10},
		{"short free margin", "short", "90", "100", "insufficient projected free margin", 10},
		{"projected margin usage", "long", "10", "23", "margin usage exceeded", 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{MarginPercent: test.limit}); err != nil {
				t.Fatal(err)
			}
			info := spotInfo()
			info.Instrument.Market = domain.Linear
			r := spotRequest(account.ID, "slippage-deficit")
			r.Instrument = info.Instrument
			r.Side = test.side
			r.Leverage = test.leverage
			r.Quantity = test.quantity
			r.SlippageBPS = "1000"
			r.FeeRate = "0"
			if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			result, err := owner.Observe(ctx, obs, gate)
			if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "rejected" || result.Orders[0].Reason != test.reason {
				t.Fatalf("entry creates deficit: %+v %v", result, err)
			}
			fills, err := store.PaperFills(ctx, account.ID)
			if err != nil || len(fills) != 0 {
				t.Fatalf("deficit entry filled: %+v %v", fills, err)
			}
		})
	}
}
