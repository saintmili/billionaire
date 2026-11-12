package paper_test

import (
	"github.com/saintmili/billionaire/internal/paper"
	"testing"
)

func TestRiskSizingRoundsDownToExecutablePrecision(t *testing.T) {
	p := paper.RiskPolicy{FixedRisk: "10"}
	q, reason := paper.SizeEntry(p, amount("1000"), amount("103"), amount("100"), amount("0.01"))
	if reason != "" || q.String() != "3.33" {
		t.Fatalf("size=%s reason=%s", q, reason)
	}
}
