package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
)

// This fixture crosses strategy evaluation, durable signal decisions and the owner.
// Public repository reads supply all financial assertions; SQL is only a fault seam.
func m4Signal(t *testing.T, s *postgres.Store, account string, action strategy.Action, side string, second int) strategy.Signal {
	t.Helper()
	ctx := context.Background()
	zero := "0"
	d := strategy.Definition{SchemaVersion: 1, ID: "m4-" + side, Name: "M4 acceptance", Implementation: "rules", Markets: []domain.MarketType{domain.Linear}, Direction: strategy.Direction(side), RiskPercent: "100", Evaluation: strategy.OnUpdate, Emission: strategy.WhileTrue, Entry: &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: &zero}}}
	v, err := s.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	i := strategy.Instance{ID: string(action) + side, VersionID: v.ID, Instrument: info.Instrument, Account: account, Action: action}
	if err := s.AddStrategyInstance(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStrategyEnabled(ctx, i.ID, true); err != nil {
		t.Fatal(err)
	}
	at := spotRequest(account, "unused").At
	c := marketdata.Stamp(domain.Candle{Instrument: info.Instrument, Timeframe: "1m", OpenTime: at.Add(-time.Minute), EndTime: at, Open: spotDecimal("100"), High: spotDecimal("110"), Low: spotDecimal("90"), Close: spotDecimal("100"), BaseVolume: spotDecimal(fmt.Sprint(second + 1)), Source: "fixture", ObservedAt: at.Add(time.Duration(second) * time.Second), Closed: true})
	result, err := s.EvaluateStrategyEvent(ctx, i.ID, marketdata.Event{Kind: "fixture", Candle: &c, AllowsEntries: true}, strategy.DefaultRegistry())
	if err != nil || len(result.Signals) != 1 {
		t.Fatalf("signal %+v %v", result, err)
	}
	return result.Signals[0]
}

func m4Decide(t *testing.T, o *postgres.PaperOwner, s strategy.Signal) (paper.SignalEntryRequest, paper.Order) {
	t.Helper()
	info := spotInfo()
	info.Instrument = s.Instrument
	r := paper.SignalEntryRequest{SignalID: s.ID, Approve: s.Action == strategy.ManualPaper, At: s.ObservedAt, Quantity: "2", Leverage: 5}
	d, err := o.DecideSignal(context.Background(), r, info, true)
	if err != nil || d.Order == nil || d.Order.Status != "pending" {
		t.Fatalf("decision %+v %v", d, err)
	}
	return r, *d.Order
}

func m4Observe(t *testing.T, o *postgres.PaperOwner, second int, price string, enabled bool) paper.Observation {
	t.Helper()
	obs, g := spotObservation(second)
	obs.Instrument.Market = domain.Linear
	obs.Price = spotDecimal(price)
	g.Enabled = enabled
	for range 2 {
		if _, err := o.Observe(context.Background(), obs, g); err != nil {
			t.Fatal(err)
		}
	}
	return obs
}

