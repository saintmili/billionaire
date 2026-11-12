package domain_test

import (
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
)

func TestCandleRequiresConsistentPricesAndExclusiveBounds(t *testing.T) {
	d := func(s string) domain.Decimal {
		v, e := domain.ParseDecimal(s)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	c := domain.Candle{
		Instrument: domain.Instrument{Provider: "bybit", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"},
		Timeframe:  "1m", Source: "bybit-rest", Revision: "1", OpenTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EndTime: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC), Open: d("10.1"), High: d("12"), Low: d("9"), Close: d("11"), BaseVolume: d("0.1"), Closed: true,
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := c
	bad.High = d("10")
	if err := bad.Validate(); err == nil {
		t.Fatal("inconsistent OHLC accepted")
	}
	bad = c
	bad.EndTime = c.OpenTime
	if err := bad.Validate(); err == nil {
		t.Fatal("empty interval accepted")
	}
	bad = c
	bad.BaseVolume = d("-0.1")
	if err := bad.Validate(); err == nil {
		t.Fatal("negative volume accepted")
	}
}
