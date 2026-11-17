package postgres_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

// SQL installs faults only. Successful public operations prove replay never writes
// forward runtime, evaluations, dedup clocks, controls, risk, finance or outbox.
func TestM5ReplayAndReportsCannotWriteForwardState(t *testing.T) {
	ctx := context.Background()
	s, o, a, dsn := newSpotTest(t)
	signal := m4Signal(t, s, a.ID, strategy.ManualPaper, "long", 0)
	req, _ := m4Decide(t, o, signal)
	m4Observe(t, o, 1, "100", true)
	policy := paper.RiskPolicy{MaxPositions: 1, DailyLoss: "10", CooldownSeconds: 600}
	if e := s.SetRiskPolicy(ctx, a.ID, signal.StrategyID, policy); e != nil {
		t.Fatal(e)
	}
	if e := s.SetKillSwitch(ctx, true); e != nil {
		t.Fatal(e)
	}
	before := m4PublicSnapshot(t, s, a.ID)
	instances, e := s.StrategyInstances(ctx)
	if e != nil {
		t.Fatal(e)
	}
	instanceJSON, _ := json.Marshal(instances)

	// A separate fixture supplies replay inputs; the forward owner remains held.
	replay, cfg, bars := linearReplay(t)
	v, e := replay.StrategyVersion(ctx, cfg.VersionID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AddStrategyVersion(ctx, v.Definition, strategy.DefaultRegistry()); e != nil {
		t.Fatal(e)
	}
	conn, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close(ctx)
	if _, e = conn.Exec(ctx, `CREATE FUNCTION millionaire.m5_deny() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forward mutation during backtest'; END; $$`); e != nil {
		t.Fatal(e)
	}
	tables := []string{"strategy_definitions", "strategy_versions", "strategy_instances", "strategy_evaluations", "strategy_signals", "signal_decisions", "paper_controls", "paper_risk_policies", "paper_accounts", "paper_ledger_entries", "paper_orders", "paper_positions", "paper_fills", "paper_price_events", "paper_funding_events", "journal_outbox"}
	for _, table := range tables {
		if _, e = conn.Exec(ctx, `CREATE TRIGGER m5_deny BEFORE INSERT OR UPDATE OR DELETE ON millionaire.`+table+` FOR EACH STATEMENT EXECUTE FUNCTION millionaire.m5_deny()`); e != nil {
			t.Fatal(table, e)
		}
	}
	for _, market := range []domain.MarketType{domain.Linear, domain.Spot} {
		c := cfg
		selected := append([]domain.Candle(nil), bars...)
		c.Instrument.Instrument.Market = market
		c.Funding = backtest.FundingDataset{}
		if market == domain.Spot {
			c.Leverage = 1
		}
		for i := range selected {
			selected[i].Instrument = c.Instrument.Instrument
			selected[i] = marketdata.Stamp(selected[i])
		}
		r, err := s.RunBacktest(ctx, c, selected)
		if err != nil || r.Status != "completed" || r.Balance.String() != "9882.1" {
			t.Fatalf("isolated %s: %+v %v", market, r, err)
		}
		if _, err = s.BacktestTrades(ctx, r.ID, analytics.Filter{}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.BacktestSignals(ctx, r.ID, analytics.SignalFilter{}, analytics.SuccessRule{ThresholdPercent: "100", StopPercent: "99", WindowBars: 2, Basis: "subsequent-closed-ohlcv"}); err != nil {
			t.Fatal(err)
		}
		snapshot, err := s.BacktestSnapshot(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.CompareBacktests(ctx, r.ID, r.ID); err != nil {
			t.Fatal(err)
		}
		if err = backtest.WriteExport(io.Discard, "json", "report", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if m4PublicSnapshot(t, s, a.ID) != before {
		t.Fatal("forward financial/decision state changed")
	}
	got, e := s.StrategyInstances(ctx)
	if e != nil {
		t.Fatal(e)
	}
	gotJSON, _ := json.Marshal(got)
	if string(gotJSON) != string(instanceJSON) {
		t.Fatal("forward instance changed")
	}
	p, e := s.RiskPolicy(ctx, a.ID, signal.StrategyID)
	if e != nil || p != policy {
		t.Fatalf("risk %+v %v", p, e)
	}
	on, e := s.KillSwitch(ctx)
	if e != nil || !on {
		t.Fatal("kill switch changed", e)
	}
	// Drop fault guards before retrying a normal forward mutation. Replay has not
	// stolen the owner; the durable decision still returns its original filled order.
	for _, table := range tables {
		if _, e = conn.Exec(ctx, `DROP TRIGGER m5_deny ON millionaire.`+table); e != nil {
			t.Fatal(e)
		}
	}
	retry, e := o.DecideSignal(ctx, req, func() marketdata.InstrumentInfo { i := spotInfo(); i.Instrument = signal.Instrument; return i }(), true)
	if e != nil || retry.Order == nil || retry.Order.Status != "filled" {
		t.Fatalf("forward retry %+v %v", retry, e)
	}
}

func TestM5ReplayMatchesCompatibleNormalPaperEconomics(t *testing.T) {
	ctx := context.Background()
	s, cfg, bars := linearReplay(t)
	cfg.Funding = backtest.FundingDataset{}
	r, e := s.RunBacktest(ctx, cfg, bars)
	if e != nil {
		t.Fatal(e)
	}
	account, e := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "matched", Name: "Matched normal paper", Currency: "USDT", InitialBalance: "10000"})
	if e != nil {
		t.Fatal(e)
	}
	binding := strategy.Instance{ID: "matched", VersionID: cfg.VersionID, Account: account.ID, Instrument: cfg.Instrument.Instrument, Action: strategy.AutoPaper}
	if e = s.AddStrategyInstance(ctx, binding); e != nil {
		t.Fatal(e)
	}
	if e = s.SetStrategyEnabled(ctx, binding.ID, true); e != nil {
		t.Fatal(e)
	}
	if e = s.SetRiskPolicy(ctx, account.ID, "", cfg.Risk); e != nil {
		t.Fatal(e)
	}
	for _, raw := range bars[:21] {
		raw.ObservedAt = raw.EndTime
		if _, e = s.SaveCandles(ctx, []domain.Candle{raw}); e != nil {
			t.Fatal(e)
		}
	}
	candle := bars[20]
	candle.ObservedAt = candle.EndTime
	event := marketdata.Event{Kind: "candle", Candle: &candle, AvailableAt: candle.EndTime, AllowsEntries: true}
	evaluation, e := s.EvaluateStrategyEvent(ctx, binding.ID, event, strategy.DefaultRegistry())
	if e != nil || len(evaluation.Signals) != 1 {
		t.Fatalf("normal evaluation %+v %v", evaluation, e)
	}
	o, e := s.AcquirePaperOwner(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer o.Close()
	signal := evaluation.Signals[0]
	if signal.Stop.String() != r.Signals[0].Stop.String() {
		t.Fatal("strategy observations disagree")
	}
	if _, e = o.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: signal.ID, At: signal.ObservedAt, Leverage: 2, FeeRate: "0.001", SlippageBPS: "0"}, cfg.Instrument, true); e != nil {
		t.Fatal(e)
	}
	// A normal source's next valid price is strictly later than the decision. This
	// fixture supplies the same 9 entry and 8.9 stop, with no intervening price path.
	at := signal.ObservedAt.Add(time.Microsecond)
	for n, price := range []string{"9", "8.9"} {
		tick := at.Add(time.Duration(n) * time.Minute)
		obs := paper.Observation{ID: price, Instrument: cfg.Instrument.Instrument, Source: "fixture", Price: spotDecimal(price), ObservedAt: tick, AvailableAt: tick, Ready: true}
		gate := paper.ExecutionGate{Now: tick, StaleAfter: time.Hour, Enabled: true}
		if _, e = o.Observe(ctx, obs, gate); e != nil {
			t.Fatal(e)
		}
		if _, e = o.Observe(ctx, obs, gate); e != nil {
			t.Fatal(e)
		}
	}
	normal, e := s.PaperTrades(ctx, analytics.Filter{AccountID: account.ID}, at.Add(time.Minute), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := s.BacktestTrades(ctx, r.ID, analytics.Filter{})
	if e != nil {
		t.Fatal(e)
	}
	if normal.ClosedTrades != 1 || normal.GrossPnL.String() != "-100" || normal.NetPnL.String() != "-117.9" || normal.Fees.String() != "17.9" || normal.CashBalance.String() != "9882.1" {
		t.Fatalf("normal %+v", normal)
	}
	if !normal.NetPnL.Equal(replay.NetPnL) || !normal.Fees.Equal(replay.Fees) || !normal.CashBalance.Equal(*replay.CashBalance) {
		t.Fatalf("normal/replay disagreement %+v %+v", normal, replay)
	}
	duplicate, e := s.EvaluateStrategyEvent(ctx, binding.ID, event, strategy.DefaultRegistry())
	if e != nil || duplicate.Reason != "duplicate event" || len(duplicate.Signals) != 0 || backtest.Hash(duplicate.State) != backtest.Hash(evaluation.State) {
		t.Fatalf("runtime/dedup changed %+v %v", duplicate, e)
	}
}
