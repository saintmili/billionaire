package postgres_test

import (
	"context"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
	"os"
	"testing"
	"time"
)

func linearReplay(t *testing.T) (*postgres.Store, backtest.Config, []domain.Candle) {
	t.Helper()
	ctx := context.Background()
	s, e := postgres.Open(ctx, testdb.New(t))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	if e = s.Migrate(ctx, postgres.Up); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open("../../../examples/strategies/breakout.yaml")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	d, e := strategy.Parse(f, strategy.DefaultRegistry())
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if e != nil {
		t.Fatal(e)
	}
	p, e := fixture.Open("../../../testdata/market/m3.json")
	if e != nil {
		t.Fatal(e)
	}
	info, e := p.Instrument(ctx, domain.Linear, "BTC/USDT")
	if e != nil {
		t.Fatal(e)
	}
	bars, e := p.History(ctx, marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: p.Now().Add(-22 * time.Minute), To: p.Now()})
	if e != nil {
		t.Fatal(e)
	}
	cfg := backtest.Config{VersionID: v.ID, Instrument: info, From: p.Now().Add(-2 * time.Minute), To: p.Now(), Timeframes: []domain.Timeframe{"1m"}, InitialCapital: "10000", FeeRate: "0.001", SlippageBPS: "0", Leverage: 2}
	rate := "0.01"
	cfg.Funding = backtest.FundingDataset{Expected: []time.Time{cfg.To}, Events: []paper.FundingEvent{{ID: "fixture-funding", Instrument: info.Instrument, EffectiveAt: cfg.To, AvailableAt: cfg.To, Rate: &rate, Price: spotDecimal("9"), Source: "fixture", Provenance: "explicit boundary fixture mark"}}}
	return s, cfg, bars
}
func TestBacktestLinearSettlesPreFundingIntervalProtection(t *testing.T) {
	s, cfg, bars := linearReplay(t)
	r, e := s.RunBacktest(context.Background(), cfg, bars)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Fills) != 2 || r.Fills[1].ExitReason != "stop_loss" || len(r.Funding) != 1 || len(r.Funding[0].Applications) != 0 {
		t.Fatalf("preceding stop must settle before boundary funding: fills=%+v funding=%+v", r.Fills, r.Funding)
	}
	if r.Balance.String() != "9882.1" {
		t.Fatalf("exact loss100 fees17.9: %s", r.Balance)
	}
}

func TestBacktestLinearPartialTargetThenFundingRetainsExposure(t *testing.T) {
	s, cfg, bars := linearReplay(t)
	cfg.Protection = paper.ProtectionConfig{TargetPrice: "12", TargetQuantity: "400", TrailActivation: "12", TrailDistance: "1"}
	last := bars[len(bars)-1]
	last.Low = spotDecimal("9")
	last.Close = spotDecimal("12")
	last.High = spotDecimal("13")
	bars[len(bars)-1] = marketdata.Stamp(last)
	cfg.Funding.Events[0].Price = spotDecimal("12")
	r, e := s.RunBacktest(context.Background(), cfg, bars)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Fills) != 2 || r.Fills[1].ExitReason != "take_profit" || r.Fills[1].Quantity.String() != "400" {
		t.Fatalf("partial target: %+v", r.Fills)
	}
	if len(r.Funding) != 1 || len(r.Funding[0].Applications) != 1 || r.Funding[0].Applications[0].Payment.String() != "-72" {
		t.Fatalf("remaining600 * mark12 * rate.01: %+v", r.Funding)
	}
	if r.Balance.String() != "8486.2" || r.Positions[0].ReservedMargin.String() != "2628" || r.Positions[0].RemainingQuantity.String() != "600" {
		t.Fatalf("cash and reserved collateral: cash%s position%+v", r.Balance, r.Positions)
	}
	if r.FundingCoverage.Status != "complete" {
		t.Fatal(r.FundingCoverage)
	}
}
