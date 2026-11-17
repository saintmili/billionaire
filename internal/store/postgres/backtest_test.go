package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestBacktestRunPersistsCausalSpotResultsAndIsolatesPaper(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open("../../../examples/strategies/breakout.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	d, err := strategy.Parse(file, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	p, err := fixture.Open("../../../testdata/market/m3.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	to := p.Now()
	from := to.Add(-2 * time.Minute)
	bars, err := p.History(ctx, marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: to.Add(-22 * time.Minute), To: to})
	if err != nil {
		t.Fatal(err)
	}
	cfg := backtest.Config{VersionID: v.ID, Instrument: info, From: from, To: to, Timeframes: []domain.Timeframe{"1m"}, Seed: 7, InitialCapital: "10000", FeeRate: "0.001", SlippageBPS: "0"}
	forward, err := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "forward", Name: "Forward", Currency: "USDT", InitialBalance: "1234"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	pending, err := owner.SubmitOrder(ctx, paper.OrderRequest{RequestID: "forward-entry", AccountID: forward.ID, Action: "open", Instrument: info.Instrument, Side: "long", Leverage: 1, Quantity: "1", At: from, FeeRate: "0.001", SlippageBPS: "0"}, info, true)
	if err != nil || pending.Status != "pending" {
		t.Fatal("forward pending", err)
	}
	if err = s.SetKillSwitch(ctx, true); err != nil {
		t.Fatal(err)
	}
	r, err := s.RunBacktest(ctx, cfg, bars)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "completed" || len(r.Signals) != 1 || len(r.Fills) != 2 {
		t.Fatalf("run: %+v", r)
	}
	entry := r.Fills[0]
	if entry.Price.String() != "9" || !entry.ExecutedAt.Equal(from.Add(time.Minute)) || r.Balance.String() != "9882.1" {
		t.Fatalf("next open9; loss100 + fees17.9: %+v balance %s", entry, r.Balance)
	}
	again, err := s.RunBacktest(ctx, cfg, bars)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == again.ID || r.Positions[0].AccountID == again.Positions[0].AccountID || r.Signals[0].InstanceID == again.Signals[0].InstanceID {
		t.Fatal("runs share identities")
	}
	if r.Manifest.ReproducibilityHash != again.Manifest.ReproducibilityHash || r.Balance.String() != again.Balance.String() || r.Fills[0].Price.String() != again.Fills[0].Price.String() {
		t.Fatal("economic rerun changed")
	}
	accounts, err := s.PaperAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].Balance.String() != "1234" {
		t.Fatal("forward account changed", err)
	}
	orders, err := s.PaperOrders(ctx, forward.ID)
	if err != nil || len(orders) != 1 || orders[0].Status != "pending" {
		t.Fatal("replay consumed forward order", err)
	}
	signals, err := s.StrategySignals(ctx, strategy.SignalFilter{})
	if err != nil || len(signals) != 0 {
		t.Fatal("forward signals leaked", err)
	}
	on, err := s.KillSwitch(ctx)
	if err != nil || !on {
		t.Fatal("forward kill switch changed", err)
	}
	owner.Close()
	s.Close()
	s, err = postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	saved, err := s.BacktestReport(ctx, r.ID)
	if err != nil || saved.Manifest.DatasetHash != r.Manifest.DatasetHash || len(saved.Fills) != 2 {
		t.Fatal("saved result unavailable", err)
	}
}
