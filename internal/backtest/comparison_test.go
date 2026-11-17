package backtest_test

import (
	"encoding/json"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/strategy"
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

func TestComparisonDoesNotNormalizeImmutableIdentifiersAsNumbers(t *testing.T) {
	a := backtest.Snapshot{Report: backtest.Report{Config: backtest.Config{VersionID: "001"}, Manifest: backtest.Manifest{DatasetHash: "001"}}}
	b := a
	b.Report.Config.VersionID = "1"
	b.Report.Manifest.DatasetHash = "1"
	c := backtest.Compare(a, b)
	if len(c.Differences) != 2 {
		t.Fatalf("identity was normalized: %+v", c.Differences)
	}
}

func TestComparisonWarnsWhenIdenticalManifestLacksModelProvenance(t *testing.T) {
	a := backtest.Snapshot{EvidenceStatus: "verified", Metrics: &analytics.Report{ValuationStatus: "complete"}, Report: backtest.Report{Manifest: backtest.Manifest{Build: "go/revision=known/modified=false"}}}
	c := backtest.Compare(a, a)
	if c.DirectlyComparable {
		t.Fatal("missing model/hash provenance was treated as directly comparable")
	}
}

func TestComparisonPreservesUnknownFundingInsteadOfAssumingZero(t *testing.T) {
	a := backtest.Snapshot{EvidenceStatus: "verified", Metrics: &analytics.Report{ValuationStatus: "complete"}, Report: backtest.Report{Config: backtest.Config{Instrument: marketdata.InstrumentInfo{Instrument: domain.Instrument{Market: domain.Linear}}}, Manifest: backtest.Manifest{Build: "known", StrategyHash: "s", ConfigurationHash: "c", DatasetHash: "d", ReproducibilityHash: "r", Model: "linear", Clock: "close", EndPolicy: "retain"}}}
	for _, status := range []string{"", "unknown", "missing"} {
		a.Report.FundingCoverage.Status = status
		c := backtest.Compare(a, a)
		if c.DirectlyComparable {
			t.Fatalf("funding %q assumed complete", status)
		}
	}
	a.Report.FundingCoverage.Status = "complete"
	if c := backtest.Compare(a, a); !c.DirectlyComparable {
		t.Fatalf("known matching funding rejected %+v", c)
	}
}

func TestComparisonRetainsCausalSignalDifferencesAcrossExecutionIdentities(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	signal := strategy.Signal{ID: "left-signal", InstanceID: "left-instance", VersionID: "immutable", EventID: "same-event", ObservedAt: at}
	saved := analytics.SignalReport{ID: "left-report", RunID: "left-run", EvidenceHash: "left-audit", Through: at, Rule: analytics.SuccessRule{ThresholdPercent: "5", StopPercent: "50", WindowBars: 1, Basis: "subsequent-closed-ohlcv"}, Outcomes: []analytics.SignalOutcome{{SignalID: signal.ID, Status: "success", ObservedBars: 1}}, Warnings: []string{"hypothetical", "Unfilled intent: left-order"}}
	left := backtest.Snapshot{Report: backtest.Report{ID: "left-run", Signals: []strategy.Signal{signal}, Warnings: []string{"Unfilled intent: left-order"}}, SignalReports: []analytics.SignalReport{saved}}
	right := left
	right.Report.ID = "right-run"
	right.Report.Signals = append([]strategy.Signal(nil), left.Report.Signals...)
	right.Report.Signals[0].ID = "right-signal"
	right.Report.Signals[0].InstanceID = "right-instance"
	right.Report.Warnings = []string{"Unfilled intent: right-order"}
	right.SignalReports = append([]analytics.SignalReport(nil), left.SignalReports...)
	right.SignalReports[0].ID = "right-report"
	right.SignalReports[0].RunID = "right-run"
	right.SignalReports[0].EvidenceHash = "right-audit"
	right.SignalReports[0].Outcomes = []analytics.SignalOutcome{{SignalID: "right-signal", Status: "success", ObservedBars: 1}}
	right.SignalReports[0].Warnings = []string{"hypothetical", "Unfilled intent: right-order"}
	before := backtest.Hash([]any{left, right})
	if c := backtest.Compare(left, right); len(c.Differences) != 0 || c.Left.SignalReports[0].ID != "left-report" || c.Right.SignalReports[0].ID != "right-report" {
		t.Fatalf("audit identity confused with assumptions %+v", c.Differences)
	}
	if backtest.Hash([]any{left, right}) != before {
		t.Fatal("comparison mutated saved evidence")
	}
	for _, change := range []struct {
		name  string
		alter func(*backtest.Snapshot)
	}{
		{"rule", func(s *backtest.Snapshot) { s.SignalReports[0].Rule.ThresholdPercent = "6" }},
		{"outcome", func(s *backtest.Snapshot) { s.SignalReports[0].Outcomes[0].Status = "failure" }},
		{"observations", func(s *backtest.Snapshot) { s.SignalReports[0].Through = at.Add(time.Minute) }},
		{"causal signal", func(s *backtest.Snapshot) { s.Report.Signals[0].ObservedAt = at.Add(time.Second) }},
		{"unknown reference", func(s *backtest.Snapshot) { s.SignalReports[0].Outcomes[0].SignalID = "missing-signal" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			// JSON creates independent public values; no alias can conceal a mutation.
			var different backtest.Snapshot
			raw, _ := json.Marshal(right)
			if e := json.Unmarshal(raw, &different); e != nil {
				t.Fatal(e)
			}
			change.alter(&different)
			c := backtest.Compare(left, different)
			if len(c.Differences) != 1 || c.Differences[0].Field != "signal_reports" {
				t.Fatalf("lost real difference %+v", c.Differences)
			}
		})
	}
}
