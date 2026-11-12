package strategy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/strategy"
)

func barsForBreakout(t *testing.T) []domain.Candle {
	t.Helper()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	decimal := func(s string) domain.Decimal {
		d, e := domain.ParseDecimal(s)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	bars := []domain.Candle{}
	i := domain.Instrument{Provider: "fixture", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	for n := range 22 {
		c := domain.Candle{Instrument: i, Timeframe: "1m", OpenTime: start.Add(time.Duration(n) * time.Minute), EndTime: start.Add(time.Duration(n+1) * time.Minute), Open: decimal("9"), High: decimal("10"), Low: decimal("8"), Close: decimal("9.5"), BaseVolume: decimal("1"), Closed: true, Source: "fixture"}
		if n == 20 {
			c.Open = decimal("10")
			c.High = decimal("12")
			c.Low = decimal("9")
			c.Close = decimal("11")
		}
		if n == 21 {
			c.High = decimal("13")
			c.Close = decimal("9.8")
		}
		c.ObservedAt = c.EndTime
		bars = append(bars, marketdata.Stamp(c))
	}
	return bars
}
func TestBreakoutUsesPrecedingTwentyAndRejectsFalseBreakout(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	bars := barsForBreakout(t)
	for _, tc := range []struct {
		n    int
		want bool
	}{{19, false}, {20, true}, {21, false}} {
		snapshot, err := strategy.NewSnapshot(bars[tc.n], bars, d.Timeframes)
		if err != nil {
			t.Fatal(err)
		}
		decision, err := strategy.Evaluate(registry, d, snapshot)
		if err != nil || decision.Entry != tc.want {
			t.Fatalf("bar %d: %+v %v", tc.n, decision, err)
		}
		if tc.want && (decision.Stop.String() != "8.9" || decision.Target.String() != "15.2") {
			t.Fatalf("incorrect risk geometry: %+v", decision)
		}
	}
}

func constant(s string) *string { return &s }
func TestRulesUseAsOfClosedHigherFramesAndValidateOperators(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	d.Implementation = "rules"
	d.Parameters = map[string]string{}
	d.Timeframes = []domain.Timeframe{"1m", "4h"}
	d.Entry = &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "sma", Timeframe: "4h", Period: 1}, Right: strategy.Value{Constant: constant("100")}}
	d, err = strategy.Prepare(d, registry)
	if err != nil {
		t.Fatal(err)
	}
	bars := barsForBreakout(t)
	event := bars[20]
	higher := event
	higher.Timeframe = "4h"
	higher.OpenTime = event.OpenTime.Truncate(4 * time.Hour).Add(-4 * time.Hour)
	higher.EndTime = higher.OpenTime.Add(4 * time.Hour)
	higher.ObservedAt = higher.EndTime
	twelve, _ := domain.ParseDecimal("12")
	higher.Open = twelve
	higher.High = twelve
	higher.Low = twelve
	higher.Close = twelve
	higher = marketdata.Stamp(higher)
	future := higher
	future.OpenTime = higher.EndTime
	future.EndTime = future.OpenTime.Add(4 * time.Hour)
	future.ObservedAt = future.EndTime
	future.Close, _ = domain.ParseDecimal("999")
	future.High = future.Close
	future = marketdata.Stamp(future)
	history := append(bars, higher, future)
	snapshot, err := strategy.NewSnapshot(event, history, d.Timeframes)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Evaluate(registry, d, snapshot)
	if err != nil || decision.Entry {
		t.Fatalf("future higher close leaked: %+v %v", decision, err)
	}
	d.Entry.Right.Constant = constant("10")
	decision, err = strategy.Evaluate(registry, d, snapshot)
	if err != nil || !decision.Entry {
		t.Fatalf("known higher close not used: %+v %v", decision, err)
	}
	d.Entry.Op = "execute"
	if _, err := strategy.Prepare(d, registry); err == nil {
		t.Fatal("unknown executable rule accepted")
	}
}
func TestCrossoverUsesPreviousValuesAndNeedsWarmup(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	d.Implementation = "rules"
	d.Parameters = map[string]string{}
	d.Entry = &strategy.Rule{Op: "crosses_above", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: constant("10")}}
	d, err = strategy.Prepare(d, registry)
	if err != nil {
		t.Fatal(err)
	}
	bars := barsForBreakout(t)
	snapshot, err := strategy.NewSnapshot(bars[20], bars, d.Timeframes)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Evaluate(registry, d, snapshot)
	if err != nil || !decision.Entry {
		t.Fatalf("crossover missed: %+v %v", decision, err)
	}
	snapshot, err = strategy.NewSnapshot(bars[0], bars, d.Timeframes)
	if err != nil {
		t.Fatal(err)
	}
	decision, err = strategy.Evaluate(registry, d, snapshot)
	if err != nil || decision.Ready {
		t.Fatal("crossover without previous bar accepted")
	}
}

