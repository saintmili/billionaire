package postgres_test

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sync"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
)

func spotDecimal(s string) domain.Decimal {
	v, err := domain.ParseDecimal(s)
	if err != nil {
		panic(err)
	}
	return v
}
func spotInfo() marketdata.InstrumentInfo {
	return marketdata.InstrumentInfo{Instrument: domain.Instrument{Provider: "fixture", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}, PriceStep: spotDecimal("0.01"), QuantityStep: spotDecimal("0.001"), MinQuantity: spotDecimal("0.001"), MinNotional: spotDecimal("5")}
}

func TestSpotOrderPersistsPendingAndFillsOnlyOnceAfterRestart(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	account, err := store.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "demo", Name: "demo", Currency: "USDT", InitialBalance: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	info := spotInfo()
	request := paper.OrderRequest{RequestID: "entry", AccountID: account.ID, Action: "open", Instrument: info.Instrument, Side: "long", Leverage: 1, Quantity: "2", At: at, FeeRate: "0.001", SlippageBPS: "0"}
	order, err := owner.SubmitSpotOrder(ctx, request, info, true)
	owner.Close()
	if err != nil || order.Status != "pending" {
		t.Fatalf("intent: %+v %v", order, err)
	}
	store.Close()
	store, err = postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err = store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	observation := paper.Observation{ID: "next-price", Instrument: info.Instrument, Source: "fixture", Price: spotDecimal("100"), ObservedAt: at.Add(time.Second), AvailableAt: at.Add(time.Second), Ready: true}
	gate := paper.ExecutionGate{Now: observation.AvailableAt, StaleAfter: time.Minute, Enabled: true}
	for range 2 {
		result, err := owner.ObserveSpot(ctx, observation, gate)
		if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
			t.Fatalf("observation: %+v %v", result, err)
		}
	}
	retry, err := owner.SubmitSpotOrder(ctx, request, info, true)
	if err != nil || retry.Status != "filled" || retry.ID != order.ID {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	saved, err := store.PaperAccount(ctx, account.ID)
	if err != nil || saved.Balance.String() != "799.8" {
		t.Fatalf("cash: %+v %v", saved, err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(positions) != 1 || positions[0].RemainingQuantity.String() != "2" || positions[0].RemainingCost.String() != "200.2" {
		t.Fatalf("positions: %+v %v", positions, err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 1 || fills[0].Fee.String() != "0.2" {
		t.Fatalf("fills: %+v %v", fills, err)
	}
	ledger, err := store.PaperLedger(ctx, account.ID)
	if err != nil || len(ledger) != 3 {
		t.Fatalf("ledger: %+v %v", ledger, err)
	}
}

func newSpotTest(t *testing.T) (*postgres.Store, *postgres.PaperOwner, paper.Account, string) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.New(t)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	account, err := store.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "account", Name: "demo", Currency: "USDT", InitialBalance: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	return store, owner, account, dsn
}
func spotRequest(accountID, id string) paper.OrderRequest {
	return paper.OrderRequest{RequestID: id, AccountID: accountID, Action: "open", Instrument: spotInfo().Instrument, Side: "long", Leverage: 1, Quantity: "2", At: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), FeeRate: "0.001", SlippageBPS: "0"}
}
func spotObservation(second int) (paper.Observation, paper.ExecutionGate) {
	at := time.Date(2026, 11, 12, 10, 0, second, 0, time.UTC)
	observation := paper.Observation{ID: fmt.Sprintf("price-%d", second), Instrument: spotInfo().Instrument, Source: "fixture", Price: spotDecimal("100"), ObservedAt: at, AvailableAt: at, Ready: true}
	return observation, paper.ExecutionGate{Now: at, StaleAfter: time.Minute, Enabled: true}
}

func TestSpotFinancialFailureRollsBackAndSameObservationRecovers(t *testing.T) {
	ctx := context.Background()
	store, owner, account, dsn := newSpotTest(t)
	request := spotRequest(account.ID, "entry")
	order, err := owner.SubmitSpotOrder(ctx, request, spotInfo(), true)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	observation, gate := spotObservation(1)
	for _, fault := range []struct{ table, operation string }{{"paper_orders", "UPDATE"}, {"paper_accounts", "UPDATE"}, {"paper_fills", "INSERT"}, {"paper_positions", "INSERT"}, {"paper_ledger_entries", "INSERT"}, {"journal_outbox", "INSERT"}, {"paper_price_events", "INSERT"}} {
		table := fault.table
		_, err := conn.Exec(ctx, `CREATE FUNCTION millionaire.fail_spot_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END; $$; CREATE TRIGGER fail_write BEFORE `+fault.operation+` ON millionaire.`+table+` FOR EACH ROW EXECUTE FUNCTION millionaire.fail_spot_write()`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.ObserveSpot(ctx, observation, gate); err == nil {
			t.Fatalf("%s write failure did not abort", table)
		}
		saved, err := store.PaperAccount(ctx, account.ID)
		if err != nil || saved.Balance.String() != "1000" {
			t.Fatalf("cash after %s: %+v %v", table, saved, err)
		}
		positions, err := store.PaperPositions(ctx, account.ID)
		if err != nil || len(positions) != 0 {
			t.Fatalf("positions after %s: %+v %v", table, positions, err)
		}
		fills, err := store.PaperFills(ctx, account.ID)
		if err != nil || len(fills) != 0 {
			t.Fatalf("fills after %s: %+v %v", table, fills, err)
		}
		orders, err := store.PaperOrders(ctx, account.ID)
		if err != nil || len(orders) != 1 || orders[0].Status != "pending" {
			t.Fatalf("order after %s: %+v %v", table, orders, err)
		}
		ledger, err := store.PaperLedger(ctx, account.ID)
		if err != nil || len(ledger) != 1 {
			t.Fatalf("ledger after %s: %+v %v", table, ledger, err)
		}
		if _, err := conn.Exec(ctx, `DROP TRIGGER fail_write ON millionaire.`+table+`; DROP FUNCTION millionaire.fail_spot_write()`); err != nil {
			t.Fatal(err)
		}
	}
	result, err := owner.ObserveSpot(ctx, observation, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].ID != order.ID || result.Orders[0].Status != "filled" {
		t.Fatalf("retry after rollback: %+v %v", result, err)
	}
}

func TestSpotObservationReplayAndOrderInputConflicts(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{Pyramiding: true}); err != nil {
		t.Fatal(err)
	}
	request := spotRequest(account.ID, "first")
	if _, err := owner.SubmitSpotOrder(ctx, request, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Quantity = "3"
	if _, err := owner.SubmitSpotOrder(ctx, changed, spotInfo(), true); err == nil {
		t.Fatal("changed request accepted")
	}
	observation, gate := spotObservation(3)
	if _, err := owner.ObserveSpot(ctx, observation, gate); err != nil {
		t.Fatal(err)
	}
	conflict := observation
	conflict.Price = spotDecimal("101")
	if _, err := owner.ObserveSpot(ctx, conflict, gate); err == nil {
		t.Fatal("conflicting observation accepted")
	}
	// New backdated intents cannot consume an event already delivered before submission.
	request.RequestID = "second"
	if _, err := owner.SubmitSpotOrder(ctx, request, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ObserveSpot(ctx, observation, gate); err != nil {
		t.Fatal(err)
	}
	older, olderGate := spotObservation(2)
	result, err := owner.ObserveSpot(ctx, older, olderGate)
	if err != nil || result.Reason != "out-of-order observation skipped" {
		t.Fatalf("out of order: %+v %v", result, err)
	}
	orders, err := store.PaperOrders(ctx, account.ID)
	if err != nil || len(orders) != 2 {
		t.Fatalf("orders: %+v %v", orders, err)
	}
	pending := 0
	for _, o := range orders {
		if o.Status == "pending" {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("old delivery filled new intent: %+v", orders)
	}
	future, now := spotObservation(4)
	now.Now = now.Now.Add(-time.Second)
	if _, err := owner.ObserveSpot(ctx, future, now); err == nil {
		t.Fatal("future observation accepted")
	}
	now.Now = future.AvailableAt
	if _, err := owner.ObserveSpot(ctx, future, now); err != nil {
		t.Fatal(err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 2 {
		t.Fatalf("fills: %+v %v", fills, err)
	}
}

func TestSpotConcurrentPendingOrdersCannotOverspend(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	errorsCh := make(chan error, 2)
	var group sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		group.Go(func() {
			request := spotRequest(account.ID, id)
			request.Quantity = "6"
			_, err := owner.SubmitSpotOrder(ctx, request, spotInfo(), true)
			errorsCh <- err
		})
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	observation, gate := spotObservation(1)
	result, err := owner.ObserveSpot(ctx, observation, gate)
	if err != nil || len(result.Orders) != 2 {
		t.Fatalf("decisions: %+v %v", result, err)
	}
	counts := map[string]int{}
	for _, order := range result.Orders {
		counts[order.Status]++
	}
	if counts["filled"] != 1 || counts["rejected"] != 1 {
		t.Fatalf("overspend: %+v", result)
	}
	saved, err := store.PaperAccount(ctx, account.ID)
	if err != nil || saved.Balance.String() != "399.4" {
		t.Fatalf("cash: %+v %v", saved, err)
	}
}

func TestSpotCloseRequestsCannotOversellHoldings(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	request := spotRequest(account.ID, "entry")
	if _, err := owner.SubmitSpotOrder(ctx, request, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	observation, gate := spotObservation(1)
	if _, err := owner.ObserveSpot(ctx, observation, gate); err != nil {
		t.Fatal(err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(positions) != 1 {
		t.Fatalf("positions: %+v %v", positions, err)
	}
	request.Action = "close"
	request.PositionID = positions[0].ID
	request.At = request.At.Add(2 * time.Second)
	request.RequestID = "too-much"
	request.Quantity = "3"
	order, err := owner.SubmitSpotOrder(ctx, request, spotInfo(), true)
	if err != nil || order.Status != "rejected" {
		t.Fatalf("excess close: %+v %v", order, err)
	}
	request.Quantity = "1.5"
	for _, id := range []string{"close-a", "close-b"} {
		request.RequestID = id
		if _, err := owner.SubmitSpotOrder(ctx, request, spotInfo(), true); err != nil {
			t.Fatal(err)
		}
	}
	observation, gate = spotObservation(3)
	result, err := owner.ObserveSpot(ctx, observation, gate)
	if err != nil || len(result.Orders) != 2 {
		t.Fatalf("close decisions: %+v %v", result, err)
	}
	counts := map[string]int{}
	for _, order := range result.Orders {
		counts[order.Status]++
	}
	if counts["filled"] != 1 || counts["rejected"] != 1 {
		t.Fatalf("oversold: %+v", result)
	}
	saved, err := store.PaperPosition(ctx, positions[0].ID)
	if err != nil || saved.RemainingQuantity.String() != "0.5" || saved.RemainingCost.String() != "50.05" {
		t.Fatalf("holdings: %+v %v", saved, err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 2 {
		t.Fatalf("fills: %+v %v", fills, err)
	}
}

func TestSpotObservationClockPreservesSubmicrosecondOrder(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	request := spotRequest(account.ID, "first")
	if _, err := owner.SubmitSpotOrder(ctx, request, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	observation, gate := spotObservation(0)
	observation.ID = "later-nanosecond"
	observation.ObservedAt = observation.ObservedAt.Add(3 * time.Nanosecond)
	observation.AvailableAt = observation.ObservedAt
	gate.Now = observation.AvailableAt
	if _, err := owner.ObserveSpot(ctx, observation, gate); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "second"
	request.At = request.At.Add(time.Nanosecond)
	if _, err := owner.SubmitSpotOrder(ctx, request, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	observation.ID = "older-nanosecond"
	observation.ObservedAt = observation.ObservedAt.Add(-time.Nanosecond)
	observation.AvailableAt = observation.ObservedAt
	gate.Now = observation.AvailableAt
	result, err := owner.ObserveSpot(ctx, observation, gate)
	if err != nil || result.Reason != "out-of-order observation skipped" {
		t.Fatalf("observation clock lost precision: %+v %v", result, err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 1 {
		t.Fatalf("retroactive fill: %+v %v", fills, err)
	}
}
