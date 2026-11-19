package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestForegroundSignalFillsOnlyOnNextObservationAndRestartDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "engine", Name: "engine", Currency: "USDT", InitialBalance: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	dec := func(v string) domain.Decimal {
		d, e := domain.ParseDecimal(v)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	i := domain.Instrument{Provider: "fixture", Market: "spot", Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	info := marketdata.InstrumentInfo{Instrument: i, PriceStep: dec("0.01"), QuantityStep: dec("0.001"), MinNotional: dec("1")}
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	bar := func(min int, price string) domain.Candle {
		return domain.Candle{Instrument: i, Timeframe: "1m", Source: "fixture", OpenTime: at.Add(time.Duration(min) * time.Minute), EndTime: at.Add(time.Duration(min+1) * time.Minute), ObservedAt: at.Add(time.Duration(min+1) * time.Minute), Open: dec(price), High: dec(price), Low: dec("90"), Close: dec(price), BaseVolume: dec("1"), Closed: true}
	}
	data := fixture.Dataset{Version: 1, Instruments: []marketdata.InstrumentInfo{info}, Candles: []domain.Candle{bar(-1, "100")}, Updates: []domain.Candle{bar(0, "100"), bar(1, "102")}}
	raw, _ := json.Marshal(data)
	path := filepath.Join(t.TempDir(), "feed.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	zero := "0"
	d := strategy.Definition{SchemaVersion: 1, ID: "engine-rule", Name: "Engine rule", Implementation: "rules", Evaluation: strategy.OnClose, Emission: strategy.OnTransition, Entry: &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: &zero}}}
	v, err := s.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddStrategyInstance(ctx, strategy.Instance{ID: "engine-instance", VersionID: v.ID, Account: a.ID, Instrument: i, Action: strategy.AutoPaper}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetStrategyEnabled(ctx, "engine-instance", true); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		p, e := fixture.Open(path)
		if e != nil {
			t.Fatal(e)
		}
		err = app.Run(ctx, s, []app.Subscription{{Provider: p, Now: p.Now, Query: marketdata.Query{Instrument: i, Timeframe: "1m", From: at.Add(-time.Minute), To: at}}}, app.Options{QueueCapacity: 1, StaleAfter: time.Minute, Quantity: "1"}, func(r app.Result) error {
			if attempt == 1 {
				fills, e := s.PaperFills(ctx, a.ID)
				if e != nil {
					return e
				}
				if len(fills) == 1 {
					return errors.New("injected consumer failure after fill commit before acknowledgment")
				}
			}
			if attempt == 0 && len(r.Signals) > 0 {
				return errors.New("injected consumer failure after durable intent")
			}
			return nil
		})
		if attempt < 2 {
			if err == nil {
				t.Fatal("injected worker failure ignored")
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			alerts, e := s.Notifications(ctx, 100)
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, alert := range alerts {
				if alert.Event.Kind == "engine_failure" {
					found = true
					if alert.Event.Reason != "foreground engine failed; inspect local command diagnostics" {
						t.Fatal("unsafe engine failure reason")
					}
				}
			}
			if !found {
				t.Fatal("foreground failure missing durable notification")
			}
		}
	}
	fills, err := s.PaperFills(ctx, a.ID)
	if err != nil || len(fills) != 1 || fills[0].Price.String() != "102" || fills[0].LatencyMillis != 60000 {
		ds, _ := s.SignalDecisions(ctx)
		ss, _ := s.StrategySignals(ctx, strategy.SignalFilter{})
		t.Fatalf("next observation fills: %+v %v decisions=%+v signals=%+v", fills, err, ds, ss)
	}

	ledger, e := s.PaperLedger(ctx, a.ID)
	if e != nil || len(ledger) != 3 {
		t.Fatalf("entry replay changed ledger count: %d %v", len(ledger), e)
	}
	orders, e := s.PaperOrders(ctx, a.ID)
	if e != nil || len(orders) != 1 {
		t.Fatalf("entry replay duplicated order: %d %v", len(orders), e)
	}
	signals, e := s.StrategySignals(ctx, strategy.SignalFilter{})
	if e != nil || len(signals) != 1 {
		t.Fatalf("evaluation replay duplicated signal: %d %v", len(signals), e)
	}
}

func TestForegroundSupervisesConfiguredFeedsAndReleasesOwnerOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	feeds := []app.Subscription{}
	for _, symbol := range []string{"BTC/USDT", "ETH/USDT"} {
		for _, frame := range []domain.Timeframe{"1m", "15m"} {
			p, err := fixture.Open("../../testdata/market/m2.json")
			if err != nil {
				t.Fatal(err)
			}
			info, err := p.Instrument(ctx, domain.Spot, symbol)
			if err != nil {
				t.Fatal(err)
			}
			end, _ := frame.Bounds(p.Now())
			from, _ := frame.Bounds(end.Add(-time.Nanosecond))
			feeds = append(feeds, app.Subscription{Provider: p, Now: p.Now, Query: marketdata.Query{Instrument: info.Instrument, Timeframe: frame, From: from, To: end}})
		}
	}
	seen := map[string]bool{}
	err = app.Run(ctx, s, feeds, app.Options{QueueCapacity: 1, StaleAfter: time.Minute}, func(r app.Result) error {
		seen[r.Event.Feed.Instrument.ID+string(r.Event.Feed.Timeframe)] = true
		if len(seen) == 4 {
			cancel()
		}
		return nil
	})
	if err != nil || len(seen) != 4 {
		t.Fatalf("configured feeds: %v %v", seen, err)
	}
	owner, err := s.AcquireEngineOwner(context.Background())
	if err != nil {
		t.Fatalf("shutdown did not release ownership: %v", err)
	}
	owner.Close()
}

