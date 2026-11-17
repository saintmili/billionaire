package backtest_test

import (
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"testing"
	"time"
)

func TestComparisonExposesOverlapWithoutClaimingMatchedPerformance(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	left := backtest.Snapshot{Report: backtest.Report{ID: "a", Status: "completed", Config: backtest.Config{From: at, To: at.Add(2 * time.Hour), InitialCapital: "1000"}}}
	right := left
	right.Report.ID = "b"
	right.Report.Config.From = at.Add(time.Hour)
	right.Report.Config.To = at.Add(3 * time.Hour)
	right.Report.Config.InitialCapital = "2000"
	r := backtest.Compare(left, right)
	if r.DirectlyComparable || r.MatchedPeriod == nil || !r.MatchedPeriod.From.Equal(at.Add(time.Hour)) || !r.MatchedPeriod.To.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("comparison: %+v", r)
	}
	found := false
	for _, d := range r.Differences {
		if d.Field == "config.initial_capital" {
			found = true
		}
	}
	if !found || len(r.Warnings) == 0 {
		t.Fatalf("missing assumptions: %+v", r)
	}
}

func TestComparisonNeverTreatsUnavailablePerformanceAsZero(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, status := range []string{"failed", "running", "cancelled", "completed"} {
		a := backtest.Snapshot{Report: backtest.Report{Status: status, Config: backtest.Config{From: at, To: at.Add(time.Hour)}}}
		b := a
		b.Report.Config.From = at.Add(time.Hour)
		b.Report.Config.To = at.Add(2 * time.Hour)
		c := backtest.Compare(a, b)
		if c.DirectlyComparable || c.MatchedPeriod != nil || c.Left.Metrics != nil {
			t.Fatalf("unavailable performance %+v", c)
		}
	}
}
func TestComparisonNumericallyEquivalentAssumptionsAndTimeframeOrder(t *testing.T) {
	a := backtest.Snapshot{Report: backtest.Report{Config: backtest.Config{InitialCapital: "1000", FeeRate: "0.001", Timeframes: []domain.Timeframe{"5m", "1m"}}}}
	b := a
	b.Report.Config.InitialCapital = "1000.0"
	b.Report.Config.FeeRate = "0.0010"
	b.Report.Config.Timeframes = []domain.Timeframe{"1m", "5m"}
	c := backtest.Compare(a, b)
	if len(c.Differences) != 0 {
		t.Fatalf("format-only difference %+v", c.Differences)
	}
	if a.Report.Config.Timeframes[0] != "5m" {
		t.Fatal("comparison mutated input")
	}
}
