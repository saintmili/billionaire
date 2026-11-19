package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/app"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
)

// Evaluate real immutable rules; entries above 90 and exits below 90.
func cycleSignal(t *testing.T, store *postgres.Store, account, instance string, action strategy.Action, second int, price string, metadata ...marketdata.InstrumentInfo) strategy.Signal {
	t.Helper()
	return cycleDirectionSignal(t, store, account, instance, action, second, price, strategy.Long, metadata...)
}

func cycleDirectionSignal(t *testing.T, store *postgres.Store, account, instance string, action strategy.Action, second int, price string, direction strategy.Direction, metadata ...marketdata.InstrumentInfo) strategy.Signal {
	t.Helper()
	ctx := context.Background()
	info := spotInfo()
	if len(metadata) > 0 {
		info = metadata[0]
	}
	threshold := "90"
	d := strategy.Definition{SchemaVersion: 1, ID: "exit-rule", Name: "Exit rule", RiskPercent: "10", Direction: direction, Markets: []domain.MarketType{info.Instrument.Market}, Implementation: "rules", Evaluation: strategy.OnUpdate, Emission: strategy.WhileTrue,
		Entry: &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: &threshold}},
		Exit:  &strategy.Rule{Op: "lt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: &threshold}}}
	v, err := store.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	i := strategy.Instance{ID: instance, VersionID: v.ID, Instrument: info.Instrument, Account: account, Action: action}
	if err = store.AddStrategyInstance(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err = store.SetStrategyEnabled(ctx, i.ID, true); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	c := marketdata.Stamp(domain.Candle{Instrument: i.Instrument, Timeframe: "1m", OpenTime: at.Add(-time.Minute), EndTime: at, Open: spotDecimal("100"), High: spotDecimal("110"), Low: spotDecimal("50"), Close: spotDecimal(price), BaseVolume: spotDecimal(fmt.Sprint(second + 1)), Source: "fixture", ObservedAt: at.Add(time.Duration(second) * time.Second), Closed: true})
	result, err := store.EvaluateStrategyEvent(ctx, i.ID, marketdata.Event{Kind: "fixture", Candle: &c, AllowsEntries: true}, strategy.DefaultRegistry())
	if err != nil || len(result.Signals) != 1 {
		t.Fatalf("signal: %+v %v", result, err)
	}
	return result.Signals[0]
}

func TestStrategyExitUsesNextObservationAndSurvivesRestart(t *testing.T) {
	for _, action := range []strategy.Action{strategy.ManualPaper, strategy.AutoPaper, strategy.AlertOnly} {
		t.Run(string(action), func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			entry := cycleSignal(t, store, account.ID, "cycle", action, 0, "100")
			r := paper.SignalEntryRequest{SignalID: entry.ID, At: entry.ObservedAt, Quantity: "1", Approve: action == strategy.ManualPaper}
			if _, err := owner.DecideSignal(ctx, r, spotInfo(), true); err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			if result, err := owner.Observe(ctx, obs, gate); err != nil || (action != strategy.AlertOnly && (len(result.Orders) != 1 || result.Orders[0].Status != "filled")) {
				t.Fatalf("entry execution: %+v %v", result, err)
			}
			exit := cycleSignal(t, store, account.ID, "cycle", action, 2, "80")
			if exit.Kind != "exit" {
				t.Fatal(exit)
			}
			if err := store.SetKillSwitch(ctx, true); err != nil {
				t.Fatal(err)
			}
			r = paper.SignalEntryRequest{SignalID: exit.ID, At: exit.ObservedAt}
			d, err := owner.DecideSignal(ctx, r, spotInfo(), false)
			if err != nil {
				t.Fatal(err)
			}
			if action == strategy.AlertOnly {
				if d.Status != "observed" || d.Order != nil {
					t.Fatal(d)
				}
				return
			}
			if action == strategy.ManualPaper {
				if d.Status != "awaiting_approval" || d.Order != nil {
					t.Fatal(d)
				}
				r.Approve = true
				d, err = owner.DecideSignal(ctx, r, spotInfo(), false)
				if err != nil {
					t.Fatal(err)
				}
			}
			if d.Order == nil || d.Order.Status != "pending" || d.Order.Request.Action != "close" || d.Order.Request.Quantity != "1" {
				t.Fatalf("exit: %+v", d)
			}
			obs, gate = spotObservation(2)
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			owner.Close()
			owner, err = store.AcquirePaperOwner(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			obs, gate = spotObservation(3)
			gate.Enabled = false
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			retry, err := owner.DecideSignal(ctx, r, spotInfo(), false)
			if err != nil || retry.Order == nil || retry.Order.ID != d.Order.ID || retry.Order.Status != "filled" {
				t.Fatalf("retry: %+v %v", retry, err)
			}
			fills, err := store.PaperFills(ctx, account.ID)
			if err != nil || len(fills) != 2 {
				t.Fatalf("fills: %+v %v", fills, err)
			}
			positions, err := store.PaperPositions(ctx, account.ID)
			if err != nil || len(positions) != 1 || positions[0].Status != "closed" {
				t.Fatalf("positions: %+v %v", positions, err)
			}
		})
	}
}

func openCycle(t *testing.T, store *postgres.Store, owner *postgres.PaperOwner, account, instance string, second int) paper.Position {
	t.Helper()
	ctx := context.Background()
	s := cycleSignal(t, store, account, instance, strategy.AutoPaper, second, "100")
	if _, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: s.ID, At: s.ObservedAt, Quantity: "1"}, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(second + 1)
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
		t.Fatalf("open: %+v %v", result, err)
	}
	ps, err := store.PaperPositions(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.OpenedAt.Equal(obs.AvailableAt) {
			return p
		}
	}
	t.Fatal("opened position not found")
	return paper.Position{}
}