func m4Reconcile(t *testing.T, s *postgres.Store, account, pnl, cash string, fillCount int) {
	t.Helper()
	ctx := context.Background()
	a, err := s.PaperAccount(ctx, account)
	if err != nil || a.Balance.String() != cash {
		t.Fatalf("cash %+v %v; want %s", a, err, cash)
	}
	ledger, err := s.PaperLedger(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	sum := spotDecimal("0")
	for _, e := range ledger {
		sum = sum.Add(e.Amount)
	}
	if !sum.Equal(a.Balance) {
		t.Fatalf("ledger %s cash %s", sum, a.Balance)
	}
	ps, err := s.PaperPositions(ctx, account)
	if err != nil || len(ps) != 1 || ps[0].Status != "closed" || !ps[0].ReservedMargin.IsZero() || !ps[0].RemainingQuantity.IsZero() || ps[0].RealizedPnL.String() != pnl {
		t.Fatalf("position %+v %v; want pnl %s", ps, err, pnl)
	}
	fs, err := s.PaperFills(ctx, account)
	if err != nil || len(fs) != fillCount {
		t.Fatalf("fills %+v %v", fs, err)
	}
}

func TestM4SignalLinearJourneyReconcilesAfterRestartAndPausedStop(t *testing.T) {
	for _, action := range []strategy.Action{strategy.ManualPaper, strategy.AutoPaper} {
		for _, side := range []string{"long", "short"} {
			t.Run(string(action)+"/"+side, func(t *testing.T) {
				ctx := context.Background()
				store, owner, a, _ := newSpotTest(t)
				signal := m4Signal(t, store, a.ID, action, side, 0)
				r, order := m4Decide(t, owner, signal)
				m4Observe(t, owner, 0, "100", true)
				fs, _ := store.PaperFills(ctx, a.ID)
				if len(fs) != 0 {
					t.Fatal("filled at signal observation")
				}
				m4Observe(t, owner, 1, "100", true)
				stop, target := "90", "110"
				if side == "short" {
					stop, target = "110", "90"
				}
				pid := paper.Identity("position", order.ID)
				_, err := owner.AdjustProtection(ctx, paper.ProtectionAdjustment{RequestID: "partial-target", PositionID: pid, At: r.At.Add(time.Second), StaleAfter: time.Minute, StopPrice: &stop, TargetPrice: &target, TargetQuantity: m4Text("0.5")})
				if err != nil {
					t.Fatal(err)
				}
				m4Observe(t, owner, 2, target, true)
				p, err := store.PaperPosition(ctx, pid)
				if err != nil || p.RemainingQuantity.String() != "1.5" || p.ReservedMargin.String() != "30" || !p.Protection.TargetDone {
					t.Fatalf("partial %+v %v", p, err)
				}
				rate := "0.01"
				event := paper.FundingEvent{ID: "remaining-funding", Instrument: signal.Instrument, EffectiveAt: r.At.Add(3 * time.Second), AvailableAt: r.At.Add(3 * time.Second), Source: "fixture", Provenance: "remaining 1.5 units", Rate: &rate, Price: spotDecimal("100")}
				funding, err := owner.ApplyFunding(ctx, event)
				wantPayment := "-1.5"
				wantMargin := "28.5"
				if side == "short" {
					wantPayment = "1.5"
					wantMargin = "31.5"
				}
				if err != nil || len(funding.Applications) != 1 || funding.Applications[0].Payment.String() != wantPayment {
					t.Fatalf("funding %+v %v", funding, err)
				}
				p, err = store.PaperPosition(ctx, pid)
				if err != nil || p.ReservedMargin.String() != wantMargin {
					t.Fatalf("funded collateral %+v %v", p, err)
				}
				owner.Close()
				owner, err = store.AcquirePaperOwner(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Close()
				if _, err := owner.ApplyFunding(ctx, event); err != nil {
					t.Fatal(err)
				}
				info := spotInfo()
				info.Instrument = signal.Instrument
				retry, err := owner.DecideSignal(ctx, r, info, true)
				if err != nil || retry.Order == nil || retry.Order.ID != order.ID || retry.Order.Status != "filled" {
					t.Fatalf("restarted retry %+v %v", retry, err)
				}
				if err := store.SetKillSwitch(ctx, true); err != nil {
					t.Fatal(err)
				}
				if err := store.SetStrategyEnabled(ctx, signal.InstanceID, false); err != nil {
					t.Fatal(err)
				}
				m4Observe(t, owner, 4, stop, false)
				pnl, cash := "-11.89", "988.11"
				fees := "0.39"
				if side == "short" {
					pnl, cash = "-8.91", "991.09"
					fees = "0.41"
				}
				m4Reconcile(t, store, a.ID, pnl, cash, 3)
				p, _ = store.PaperPosition(ctx, pid)
				if p.FeesPaid.String() != fees {
					t.Fatalf("fees %s", p.FeesPaid)
				}
			})
		}
	}
}

func TestM4SignalFundingGapLiquidationBeatsQueuedClose(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			ctx := context.Background()
			s, o, a, _ := newSpotTest(t)
			signal := m4Signal(t, s, a.ID, strategy.AutoPaper, side, 0)
			r, order := m4Decide(t, o, signal)
			m4Observe(t, o, 1, "100", true)
			rate := "0.01"
			event := paper.FundingEvent{ID: "fund-gap", Instrument: signal.Instrument, EffectiveAt: r.At.Add(2 * time.Second), AvailableAt: r.At.Add(2 * time.Second), Source: "fixture", Provenance: "gap fixture", Rate: &rate, Price: spotDecimal("100")}
			if _, err := o.ApplyFunding(ctx, event); err != nil {
				t.Fatal(err)
			}
			close := spotRequest(a.ID, "competing-close")
			close.Instrument = signal.Instrument
			close.Side = side
			close.Leverage = 5
			close.Action = "close"
			close.PositionID = paper.Identity("position", order.ID)
			close.At = r.At.Add(3 * time.Second)
			info := spotInfo()
			info.Instrument = signal.Instrument
			if pending, err := o.SubmitOrder(ctx, close, info, false); err != nil || pending.Status != "pending" {
				t.Fatalf("queued close %+v %v", pending, err)
			}
			price, deficit, fee := "50", "62.5", "0.5"
			if side == "short" {
				price, deficit, fee = "150", "59.5", "1.5"
			}
			m4Observe(t, o, 4, price, false)
			m4Reconcile(t, s, a.ID, "-40.2", "959.8", 2)
			fs, _ := s.PaperFills(ctx, a.ID)
			found := false
			for _, f := range fs {
				if f.ExitReason == "liquidation_approximate" {
					found = true
					if !f.Gap || f.IsolatedDeficit.String() != deficit || f.Fee.String() != fee {
						t.Fatalf("liquidation %+v", f)
					}
				}
			}
			if !found {
				t.Fatal("missing liquidation")
			}
			orders, _ := s.PaperOrders(ctx, a.ID)
			for _, order := range orders {
				if order.Request.RequestID == close.RequestID && order.Status != "rejected" {
					t.Fatalf("queued close %+v", order)
				}
			}
		})
	}
}

