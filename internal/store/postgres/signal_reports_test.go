package postgres_test

import (
	"context"
	"github.com/saintmili/billionaire/internal/analytics"
	"testing"
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
