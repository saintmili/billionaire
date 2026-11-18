package postgres_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestStrategyVersionsAndRepeatedEventsAreTransactional(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../examples/strategies/breakout.yaml")
	if err != nil {
		t.Fatal(err)
	}
	registry := strategy.DefaultRegistry()
	definition, err := strategy.Parse(strings.NewReader(string(raw)), registry)
	if err != nil {
		t.Fatal(err)
	}
	definition.Evaluation = strategy.OnUpdate
	definition.Emission = strategy.WhileTrue
	version, err := store.AddStrategyVersion(ctx, definition, registry)
	if err != nil {
		t.Fatal(err)
	}
	same, err := store.AddStrategyVersion(ctx, definition, registry)
	if err != nil || same.ID != version.ID || same.Number != 1 {
		t.Fatal("identical version was not idempotent")
	}
	edited := definition
	edited.Parameters = map[string]string{"lookback": "19", "stop_offset": "0.1", "reward_risk": "2"}
	second, err := store.AddStrategyVersion(ctx, edited, registry)
	if err != nil || second.ID == version.ID || second.Number != 2 {
		t.Fatal("edit did not create a version")
	}
	original, err := store.StrategyVersion(ctx, version.ID)
	if err != nil || original.Definition.Parameters["lookback"] != "20" {
		t.Fatal("existing version mutated")
	}
	provider, err := fixture.Open("../../../testdata/market/m3.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := provider.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	q := marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: provider.Now().Add(-22 * time.Minute), To: provider.Now()}
	bars, err := provider.History(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCandles(ctx, bars); err != nil {
		t.Fatal(err)
	}
	instance := strategy.Instance{ID: "breakout-demo", VersionID: version.ID, Instrument: info.Instrument, Account: strategyAccount(t, store), Action: strategy.AlertOnly}
	if err := store.AddStrategyInstance(ctx, instance); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStrategyEnabled(ctx, instance.ID, true); err != nil {
		t.Fatal(err)
	}
	first := bars[20]
	first.Closed = false
	first.ObservedAt = first.OpenTime.Add(10 * time.Second)
	first = marketdata.Stamp(first)
	event := marketdata.Event{Kind: "candle", Candle: &first}
	var group sync.WaitGroup
	results := make(chan strategy.Evaluation, 4)
	failures := make(chan error, 4)
	for range 4 {
		group.Go(func() {
			result, err := store.EvaluateStrategyEvent(ctx, instance.ID, event, registry)
			results <- result
			failures <- err
		})
	}
	group.Wait()
	close(results)
	close(failures)
	count := 0
	for result := range results {
		count += len(result.Signals)
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatalf("concurrent replay produced %d signals", count)
	}
	next := first
	next.ObservedAt = next.ObservedAt.Add(time.Second)
	next = marketdata.Stamp(next)
	result, err := store.EvaluateStrategyEvent(ctx, instance.ID, marketdata.Event{Kind: "candle", Candle: &next}, registry)
	if err != nil || len(result.Signals) != 1 {
		t.Fatalf("distinct while_true event suppressed: %+v %v", result, err)
	}
	signals, err := store.StrategySignals(ctx, strategy.SignalFilter{InstanceID: instance.ID})
	if err != nil || len(signals) != 2 {
		t.Fatalf("signals not durably distinct: %d %v", len(signals), err)
	}
}

func TestStrategyVersionProtectionAndEvaluationRollback(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../examples/strategies/breakout.yaml")
	if err != nil {
		t.Fatal(err)
	}
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(string(raw)), registry)
	if err != nil {
		t.Fatal(err)
	}
	version, err := store.AddStrategyVersion(ctx, d, registry)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "UPDATE billionaire.strategy_versions SET number=number+1 WHERE id=$1", version.ID); err == nil {
		t.Fatal("database allowed an immutable version update")
	}
	provider, err := fixture.Open("../../../testdata/market/m3.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := provider.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	bars, err := provider.History(ctx, marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: provider.Now().Add(-22 * time.Minute), To: provider.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCandles(ctx, bars); err != nil {
		t.Fatal(err)
	}
	i := strategy.Instance{ID: "rollback-demo", VersionID: version.ID, Instrument: info.Instrument, Account: strategyAccount(t, store), Action: strategy.AlertOnly}
	if err := store.AddStrategyInstance(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStrategyEnabled(ctx, i.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE FUNCTION billionaire.fail_signal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END; $$; CREATE TRIGGER fail_signal BEFORE INSERT ON billionaire.strategy_signals FOR EACH ROW EXECUTE FUNCTION billionaire.fail_signal()`); err != nil {
		t.Fatal(err)
	}
	event := marketdata.Event{Kind: "candle", Candle: &bars[20]}
	if _, err := store.EvaluateStrategyEvent(ctx, i.ID, event, registry); err == nil {
		t.Fatal("signal storage failure was ignored")
	}
	signals, err := store.StrategySignals(ctx, strategy.SignalFilter{InstanceID: i.ID})
	if err != nil || len(signals) != 0 {
		t.Fatal("failed transaction exposed signals")
	}
	if _, err := conn.Exec(ctx, "DROP TRIGGER fail_signal ON billionaire.strategy_signals; DROP FUNCTION billionaire.fail_signal()"); err != nil {
		t.Fatal(err)
	}
	result, err := store.EvaluateStrategyEvent(ctx, i.ID, event, registry)
	if err != nil || len(result.Signals) != 1 {
		t.Fatalf("failure consumed dedup/state before commit: %+v %v", result, err)
	}
	if err := store.SetStrategyEnabled(ctx, i.ID, false); err != nil {
		t.Fatal(err)
	}
	later := bars[21]
	later.Close, _ = domain.ParseDecimal("14")
	later.High = later.Close
	later = marketdata.Stamp(later)
	result, err = store.EvaluateStrategyEvent(ctx, i.ID, marketdata.Event{Kind: "candle", Candle: &later}, registry)
	if err != nil || len(result.Signals) != 0 || result.Reason != "instance disabled" {
		t.Fatal("disabled instance emitted")
	}
}
func TestStoredSnapshotsExcludeFutureHigherClosesAndCorrections(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../examples/strategies/breakout.yaml")
	if err != nil {
		t.Fatal(err)
	}
	registry := strategy.DefaultRegistry()
	d, err := strategy.Parse(strings.NewReader(string(raw)), registry)
	if err != nil {
		t.Fatal(err)
	}
	d.ID = "snapshot-guard"
	d.Implementation = "rules"
	d.Parameters = map[string]string{}
	d.Timeframes = []domain.Timeframe{"1m", "4h"}
	threshold := "100"
	d.Entry = &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close", Timeframe: "4h"}, Right: strategy.Value{Constant: &threshold}}
	version, err := store.AddStrategyVersion(ctx, d, registry)
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
	bars, err := provider.History(ctx, marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: provider.Now().Add(-22 * time.Minute), To: provider.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCandles(ctx, bars); err != nil {
		t.Fatal(err)
	}
	start := bars[0].OpenTime
	higher := bars[0]
	higher.Timeframe = "4h"
	higher.OpenTime = start.Add(-4 * time.Hour)
	higher.EndTime = start
	higher.ObservedAt = start
	twelve, _ := domain.ParseDecimal("12")
	higher.Open = twelve
	higher.Close = twelve
	higher.High = twelve
	higher.Low = twelve
	higher = marketdata.Stamp(higher)
	if _, err := store.SaveCandles(ctx, []domain.Candle{higher}); err != nil {
		t.Fatal(err)
	}
	future := higher
	future.OpenTime = start
	future.EndTime = start.Add(4 * time.Hour)
	future.ObservedAt = future.EndTime
	future.Close, _ = domain.ParseDecimal("999")
	future.High = future.Close
	future = marketdata.Stamp(future)
	correction := higher
	correction.Close = future.Close
	correction.High = future.Close
	correction.ObservedAt = start.Add(3 * time.Hour)
	correction = marketdata.Stamp(correction)
	if _, err := store.SaveCandles(ctx, []domain.Candle{future, correction}); err != nil {
		t.Fatal(err)
	}
	i := strategy.Instance{ID: "snapshot-demo", VersionID: version.ID, Instrument: info.Instrument, Account: strategyAccount(t, store), Action: strategy.AlertOnly}
	if err := store.AddStrategyInstance(ctx, i); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStrategyEnabled(ctx, i.ID, true); err != nil {
		t.Fatal(err)
	}
	result, err := store.EvaluateStrategyEvent(ctx, i.ID, marketdata.Event{Kind: "candle", Candle: &bars[20]}, registry)
	if err != nil || !result.Ready || len(result.Signals) != 0 {
		t.Fatalf("future higher data leaked: %+v %v", result, err)
	}
}

func TestRecoveryHistoryLimitKeepsPrecedingPrimaryWindow(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	registry := strategy.DefaultRegistry()
	d := strategy.Definition{SchemaVersion: 1, ID: "long-recovery", Name: "long recovery", Implementation: "breakout", Parameters: map[string]string{"lookback": "5000"}}
	version, err := store.AddStrategyVersion(ctx, d, registry)
	if err != nil {
		t.Fatal(err)
	}
	i := domain.Instrument{Provider: "fixture", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	parse := func(s string) domain.Decimal {
		v, e := domain.ParseDecimal(s)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]domain.Candle, 5002)
	for n := range bars {
		c := domain.Candle{Instrument: i, Timeframe: "1m", OpenTime: start.Add(time.Duration(n) * time.Minute), EndTime: start.Add(time.Duration(n+1) * time.Minute), Open: parse("9"), High: parse("10"), Low: parse("8"), Close: parse("9.5"), BaseVolume: parse("1"), Closed: true, Source: "fixture"}
		if n == 5000 {
			c.Close = parse("11")
			c.High = parse("12")
		}
		c.ObservedAt = c.EndTime
		bars[n] = marketdata.Stamp(c)
	}
	if _, err := store.SaveCandles(ctx, bars); err != nil {
		t.Fatal(err)
	}
	instance := strategy.Instance{ID: "recovery-limit", VersionID: version.ID, Instrument: i, Account: strategyAccount(t, store), Action: strategy.AlertOnly}
	if err := store.AddStrategyInstance(ctx, instance); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStrategyEnabled(ctx, instance.ID, true); err != nil {
		t.Fatal(err)
	}
	result, err := store.EvaluateStrategyEvent(ctx, instance.ID, marketdata.Event{Kind: "recovered_candle", Candle: &bars[5000], AvailableAt: bars[5001].ObservedAt}, registry)
	if err != nil || !result.Ready || len(result.Signals) != 1 {
		t.Fatalf("later recovery candle consumed preceding history limit: %+v %v", result, err)
	}
}

func strategyAccount(t *testing.T, store *postgres.Store) string {
	t.Helper()
	a, err := store.CreatePaperAccount(context.Background(), paper.CreateAccount{RequestID: "strategy-demo", Name: "Strategy demo", Currency: "USDT", InitialBalance: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}
