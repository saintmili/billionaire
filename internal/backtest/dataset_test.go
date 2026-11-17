package backtest_test

import (
	"context"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/strategy"
	"os"
	"testing"
	"time"
)

func TestFreezeNormalizesDuplicatesAndRequiresLookback(t *testing.T) {
	p, e := fixture.Open("../../testdata/market/m3.json")
	if e != nil {
		t.Fatal(e)
	}
	info, e := p.Instrument(context.Background(), domain.Spot, "BTC/USDT")
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open("../../examples/strategies/breakout.yaml")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	d, e := strategy.Parse(f, strategy.DefaultRegistry())
	if e != nil {
		t.Fatal(e)
	}
	v := strategy.Version{ID: strategy.ContentHash(d), Definition: d}
	cfg := backtest.Config{VersionID: v.ID, Instrument: info, From: p.Now().Add(-2 * time.Minute), To: p.Now(), Timeframes: []domain.Timeframe{"1m"}, InitialCapital: "10000", FeeRate: "0.001", SlippageBPS: "0"}
	bars, e := p.History(context.Background(), marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: p.Now().Add(-22 * time.Minute), To: p.Now()})
	if e != nil {
		t.Fatal(e)
	}
	in, _, e := backtest.Freeze(cfg, v, bars)
	if e != nil {
		t.Fatal(e)
	}
	shuffled := append([]domain.Candle{bars[21]}, bars[:21]...)
	shuffled = append(shuffled, bars[0])
	again, _, e := backtest.Freeze(cfg, v, shuffled)
	if e != nil || backtest.Hash(in) != backtest.Hash(again) {
		t.Fatalf("dedup/order changes dataset: %v", e)
	}
	if _, _, e = backtest.Freeze(cfg, v, bars[1:]); e == nil {
		t.Fatal("missing lookback accepted")
	}
	cfg.AcknowledgeGaps = true
	if _, _, e = backtest.Freeze(cfg, v, bars[1:]); e == nil {
		t.Fatal("acknowledgement waived lookback")
	}
}

func TestRecordedCloseRevisionClockRetainsFirstAvailability(t *testing.T) {
	p, e := fixture.Open("../../testdata/market/m3.json")
	if e != nil {
		t.Fatal(e)
	}
	info, e := p.Instrument(context.Background(), domain.Spot, "BTC/USDT")
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open("../../examples/strategies/breakout.yaml")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	d, e := strategy.Parse(f, strategy.DefaultRegistry())
	if e != nil {
		t.Fatal(e)
	}
	v := strategy.Version{ID: strategy.ContentHash(d), Definition: d}
	cfg := backtest.Config{VersionID: v.ID, Instrument: info, From: p.Now().Add(-2 * time.Minute), To: p.Now(), Timeframes: []domain.Timeframe{"1m"}, InitialCapital: "10000", FeeRate: "0.001", SlippageBPS: "0", ClockMode: "recorded-close"}
	bars, e := p.History(context.Background(), marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: p.Now().Add(-22 * time.Minute), To: p.Now()})
	if e != nil {
		t.Fatal(e)
	}
	for i := range bars {
		bars[i].ObservedAt = bars[i].EndTime
	}
	duplicate := bars[0]
	duplicate.ObservedAt = duplicate.ObservedAt.Add(time.Second)
	in, _, e := backtest.Freeze(cfg, v, append(bars, duplicate))
	if e != nil {
		t.Fatal(e)
	}
	if !in.Candles[0].ObservedAt.Equal(bars[0].EndTime) {
		t.Fatal("refetch delayed first recorded availability")
	}
	corrected := bars[0]
	corrected.Close, _ = domain.ParseDecimal("9.6")
	corrected.ObservedAt = cfg.To
	corrected = marketdata.Stamp(corrected)
	in, _, e = backtest.Freeze(cfg, v, append(bars, corrected))
	if e != nil {
		t.Fatal(e)
	}
	if !backtest.Availability(cfg, in.Candles[len(in.Candles)-1]).Equal(cfg.To) {
		t.Fatal("later correction leaked to original close")
	}
	conflict := corrected
	conflict.Close, _ = domain.ParseDecimal("9.7")
	conflict = marketdata.Stamp(conflict)
	if _, _, e = backtest.Freeze(cfg, v, append(append(bars, corrected), conflict)); e == nil {
		t.Fatal("same-clock contradictory revisions accepted")
	}
	cfg.ClockMode = "close"
	if _, _, e = backtest.Freeze(cfg, v, append(bars, corrected)); e == nil {
		t.Fatal("known later correction silently backdated")
	}
}
