package bybit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/bybit"
)

func TestPublicHistoryPaginatesBothMarketsAndSymbols(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(time.Hour)
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-BAPI-API-KEY") != "" || r.Header.Get("Authorization") != "" {
			t.Error("authenticated market access")
		}
		category, symbol := r.URL.Query().Get("category"), r.URL.Query().Get("symbol")
		base := symbol[:len(symbol)-4]
		var result any
		switch r.URL.Path {
		case "/v5/market/instruments-info":
			result = map[string]any{"category": category, "list": []any{map[string]any{"symbol": symbol, "baseCoin": base, "quoteCoin": "USDT", "settleCoin": "USDT", "contractType": "LinearPerpetual", "status": "Trading", "priceFilter": map[string]string{"tickSize": "0.01"}, "lotSizeFilter": map[string]string{"basePrecision": "0.001", "qtyStep": "0.001", "minOrderQty": "0.001", "minNotionalValue": "5", "minOrderAmt": "5"}}}}
		case "/v5/market/kline":
			pages++
			end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
			step := time.Minute
			if r.URL.Query().Get("interval") == "15" {
				step = 15 * time.Minute
			}
			list := [][]string{}
			for n := 3; n >= 0; n-- {
				at := start.Add(time.Duration(n) * step)
				if at.UnixMilli() <= end && len(list) < 2 {
					list = append(list, []string{strconv.FormatInt(at.UnixMilli(), 10), "10.01", "12", "9", "11", "0.125", "1.3"})
				}
			}
			result = map[string]any{"category": category, "symbol": symbol, "list": list}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "time": now.UnixMilli(), "result": result})
	}))
	defer server.Close()
	client, err := bybit.New(bybit.Options{RESTURL: server.URL, Now: func() time.Time { return now }, PageSize: 2, RequestInterval: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	for _, market := range []domain.MarketType{domain.Spot, domain.Linear} {
		for _, symbol := range []string{"BTC/USDT", "ETH/USDT"} {
			info, err := client.Instrument(context.Background(), market, symbol)
			if err != nil {
				t.Fatal(err)
			}
			for _, frame := range []domain.Timeframe{"1m", "15m"} {
				step := time.Minute
				if frame == "15m" {
					step = 15 * time.Minute
				}
				bars, err := client.History(context.Background(), marketdata.Query{Instrument: info.Instrument, Timeframe: frame, From: start, To: start.Add(4 * step)})
				if err != nil || len(bars) != 4 {
					t.Fatalf("%s %s %s: %d %v", market, symbol, frame, len(bars), err)
				}
				if !bars[0].OpenTime.Equal(start) || bars[0].Open.String() != "10.01" || !bars[3].Closed {
					t.Fatal("history was not normalized")
				}
			}
		}
	}
	if pages != 16 {
		t.Fatalf("expected two pages per series, got %d", pages)
	}
}
