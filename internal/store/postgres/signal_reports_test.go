package postgres_test

import (
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"testing"
	"time"
)

func TestSignalReportRequiresPersistedCompletedRunAndValidRule(t *testing.T) {
	s, _, _, _ := newSpotTest(t)
	_, err := s.BacktestSignals(context.Background(), "absent", analytics.SignalFilter{}, analytics.SuccessRule{ThresholdPercent: "1", StopPercent: "1", WindowBars: 2, Basis: "subsequent-closed-ohlcv"})
	if err == nil {
		t.Fatal("missing run accepted")
	}
	if _, err = s.SignalReport(context.Background(), "absent"); err == nil {
		t.Fatal("missing report accepted")
	}
}

func TestForwardSignalReportRetainsRejectedSuccessAndReplaySuppression(t *testing.T) {
	ctx := context.Background()
	s, owner, a, _ := newSpotTest(t)
	// User-facing rules strategy: satisfied updates at 0,1,2 seconds; a 2s
	// cooldown emits two signals and suppresses exactly one candidate.
	d := strategy.Definition{SchemaVersion: 1, ID: "report-demo", Name: "Report demo", Implementation: "rules", Direction: strategy.Long, Markets: []domain.MarketType{domain.Spot}, Timeframes: []domain.Timeframe{"1m"}, Primary: "1m", Evaluation: strategy.OnUpdate, Emission: strategy.WhileTrue, SignalCooldown: "2s", RiskPercent: "1", Parameters: map[string]string{}, Entry: &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: strptr("99")}}}
	v, e := s.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if e != nil {
		t.Fatal(e)
	}
	i := strategy.Instance{ID: "report-demo", VersionID: v.ID, Instrument: spotInfo().Instrument, Account: a.ID, Action: strategy.AutoPaper}
	if e = s.AddStrategyInstance(ctx, i); e != nil {
		t.Fatal(e)
	}
	if e = s.SetStrategyEnabled(ctx, i.ID, true); e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	signals := []strategy.Signal{}
	for n := range 3 {
		c := marketdata.Stamp(domain.Candle{Instrument: i.Instrument, Timeframe: "1m", OpenTime: at, EndTime: at.Add(time.Minute), Open: spotDecimal("100"), High: spotDecimal("101"), Low: spotDecimal("90"), Close: spotDecimal("100"), Source: "fixture", ObservedAt: at.Add(time.Duration(n) * time.Second)})
		ev := marketdata.Event{Kind: "fixture", Candle: &c, AllowsEntries: false}
		r, e := s.EvaluateStrategyEvent(ctx, i.ID, ev, strategy.DefaultRegistry())
		if e != nil {
			t.Fatal(e)
		}
		signals = append(signals, r.Signals...)
		duplicate, e := s.EvaluateStrategyEvent(ctx, i.ID, ev, strategy.DefaultRegistry())
		if e != nil || len(duplicate.Signals) != 0 || len(duplicate.Candidates) != 0 {
			t.Fatal("replay inflated evidence", e)
		}
	}
	if len(signals) != 2 {
		t.Fatalf("signals %+v", signals)
	}
	for _, signal := range signals {
		decision, e := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt}, spotInfo(), true)
		if e != nil || decision.Status != "rejected" {
			t.Fatalf("rejection %+v %v", decision, e)
		}
	}
	// Full subsequent bars, one favorable for both untraded signals.
	bars := []domain.Candle{}
	for n := 1; n <= 2; n++ {
		c := domain.Candle{Instrument: i.Instrument, Timeframe: "1m", OpenTime: at.Add(time.Duration(n) * time.Minute), EndTime: at.Add(time.Duration(n+1) * time.Minute), Open: spotDecimal("100"), High: spotDecimal("111"), Low: spotDecimal("99"), Close: spotDecimal("110"), Source: "fixture", Closed: true, ObservedAt: at.Add(time.Duration(n+1) * time.Minute)}
		bars = append(bars, marketdata.Stamp(c))
	}
	if _, e = s.SaveCandles(ctx, bars); e != nil {
		t.Fatal(e)
	}
	rule := analytics.SuccessRule{ThresholdPercent: "10", StopPercent: "5", WindowBars: 2, Basis: "subsequent-closed-ohlcv"}
	f := analytics.SignalFilter{Filter: analytics.Filter{AccountID: a.ID, StrategyVersion: v.ID}, Timeframe: "1m"}
	r, e := s.PaperSignals(ctx, f, rule, at.Add(3*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Groups) != 1 || r.Groups[0].Generated != 2 || r.Groups[0].Candidates != 3 || r.Groups[0].Suppressed != 1 || r.Groups[0].SuppressionRate.String() != "0.3333333333333333" || r.Groups[0].Entries != 0 || r.Groups[0].IntervalsSeconds[0].String() != "2" {
		t.Fatalf("counts %+v", r)
	}
	for _, o := range r.Outcomes {
		if o.Status != "success" {
			t.Fatalf("rejected signals must remain eligible %+v", r)
		}
	}
	read, e := s.SignalReport(ctx, r.ID)
	if e != nil || jsonText(r) != jsonText(read) {
		t.Fatalf("retained report %+v %v", read, e)
	}
	again, e := s.PaperSignals(ctx, f, rule, at.Add(3*time.Minute))
	if e != nil || again.ID != r.ID {
		t.Fatal("non deterministic report", e)
	}
	before, e := s.PaperAccount(ctx, a.ID)
	if e != nil || before.Balance.String() != "1000" {
		t.Fatal("report mutated account", e)
	}
}
func strptr(s string) *string { return &s }

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
