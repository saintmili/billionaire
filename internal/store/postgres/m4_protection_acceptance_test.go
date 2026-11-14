package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

func TestM4SignalLinearAmbiguousStopsUseAdverseSlippage(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			ctx := context.Background()
			s, o, a, _ := newSpotTest(t)
			signal := m4Signal(t, s, a.ID, strategy.AutoPaper, side, 0)
			info := spotInfo()
			info.Instrument = signal.Instrument
			r := paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt, Quantity: "2", Leverage: 5, SlippageBPS: "100"}
			d, err := o.DecideSignal(ctx, r, info, true)
			if err != nil || d.Order == nil {
				t.Fatalf("decision %+v %v", d, err)
			}
			m4Observe(t, o, 1, "100", true)
			obs, g := spotObservation(3)
			obs.Instrument = signal.Instrument
			obs.Price = spotDecimal("100")
			obs.Bar = &paper.PriceBar{Start: r.At.Add(2 * time.Second), End: obs.ObservedAt, Open: spotDecimal("100"), High: spotDecimal("125"), Low: spotDecimal("75"), Close: obs.Price}
			g.Enabled = false
			if _, err := o.Observe(ctx, obs, g); err != nil {
				t.Fatal(err)
			}
			fs, err := s.PaperFills(ctx, a.ID)
			if err != nil || len(fs) != 2 {
				t.Fatalf("fills %+v %v", fs, err)
			}
			price, pnl, cash := "89.1", "-24.1802", "975.8198"
			if side == "short" {
				price, pnl, cash = "111.1", "-24.6202", "975.3798"
			}
			found := false
			for _, f := range fs {
				if f.ExitReason == "stop_loss" {
					found = true
					if !f.Ambiguous || f.Price.String() != price {
						t.Fatalf("ambiguous stop %+v", f)
					}
				}
			}
			if !found {
				t.Fatal(fs)
			}
			m4Reconcile(t, s, a.ID, pnl, cash, 2)
		})
	}
}

func TestM4SignalLinearTrailingSkipsOverlappingBarAndSurvivesRestart(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			ctx := context.Background()
			s, o, a, _ := newSpotTest(t)
			signal := m4Signal(t, s, a.ID, strategy.AutoPaper, side, 0)
			r, order := m4Decide(t, o, signal)
			m4Observe(t, o, 1, "100", true)
			activation, stop, best, adverse, close, next := "110", "90", "125", "95", "110", "114"
			if side == "short" {
				activation, stop, best, adverse, close, next = "90", "110", "75", "105", "90", "86"
			}
			pid := paper.Identity("position", order.ID)
			if _, err := o.AdjustProtection(ctx, paper.ProtectionAdjustment{RequestID: "trail", PositionID: pid, At: r.At.Add(time.Second), StaleAfter: time.Minute, StopPrice: &stop, TargetPrice: m4Text(""), TrailActivation: &activation, TrailDistance: m4Text("10")}); err != nil {
				t.Fatal(err)
			}
			obs, g := spotObservation(3)
			obs.Instrument = signal.Instrument
			obs.Price = spotDecimal(close)
			high, low := best, adverse
			if side == "short" {
				high, low = adverse, best
			}
			obs.Bar = &paper.PriceBar{Start: r.At.Add(2 * time.Second), End: obs.ObservedAt, Open: spotDecimal("100"), High: spotDecimal(high), Low: spotDecimal(low), Close: obs.Price}
			result, err := o.Observe(ctx, obs, g)
			if err != nil || len(result.Orders) != 0 {
				t.Fatalf("same-bar trailing %+v %v", result, err)
			}
			p, err := s.PaperPosition(ctx, pid)
			wantStop := "115"
			if side == "short" {
				wantStop = "85"
			}
			if err != nil || !p.Protection.TrailingActive || p.Protection.StopPrice != wantStop {
				t.Fatalf("trail %+v %v", p, err)
			}
			obs, g = spotObservation(4)
			obs.Instrument = signal.Instrument
			obs.Price = spotDecimal("100")
			obs.Bar = &paper.PriceBar{Start: r.At.Add(2 * time.Second), End: obs.ObservedAt, Open: spotDecimal("100"), High: spotDecimal("130"), Low: spotDecimal("70"), Close: obs.Price}
			result, err = o.Observe(ctx, obs, g)
			if err != nil || len(result.Orders) != 0 {
				t.Fatalf("overlapping bar %+v %v", result, err)
			}
			o.Close()
			o, err = s.AcquirePaperOwner(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer o.Close()
			m4Observe(t, o, 5, next, false)
			fs, err := s.PaperFills(ctx, a.ID)
			if err != nil || len(fs) != 2 {
				t.Fatalf("fills %+v %v", fs, err)
			}
			found := false
			for _, f := range fs {
				if f.ExitReason == "trailing_stop" && f.Gap && f.Price.String() == next {
					found = true
				}
			}
			if !found {
				t.Fatal(fs)
			}
		})
	}
}