func TestStrategyExitMatchesOpeningInstanceAndRemainingQuantity(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	p := openCycle(t, store, owner, account.ID, "owner", 0)
	// Even the same strategy definition on the same account cannot close another instance.
	s := cycleSignal(t, store, account.ID, "other", strategy.AutoPaper, 2, "80")
	d, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: s.ID, At: s.ObservedAt}, spotInfo(), true)
	if err != nil || d.Status != "rejected" || d.Reason != "no matching strategy exposure" || d.Order != nil {
		t.Fatalf("scope: %+v %v", d, err)
	}
	close := spotRequest(account.ID, "partial")
	close.Action = "close"
	close.PositionID = p.ID
	close.Quantity = "0.4"
	close.At = s.ObservedAt
	if _, err = owner.SubmitOrder(ctx, close, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(3)
	if _, err = owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	s = cycleSignal(t, store, account.ID, "owner", strategy.AutoPaper, 4, "80")
	r := paper.SignalEntryRequest{SignalID: s.ID, At: s.ObservedAt}
	d, err = owner.DecideSignal(ctx, r, spotInfo(), true)
	if err != nil || d.Order == nil || d.Order.Request.Quantity != "0.6" {
		t.Fatalf("remaining close: %+v %v", d, err)
	}
	obs, gate = spotObservation(5)
	if _, err = owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	ps, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(ps) != 1 || ps[0].Status != "closed" {
		t.Fatalf("remaining: %+v %v", ps, err)
	}
}

func TestStrategyExitRejectsMultipleMatchingPositions(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{Pyramiding: true}); err != nil {
		t.Fatal(err)
	}
	openCycle(t, store, owner, account.ID, "owner", 0)
	openCycle(t, store, owner, account.ID, "owner", 2)
	s := cycleSignal(t, store, account.ID, "owner", strategy.AutoPaper, 4, "80")
	d, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: s.ID, At: s.ObservedAt}, spotInfo(), true)
	if err != nil || d.Status != "rejected" || d.Reason != "multiple matching positions unsupported; close explicitly by position" || d.Order != nil {
		t.Fatalf("multiple: %+v %v", d, err)
	}
}