func m4PublicSnapshot(t *testing.T, s *postgres.Store, a string) string {
	t.Helper()
	ctx := context.Background()
	account, e := s.PaperAccount(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	ps, e := s.PaperPositions(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	fs, e := s.PaperFills(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	ls, e := s.PaperLedger(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	os, e := s.PaperOrders(ctx, a)
	if e != nil {
		t.Fatal(e)
	}
	fund, e := s.PaperFunding(ctx)
	if e != nil {
		t.Fatal(e)
	}
	ds, e := s.SignalDecisions(ctx)
	if e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal([]any{account, ps, fs, ls, os, fund, ds})
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestM4JoinedSignalExitsRollbackAndIdenticalEventRetries(t *testing.T) {
	for _, kind := range []string{"partial-target", "funding-liquidation"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, o, a, dsn := newSpotTest(t)
			signal := m4Signal(t, s, a.ID, strategy.ManualPaper, "long", 0)
			r, order := m4Decide(t, o, signal)
			m4Observe(t, o, 1, "100", true)
			if kind == "partial-target" {
				if _, err := o.AdjustProtection(ctx, paper.ProtectionAdjustment{RequestID: "partial", PositionID: paper.Identity("position", order.ID), At: r.At.Add(time.Second), StaleAfter: time.Minute, StopPrice: m4Text("90"), TargetPrice: m4Text("110"), TargetQuantity: m4Text("0.5")}); err != nil {
					t.Fatal(err)
				}
			}
			conn, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close(ctx)
			obs, g := spotObservation(2)
			obs.Instrument = signal.Instrument
			obs.Price = spotDecimal("110")
			rate := "0.2"
			event := paper.FundingEvent{ID: "funding-fault", Instrument: signal.Instrument, EffectiveAt: r.At.Add(2 * time.Second), AvailableAt: r.At.Add(2 * time.Second), Source: "fixture", Provenance: "exhausted collateral fixture", Rate: &rate, Price: spotDecimal("100")}
			apply := func() error {
				if kind == "partial-target" {
					_, err := o.Observe(ctx, obs, g)
					return err
				}
				_, err := o.ApplyFunding(ctx, event)
				return err
			}
			outcome := "paper_price_events"
			if kind == "funding-liquidation" {
				outcome = "paper_funding_events"
			}
			for _, fault := range []struct{ table, op string }{{"paper_fills", "INSERT"}, {"paper_accounts", "UPDATE"}, {"paper_positions", "INSERT"}, {"paper_ledger_entries", "INSERT"}, {"journal_outbox", "INSERT"}, {outcome, "INSERT"}} {
				before := m4PublicSnapshot(t, s, a.ID)
				_, err := conn.Exec(ctx, `CREATE FUNCTION millionaire.m4_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END; $$; CREATE TRIGGER m4_fail BEFORE `+fault.op+` ON millionaire.`+fault.table+` FOR EACH ROW EXECUTE FUNCTION millionaire.m4_fail()`)
				if err != nil {
					t.Fatal(err)
				}
				if err := apply(); err == nil {
					t.Fatalf("%s failure ignored", fault.table)
				}
				if after := m4PublicSnapshot(t, s, a.ID); after != before {
					t.Fatalf("%s exposed partial state", fault.table)
				}
				if _, err := conn.Exec(ctx, `DROP TRIGGER m4_fail ON millionaire.`+fault.table+`; DROP FUNCTION millionaire.m4_fail()`); err != nil {
					t.Fatal(err)
				}
			}
			if err := apply(); err != nil {
				t.Fatal(err)
			}
			after := m4PublicSnapshot(t, s, a.ID)
			if err := apply(); err != nil {
				t.Fatal(err)
			}
			if m4PublicSnapshot(t, s, a.ID) != after {
				t.Fatal("identical event duplicated state")
			}
			p, _ := s.PaperPosition(ctx, paper.Identity("position", order.ID))
			if kind == "partial-target" {
				if p.RemainingQuantity.String() != "1.5" || p.ReservedMargin.String() != "30" {
					t.Fatal(p)
				}
			} else {
				m4Reconcile(t, s, a.ID, "-40.2", "959.8", 2)
			}
		})
	}
}

func m4Text(s string) *string { return &s }
