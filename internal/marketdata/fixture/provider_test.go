package fixture_test

import (
	"context"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
)

func TestFixtureHistoryIsDeterministicAcrossMarkets(t *testing.T) {
	provider, err := fixture.Open("../../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, market := range []domain.MarketType{domain.Spot, domain.Linear} {
		for _, symbol := range []string{"BTC/USDT", "ETH/USDT"} {
			info, err := provider.Instrument(context.Background(), market, symbol)
			if err != nil {
				t.Fatal(err)
			}
			for _, frame := range []domain.Timeframe{"1m", "15m"} {
				bars, err := provider.History(context.Background(), marketdata.Query{Instrument: info.Instrument, Timeframe: frame, From: start, To: start.Add(time.Hour)})
				want := 60
				if frame == "15m" {
					want = 4
				}
				if err != nil || len(bars) != want {
					t.Fatalf("fixture %s %s %s: %d %v", market, symbol, frame, len(bars), err)
				}
			}
		}
	}
}
