package analytics_test

import (
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/shopspring/decimal"
	"testing"
	"time"
)

func TestSignalReportDistinctCandidatesAndRetrospectiveAmbiguity(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := domain.Instrument{Provider: "bybit", Market: domain.Spot, Base: "BTC", Quote: "USDT"}
	s := strategy.Signal{ID: "one", VersionID: "version", Kind: "entry", Direction: strategy.Long, Instrument: inst, Timeframe: "1m", ObservedAt: at, ProposedEntry: decimal.RequireFromString("100")}
	c := strategy.Candidate{ID: "one", VersionID: "version", Kind: "entry", Instrument: inst, Timeframe: "1m", ObservedAt: at, Emitted: true}
	suppressed := c
	suppressed.ID = "two"
	suppressed.Emitted = false
	bar := domain.Candle{Instrument: inst, Timeframe: "1m", OpenTime: at, EndTime: at.Add(time.Minute), Open: decimal.RequireFromString("100"), High: decimal.RequireFromString("111"), Low: decimal.RequireFromString("89"), Close: decimal.RequireFromString("100"), Closed: true}
	r, err := analytics.Signals(analytics.SignalInput{Scope: "backtest", Signals: []strategy.Signal{s, s}, Candidates: []strategy.Candidate{c, c, suppressed}, Candles: []domain.Candle{bar}, Through: at.Add(time.Minute)}, analytics.SignalFilter{}, analytics.SuccessRule{ThresholdPercent: "10", StopPercent: "10", WindowBars: 1, Basis: "subsequent-closed-ohlcv"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Groups) != 1 || r.Groups[0].Generated != 1 || r.Groups[0].Candidates != 2 || r.Groups[0].SuppressionRate.String() != "0.5" {
		t.Fatalf("%+v", r)
	}
	if r.Outcomes[0].Status != "ambiguous" {
		t.Fatalf("%+v", r.Outcomes)
	}
}