func TestStrategyExitExplicitQuantityAndStaleOutcome(t *testing.T) {
	for _, quantity := range []string{"0.3", "2"} {
		t.Run(quantity, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			openCycle(t, store, owner, account.ID, "owner", 0)
			s := cycleSignal(t, store, account.ID, "owner", strategy.AutoPaper, 2, "80")
			d, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: s.ID, At: s.ObservedAt, Quantity: quantity}, spotInfo(), true)
			if err != nil || d.Order == nil || d.Order.Request.Quantity != quantity {
				t.Fatalf("quantity: %+v %v", d, err)
			}
			if quantity == "2" {
				if d.Status != "rejected" || d.Order.Status != "rejected" {
					t.Fatal(d)
				}
				return
			}
			obs, gate := spotObservation(3)
			gate.Now = gate.Now.Add(2 * time.Minute)
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			ds, err := store.SignalDecisions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range ds {
				if d.SignalID == s.ID && (d.Order == nil || d.Order.Status != "rejected" || d.Order.Reason != "source stale/unready") {
					t.Fatal(d)
				}
			}
			fills, err := store.PaperFills(ctx, account.ID)
			if err != nil || len(fills) != 1 {
				t.Fatalf("stale fills: %+v %v", fills, err)
			}
		})
	}
}

func TestStrategyExitsCompeteWithManualProtectionAndLiquidation(t *testing.T) {
	for _, competitor := range []string{"manual", "protection", "liquidation"} {
		t.Run(competitor, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			info := spotInfo()
			leverage := 1
			if competitor == "liquidation" {
				info.Instrument.Market = domain.Linear
				leverage = 10
			}
			entry := cycleSignal(t, store, account.ID, "cycle", strategy.AutoPaper, 0, "100", info)
			d, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: entry.ID, At: entry.ObservedAt, Quantity: "1", Leverage: leverage}, info, true)
			if err != nil || d.Order == nil {
				t.Fatalf("entry: %+v %v", d, err)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			result, err := owner.Observe(ctx, obs, gate)
			if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
				t.Fatalf("open: %+v %v", result, err)
			}
			p, err := store.PaperPosition(ctx, paper.Identity("position", d.Order.ID))
			if err != nil {
				t.Fatal(err)
			}
			a := cycleSignal(t, store, account.ID, "cycle", strategy.AutoPaper, 2, "80", info)
			b := cycleSignal(t, store, account.ID, "cycle", strategy.AutoPaper, 3, "80", info)
			errs := make(chan error, 3)
			var wg sync.WaitGroup
			for _, s := range []strategy.Signal{a, b} {
				wg.Add(1)
				go func(s strategy.Signal) {
					defer wg.Done()
					d, e := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: s.ID, At: s.ObservedAt}, info, true)
					if e == nil && (d.Order == nil || d.Order.Status != "pending") {
						e = fmt.Errorf("exit %+v", d)
					}
					errs <- e
				}(s)
			}
			if competitor == "manual" {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r := spotRequest(account.ID, "competing-manual")
					r.Action = "close"
					r.PositionID = p.ID
					r.Quantity = "1"
					r.At = b.ObservedAt
					_, e := owner.SubmitOrder(ctx, r, info, true)
					errs <- e
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			obs, gate = spotObservation(4)
			obs.Instrument = info.Instrument
			if competitor == "protection" {
				obs.Price = spotDecimal("40")
			}
			if competitor == "liquidation" {
				obs.Price = spotDecimal("80")
			}
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			fills, err := store.PaperFills(ctx, account.ID)
			if err != nil || len(fills) != 2 {
				t.Fatalf("competing fills: %+v %v", fills, err)
			}
			if competitor != "manual" {
				expected := "stop_loss"
				if competitor == "liquidation" {
					expected = "liquidation_approximate"
				}
				found := false
				for _, fill := range fills {
					found = found || fill.ExitReason == expected
				}
				if !found {
					t.Fatalf("competing %s did not settle: %+v", competitor, fills)
				}
			}
			p, err = store.PaperPosition(ctx, p.ID)
			if err != nil || p.Status != "closed" || !p.RemainingQuantity.IsZero() {
				t.Fatalf("closed: %+v %v", p, err)
			}
			orders, err := store.PaperOrders(ctx, account.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range orders {
				if o.Status == "pending" {
					t.Fatalf("unresolved competitor: %+v", o)
				}
			}
		})
	}
}

