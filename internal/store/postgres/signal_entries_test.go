package postgres_test

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/strategy"
)

func TestPaperStrategyRequiresExistingAccount(t *testing.T) {
	ctx := context.Background()
	store, _, _, _ := newSpotTest(t)
	raw, err := os.ReadFile("../../../examples/strategies/breakout.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := strategy.Parse(strings.NewReader(string(raw)), strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	v, err := store.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	err = store.AddStrategyInstance(ctx, strategy.Instance{ID: "missing-account", VersionID: v.ID, Instrument: spotInfo().Instrument, Account: "missing", Action: strategy.AutoPaper})
	if err == nil {
		t.Fatal("paper strategy bound to nonexistent account")
	}
}

func signalFor(t *testing.T, store *postgres.Store, account string, action strategy.Action, second int, ready ...bool) strategy.Signal {
	t.Helper()
	ctx := context.Background()
	zero := "0"
	d := strategy.Definition{SchemaVersion: 1, ID: "signal-rule", Name: "Signal rule", Implementation: "rules", Evaluation: strategy.OnUpdate, Emission: strategy.WhileTrue, Entry: &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: &zero}}}
	v, err := store.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	id := string(action)
	i := strategy.Instance{ID: id, VersionID: v.ID, Instrument: spotInfo().Instrument, Account: account, Action: action}
	if err := store.AddStrategyInstance(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStrategyEnabled(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	c := marketdata.Stamp(domain.Candle{Instrument: i.Instrument, Timeframe: "1m", OpenTime: at.Add(-time.Minute), EndTime: at, Open: spotDecimal("100"), High: spotDecimal("101"), Low: spotDecimal("90"), Close: spotDecimal("100"), BaseVolume: spotDecimal(fmt.Sprint(second)), Source: "fixture", ObservedAt: at.Add(time.Duration(second) * time.Second), Closed: true})
	allowsEntries := true
	if len(ready) > 0 {
		allowsEntries = ready[0]
	}
	result, err := store.EvaluateStrategyEvent(ctx, id, marketdata.Event{Kind: "fixture", Candle: &c, AllowsEntries: allowsEntries}, strategy.DefaultRegistry())
	if err != nil || len(result.Signals) != 1 {
		t.Fatalf("signal: %+v %v", result, err)
	}
	return result.Signals[0]
}

func TestSignalApprovalWaitsAndRecoversWithoutDuplicateIntent(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	signal := signalFor(t, store, account.ID, strategy.ManualPaper, 0)
	r := paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt}
	d, err := owner.DecideSignal(ctx, r, spotInfo(), true)
	if err != nil || d.Status != "awaiting_approval" || d.Order != nil {
		t.Fatalf("manual observation: %+v %v", d, err)
	}
	r.Approve = true
	d, err = owner.DecideSignal(ctx, r, spotInfo(), true)
	if err != nil || d.Order == nil || d.Order.Status != "pending" {
		t.Fatalf("approval: %+v %v", d, err)
	}
	obs, gate := spotObservation(0)
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "pending" {
		t.Fatalf("same price: %+v %v", result, err)
	}
	owner.Close()
	owner, err = store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	obs, gate = spotObservation(1)
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	retry, err := owner.DecideSignal(ctx, r, spotInfo(), true)
	if err != nil || retry.Order == nil || retry.Order.ID != d.Order.ID || retry.Order.Status != "filled" {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 1 || fills[0].Price.String() != "100" || fills[0].LatencyMillis != 1000 {
		t.Fatalf("fills: %+v %v", fills, err)
	}
}

func TestSignalCannotBypassExplicitApproval(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	signal := signalFor(t, store, account.ID, strategy.ManualPaper, 0)
	r := spotRequest(account.ID, "bypass")
	r.SignalID = signal.ID
	r.StrategyID = signal.StrategyID
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err == nil {
		t.Fatal("standalone submission bypassed approval")
	}
	orders, err := store.PaperOrders(ctx, account.ID)
	if err != nil || len(orders) != 0 {
		t.Fatalf("orders: %+v %v", orders, err)
	}
}

func TestSignalActionModesAndFillRevalidation(t *testing.T) {
	for _, action := range []strategy.Action{strategy.AlertOnly, strategy.ManualPaper, strategy.AutoPaper} {
		t.Run(string(action), func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			signal := signalFor(t, store, account.ID, action, 0)
			pending, err := store.PendingSignalEntries(ctx, spotInfo(), 10)
			if err != nil || len(pending) != 1 {
				t.Fatalf("queue: %+v %v", pending, err)
			}
			r := paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt}
			d, err := owner.DecideSignal(ctx, r, spotInfo(), true)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case strategy.AlertOnly:
				if d.Status != "observed" || d.Order != nil {
					t.Fatalf("alert: %+v", d)
				}
			case strategy.ManualPaper:
				if d.Status != "awaiting_approval" || d.Order != nil {
					t.Fatalf("manual: %+v", d)
				}
			case strategy.AutoPaper:
				if d.Order == nil || d.Order.Status != "pending" {
					t.Fatalf("auto: %+v", d)
				}
				if err := store.SetKillSwitch(ctx, true); err != nil {
					t.Fatal(err)
				}
				obs, gate := spotObservation(1)
				if _, err := owner.Observe(ctx, obs, gate); err != nil {
					t.Fatal(err)
				}
				ds, err := store.SignalDecisions(ctx)
				if err != nil || len(ds) != 1 || ds[0].Order.Status != "rejected" || ds[0].Order.Reason != "kill switch active" {
					t.Fatalf("revalidated: %+v %v", ds, err)
				}
			}
			pending, err = store.PendingSignalEntries(ctx, spotInfo(), 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("consumed queue: %+v %v", pending, err)
			}
			signals, err := store.StrategySignals(ctx, strategy.SignalFilter{})
			if err != nil || len(signals) != 1 || signals[0].ID != signal.ID || signals[0].Reason != signal.Reason {
				t.Fatalf("signal changed: %+v %v", signals, err)
			}
		})
	}
}

