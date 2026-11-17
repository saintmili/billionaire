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
