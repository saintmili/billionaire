package postgres_test

import (
	"context"
	"fmt"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKillSwitchRejectsPendingEntriesAndSurvivesOwnerRestart(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	request := spotRequest(account.ID, "pending")
	if _, err := owner.SubmitOrder(ctx, request, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetKillSwitch(ctx, true); err != nil {
		t.Fatal(err)
	}
	owner.Close()
	owner, err := store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	obs, gate := spotObservation(1)
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Reason != "kill switch active" {
		t.Fatalf("%+v %v", result, err)
	}
	balance, err := store.PaperAccount(ctx, account.ID)
	if err != nil || balance.Balance.String() != "1000" {
		t.Fatalf("%+v %v", balance, err)
	}
	on, err := store.KillSwitch(ctx)
	if err != nil || !on {
		t.Fatalf("switch %v %v", on, err)
	}
}

func TestExecutionRechecksAccountPolicyAndSizesAtActualPrice(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	request := spotRequest(account.ID, "sized")
	request.Quantity = "risk"
	request.StopPrice = "90"
	if _, err := owner.SubmitOrder(ctx, request, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{FixedRisk: "10"}); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || result.Orders[0].Status != "filled" {
		t.Fatalf("%+v %v", result, err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 1 || fills[0].Quantity.String() != "1" {
		t.Fatalf("%+v %v", fills, err)
	}
	retry, err := owner.SubmitOrder(ctx, request, spotInfo(), true)
	if err != nil || retry.Status != "filled" {
		t.Fatalf("retry %+v %v", retry, err)
	}
}

func TestRiskPoliciesSerializeSharedAccountStrategies(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	raw, err := os.ReadFile("../../../examples/strategies/breakout.yaml")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := strategy.Parse(strings.NewReader(string(raw)), strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		definition.ID = id
		if _, err := store.AddStrategyVersion(ctx, definition, strategy.DefaultRegistry()); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{MaxPositions: 1, Pyramiding: true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := store.SetRiskPolicy(ctx, account.ID, id, paper.RiskPolicy{MaxPositions: 10, Pyramiding: true}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		wg.Go(func() {
			r := spotRequest(account.ID, id)
			r.StrategyID = id
			_, err := owner.SubmitOrder(ctx, r, spotInfo(), true)
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
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil {
		t.Fatal(err)
	}
	filled, rejected := 0, 0
	for _, order := range result.Orders {
		if order.Status == "filled" {
			filled++
		}
		if order.Reason == "maximum positions exceeded" {
			rejected++
		}
	}
	if filled != 1 || rejected != 1 {
		t.Fatalf("%+v", result)
	}
	orders, err := store.PaperOrders(ctx, account.ID)
	if err != nil || len(orders) != 2 {
		t.Fatalf("%+v %v", orders, err)
	}
	for _, r := range orders {
		retry, err := owner.SubmitOrder(ctx, r.Request, spotInfo(), true)
		if err != nil || retry.Status != r.Status {
			t.Fatalf("%+v %v", retry, err)
		}
	}
}

func TestKillSwitchAllowsPartialAndFullCloses(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	r := spotRequest(account.ID, "entry")
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	if err := store.SetKillSwitch(ctx, true); err != nil {
		t.Fatal(err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"partial", "full"} {
		r.RequestID = id
		r.Action = "close"
		r.PositionID = positions[0].ID
		r.Quantity = "1"
		r.At = r.At.Add(time.Second)
		order, err := owner.SubmitOrder(ctx, r, spotInfo(), false)
		if err != nil || order.Status != "pending" {
			t.Fatalf("%+v %v", order, err)
		}
		obs, gate = spotObservation(i + 2)
		gate.Enabled = false
		result, err := owner.Observe(ctx, obs, gate)
		if err != nil || result.Orders[0].Status != "filled" {
			t.Fatalf("%+v %v", result, err)
		}
	}
	positions, err = store.PaperPositions(ctx, account.ID)
	if err != nil || positions[0].Status != "closed" {
		t.Fatalf("%+v %v", positions, err)
	}
}

func TestRiskCooldownAndDailyLossResetAtUTCMidnight(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{Pyramiding: true, DailyLoss: "0.2", CooldownSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	r := spotRequest(account.ID, "first")
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	r.RequestID = "blocked"
	r.At = obs.AvailableAt
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate = spotObservation(2)
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || result.Orders[0].Reason != "daily loss limit reached" {
		t.Fatalf("%+v %v", result, err)
	}
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{Pyramiding: true, CooldownSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	r.RequestID = "cooldown"
	r.At = obs.AvailableAt
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate = spotObservation(3)
	result, err = owner.Observe(ctx, obs, gate)
	if err != nil || result.Orders[0].Reason != "entry cooldown active" {
		t.Fatalf("%+v %v", result, err)
	}
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{Pyramiding: true, DailyLoss: "0.2", CooldownSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	next := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	r.RequestID = "new-day"
	r.At = next.Add(-time.Second)
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs.ID = "midnight"
	obs.ObservedAt = next
	obs.AvailableAt = next
	gate.Now = next
	result, err = owner.Observe(ctx, obs, gate)
	if err != nil || result.Orders[0].Status != "filled" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestKillSwitchRacesSerializeWithExecution(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	for index := range 5 {
		if err := store.SetKillSwitch(ctx, false); err != nil {
			t.Fatal(err)
		}
		if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{Pyramiding: true}); err != nil {
			t.Fatal(err)
		}
		r := spotRequest(account.ID, fmt.Sprintf("race-%d", index))
		r.Quantity = "0.1"
		r.At = r.At.Add(time.Duration(index*2) * time.Second)
		if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
			t.Fatal(err)
		}
		obs, gate := spotObservation(index*2 + 1)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Go(func() { errs <- store.SetKillSwitch(ctx, true) })
		wg.Go(func() { _, err := owner.Observe(ctx, obs, gate); errs <- err })
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		r.RequestID = fmt.Sprintf("after-%d", index)
		order, err := owner.SubmitOrder(ctx, r, spotInfo(), true)
		if err != nil || order.Reason != "kill switch active" {
			t.Fatalf("entry after switch completed: %+v %v", order, err)
		}
	}
}

func TestRiskInvalidGeometryAndMissingStatePersistRejections(t *testing.T) {
	for _, tt := range []struct {
		name, quantity, stop string
		policy               paper.RiskPolicy
		reason               string
	}{
		{"missing policy", "risk", "90", paper.RiskPolicy{}, "risk sizing policy unavailable"},
		{"zero distance", "risk", "100", paper.RiskPolicy{FixedRisk: "10"}, "invalid entry/stop distance or stop precision"},
		{"missing stop", "risk", "", paper.RiskPolicy{FixedRisk: "10"}, "invalid entry/stop distance"},
		{"bad stop precision", "risk", "90.001", paper.RiskPolicy{FixedRisk: "10"}, "invalid entry/stop distance or stop precision"},
		{"manual risk breach", "2", "90", paper.RiskPolicy{FixedRisk: "10"}, "entry risk budget exceeded"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			if err := store.SetRiskPolicy(ctx, account.ID, "", tt.policy); err != nil {
				t.Fatal(err)
			}
			r := spotRequest(account.ID, "request")
			r.Quantity = tt.quantity
			r.StopPrice = tt.stop
			if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			result, err := owner.Observe(ctx, obs, gate)
			if err != nil || result.Orders[0].Reason != tt.reason {
				t.Fatalf("%+v %v", result, err)
			}
			orders, err := store.PaperOrders(ctx, account.ID)
			if err != nil || orders[0].Reason != tt.reason {
				t.Fatalf("persisted %+v %v", orders, err)
			}
			fills, err := store.PaperFills(ctx, account.ID)
			if err != nil || len(fills) != 0 {
				t.Fatalf("rejected fill %+v %v", fills, err)
			}
		})
	}
}

func TestSignalLinkedRiskRejectionRetainsSignalIdentity(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	signal := signalFor(t, store, account.ID, strategy.ManualPaper, 0)
	r := paper.SignalEntryRequest{SignalID: signal.ID, Approve: true, At: signal.ObservedAt, Quantity: "2"}
	d, err := owner.DecideSignal(ctx, r, spotInfo(), true)
	if err != nil || d.Order == nil {
		t.Fatalf("intent: %+v %v", d, err)
	}
	obs, gate := spotObservation(1)
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || result.Orders[0].Reason != "entry risk budget exceeded" {
		t.Fatalf("risk: %+v %v", result, err)
	}
	decisions, err := store.SignalDecisions(ctx)
	if err != nil || len(decisions) != 1 || decisions[0].Order.Request.SignalID != signal.ID || decisions[0].Order.Reason != "entry risk budget exceeded" {
		t.Fatalf("decision: %+v %v", decisions, err)
	}
	signals, err := store.StrategySignals(ctx, strategy.SignalFilter{})
	if err != nil || len(signals) != 1 || signals[0].ID != signal.ID {
		t.Fatalf("signals: %+v %v", signals, err)
	}
	// Risk rejections are final for this observation, including restart/retry.
	retry, err := owner.DecideSignal(ctx, r, spotInfo(), true)
	if err != nil || retry.Order.Status != "rejected" || retry.Order.ID != d.Order.ID {
		t.Fatalf("retry: %+v %v", retry, err)
	}
}

func TestPercentageSizingRejectsStaleValuationOfOtherHoldings(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	r := spotRequest(account.ID, "btc-entry")
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{PercentRisk: "1"}); err != nil {
		t.Fatal(err)
	}
	info := spotInfo()
	info.Instrument.Base = "ETH"
	info.Instrument.ID = "ETHUSDT"
	r.RequestID = "eth-entry"
	r.Instrument = info.Instrument
	r.StopPrice = "90"
	r.Quantity = "risk"
	r.At = r.At.Add(time.Minute)
	if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
		t.Fatal(err)
	}
	obs.ID = "eth-price"
	obs.Instrument = info.Instrument
	obs.ObservedAt = obs.ObservedAt.Add(2 * time.Minute)
	obs.AvailableAt = obs.ObservedAt
	gate.Now = obs.AvailableAt
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || result.Orders[0].Reason != "valuation source stale/unready" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestExecutionFailuresPersistReasonsWithoutFinancialChanges(t *testing.T) {
	for _, tt := range []struct {
		name           string
		ready, enabled bool
		quantity       string
		reason         string
	}{
		{"unready", false, true, "2", "source stale/unready"},
		{"disabled", true, false, "2", "market disabled"},
		{"insufficient funds", true, true, "20", "insufficient USDT funds"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			r := spotRequest(account.ID, "entry")
			r.Quantity = tt.quantity
			if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			obs.Ready = tt.ready
			gate.Enabled = tt.enabled
			result, err := owner.Observe(ctx, obs, gate)
			if err != nil || result.Orders[0].Reason != tt.reason {
				t.Fatalf("%+v %v", result, err)
			}
			orders, err := store.PaperOrders(ctx, account.ID)
			if err != nil || orders[0].Reason != tt.reason {
				t.Fatalf("%+v %v", orders, err)
			}
			saved, err := store.PaperAccount(ctx, account.ID)
			if err != nil || saved.Balance.String() != "1000" {
				t.Fatalf("%+v %v", saved, err)
			}
			fills, err := store.PaperFills(ctx, account.ID)
			if err != nil || len(fills) != 0 {
				t.Fatalf("%+v %v", fills, err)
			}
		})
	}
}