// Metadata is an external public boundary and may consume enough time to make
// an observation stale after it has already been admitted by the synchronizer.
type agingProvider struct {
	marketdata.Provider
	now       func() time.Time
	advance   func()
	streaming atomic.Bool
}

func (p *agingProvider) Stream(ctx context.Context, q marketdata.Query, consume func(domain.Candle) error) error {
	return p.Provider.Stream(ctx, q, func(c domain.Candle) error { p.streaming.Store(true); return consume(c) })
}
func (p *agingProvider) Instrument(ctx context.Context, m domain.MarketType, symbol string) (marketdata.InstrumentInfo, error) {
	info, err := p.Provider.Instrument(ctx, m, symbol)
	if p.streaming.Load() {
		p.advance()
	}
	return info, err
}
func TestForegroundRevalidatesClockAfterWarmupAndMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	p, err := fixture.Open("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	var offset atomic.Int64
	clock := func() time.Time { return p.Now().Add(time.Duration(offset.Load())) }
	wrapped := &agingProvider{Provider: p, now: clock, advance: func() { offset.Store(int64(2 * time.Minute)) }}
	to, _ := domain.Timeframe("1m").Bounds(clock())
	seen := false
	err = app.Run(ctx, s, []app.Subscription{{Provider: wrapped, Now: clock, Query: marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: to.Add(-time.Minute), To: to}}}, app.Options{QueueCapacity: 1, StaleAfter: time.Minute}, func(r app.Result) error {
		if r.Event.Candle != nil {
			seen = true
			if r.Event.AllowsEntries {
				t.Fatal("event stayed eligible after metadata aged it")
			}
			o, e := s.RecordedPaperObservation(ctx, paper.Observation{ID: marketdata.EventID(*r.Event.Candle), Instrument: info.Instrument})
			if e != nil || o == nil || o.Ready || !o.AvailableAt.Equal(r.Event.AvailableAt) {
				t.Fatalf("stale observation changed availability or remained valid: %+v %v", o, e)
			}
			cancel()
		}
		return nil
	})
	if err != nil || !seen {
		t.Fatalf("stale event check: %v seen=%v", err, seen)
	}
}