func TestEngulfingRequiresTwoCompletedBodies(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	d.Implementation = "engulfing"
	d.Parameters = map[string]string{}
	d, err = strategy.Prepare(d, registry)
	if err != nil {
		t.Fatal(err)
	}
	bars := barsForBreakout(t)
	prev := bars[19]
	prev.Open, _ = domain.ParseDecimal("10")
	prev.Close, _ = domain.ParseDecimal("9")
	prev.High = prev.Open
	prev = marketdata.Stamp(prev)
	current := bars[20]
	current.Open, _ = domain.ParseDecimal("8.9")
	current.Low, _ = domain.ParseDecimal("8")
	current.Close, _ = domain.ParseDecimal("10.1")
	current = marketdata.Stamp(current)
	snapshot, err := strategy.NewSnapshot(current, []domain.Candle{prev}, d.Timeframes)
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Evaluate(registry, d, snapshot)
	if err != nil || !result.Entry {
		t.Fatalf("engulfing missed: %+v %v", result, err)
	}
	current.Closed = false
	current.ObservedAt = current.OpenTime.Add(30 * time.Second)
	current = marketdata.Stamp(current)
	d.Evaluation = strategy.OnUpdate
	snapshot, err = strategy.NewSnapshot(current, []domain.Candle{prev}, d.Timeframes)
	if err != nil {
		t.Fatal(err)
	}
	result, err = strategy.Evaluate(registry, d, snapshot)
	if err != nil || result.Entry {
		t.Fatal("forming engulfing candle emitted")
	}
}
func TestSupportPivotIsUnavailableUntilRightBarsComplete(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	d.Implementation = "support_resistance"
	d.Parameters = map[string]string{"lookback": "4", "pivot_left": "1", "pivot_right": "2", "zone_tolerance": "0.2", "min_touches": "1", "setup": "rejection"}
	d, err = strategy.Prepare(d, registry)
	if err != nil {
		t.Fatal(err)
	}
	bars := barsForBreakout(t)[:8]
	for n, low := range []string{"9", "9", "9", "8", "8", "5", "7", "5.1"} {
		bars[n].Low, _ = domain.ParseDecimal(low)
		bars[n].Open, _ = domain.ParseDecimal("9.5")
		bars[n].Close, _ = domain.ParseDecimal("9.6")
		if n == 7 {
			bars[n].Open, _ = domain.ParseDecimal("8")
			bars[n].Close, _ = domain.ParseDecimal("9")
		}
		bars[n] = marketdata.Stamp(bars[n])
	}
	for _, tc := range []struct {
		index int
		want  bool
	}{{6, false}, {7, true}} {
		snapshot, err := strategy.NewSnapshot(bars[tc.index], bars, d.Timeframes)
		if err != nil {
			t.Fatal(err)
		}
		result, err := strategy.Evaluate(registry, d, snapshot)
		if err != nil || result.Entry != tc.want {
			t.Fatalf("confirmation at %d: %+v %v", tc.index, result, err)
		}
	}
}

func TestShortBreakoutIsSymmetricAndRejectsSpot(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	d.Direction = strategy.Short
	d.Markets = []domain.MarketType{domain.Linear}
	d, err = strategy.Prepare(d, registry)
	if err != nil {
		t.Fatal(err)
	}
	bars := barsForBreakout(t)
	for n := range bars {
		bars[n].Instrument.Market = domain.Linear
		bars[n] = marketdata.Stamp(bars[n])
	}
	event := bars[20]
	event.Open, _ = domain.ParseDecimal("8.5")
	event.Close, _ = domain.ParseDecimal("7")
	event.High, _ = domain.ParseDecimal("9")
	event.Low, _ = domain.ParseDecimal("6")
	event = marketdata.Stamp(event)
	snapshot, err := strategy.NewSnapshot(event, bars, d.Timeframes)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := strategy.Evaluate(registry, d, snapshot)
	if err != nil || !decision.Entry || decision.Stop.String() != "9.1" || decision.Target.String() != "2.8" {
		t.Fatalf("short geometry: %+v %v", decision, err)
	}
	event.Instrument.Market = domain.Spot
	event = marketdata.Stamp(event)
	snapshot, err = strategy.NewSnapshot(event, nil, d.Timeframes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strategy.Evaluate(registry, d, snapshot); err == nil {
		t.Fatal("short evaluation accepted spot")
	}
}

func TestReferenceBreakoutAcrossInitialInstruments(t *testing.T) {
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(breakoutYAML), registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{"BTC", "ETH"} {
		for _, market := range []domain.MarketType{domain.Spot, domain.Linear} {
			bars := barsForBreakout(t)
			for n := range bars {
				bars[n].Instrument.Base = base
				bars[n].Instrument.ID = base + "USDT"
				bars[n].Instrument.Market = market
				bars[n] = marketdata.Stamp(bars[n])
			}
			for _, n := range []int{20, 21} {
				snapshot, err := strategy.NewSnapshot(bars[n], bars, d.Timeframes)
				if err != nil {
					t.Fatal(err)
				}
				decision, err := strategy.Evaluate(registry, d, snapshot)
				if err != nil || decision.Entry != (n == 20) {
					t.Fatalf("%s %s bar %d: %+v %v", base, market, n, decision, err)
				}
			}
		}
	}
}