func TestStrategyExitApprovalReceiptSurvivesResponseLoss(t *testing.T) {
	ctx := context.Background()
	store, owner, account, dsn := newSpotTest(t)
	entry := cycleSignal(t, store, account.ID, "manual", strategy.ManualPaper, 0, "100")
	if _, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: entry.ID, At: entry.ObservedAt, Quantity: "1", Approve: true}, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	exit := cycleSignal(t, store, account.ID, "manual", strategy.ManualPaper, 2, "80")
	service := app.NewControl(owner, []marketdata.InstrumentInfo{spotInfo()}, time.Minute)
	raw, _ := json.Marshal(paper.SignalEntryRequest{SignalID: exit.ID, At: exit.ObservedAt, Quantity: "0.3"})
	command := app.Command{ID: "approve-exit", Kind: "signal.approve", Payload: raw}
	// A failed receipt write must roll back both the close intent and decision.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `CREATE FUNCTION billionaire.fail_exit_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END; $$; CREATE TRIGGER fail_exit_receipt BEFORE INSERT ON billionaire.control_outcomes FOR EACH ROW EXECUTE FUNCTION billionaire.fail_exit_receipt()`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Execute(ctx, command); err == nil {
		t.Fatal("receipt failure ignored")
	}
	orders, err := store.PaperOrders(ctx, account.ID)
	if err != nil || len(orders) != 1 {
		t.Fatalf("partial exit intent: %+v %v", orders, err)
	}
	decisions, err := store.SignalDecisions(ctx)
	if err != nil || len(decisions) != 1 {
		t.Fatalf("partial exit decision: %+v %v", decisions, err)
	}
	if _, err = service.Outcome(ctx, command.ID); err == nil {
		t.Fatal("failed action has receipt")
	}
	if _, err = conn.Exec(ctx, `DROP TRIGGER fail_exit_receipt ON billionaire.control_outcomes; DROP FUNCTION billionaire.fail_exit_receipt()`); err != nil {
		t.Fatal(err)
	}
	first, err := service.Execute(ctx, command)
	if err != nil || first.Status != "pending" {
		t.Fatalf("approval: %+v %v", first, err)
	}
	// The client loses its response. Restart and query/retry the immutable receipt.
	owner.Close()
	owner, err = store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	service = app.NewControl(owner, []marketdata.InstrumentInfo{spotInfo()}, time.Minute)
	recovered, err := service.Outcome(ctx, command.ID)
	if err != nil || !sameExitJSON(recovered.Result, first.Result) {
		t.Fatalf("receipt: %+v %v", recovered, err)
	}
	if retry, err := service.Execute(ctx, command); err != nil || !sameExitJSON(retry.Result, first.Result) {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	obs, gate = spotObservation(3)
	if _, err = owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(positions) != 1 || positions[0].RemainingQuantity.String() != "0.7" {
		t.Fatalf("partial close: %+v %v", positions, err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 2 {
		t.Fatalf("fills: %+v %v", fills, err)
	}
}

func sameExitJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func TestStrategyExitPreservesHistoricalObservedDecision(t *testing.T) {
	ctx := context.Background()
	store, owner, account, dsn := newSpotTest(t)
	openCycle(t, store, owner, account.ID, "owner", 0)
	s := cycleSignal(t, store, account.ID, "owner", strategy.AutoPaper, 2, "80")
	// Database fixture represents the terminal observation made by the prior
	// release. Upgrading never reinterprets already-consumed exit signals.
	old := paper.SignalDecision{SignalID: s.ID, Status: "observed", Reason: "signal is observation only", Request: paper.SignalEntryRequest{SignalID: s.ID, At: s.ObservedAt}.Normalize()}
	raw, _ := json.Marshal(old)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `INSERT INTO billionaire.signal_decisions(signal_id,status,payload) VALUES($1,$2,$3)`, s.ID, old.Status, raw); err != nil {
		t.Fatal(err)
	}
	d, err := owner.DecideSignal(ctx, old.Request, spotInfo(), true)
	if err != nil || d.Status != "observed" || d.Order != nil {
		t.Fatalf("historical outcome: %+v %v", d, err)
	}
	orders, err := store.PaperOrders(ctx, account.ID)
	if err != nil || len(orders) != 1 {
		t.Fatalf("retroactive orders: %+v %v", orders, err)
	}
}

func TestStrategyExitDoesNotClaimStandalonePositionWithSameStrategyLabel(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	s := cycleSignal(t, store, account.ID, "owner", strategy.AutoPaper, 0, "80")
	r := spotRequest(account.ID, "standalone")
	r.StrategyID = s.StrategyID
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	s = cycleSignal(t, store, account.ID, "owner", strategy.AutoPaper, 2, "80")
	d, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: s.ID, At: s.ObservedAt}, spotInfo(), true)
	if err != nil || d.Status != "rejected" || d.Order != nil || d.Reason != "no matching strategy exposure" {
		t.Fatalf("unproven owner: %+v %v", d, err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(positions) != 1 || positions[0].Status != "open" {
		t.Fatalf("standalone: %+v %v", positions, err)
	}
}

func TestStrategyExitOppositeEntryNeverClosesExistingPosition(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	entry := cycleSignal(t, store, account.ID, "long", strategy.AutoPaper, 0, "100", info)
	d, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: entry.ID, At: entry.ObservedAt, Quantity: "1"}, info, true)
	if err != nil || d.Order == nil {
		t.Fatalf("long: %+v %v", d, err)
	}
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
		t.Fatalf("entry fill: %+v %v", result, err)
	}
	short := cycleDirectionSignal(t, store, account.ID, "short", strategy.AutoPaper, 2, "100", strategy.Short, info)
	if short.Kind != "entry" || short.Direction != strategy.Short {
		t.Fatal(short)
	}
	d, err = owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: short.ID, At: short.ObservedAt, Quantity: "1"}, info, true)
	if err != nil || d.Order == nil || d.Order.Request.Action != "open" || d.Order.Request.PositionID != "" {
		t.Fatalf("opposite entry: %+v %v", d, err)
	}
	obs, gate = spotObservation(3)
	obs.Instrument = info.Instrument
	if _, err = owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	ps, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(ps) != 1 || ps[0].Direction() != "long" || ps[0].RemainingQuantity.String() != "1" {
		t.Fatalf("original holding: %+v %v", ps, err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 1 {
		t.Fatalf("implicit exit: %+v %v", fills, err)
	}
}

func TestStrategyExitLinearShortUsesPositionDirectionAndLeverage(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	entry := cycleDirectionSignal(t, store, account.ID, "short", strategy.AutoPaper, 0, "100", strategy.Short, info)
	d, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: entry.ID, At: entry.ObservedAt, Quantity: "1", Leverage: 5}, info, true)
	if err != nil || d.Order == nil {
		t.Fatalf("short: %+v %v", d, err)
	}
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
		t.Fatalf("entry: %+v %v", result, err)
	}
	exit := cycleDirectionSignal(t, store, account.ID, "short", strategy.AutoPaper, 2, "80", strategy.Short, info)
	d, err = owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: exit.ID, At: exit.ObservedAt}, info, false)
	if err != nil || d.Order == nil || d.Order.Request.Side != "short" || d.Order.Request.Leverage != 5 || d.Order.Request.Action != "close" {
		t.Fatalf("short close: %+v %v", d, err)
	}
	obs, gate = spotObservation(3)
	obs.Instrument = info.Instrument
	gate.Enabled = false
	if _, err = owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	saved, err := store.PaperAccount(ctx, account.ID)
	if err != nil || saved.Balance.String() != "999.8" {
		t.Fatalf("short settlement: %+v %v", saved, err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(positions) != 1 || positions[0].Status != "closed" || !positions[0].ReservedMargin.IsZero() {
		t.Fatalf("short exposure: %+v %v", positions, err)
	}
}
