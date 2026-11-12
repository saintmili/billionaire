package marketdata_test

import (
	"context"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
)

func TestAggregationRequiresCompleteUTCIntervals(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := func(s string) domain.Decimal {
		v, e := domain.ParseDecimal(s)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	i := domain.Instrument{Provider: "bybit", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	bars := []domain.Candle{}
	for n := range 2 {
		bars = append(bars, domain.Candle{Instrument: i, Timeframe: "1m", OpenTime: start.Add(time.Duration(n) * time.Minute), EndTime: start.Add(time.Duration(n+1) * time.Minute), Open: d("10"), High: d("12"), Low: d("9"), Close: d("11"), BaseVolume: d("0.1"), Closed: true, Source: "bybit", Revision: "fixture"})
	}
	got, err := marketdata.Aggregate(bars, "2m")
	if err != nil || len(got) != 1 {
		t.Fatalf("aggregate: %v %v", got, err)
	}
	if got[0].BaseVolume.String() != "0.2" || got[0].Close.String() != "11" || !got[0].EndTime.Equal(start.Add(2*time.Minute)) {
		t.Fatalf("wrong aggregate: %+v", got[0])
	}
	if _, err := marketdata.Aggregate(bars[:1], "2m"); err == nil {
		t.Fatal("incomplete aggregate accepted")
	}
	monthStart, monthEnd := domain.Timeframe("1M").Bounds(time.Date(2024, 2, 15, 1, 0, 0, 0, time.UTC))
	if monthStart.Day() != 1 || monthStart.Month() != time.February || monthEnd.Month() != time.March || monthEnd.Sub(monthStart) != 29*24*time.Hour {
		t.Fatal("calendar month treated as fixed duration")
	}
}

func TestAggregationRejectsMissingWholeBuckets(t *testing.T) {
	p, err := fixture.Open("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := p.Instrument(context.Background(), domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bars, err := p.History(context.Background(), marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: start, To: start.Add(6 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	withGap := append(bars[:2:2], bars[4:]...)
	if _, err := marketdata.Aggregate(withGap, "2m"); err == nil {
		t.Fatal("entire missing bucket was silently omitted")
	}
}