func TestSignalApprovalRejectsConflictingRetry(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	signal := signalFor(t, store, account.ID, strategy.ManualPaper, 0)
	r := paper.SignalEntryRequest{SignalID: signal.ID, Approve: true, At: signal.ObservedAt, Quantity: "0.5"}
	d, err := owner.DecideSignal(ctx, r, spotInfo(), true)
	if err != nil || d.Order == nil {
		t.Fatalf("approval: %+v %v", d, err)
	}
	r.Quantity = "1"
	if _, err := owner.DecideSignal(ctx, r, spotInfo(), true); err == nil {
		t.Fatal("conflicting approval accepted")
	}
	if err := store.SetStrategyEnabled(ctx, signal.InstanceID, false); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || result.Orders[0].Reason != "strategy instance disabled" {
		t.Fatalf("disabled: %+v %v", result, err)
	}
}

func TestSignalDecisionRollbackLeavesWorkRetryable(t *testing.T) {
	for _, table := range []string{"paper_orders", "signal_decisions"} {
		t.Run(table, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, dsn := newSpotTest(t)
			signal := signalFor(t, store, account.ID, strategy.AutoPaper, 0)
			conn, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close(ctx)
			_, err = conn.Exec(ctx, `CREATE FUNCTION billionaire.fail_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END; $$; CREATE TRIGGER fail_decision BEFORE INSERT ON billionaire.`+table+` FOR EACH ROW EXECUTE FUNCTION billionaire.fail_decision()`)
			if err != nil {
				t.Fatal(err)
			}
			r := paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt}
			if _, err := owner.DecideSignal(ctx, r, spotInfo(), true); err == nil {
				t.Fatal("write failure ignored")
			}
			ds, err := store.SignalDecisions(ctx)
			if err != nil || len(ds) != 0 {
				t.Fatalf("partial decision: %+v %v", ds, err)
			}
			orders, err := store.PaperOrders(ctx, account.ID)
			if err != nil || len(orders) != 0 {
				t.Fatalf("partial order: %+v %v", orders, err)
			}
			pending, err := store.PendingSignalEntries(ctx, spotInfo(), 10)
			if err != nil || len(pending) != 1 {
				t.Fatalf("lost work: %+v %v", pending, err)
			}
			if _, err := conn.Exec(ctx, `DROP TRIGGER fail_decision ON billionaire.`+table+`; DROP FUNCTION billionaire.fail_decision()`); err != nil {
				t.Fatal(err)
			}
			d, err := owner.DecideSignal(ctx, r, spotInfo(), true)
			if err != nil || d.Order == nil || d.Order.Status != "pending" {
				t.Fatalf("retry: %+v %v", d, err)
			}
		})
	}
}

