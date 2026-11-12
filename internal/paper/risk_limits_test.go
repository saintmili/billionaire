package paper_test

import (
	"github.com/saintmili/billionaire/internal/paper"
	"testing"
	"time"
)

func TestEntryRiskLimitsAtBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name   string
		p      paper.RiskPolicy
		s      paper.RiskState
		reason string
	}{
		{"position cap", paper.RiskPolicy{MaxPositions: 2, Pyramiding: true}, paper.RiskState{Positions: 2}, "maximum positions exceeded"},
		{"position below", paper.RiskPolicy{MaxPositions: 2}, paper.RiskState{Positions: 1}, ""},
		{"pyramiding default", paper.RiskPolicy{}, paper.RiskState{SymbolPositions: 1}, "pyramiding disabled"},
		{"exposure equality", paper.RiskPolicy{SymbolExposure: "300", Pyramiding: true}, paper.RiskState{SymbolExposure: amount("100")}, ""},
		{"exposure over", paper.RiskPolicy{SymbolExposure: "299", Pyramiding: true}, paper.RiskState{SymbolExposure: amount("100")}, "per-symbol exposure exceeded"},
		{"loss equality", paper.RiskPolicy{DailyLoss: "10"}, paper.RiskState{DailyPnL: amount("-10")}, "daily loss limit reached"},
		{"loss below", paper.RiskPolicy{DailyLoss: "10"}, paper.RiskState{DailyPnL: amount("-9.99")}, ""},
		{"cooldown equality", paper.RiskPolicy{CooldownSeconds: 60}, paper.RiskState{LastEntry: now.Add(-time.Minute)}, ""},
		{"cooldown active", paper.RiskPolicy{CooldownSeconds: 60}, paper.RiskState{LastEntry: now.Add(-59 * time.Second)}, "entry cooldown active"},
		{"margin equality", paper.RiskPolicy{MarginPercent: "20"}, paper.RiskState{Equity: amount("1000")}, ""},
		{"margin over", paper.RiskPolicy{MarginPercent: "19"}, paper.RiskState{Equity: amount("1000")}, "margin usage exceeded"},
		{"risk equality", paper.RiskPolicy{FixedRisk: "20"}, paper.RiskState{}, ""},
		{"risk over", paper.RiskPolicy{FixedRisk: "19"}, paper.RiskState{}, "entry risk budget exceeded"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := paper.EntryRiskReason(tt.p, tt.s, amount("2"), amount("100"), amount("90"), 1, now)
			if got != tt.reason {
				t.Fatalf("reason %q want %q", got, tt.reason)
			}
		})
	}
}

func TestRiskPolicyValidationAndUnavailableSizingState(t *testing.T) {
	for _, p := range []paper.RiskPolicy{{FixedRisk: "0"}, {PercentRisk: "101"}, {DailyLoss: "bad"}, {MarginPercent: "-1"}, {MaxPositions: -1}, {MaxLeverage: -1}, {CooldownSeconds: -1}} {
		if p.Validate() == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
	for _, stop := range []string{"0", "100", "101"} {
		if _, why := paper.SizeEntry(paper.RiskPolicy{FixedRisk: "10"}, amount("1000"), amount("100"), amount(stop), amount("0.01")); why == "" {
			t.Fatalf("accepted stop %s", stop)
		}
	}
	if _, why := paper.SizeEntry(paper.RiskPolicy{PercentRisk: "1"}, amount("0"), amount("100"), amount("90"), amount("0.01")); why == "" {
		t.Fatal("unavailable equity accepted")
	}
	q, why := paper.SizeEntry(paper.RiskPolicy{FixedRisk: "20", PercentRisk: "1"}, amount("1000"), amount("100"), amount("90"), amount("0.01"))
	if why != "" || q.String() != "1" {
		t.Fatalf("strictest budget %s %s", q, why)
	}
}
