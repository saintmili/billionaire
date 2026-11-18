package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
			if attempt == 0 && len(r.Signals) > 0 {
				return errors.New("injected consumer failure after durable intent")
			}
			return nil
		})
		if attempt == 0 {
			if err == nil {
				t.Fatal("injected worker failure ignored")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	fills, err := s.PaperFills(ctx, a.ID)
	if err != nil || len(fills) != 1 || fills[0].Price.String() != "102" || fills[0].LatencyMillis != 60000 {
		ds, _ := s.SignalDecisions(ctx)
		ss, _ := s.StrategySignals(ctx, strategy.SignalFilter{})
		t.Fatalf("next observation fills: %+v %v decisions=%+v signals=%+v", fills, err, ds, ss)
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
