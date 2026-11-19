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
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestForegroundFundingPrecedesPriceAndRecoversAfterCommittedConsumerFailure(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	dec := func(v string) domain.Decimal {
		d, e := domain.ParseDecimal(v)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	i := domain.Instrument{Provider: "fixture", Market: domain.Linear, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	info := marketdata.InstrumentInfo{Instrument: i, PriceStep: dec("0.01"), QuantityStep: dec("0.001"), MinNotional: dec("1")}
	a, err := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "funded-engine", Name: "Funded engine", Currency: "USDT", InitialBalance: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	order, err := owner.SubmitOrder(ctx, paper.OrderRequest{RequestID: "entry", AccountID: a.ID, Action: "open", Instrument: i, Side: "long", Quantity: "2", Leverage: 5, At: at.Add(-3 * time.Minute), FeeRate: "0", SlippageBPS: "0"}, info, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Observe(ctx, paper.Observation{ID: "entry-price", Instrument: i, Source: "fixture", Price: dec("100"), ObservedAt: at.Add(-2 * time.Minute), AvailableAt: at.Add(-2 * time.Minute), Ready: true}, paper.ExecutionGate{Now: at.Add(-2 * time.Minute), StaleAfter: time.Minute, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	owner.Close()
	bar := func(n int) domain.Candle {
		return domain.Candle{Instrument: i, Timeframe: "1m", Source: "fixture", OpenTime: at.Add(time.Duration(n) * time.Minute), EndTime: at.Add(time.Duration(n+1) * time.Minute), ObservedAt: at.Add(time.Duration(n+1) * time.Minute), Open: dec("100"), High: dec("100"), Low: dec("100"), Close: dec("100"), BaseVolume: dec("1"), Closed: true}
	}
	positive, negative := "0.01", "-0.01"
	events := []paper.FundingEvent{}
	for n := 1; n <= 3; n++ {
		e := paper.FundingEvent{ID: paper.Identity("funding", string(rune('0'+n))), Instrument: i, EffectiveAt: at.Add(time.Duration(n) * time.Minute), AvailableAt: at.Add(time.Duration(n) * time.Minute), Source: "fixture", Provenance: "explicit monitoring funding fixture", Price: dec("100"), IntervalMinutes: 1}
		switch n {
		case 1:
			e.Rate = &positive
		case 2:
			e.Rate = &negative
		case 3:
			e.MissingReason = "provider outage"
		}
		events = append(events, e)
	}
	data := fixture.Dataset{Version: 1, Instruments: []marketdata.InstrumentInfo{info}, Candles: []domain.Candle{bar(-1)}, Updates: []domain.Candle{bar(0), bar(1), bar(2)}, Funding: events}
	path := filepath.Join(t.TempDir(), "funding.json")
	raw, _ := json.Marshal(data)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		p, err := fixture.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		err = app.Run(ctx, s, []app.Subscription{{Provider: p, Now: p.Now, Query: marketdata.Query{Instrument: i, Timeframe: "1m", From: at.Add(-time.Minute), To: at}}}, app.Options{QueueCapacity: 1, StaleAfter: time.Minute, Quantity: "1"}, func(result app.Result) error {
			if len(result.Funding) > 0 && result.Funding[0].Event.ID == events[0].ID {
				position, e := s.PaperPosition(ctx, paper.Identity("position", order.ID))
				if e != nil || position.ReservedMargin.String() != "38" {
					t.Fatalf("funding not before price %+v %v", position, e)
				}
				if attempt == 0 {
					return errors.New("injected after funding commit")
				}
			}
			return nil
		})
		if attempt == 0 {
			if err == nil {
				t.Fatal("failure not injected")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	history, err := s.PaperFunding(ctx)
	if err != nil || len(history) != 3 {
		t.Fatalf("durable intervals %+v %v", history, err)
	}
	if len(history[0].Applications) != 1 || history[0].Applications[0].Payment.String() != "-2" || len(history[1].Applications) != 1 || history[1].Applications[0].Payment.String() != "2" || history[2].Warning == "" {
		t.Fatalf("signed funding and coverage %+v", history)
	}
	p, err := s.PaperPosition(ctx, paper.Identity("position", order.ID))
	if err != nil || p.ReservedMargin.String() != "40" || !p.FundingPnL.IsZero() {
		t.Fatalf("funding reconciliation %+v %v", p, err)
	}
	ledger, err := s.PaperLedger(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	sum := dec("0")
	for _, e := range ledger {
		sum = sum.Add(e.Amount)
	}
	account, err := s.PaperAccount(ctx, a.ID)
	if err != nil || sum.String() != "960" || !sum.Equal(account.Balance) {
		t.Fatalf("cash reconciliation %s %+v %v", sum, account, err)
	}
}
