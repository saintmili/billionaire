package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestCandleStorageSeparatesMarketsAndRetainsCorrections(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d, _ := domain.ParseDecimal("10.000000000000000001")
	volume, _ := domain.ParseDecimal("0.1")
	for _, market := range []domain.MarketType{domain.Spot, domain.Linear} {
		for _, base := range []string{"BTC", "ETH"} {
			for _, frame := range []domain.Timeframe{"1m", "15m"} {
				i := domain.Instrument{Provider: "bybit", Market: market, Base: base, Quote: "USDT", ID: base + "USDT"}
				_, end := frame.Bounds(at)
				candle := marketdata.Stamp(domain.Candle{Instrument: i, Timeframe: frame, OpenTime: at, EndTime: end, Open: d, High: d, Low: d, Close: d, BaseVolume: volume, Closed: true, Source: "bybit", ObservedAt: end})
				for n := range 2 {
					changed, err := store.SaveCandles(ctx, []domain.Candle{candle})
					if err != nil || changed != 1-n {
						t.Fatalf("idempotent save: %d %v", changed, err)
					}
				}
				q := marketdata.Query{Instrument: i, Timeframe: frame, From: at, To: end}
				bars, err := store.Candles(ctx, q)
				if err != nil || len(bars) != 1 || bars[0].Close.String() != d.String() {
					t.Fatalf("stored candles: %v %v", bars, err)
				}
				candle.Close = volume.Add(d)
				candle.High = candle.Close
				candle.ObservedAt = end.Add(time.Second)
				candle = marketdata.Stamp(candle)
				if _, err := store.SaveCandles(ctx, []domain.Candle{candle}); err != nil {
					t.Fatal(err)
				}
				revisions, err := store.CandleRevisions(ctx, q, at)
				if _, err := store.SaveCandles(ctx, []domain.Candle{bars[0]}); err != nil {
					t.Fatal(err)
				}
				latest, readErr := store.Candles(ctx, q)
				if readErr != nil || len(latest) != 1 || !latest[0].Close.Equal(candle.Close) {
					t.Fatal("an older observation replaced a newer correction")
				}
				if err != nil || len(revisions) != 2 {
					t.Fatalf("correction history: %d %v", len(revisions), err)
				}
			}
		}
	}
}