func TestForegroundRestoresEvaluationBeforeIntentCommit(t *testing.T) {
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
	a, err := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "engine", Name: "engine", Currency: "USDT", InitialBalance: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	dec := func(v string) domain.Decimal {
		d, e := domain.ParseDecimal(v)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	i := domain.Instrument{Provider: "fixture", Market: "spot", Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	info := marketdata.InstrumentInfo{Instrument: i, PriceStep: dec("0.01"), QuantityStep: dec("0.001"), MinNotional: dec("1")}
	at := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	bar := func(min int, price string) domain.Candle {
		return domain.Candle{Instrument: i, Timeframe: "1m", Source: "fixture", OpenTime: at.Add(time.Duration(min) * time.Minute), EndTime: at.Add(time.Duration(min+1) * time.Minute), ObservedAt: at.Add(time.Duration(min+1) * time.Minute), Open: dec(price), High: dec(price), Low: dec("90"), Close: dec(price), BaseVolume: dec("1"), Closed: true}
	}
	data := fixture.Dataset{Version: 1, Instruments: []marketdata.InstrumentInfo{info}, Candles: []domain.Candle{bar(-1, "100")}, Updates: []domain.Candle{bar(0, "100"), bar(1, "102")}}
	raw, _ := json.Marshal(data)
	path := filepath.Join(t.TempDir(), "feed.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	zero := "0"
	d := strategy.Definition{SchemaVersion: 1, ID: "engine-rule", Name: "Engine rule", Implementation: "rules", Evaluation: strategy.OnClose, Emission: strategy.OnTransition, Entry: &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: &zero}}}
	v, err := s.AddStrategyVersion(ctx, d, strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddStrategyInstance(ctx, strategy.Instance{ID: "engine-instance", VersionID: v.ID, Account: a.ID, Instrument: i, Action: strategy.AutoPaper}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetStrategyEnabled(ctx, "engine-instance", true); err != nil {
		t.Fatal(err)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE FUNCTION billionaire.fail_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected decision failure'; END; $$; CREATE TRIGGER fail_decision BEFORE INSERT ON billionaire.signal_decisions FOR EACH ROW EXECUTE FUNCTION billionaire.fail_decision()`)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		p, err := fixture.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		err = app.Run(ctx, s, []app.Subscription{{Provider: p, Now: p.Now, Query: marketdata.Query{Instrument: i, Timeframe: "1m", From: at.Add(-time.Minute), To: at}}}, app.Options{QueueCapacity: 1, StaleAfter: time.Minute, Quantity: "1"}, nil)
		if attempt == 0 {
			if err == nil {
				t.Fatal("decision fault ignored")
			}
			signals, e := s.StrategySignals(ctx, strategy.SignalFilter{})
			if e != nil || len(signals) != 1 {
				t.Fatalf("evaluation was not durable: %v %v", signals, e)
			}
			orders, e := s.PaperOrders(ctx, a.ID)
			if e != nil || len(orders) != 0 {
				t.Fatalf("failed decision leaked intent: %v %v", orders, e)
			}
			if _, e = conn.Exec(ctx, "DROP TRIGGER fail_decision ON billionaire.signal_decisions"); e != nil {
				t.Fatal(e)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	fills, err := s.PaperFills(ctx, a.ID)
	if err != nil || len(fills) != 1 || fills[0].Price.String() != "102" {
		t.Fatalf("recovered intent failed next actual observation: %+v %v", fills, err)
	}
	signals, err := s.StrategySignals(ctx, strategy.SignalFilter{})
	if err != nil || len(signals) != 1 {
		t.Fatalf("duplicate evaluation: %v %v", signals, err)
	}
	alerts, err := s.Notifications(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, alert := range alerts {
		kinds[alert.Event.Kind]++
	}
	if kinds["signal"] != 1 || kinds["order_opened"] != 1 {
		t.Fatalf("financial/evaluation notification replay: %v", kinds)
	}
}

func TestForegroundRechecksClockAtFinancialTransactionAfterSlowFreshnessWrite(t *testing.T) {
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
	p, err := fixture.Open("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	dec := func(v string) domain.Decimal {
		d, e := domain.ParseDecimal(v)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	now := time.Now().UTC()
	start := now.Truncate(time.Minute)
	bar := domain.Candle{Instrument: info.Instrument, Timeframe: "1m", Source: "fixture", OpenTime: start.Add(-time.Minute), EndTime: start, ObservedAt: now, Open: dec("100"), High: dec("100"), Low: dec("100"), Close: dec("100"), BaseVolume: dec("1"), Closed: true}
	update := bar
	update.OpenTime = start
	update.EndTime = start.Add(time.Minute)
	update.Closed = false
	raw, _ := json.Marshal(fixture.Dataset{Version: 1, Instruments: []marketdata.InstrumentInfo{info}, Candles: []domain.Candle{bar}, Updates: []domain.Candle{update}})
	path := filepath.Join(t.TempDir(), "transaction-clock.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := fixture.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "transaction-clock", Name: "Clock", Currency: "USDT", InitialBalance: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.SubmitOrder(ctx, paper.OrderRequest{RequestID: "waiting-entry", AccountID: account.ID, Action: "open", Instrument: info.Instrument, Side: "long", Quantity: "1", Leverage: 1, At: now.Add(-time.Second), FeeRate: "0", SlippageBPS: "0"}, info, true)
	owner.Close()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	// Slow an unrelated durable write between the engine's initial readiness read
	// and acquisition of the financial transaction. This must age the actual fill.
	_, err = conn.Exec(ctx, `CREATE FUNCTION billionaire.slow_freshness() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1); RETURN NEW; END; $$; CREATE TRIGGER slow_freshness BEFORE INSERT ON billionaire.notification_freshness FOR EACH ROW EXECUTE FUNCTION billionaire.slow_freshness()`)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	err = app.Run(ctx, s, []app.Subscription{{Provider: provider, Now: time.Now, Query: marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: start.Add(-time.Minute), To: start}}}, app.Options{QueueCapacity: 1, StaleAfter: 500 * time.Millisecond}, func(result app.Result) error {
		if result.Event.Candle != nil {
			seen = true
			if result.Event.AllowsEntries {
				t.Fatal("post-transaction strategy readiness retained stale admission")
			}
			fills, e := s.PaperFills(ctx, account.ID)
			if e != nil || len(fills) != 0 {
				t.Fatalf("stale transaction filled admitted event: %v %v", fills, e)
			}
		}
		return nil
	})
	if err != nil || !seen {
		t.Fatalf("transaction clock check: %v seen=%v", err, seen)
	}
}
