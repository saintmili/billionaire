package app_test

import (
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
	"testing"
	"time"
)

func TestOwnerHealthRecomputesFreshnessAndNeverTrustsAnEarlierSession(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	p, err := fixture.Open("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	now := p.Now()
	to, _ := domain.Timeframe("1m").Bounds(now)
	feeds := []app.Subscription{{Provider: p, Now: func() time.Time { return now }, Query: marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: to.Add(-time.Minute), To: to}}}
	owner, err := s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	h := app.NewHealth(s, owner, feeds, time.Minute)
	snapshot, err := h.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EntriesReady || snapshot.Feeds[0].Status != "recovering" {
		t.Fatalf("startup trusted persisted readiness: %+v", snapshot)
	}
	// Only the current runtime's processed event can establish readiness.
	h.Processed(marketdata.Event{Feed: marketdata.FeedState{Instrument: info.Instrument, Timeframe: "1m", Connected: true, LastEvent: now, LastClosed: to}}, now)
	snapshot, err = h.Snapshot(ctx)
	if err != nil || !snapshot.EntriesReady {
		t.Fatalf("ready: %+v %v", snapshot, err)
	}
	now = now.Add(2 * time.Minute)
	snapshot, err = h.Snapshot(ctx)
	if err != nil || snapshot.EntriesReady || snapshot.Feeds[0].Status != "stale" {
		t.Fatalf("stale: %+v %v", snapshot, err)
	}
	owner.Close()
	snapshot, err = h.Snapshot(ctx)
	if err == nil || snapshot.Ownership != "lost" || snapshot.EntriesReady {
		t.Fatalf("lost owner: %+v %v", snapshot, err)
	}
}

func TestHealthGatesNewExposureButReportsValidPriceManagementDuringRecovery(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	p, err := fixture.Open("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	now := p.Now()
	to, _ := domain.Timeframe("1m").Bounds(now)
	owner, err := s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	account, err := owner.CreateAccount(ctx, paper.CreateAccount{RequestID: "health-account", Name: "Health", Currency: "USDT", InitialBalance: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	h := app.NewHealth(s, owner, []app.Subscription{{Provider: p, Now: func() time.Time { return now }, Query: marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: to.Add(-time.Minute), To: to}}}, time.Minute)
	service := app.NewControl(owner, []marketdata.InstrumentInfo{info}, time.Minute, h)
	order := paper.OrderRequest{RequestID: "startup-order", AccountID: account.ID, Action: "open", Instrument: info.Instrument, Side: "long", Quantity: "1", Leverage: 1, At: now.Add(-time.Second), FeeRate: "0", SlippageBPS: "0"}
	payload, _ := json.Marshal(order)
	command := app.Command{ID: "startup-control", Kind: "order.submit", Payload: payload}
	original, err := service.Execute(ctx, command)
	if err != nil || original.Status != "rejected" {
		t.Fatalf("startup entry was admitted: %+v %v", original, err)
	}
	state := marketdata.FeedState{Instrument: info.Instrument, Timeframe: "1m", Connected: true, LastEvent: now, LastClosed: to}
	h.Processed(marketdata.Event{Feed: state}, now)
	order.RequestID = "ready-order"
	payload, _ = json.Marshal(order)
	command.ID = "ready-control"
	command.Payload = payload
	outcome, err := service.Execute(ctx, command)
	if err != nil || outcome.Status != "pending" {
		t.Fatalf("fresh entry rejected: %+v %v", outcome, err)
	}
	observation := paper.Observation{ID: "health-price", Instrument: info.Instrument, Source: "fixture", Price: info.MinNotional, ObservedAt: now, AvailableAt: now, Ready: true}
	if _, err = owner.Observe(ctx, observation, paper.ExecutionGate{Now: now, StaleAfter: time.Minute, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	state.Gaps = 1
	candle := domain.Candle{ObservedAt: now}
	h.Processed(marketdata.Event{Feed: state, Candle: &candle, AvailableAt: now}, now)
	snapshot, err := service.Health(ctx)
	if err != nil || snapshot.EntriesReady || len(snapshot.Exposure) != 1 || snapshot.Exposure[0].Monitoring != "monitored" || snapshot.Counts.OpenPositions != 1 {
		t.Fatalf("gap health conflated entries/protection: %+v %v", snapshot, err)
	}
	// Historical filter warmup cannot stand in for a missing live subscription.
	zero := "0"
	definition := strategy.Definition{SchemaVersion: 1, ID: "health-filter", Name: "Health filter", Implementation: "rules", Primary: "1m", Timeframes: []domain.Timeframe{"1m", "15m"}, Evaluation: strategy.OnClose, Emission: strategy.OnTransition, Entry: &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: &zero}}}
	_, instance, e := owner.RegisterStrategy(ctx, definition, &strategy.Instance{ID: "health-instance", Instrument: info.Instrument, Account: account.ID, Action: strategy.AutoPaper})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = owner.SetControl(ctx, "strategy.enabled", instance.ID, "", true, paper.RiskPolicy{}); e != nil {
		t.Fatal(e)
	}
	snapshot, err = service.Health(ctx)
	if err != nil || snapshot.EntriesReady || len(snapshot.Feeds) != 2 || snapshot.Feeds[1].Status != "unmonitored" || snapshot.Counts.ActiveInstances != 1 {
		t.Fatalf("required filter missing from health: %+v %v", snapshot, err)
	}
	// Changing subscriptions must expose existing positions as unmonitored.
	unmonitored := app.NewHealth(s, owner, nil, time.Minute)
	snapshot, err = unmonitored.Snapshot(ctx)
	if err != nil || snapshot.EntriesReady || snapshot.Exposure[0].Monitoring != "unmonitored" {
		t.Fatalf("unmonitored exposure hidden: %+v %v", snapshot, err)
	}
	// Database unavailability clears aggregates instead of reporting zero/healthy.
	s.Close()
	snapshot, err = h.Snapshot(ctx)
	if err == nil || snapshot.Database != "unavailable" || snapshot.Counts != nil || snapshot.EntriesReady {
		t.Fatalf("unavailable DB appeared healthy: %+v %v", snapshot, err)
	}
}
