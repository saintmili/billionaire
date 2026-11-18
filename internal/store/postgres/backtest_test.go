package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	"os"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/analytics"
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

	if _, e := owner.Observe(ctx, paper.Observation{ID: "forged-close-phase", Instrument: info.Instrument, Source: "fixture", Price: spotDecimal("9"), ObservedAt: from, AvailableAt: from, Ready: true}, paper.ExecutionGate{ReplayProtectionOnly: true, Now: from, StaleAfter: time.Hour, Enabled: true}); e == nil {
		t.Fatal("forward owner accepted replay protection-only gate")
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

	for range 2 {
		metrics, e := s.BacktestTrades(ctx, r.ID, analytics.Filter{})
		if e != nil {
			t.Fatal(e)
		}
		if metrics.Scope != "backtest" || metrics.RunID != r.ID || metrics.ClosedTrades != 1 || metrics.GrossPnL.String() != "-100" || metrics.NetPnL.String() != "-117.9" || metrics.Fees.String() != "17.9" || metrics.LedgerTotal.String() != "9882.1" || metrics.LedgerCashReconciled == nil || !*metrics.LedgerCashReconciled {
			t.Fatalf("persisted trades: %+v", metrics)
		}

		if len(metrics.Equity) < 4 || metrics.Return == nil || metrics.Return.String() != "-0.01179" || metrics.MaximumDrawdown.String() != "117.9" || metrics.MaximumDrawdownFraction.String() != "0.01179" || metrics.ValuationStatus != "complete" {
			t.Fatalf("persisted equity %+v", metrics)
		}
		hasExposure := false
		for _, point := range metrics.Equity {
			if point.Exposure != nil && point.Exposure.String() == "9000" {
				hasExposure = true
				if point.Leverage == nil {
					t.Fatal("missing observed leverage")
				}
			}
		}
		if !hasExposure {
			t.Fatal("no event-time entry exposure")
		}
		filtered, e := s.BacktestTrades(ctx, r.ID, analytics.Filter{StrategyVersion: v.ID})
		if e != nil || filtered.ClosedTrades != 1 || filtered.Trades[0].StrategyVersion != v.ID || len(filtered.Equity) != 0 {
			t.Fatalf("version filtered metrics: %+v %v", filtered, e)
		}
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

	t.Run("retained comparison is exact and independent of execution schemas", func(t *testing.T) {
		comparison, e := s.CompareBacktests(ctx, r.ID, again.ID)
		if e != nil || len(comparison.Differences) != 0 || comparison.Left.Metrics.Return.String() != "-0.01179" || comparison.Left.Metrics.MaximumDrawdown.String() != "117.9" || comparison.Left.Metrics.ClosedTrades != 1 || comparison.Left.PendingOrders == nil {
			t.Fatalf("comparison %+v %v", comparison, e)
		}
		changed := cfg
		changed.FeeRate = "0.002"
		different, e := s.RunBacktest(ctx, changed, bars)
		if e != nil {
			t.Fatal(e)
		}
		comparison, e = s.CompareBacktests(ctx, r.ID, different.ID)
		found := false
		for _, d := range comparison.Differences {
			if d.Field == "config.fee_rate" {
				found = true
			}
		}
		if e != nil || comparison.DirectlyComparable || !found {
			t.Fatalf("changed assumptions %+v %v", comparison, e)
		}
		selected, e := s.BacktestStrategies(ctx, postgres.StrategyReportFilter{RunIDs: []string{r.ID, again.ID}, AccountID: r.AccountID})
		if e != nil || len(selected) != 1 || selected[0].Report.ID != r.ID {
			t.Fatalf("dimensions %+v %v", selected, e)
		}
	})
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
		rerun, e := s.RunFrozenBacktest(ctx, in)
		if e != nil || rerun.Balance.String() != r.Balance.String() {
			t.Fatal("frozen rerun changed", e)
		}
	})

	t.Run("cache-only selection retains metadata and exact source payloads", func(t *testing.T) {
		if e := s.SaveInstrument(ctx, info); e != nil {
			t.Fatal(e)
		}
		if _, e := s.SaveCandles(ctx, bars); e != nil {
			t.Fatal(e)
		}
		metadata, e := s.InstrumentInfo(ctx, info.Instrument)
		if e != nil || backtest.Hash(metadata) != backtest.Hash(info) {
			t.Fatal("cached metadata", e)
		}
		selected, e := s.SelectBacktestDataset(ctx, cfg, nil, false)
		if e != nil || len(selected.Candles) != 22 {
			t.Fatal("cached selection", e)
		}
		missing := info.Instrument
		missing.ID = "UNKNOWN"
		if _, e = s.InstrumentInfo(ctx, missing); e == nil {
			t.Fatal("missing metadata accepted")
		}
	})

	t.Run("higher frame published at primary boundary without future leakage", func(t *testing.T) {
		multi := d
		multi.Timeframes = []domain.Timeframe{"1m", "5m"}
		multi.Implementation = "rules"
		multi.Parameters = map[string]string{}
		threshold := "10"
		multi.Entry = &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close", Timeframe: "5m"}, Right: strategy.Value{Constant: &threshold}}
		mv, e := s.AddStrategyVersion(ctx, multi, strategy.DefaultRegistry())
		if e != nil {
			t.Fatal(e)
		}
		mc := cfg
		mc.VersionID = mv.ID
		mc.Timeframes = []domain.Timeframe{"5m", "1m"}
		mc.From = from.Add(-2 * time.Minute)
		higher, e := p.History(ctx, marketdata.Query{Instrument: info.Instrument, Timeframe: "5m", From: from.Add(-10 * time.Minute), To: from})
		if e != nil {
			t.Fatal(e)
		}
		// Latest higher candle closes at 00:20 with a deliberately strong close.
		higher[len(higher)-1].Close = spotDecimal("11")
		higher[len(higher)-1].High = spotDecimal("12")
		higher[len(higher)-1] = marketdata.Stamp(higher[len(higher)-1])
		selected := append(append([]domain.Candle{}, bars...), higher...)
		result, e := s.RunBacktest(ctx, mc, selected)
		if e != nil {
			t.Fatal(e)
		}
		if len(result.Signals) != 1 || !result.Signals[0].ObservedAt.Equal(from) {
			t.Fatalf("higher close must be visible at 00:20, not earlier: %+v", result.Signals)
		}
		// Reordering frames, events and duplicates cannot change the selected data.
		for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
			selected[i], selected[j] = selected[j], selected[i]
		}
		selected = append(selected, selected[0])
		mc.Timeframes = []domain.Timeframe{"1m", "5m"}
		again, e := s.RunBacktest(ctx, mc, selected)
		if e != nil || again.Manifest.DatasetHash != result.Manifest.DatasetHash || again.Balance.String() != result.Balance.String() {
			t.Fatal("unordered input changes replay", e)
		}
	})

	t.Run("recorded higher correction remains absent before availability", func(t *testing.T) {
		multi := d
		multi.Implementation = "rules"
		multi.Parameters = map[string]string{}
		multi.Timeframes = []domain.Timeframe{"1m", "5m"}
		threshold := "10"
		multi.Entry = &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close", Timeframe: "5m"}, Right: strategy.Value{Constant: &threshold}}
		mv, e := s.AddStrategyVersion(ctx, multi, strategy.DefaultRegistry())
		if e != nil {
			t.Fatal(e)
		}
		mc := cfg
		mc.VersionID = mv.ID
		mc.Timeframes = []domain.Timeframe{"1m", "5m"}
		mc.From = from.Add(-2 * time.Minute)
		mc.ClockMode = "recorded-close"
		higher, e := p.History(ctx, marketdata.Query{Instrument: info.Instrument, Timeframe: "5m", From: from.Add(-10 * time.Minute), To: from})
		if e != nil {
			t.Fatal(e)
		}
		selected := append(append([]domain.Candle{}, bars...), higher...)
		for i := range selected {
			selected[i].ObservedAt = selected[i].EndTime
		}
		corrected := higher[len(higher)-1]
		corrected.Close = spotDecimal("11")
		corrected.High = spotDecimal("12")
		corrected.ObservedAt = from.Add(time.Minute)
		corrected = marketdata.Stamp(corrected)
		selected = append(selected, corrected)
		result, e := s.RunBacktest(ctx, mc, selected)
		if e != nil {
			t.Fatal(e)
		}
		if len(result.Signals) != 1 || !result.Signals[0].ObservedAt.Equal(from.Add(time.Minute)) {
			t.Fatalf("correction leaked before its recorded availability: %+v", result.Signals)
		}
	})

	t.Run("late close cannot fill an entry at a later close or outside range", func(t *testing.T) {
		recorded := append([]domain.Candle{}, bars...)
		for i := range recorded {
			recorded[i].ObservedAt = recorded[i].EndTime
		}
		recorded[20].ObservedAt = from.Add(90 * time.Second)
		recorded[21].Close = spotDecimal("9.8")
		recorded[21] = marketdata.Stamp(recorded[21])
		mc := cfg
		mc.ClockMode = "recorded-close"
		result, e := s.RunBacktest(ctx, mc, recorded)
		if e != nil {
			t.Fatal(e)
		}
		if len(result.Fills) != 0 || len(result.Orders) != 1 || result.Orders[0].Status != "pending" {
			t.Fatalf("late close consumed intent without next open: %+v", result.Orders)
		}
		recorded[20].ObservedAt = cfg.To.Add(time.Minute)
		result, e = s.RunBacktest(ctx, mc, recorded)
		if e != nil {
			t.Fatal(e)
		}
		if len(result.Signals) != 0 || len(result.Fills) != 0 {
			t.Fatal("post-range publication traded")
		}
	})
	t.Run("frozen version remains runnable without upstream version", func(t *testing.T) {
		frozen, e := s.BacktestInputs(ctx, r.ID)
		if e != nil {
			t.Fatal(e)
		}
		frozen.Version.Definition.Name = "Retained only in this frozen dataset"
		frozen.Version.ID = strategy.ContentHash(frozen.Version.Definition)
		frozen.Config.VersionID = frozen.Version.ID
		// This immutable version has never been registered in the upstream store.
		result, e := s.RunFrozenBacktest(ctx, frozen)
		if e != nil || result.Balance.String() != r.Balance.String() {
			t.Fatal("frozen version consulted upstream", e)
		}
	})
	t.Run("acknowledged gap has no invented open or ready strategy", func(t *testing.T) {
		gap := append(append([]domain.Candle{}, bars[:20]...), bars[21:]...)
		if _, e := s.RunBacktest(ctx, cfg, gap); e == nil {
			t.Fatal("unacknowledged gap accepted")
		}
		acknowledged := cfg
		acknowledged.AcknowledgeGaps = true
		result, e := s.RunBacktest(ctx, acknowledged, gap)
		if e != nil {
			t.Fatal(e)
		}
		if len(result.Fills) != 0 || len(result.Signals) != 0 || len(result.Warnings) < 4 {
			t.Fatalf("gap fabricated execution: %+v", result)
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

	t.Run("OHLCV cannot reproduce update mode", func(t *testing.T) {
		update := d
		update.Evaluation = strategy.OnUpdate
		uv, e := s.AddStrategyVersion(ctx, update, strategy.DefaultRegistry())
		if e != nil {
			t.Fatal(e)
		}
		unsupported := cfg
		unsupported.VersionID = uv.ID
		if result, e := s.RunBacktest(ctx, unsupported, bars); e == nil || result.ID != "" {
			t.Fatal("OHLCV update-mode replay accepted")
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
		for _, sql := range []string{"GRANT USAGE ON SCHEMA billionaire TO " + quoted, "GRANT SELECT ON billionaire.strategy_versions TO " + quoted, "GRANT SELECT, INSERT, UPDATE ON billionaire.backtest_runs TO " + quoted} {
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
		comparison, e := s.CompareBacktests(ctx, r.ID, result.ID)
		if e != nil || comparison.Right.Metrics != nil || comparison.Right.RejectedDecisions != nil || comparison.Right.EvidenceStatus != "run_failed" || comparison.DirectlyComparable {
			t.Fatalf("failed performance invented %+v %v", comparison, e)
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

// Seed a large decision history at the database boundary rather than replaying
// thousands of candles. All observations below use the public run/report APIs.
func TestBacktestRetainsDecisionsBeyondInteractiveLimit(t *testing.T) {
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
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	_, err = admin.Exec(ctx, `
 CREATE FUNCTION public.seed_large_decision_fixture() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
   IF pg_trigger_depth() > 1 THEN RETURN NEW; END IF;
   EXECUTE format('INSERT INTO %I.strategy_evaluations(instance_id,event_id,observed_at,event,result)
     SELECT e.instance_id,''fixture-''||n,e.observed_at,e.event,e.result FROM %I.strategy_evaluations e
     JOIN %I.strategy_signals s ON s.instance_id=e.instance_id AND s.event_id=e.event_id
     CROSS JOIN generate_series(1,10000) n WHERE s.id=$1', TG_TABLE_SCHEMA,TG_TABLE_SCHEMA,TG_TABLE_SCHEMA) USING NEW.signal_id;
   EXECUTE format('INSERT INTO %I.strategy_signals(id,instance_id,event_id,kind,direction,observed_at,payload)
     SELECT ''zz-fixture-''||lpad(n::text,5,''0''),s.instance_id,''fixture-''||n,s.kind,s.direction,s.observed_at,
       jsonb_set(s.payload,''{id}'',to_jsonb(''zz-fixture-''||lpad(n::text,5,''0'')))
     FROM %I.strategy_signals s CROSS JOIN generate_series(1,10000) n WHERE s.id=$1',TG_TABLE_SCHEMA,TG_TABLE_SCHEMA) USING NEW.signal_id;
   EXECUTE format('INSERT INTO %I.signal_decisions(signal_id,status,payload)
     SELECT ''zz-fixture-''||lpad(n::text,5,''0''),CASE WHEN n=10000 THEN ''rejected'' ELSE ''observed'' END,
       jsonb_build_object(''signal_id'',''zz-fixture-''||lpad(n::text,5,''0''),
       ''status'',CASE WHEN n=10000 THEN ''rejected'' ELSE ''observed'' END,''reason'',''large-history-fixture'')
     FROM generate_series(1,10000) n',TG_TABLE_SCHEMA);
   RETURN NEW;
 END $$;
 CREATE FUNCTION public.install_large_decision_fixture() RETURNS event_trigger LANGUAGE plpgsql AS $$
 DECLARE command record;
 BEGIN
   FOR command IN SELECT * FROM pg_event_trigger_ddl_commands() LOOP
     IF command.object_type='table' AND command.schema_name LIKE 'bt_%'
       AND command.object_identity=format('%I.signal_decisions',command.schema_name)
       AND NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=command.objid AND tgname='large_decision_fixture') THEN
       EXECUTE format('CREATE TRIGGER large_decision_fixture AFTER INSERT ON %I.signal_decisions
         FOR EACH ROW EXECUTE FUNCTION public.seed_large_decision_fixture()',command.schema_name);
     END IF;
   END LOOP;
 END $$;
 CREATE EVENT TRIGGER install_large_decision_fixture ON ddl_command_end WHEN TAG IN ('CREATE TABLE')
 EXECUTE FUNCTION public.install_large_decision_fixture();`)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("../../../examples/strategies/breakout.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := strategy.Parse(f, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	provider, err := fixture.Open("../../../testdata/market/m3.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := provider.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	to := provider.Now()
	bars, err := provider.History(ctx, marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: to.Add(-22 * time.Minute), To: to})
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.RunBacktest(ctx, backtest.Config{VersionID: v.ID, Instrument: info, From: to.Add(-2 * time.Minute), To: to, Timeframes: []domain.Timeframe{"1m"}, Seed: 7, InitialCapital: "10000", FeeRate: "0.001", SlippageBPS: "0"}, bars)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || len(report.Decisions) != 10001 {
		t.Fatalf("completed run must retain all 10001 decisions: status=%s decisions=%d", report.Status, len(report.Decisions))
	}
	saved, err := s.BacktestReport(ctx, report.ID)
	if err != nil || len(saved.Decisions) != 10001 {
		t.Fatalf("saved decisions=%d err=%v", len(saved.Decisions), err)
	}
	snapshot, err := s.BacktestSnapshot(ctx, report.ID)
	if err != nil || snapshot.EvidenceStatus != "verified" || snapshot.RejectedDecisions == nil || *snapshot.RejectedDecisions != 1 {
		t.Fatalf("late rejection evidence: %+v err=%v", snapshot.RejectedDecisions, err)
	}
	for _, format := range []string{"json", "csv"} {
		var out bytes.Buffer
		if err := backtest.WriteExport(&out, format, "report", snapshot); err != nil {
			t.Fatal(err)
		}
		// Both export encodings carry the full retained report and rejection count.
		if !bytes.Contains(out.Bytes(), []byte("zz-fixture-10000")) {
			t.Fatalf("%s export lost late decision", format)
		}
		if format == "json" {
			var exported backtest.Export
			if err := json.Unmarshal(out.Bytes(), &exported); err != nil {
				t.Fatal(err)
			}
			if len(exported.Context.Report.Decisions) != 10001 || exported.Context.RejectedDecisions == nil || *exported.Context.RejectedDecisions != 1 {
				t.Fatal("export lost decision evidence")
			}
		}
	}
}
