package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
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
	for _, optIn := range []bool{false, true} {
		_, e := owner.Observe(ctx, paper.Observation{Phase: "next_open", ID: "forged-replay", Instrument: info.Instrument, Source: "fixture", Price: spotDecimal("9"), ObservedAt: from, AvailableAt: from, Ready: true}, paper.ExecutionGate{ReplayNextOpen: optIn, Now: from, StaleAfter: time.Hour, Enabled: true})
		if e == nil {
			t.Fatal("forward owner accepted replay phase")
		}
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
	t.Run("frozen provenance survives provider-store correction", func(t *testing.T) {
		in, e := s.BacktestInputs(ctx, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		corrected := bars[0]
		corrected.Close = spotDecimal("9.6")
		corrected.ObservedAt = corrected.ObservedAt.Add(time.Hour)
		corrected = marketdata.Stamp(corrected)
		if _, e = s.SaveCandles(ctx, []domain.Candle{corrected}); e != nil {
			t.Fatal(e)
		}
		retained, e := s.BacktestInputs(ctx, r.ID)
		if e != nil || backtest.Hash(retained) != backtest.Hash(in) {
			t.Fatal("provider correction changed frozen inputs", e)
		}
		rerun, e := s.RunBacktest(ctx, in.Config, in.Candles)
		if e != nil || rerun.Balance.String() != r.Balance.String() {
			t.Fatal("frozen rerun changed", e)
		}
	})
	t.Run("end of range retains last signal pending", func(t *testing.T) {
		short := cfg
		short.To = from.Add(time.Minute)
		result, e := s.RunBacktest(ctx, short, bars)
		if e != nil || result.Status != "completed" || len(result.Fills) != 0 || len(result.Orders) != 1 || result.Orders[0].Status != "pending" {
			t.Fatalf("end pending: %+v %v", result, e)
		}
	})
	t.Run("rejection and invalid configuration are explicit", func(t *testing.T) {
		limited := cfg
		limited.Risk.SymbolExposure = "1"
		result, e := s.RunBacktest(ctx, limited, bars)
		if e != nil || len(result.Fills) != 0 || len(result.Orders) != 1 || result.Orders[0].Status != "rejected" || result.Orders[0].Reason == "" {
			t.Fatalf("risk rejection: %+v %v", result, e)
		}
		for _, change := range []func(*backtest.Config){func(c *backtest.Config) { c.InitialCapital = "-1" }, func(c *backtest.Config) { c.FeeRate = "2" }, func(c *backtest.Config) { c.SlippageBPS = "10000" }, func(c *backtest.Config) { c.Risk.PercentRisk = "101" }, func(c *backtest.Config) { c.Timeframes = []domain.Timeframe{"1m", "5m"} }, func(c *backtest.Config) { c.Instrument.Instrument.Market = domain.Linear }} {
			invalid := cfg
			change(&invalid)
			result, e := s.RunBacktest(ctx, invalid, bars)
			if e == nil || result.ID != "" {
				t.Fatal("invalid input executed")
			}
		}
		linearOnly := d
		linearOnly.Markets = []domain.MarketType{domain.Linear}
		unsupported, e := s.AddStrategyVersion(ctx, linearOnly, strategy.DefaultRegistry())
		if e != nil {
			t.Fatal(e)
		}
		incompatible := cfg
		incompatible.VersionID = unsupported.ID
		if result, e := s.RunBacktest(ctx, incompatible, bars); e == nil || result.ID != "" {
			t.Fatal("unsupported strategy market executed")
		}
		if result, e := s.RunBacktest(ctx, cfg, bars[1:]); e == nil || result.ID != "" {
			t.Fatal("missing warmup executed")
		}
	})
	t.Run("duplicate delivery and changed assumptions", func(t *testing.T) {
		duplicated := append(append([]domain.Candle{}, bars...), bars[20])
		result, e := s.RunBacktest(ctx, cfg, duplicated)
		if e != nil || len(result.Signals) != 1 || result.Manifest.DatasetHash != r.Manifest.DatasetHash {
			t.Fatal("duplicate changed replay", e)
		}
		changed := cfg
		changed.SlippageBPS = "100"
		slipped, e := s.RunBacktest(ctx, changed, bars)
		if e != nil {
			t.Fatal(e)
		}
		if len(slipped.Fills) == 0 || slipped.Fills[0].Price.String() != "9.09" || slipped.Manifest.ReproducibilityHash == r.Manifest.ReproducibilityHash {
			t.Fatal("changed slippage not visible")
		}
	})
	t.Run("failed provisioning remains identifiable", func(t *testing.T) {
		admin, e := pgx.Connect(ctx, dsn)
		if e != nil {
			t.Fatal(e)
		}
		defer admin.Close(ctx)
		role := fmt.Sprintf("backtest_restricted_%d", time.Now().UnixNano())
		quoted := pgx.Identifier{role}.Sanitize()
		if _, e = admin.Exec(ctx, "CREATE ROLE "+quoted+" LOGIN"); e != nil {
			t.Fatal(e)
		}
		defer func() { admin.Exec(ctx, "DROP OWNED BY "+quoted); admin.Exec(ctx, "DROP ROLE "+quoted) }()
		for _, sql := range []string{"GRANT USAGE ON SCHEMA millionaire TO " + quoted, "GRANT SELECT ON millionaire.strategy_versions TO " + quoted, "GRANT SELECT, INSERT, UPDATE ON millionaire.backtest_runs TO " + quoted} {
			if _, e = admin.Exec(ctx, sql); e != nil {
				t.Fatal(e)
			}
		}
		u, e := url.Parse(dsn)
		if e != nil {
			t.Fatal(e)
		}
		u.User = url.User(role)
		restricted, e := postgres.Open(ctx, u.String())
		if e != nil {
			t.Fatal(e)
		}
		defer restricted.Close()
		result, e := restricted.RunBacktest(ctx, cfg, bars)
		if e == nil || result.ID == "" || result.Status != "failed" {
			t.Fatalf("failed: %+v %v", result, e)
		}
		saved, e := s.BacktestReport(ctx, result.ID)
		if e != nil || saved.Status != "failed" || saved.Error == "" {
			t.Fatal("failed run lost", e)
		}
	})
	t.Run("cancelled provisioning remains identifiable", func(t *testing.T) {
		blocker, e := pgx.Connect(ctx, dsn)
		if e != nil {
			t.Fatal(e)
		}
		defer blocker.Close(ctx)
		if _, e = blocker.Exec(ctx, "SELECT pg_advisory_lock(7341928601)"); e != nil {
			t.Fatal(e)
		}
		defer blocker.Exec(ctx, "SELECT pg_advisory_unlock(7341928601)")
		cancelled, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		result, e := s.RunBacktest(cancelled, cfg, bars)
		if !errors.Is(e, context.DeadlineExceeded) || result.ID == "" || result.Status != "cancelled" {
			t.Fatalf("cancelled: %+v %v", result, e)
		}
		saved, e := s.BacktestReport(ctx, result.ID)
		if e != nil || saved.Status != "cancelled" || saved.Error == "" {
			t.Fatal("cancelled run lost", e)
		}
	})
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
