package analytics_test

import (
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
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
	r, err := analytics.Signals(analytics.SignalInput{Scope: "backtest", CandidatesAvailable: true, Signals: []strategy.Signal{s, s}, Candidates: []strategy.Candidate{c, c, suppressed}, Candles: []domain.Candle{bar}, Through: at.Add(time.Minute)}, analytics.SignalFilter{}, analytics.SuccessRule{ThresholdPercent: "10", StopPercent: "10", WindowBars: 1, Basis: "subsequent-closed-ohlcv"})
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

func TestSignalsHypotheticalOutcomesIgnoreTradeProfitAndPreserveUnknowns(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	inst := domain.Instrument{Provider: "fixture", Market: domain.Linear, ID: "BTCUSDT", Base: "BTC", Quote: "USDT"}
	rule := analytics.SuccessRule{ThresholdPercent: "10", StopPercent: "5", WindowBars: 2, Basis: "subsequent-closed-ohlcv"}
	makeSignal := func(id string, at time.Time) strategy.Signal {
		return strategy.Signal{ID: id, VersionID: "version", Kind: "entry", Direction: strategy.Long, Instrument: inst, Timeframe: "1m", ObservedAt: at, ProposedEntry: decimal.RequireFromString("100")}
	}
	makeBar := func(min int, high, low string) domain.Candle {
		return domain.Candle{Instrument: inst, Timeframe: "1m", OpenTime: at.Add(time.Duration(min) * time.Minute), EndTime: at.Add(time.Duration(min+1) * time.Minute), High: decimal.RequireFromString(high), Low: decimal.RequireFromString(low), Closed: true}
	}
	cases := []struct {
		name, status string
		bars         []domain.Candle
		through      time.Time
		direction    strategy.Direction
	}{
		{"rejected untraded success", "success", []domain.Candle{makeBar(0, "111", "96")}, at.Add(2 * time.Minute), strategy.Long},
		{"stop first", "failure", []domain.Candle{makeBar(0, "102", "94"), makeBar(1, "111", "96")}, at.Add(2 * time.Minute), strategy.Long},
		{"unknown gap", "missing-data", []domain.Candle{makeBar(1, "111", "96")}, at.Add(2 * time.Minute), strategy.Long},
		{"range incomplete", "incomplete", []domain.Candle{makeBar(0, "102", "96")}, at.Add(time.Minute), strategy.Long},
		{"timeout", "failure", []domain.Candle{makeBar(0, "102", "96"), makeBar(1, "103", "97")}, at.Add(2 * time.Minute), strategy.Long},
		{"short favorable", "success", []domain.Candle{makeBar(0, "101", "89")}, at.Add(2 * time.Minute), strategy.Short},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := makeSignal("one", at)
			s.Direction = tc.direction
			r, e := analytics.Signals(analytics.SignalInput{Scope: "backtest", Signals: []strategy.Signal{s}, Candles: tc.bars, Through: tc.through}, analytics.SignalFilter{}, rule)
			if e != nil || len(r.Outcomes) != 1 || r.Outcomes[0].Status != tc.status {
				t.Fatalf("%+v %v", r, e)
			}
		})
	}
	// Signals three minutes apart: one is converted by two fills in one entry order.
	s1, s2 := makeSignal("one", at), makeSignal("two", at.Add(3*time.Minute))
	r, e := analytics.Signals(analytics.SignalInput{Scope: "forward-paper", Signals: []strategy.Signal{s2, s1, s1}, Orders: []paper.Order{{ID: "entry", Request: paper.OrderRequest{SignalID: "one", Action: "open"}}}, Fills: []paper.Fill{{OrderID: "entry"}, {OrderID: "entry"}}, Through: at.Add(4 * time.Minute)}, analytics.SignalFilter{}, rule)
	if e != nil || r.Groups[0].ConversionRate.String() != "0.5" || r.Groups[0].IntervalsSeconds[0].String() != "180" || r.Groups[0].SuppressionRate != nil {
		t.Fatalf("%+v %v", r, e)
	}
}
