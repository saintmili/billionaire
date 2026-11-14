package postgres_test

import (
	"context"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"testing"
	"time"
)

func TestDailyLossIncludesFundingAndCapsWaivedIsolatedLoss(t *testing.T) {
	for _, scenario := range []struct{ name, rate, gap, limit, reason string }{
		{"funding debit", "0.01", "", "2.2", "daily loss limit reached"},
		{"funding credit", "-0.01", "", "0.2", ""},
		{"waived gap loss", "0", "50", "41", ""},
		{"actual gap loss", "0", "50", "40.2", "daily loss limit reached"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			info := spotInfo()
			info.Instrument.Market = domain.Linear
			r := spotRequest(account.ID, "first")
			r.Instrument = info.Instrument
			r.Leverage = 5
			if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			if _, err := owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			event := paper.FundingEvent{ID: "interval", Instrument: info.Instrument, EffectiveAt: r.At.Add(2 * time.Second), AvailableAt: r.At.Add(2 * time.Second), Source: "fixture", Provenance: "acceptance", Rate: &scenario.rate, Price: spotDecimal("100")}
			if _, err := owner.ApplyFunding(ctx, event); err != nil {
				t.Fatal(err)
			}
			if scenario.gap != "" {
				obs, gate = spotObservation(3)
				obs.Instrument = info.Instrument
				obs.Price = spotDecimal(scenario.gap)
				if _, err := owner.Observe(ctx, obs, gate); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{DailyLoss: scenario.limit, Pyramiding: true}); err != nil {
				t.Fatal(err)
			}
			r.RequestID = "next"
			r.At = r.At.Add(4 * time.Second)
			if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
				t.Fatal(err)
			}
			obs, gate = spotObservation(5)
			obs.Instrument = info.Instrument
			result, err := owner.Observe(ctx, obs, gate)
			if err != nil || len(result.Orders) != 1 || result.Orders[0].Reason != scenario.reason {
				t.Fatalf("daily realized economics: %+v %v", result, err)
			}
		})
	}
}

func TestM4EntryPathsShareExecutionGatesAndDurableRejections(t *testing.T) {
	for _, path := range []string{"standalone", "approved", "auto"} {
		for _, test := range []struct{ name, reason string }{
			{"account limit", "maximum leverage exceeded"}, {"strategy limit", "maximum leverage exceeded"}, {"insufficient margin", "insufficient projected free margin"}, {"unready", "source stale/unready"}, {"stale", "source stale/unready"}, {"disabled", "market disabled"}, {"kill switch", "kill switch active"},
		} {
			t.Run(path+"/"+test.name, func(t *testing.T) {
				ctx := context.Background()
				s, o, a, _ := newSpotTest(t)
				action := strategy.ManualPaper
				if path == "auto" {
					action = strategy.AutoPaper
				}
				signal := m4Signal(t, s, a.ID, action, "long", 0)
				info := spotInfo()
				info.Instrument = signal.Instrument
				quantity := "2"
				if test.name == "insufficient margin" {
					quantity = "60"
				}
				request := spotRequest(a.ID, "standalone")
				request.Instrument = signal.Instrument
				request.StrategyID = signal.StrategyID
				request.Leverage = 5
				request.Quantity = quantity
				decision := paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt, Approve: path == "approved", Quantity: quantity, Leverage: 5}
				if path == "standalone" {
					if _, err := o.SubmitOrder(ctx, request, info, true); err != nil {
						t.Fatal(err)
					}
				} else {
					d, err := o.DecideSignal(ctx, decision, info, true)
					if err != nil || d.Order == nil || d.Order.Status != "pending" {
						t.Fatalf("admission %+v %v", d, err)
					}
				}
				switch test.name {
				case "account limit", "strategy limit":
					scope := ""
					if test.name == "strategy limit" {
						scope = signal.StrategyID
					}
					if err := s.SetRiskPolicy(ctx, a.ID, scope, paper.RiskPolicy{MaxLeverage: 4}); err != nil {
						t.Fatal(err)
					}
				case "kill switch":
					if err := s.SetKillSwitch(ctx, true); err != nil {
						t.Fatal(err)
					}
					o.Close()
					var err error
					o, err = s.AcquirePaperOwner(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer o.Close()
				}
				obs, g := spotObservation(1)
				obs.Instrument = signal.Instrument
				switch test.name {
				case "unready":
					obs.Ready = false
				case "stale":
					g.Now = g.Now.Add(2 * time.Minute)
				case "disabled":
					g.Enabled = false
				}
				for range 2 {
					result, err := o.Observe(ctx, obs, g)
					if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "rejected" || result.Orders[0].Reason != test.reason {
						t.Fatalf("gate %+v %v", result, err)
					}
				}
				orders, err := s.PaperOrders(ctx, a.ID)
				if err != nil || len(orders) != 1 || orders[0].Reason != test.reason {
					t.Fatalf("durable rejection %+v %v", orders, err)
				}
				if path == "standalone" {
					retry, err := o.SubmitOrder(ctx, request, info, true)
					if err != nil || retry.ID != orders[0].ID || retry.Status != "rejected" {
						t.Fatalf("retry %+v %v", retry, err)
					}
				} else {
					retry, err := o.DecideSignal(ctx, decision, info, true)
					if err != nil || retry.Order == nil || retry.Order.ID != orders[0].ID || retry.Order.Status != "rejected" {
						t.Fatalf("retry %+v %v", retry, err)
					}
				}
				a, err = s.PaperAccount(ctx, a.ID)
				fs, e := s.PaperFills(ctx, a.ID)
				ls, le := s.PaperLedger(ctx, a.ID)
				if err != nil || e != nil || le != nil || a.Balance.String() != "1000" || len(fs) != 0 || len(ls) != 1 {
					t.Fatalf("rejection moved money %+v %+v %+v", a, fs, ls)
				}
			})
		}
	}
}

func TestM4EntryPathsShareAdmissionGates(t *testing.T) {
	for _, path := range []string{"standalone", "approved", "auto"} {
		for _, reason := range []string{"market disabled", "kill switch active"} {
			t.Run(path+"/"+reason, func(t *testing.T) {
				ctx := context.Background()
				s, o, a, _ := newSpotTest(t)
				action := strategy.ManualPaper
				if path == "auto" {
					action = strategy.AutoPaper
				}
				signal := m4Signal(t, s, a.ID, action, "long", 0)
				info := spotInfo()
				info.Instrument = signal.Instrument
				enabled := reason != "market disabled"
				if reason == "kill switch active" {
					if err := s.SetKillSwitch(ctx, true); err != nil {
						t.Fatal(err)
					}
				}
				var order paper.Order
				if path == "standalone" {
					r := spotRequest(a.ID, "admission")
					r.Instrument = signal.Instrument
					var err error
					order, err = o.SubmitOrder(ctx, r, info, enabled)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					d, err := o.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: signal.ID, Approve: path == "approved", At: signal.ObservedAt, Quantity: "2", Leverage: 5}, info, enabled)
					if err != nil || d.Order == nil {
						t.Fatalf("decision %+v %v", d, err)
					}
					order = *d.Order
				}
				if order.Status != "rejected" || order.Reason != reason {
					t.Fatal(order)
				}
				a, err := s.PaperAccount(ctx, a.ID)
				fs, e := s.PaperFills(ctx, a.ID)
				if err != nil || e != nil || a.Balance.String() != "1000" || len(fs) != 0 {
					t.Fatalf("admission moved money %+v %+v", a, fs)
				}
			})
		}
	}
}