func TestM4ConcurrentApprovedAndAutoEntriesRespectAccountLimit(t *testing.T) {
	ctx := context.Background()
	s, o, a, _ := newSpotTest(t)
	if err := s.SetRiskPolicy(ctx, a.ID, "", paper.RiskPolicy{MaxPositions: 1, Pyramiding: true}); err != nil {
		t.Fatal(err)
	}
	manual := m4Signal(t, s, a.ID, strategy.ManualPaper, "long", 0)
	auto := m4Signal(t, s, a.ID, strategy.AutoPaper, "short", 0)
	var wg sync.WaitGroup
	for _, signal := range []strategy.Signal{manual, auto} {
		wg.Go(func() {
			info := spotInfo()
			info.Instrument = signal.Instrument
			_, err := o.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: signal.ID, Approve: signal.Action == strategy.ManualPaper, At: signal.ObservedAt, Quantity: "2", Leverage: 5}, info, true)
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	obs, g := spotObservation(1)
	obs.Instrument = manual.Instrument
	for range 3 {
		wg.Go(func() {
			if _, err := o.Observe(ctx, obs, g); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	ps, err := s.PaperPositions(ctx, a.ID)
	if err != nil || len(ps) != 1 {
		t.Fatalf("account limit %+v %v", ps, err)
	}
	orders, err := s.PaperOrders(ctx, a.ID)
	if err != nil || len(orders) != 2 {
		t.Fatalf("orders %+v %v", orders, err)
	}
	counts := map[string]int{}
	for _, order := range orders {
		counts[order.Status]++
	}
	if counts["filled"] != 1 || counts["rejected"] != 1 {
		t.Fatal(orders)
	}
	fs, err := s.PaperFills(ctx, a.ID)
	if err != nil || len(fs) != 1 {
		t.Fatalf("fills %+v %v", fs, err)
	}
}

func TestM4OppositeSignalCannotImplicitlyReverseExposure(t *testing.T) {
	ctx := context.Background()
	s, o, a, _ := newSpotTest(t)
	long := m4Signal(t, s, a.ID, strategy.AutoPaper, "long", 0)
	_, order := m4Decide(t, o, long)
	m4Observe(t, o, 1, "100", true)
	short := m4Signal(t, s, a.ID, strategy.AutoPaper, "short", 2)
	m4Decide(t, o, short)
	m4Observe(t, o, 3, "100", true)
	ps, err := s.PaperPositions(ctx, a.ID)
	if err != nil || len(ps) != 1 || ps[0].ID != paper.Identity("position", order.ID) || ps[0].Direction() != "long" || ps[0].RemainingQuantity.String() != "2" || ps[0].Status != "open" {
		t.Fatalf("implicit reversal %+v %v", ps, err)
	}
	orders, err := s.PaperOrders(ctx, a.ID)
	if err != nil || len(orders) != 2 {
		t.Fatal(err)
	}
	for _, order := range orders {
		if order.Request.SignalID == short.ID && (order.Status != "rejected" || order.Reason != "pyramiding disabled") {
			t.Fatal(order)
		}
	}
}

func TestM4LiquidationReplaySerializesWithPendingEntryAndClose(t *testing.T) {
	ctx := context.Background()
	s, o, a, _ := newSpotTest(t)
	signal := m4Signal(t, s, a.ID, strategy.AutoPaper, "long", 0)
	r, entry := m4Decide(t, o, signal)
	m4Observe(t, o, 1, "100", true)
	rate := "0.01"
	event := paper.FundingEvent{ID: "concurrent-funding", Instrument: signal.Instrument, EffectiveAt: r.At.Add(2 * time.Second), AvailableAt: r.At.Add(2 * time.Second), Source: "fixture", Provenance: "concurrency fixture", Rate: &rate, Price: spotDecimal("100")}
	if _, err := o.ApplyFunding(ctx, event); err != nil {
		t.Fatal(err)
	}
	next := m4Signal(t, s, a.ID, strategy.ManualPaper, "short", 3)
	m4Decide(t, o, next)
	close := spotRequest(a.ID, "queued")
	close.Instrument = signal.Instrument
	close.Action = "close"
	close.Leverage = 5
	close.PositionID = paper.Identity("position", entry.ID)
	close.At = r.At.Add(3 * time.Second)
	info := spotInfo()
	info.Instrument = signal.Instrument
	if _, err := o.SubmitOrder(ctx, close, info, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKillSwitch(ctx, true); err != nil {
		t.Fatal(err)
	}
	obs, g := spotObservation(4)
	obs.Instrument = signal.Instrument
	obs.Price = spotDecimal("50")
	g.Enabled = false
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := o.Observe(ctx, obs, g); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if _, err := o.ApplyFunding(ctx, event); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	m4Reconcile(t, s, a.ID, "-40.2", "959.8", 2)
	orders, err := s.PaperOrders(ctx, a.ID)
	if err != nil || len(orders) != 4 {
		t.Fatalf("orders %+v %v", orders, err)
	}
	counts := map[string]int{}
	for _, order := range orders {
		counts[order.Status]++
	}
	if counts["filled"] != 2 || counts["rejected"] != 2 {
		t.Fatal(orders)
	}
}

func TestM4FundingAndPartialExitPreserveEighteenDecimalResidual(t *testing.T) {
	ctx := context.Background()
	s, o, a, _ := newSpotTest(t)
	signal := m4Signal(t, s, a.ID, strategy.AutoPaper, "long", 0)
	info := spotInfo()
	info.Instrument = signal.Instrument
	info.MinNotional = spotDecimal("0.01")
	r := paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt, Quantity: "0.003", Leverage: 3, FeeRate: "0"}
	d, err := o.DecideSignal(ctx, r, info, true)
	if err != nil || d.Order == nil {
		t.Fatalf("decision %+v %v", d, err)
	}
	m4Observe(t, o, 1, "100", true)
	rate := "0.000000000000000001"
	event := paper.FundingEvent{ID: "fractional", Instrument: signal.Instrument, EffectiveAt: r.At.Add(2 * time.Second), AvailableAt: r.At.Add(2 * time.Second), Source: "fixture", Provenance: "18 decimal funding", Rate: &rate, Price: spotDecimal("100")}
	result, err := o.ApplyFunding(ctx, event)
	if err != nil || len(result.Applications) != 1 || result.Applications[0].Payment.String() != "-0.000000000000000001" {
		t.Fatalf("fractional funding %+v %v", result, err)
	}
	pid := paper.Identity("position", d.Order.ID)
	for _, step := range []struct {
		id, quantity string
		second       int
	}{{"fractional-partial", "0.001", 3}, {"fractional-final", "0.002", 5}} {
		close := spotRequest(a.ID, step.id)
		close.Instrument = signal.Instrument
		close.Action = "close"
		close.PositionID = pid
		close.Quantity = step.quantity
		close.Leverage = 3
		close.FeeRate = "0"
		close.At = r.At.Add(time.Duration(step.second) * time.Second)
		if _, err := o.SubmitOrder(ctx, close, info, false); err != nil {
			t.Fatal(err)
		}
		m4Observe(t, o, step.second+1, "100", false)
		if step.second == 3 {
			p, err := s.PaperPosition(ctx, pid)
			if err != nil || p.ReservedMargin.String() != "0.066666666666666666" {
				t.Fatalf("partial residual %+v %v", p, err)
			}
		}
	}
	m4Reconcile(t, s, a.ID, "-0.000000000000000001", "999.999999999999999999", 3)
}