func TestDistinctWhileTrueSignalsHaveIndependentRiskDecisions(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	first := signalFor(t, store, account.ID, strategy.AutoPaper, 0)
	d, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: first.ID, At: first.ObservedAt}, spotInfo(), true)
	if err != nil || d.Order == nil {
		t.Fatalf("first: %+v %v", d, err)
	}
	obs, gate := spotObservation(1)
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	// Close explicitly: the per-candle guard must still reject the next distinct signal.
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(positions) != 1 {
		t.Fatalf("positions: %+v %v", positions, err)
	}
	close := spotRequest(account.ID, "explicit-close")
	close.Action = "close"
	close.PositionID = positions[0].ID
	close.Quantity = positions[0].RemainingQuantity.String()
	close.At = obs.AvailableAt
	if _, err := owner.SubmitOrder(ctx, close, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate = spotObservation(2)
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	second := signalFor(t, store, account.ID, strategy.AutoPaper, 3)
	if second.ID == first.ID {
		t.Fatal("distinct updates collapsed")
	}
	d, err = owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: second.ID, At: second.ObservedAt}, spotInfo(), true)
	if err != nil || d.Order == nil {
		t.Fatalf("second intent: %+v %v", d, err)
	}
	obs, gate = spotObservation(4)
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Reason != "entry already filled for signal candle" {
		t.Fatalf("candle guard: %+v %v", result, err)
	}
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{Pyramiding: true}); err != nil {
		t.Fatal(err)
	}
	third := signalFor(t, store, account.ID, strategy.AutoPaper, 5)
	if _, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: third.ID, At: third.ObservedAt}, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate = spotObservation(6)
	result, err = owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
		t.Fatalf("explicit pyramiding: %+v %v", result, err)
	}
}

func TestUnreadySignalDecisionIsDurableRejection(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	signal := signalFor(t, store, account.ID, strategy.AutoPaper, 0, false)
	r := paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt}
	d, err := owner.DecideSignal(ctx, r, spotInfo(), true)
	if err != nil || d.Status != "rejected" || d.Reason != "signal source stale/unready" || d.Order != nil {
		t.Fatalf("unready: %+v %v", d, err)
	}
	orders, err := store.PaperOrders(ctx, account.ID)
	if err != nil || len(orders) != 0 {
		t.Fatalf("orders: %+v %v", orders, err)
	}
	ds, err := store.SignalDecisions(ctx)
	if err != nil || len(ds) != 1 || ds[0].Reason != d.Reason {
		t.Fatalf("decisions: %+v %v", ds, err)
	}
}

func TestApprovalRetryRejectsChangedMetadata(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	signal := signalFor(t, store, account.ID, strategy.ManualPaper, 0)
	r := paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt, Approve: true}
	if _, err := owner.DecideSignal(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	changed := spotInfo()
	changed.PriceStep = spotDecimal("0.1")
	if _, err := owner.DecideSignal(ctx, r, changed, true); err == nil {
		t.Fatal("conflicting metadata accepted")
	}
}
