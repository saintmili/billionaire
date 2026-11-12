package bybit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/bybit"
)

func TestPublicErrorsAndRateLimitsAreBounded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		want     string
		attempts int
	}{
		{"geographic block", 403, `country unavailable`, "HTTP 403", 1},
		{"invalid JSON", 200, `{`, "invalid Bybit", 1},
		{"provider error", 200, `{"retCode":10001}`, "retCode=10001", 1},
		{"clock drift", 200, `{"retCode":0,"time":1}`, "clock drift", 1},
		{"rate limit", 429, `{}`, "HTTP 429", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			provider, err := bybit.New(bybit.Options{RESTURL: server.URL, Now: func() time.Time { return now }, RequestInterval: time.Nanosecond})
			if err != nil {
				t.Fatal(err)
			}
			_, err = provider.Instrument(context.Background(), domain.Spot, "BTC/USDT")
			if err == nil || !strings.Contains(err.Error(), tc.want) || int(attempts.Load()) != tc.attempts {
				t.Fatalf("unexpected public failure: %v attempts=%d", err, attempts.Load())
			}
		})
	}
}
func TestHistoryRejectsMalformedRowsAndDoesNotCloseFormingCandles(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(30 * time.Second)
	for _, row := range [][]string{{"1767225600000", "10", "12", "9", "11", "1", "11"}, {"1767225600000", "NaN"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var result any
			if strings.Contains(r.URL.Path, "instruments-info") {
				result = map[string]any{"category": "spot", "list": []any{map[string]any{"symbol": "BTCUSDT", "baseCoin": "BTC", "quoteCoin": "USDT", "status": "Trading", "priceFilter": map[string]string{"tickSize": "0.01"}, "lotSizeFilter": map[string]string{"basePrecision": "0.001", "minOrderAmt": "5"}}}}
			} else {
				result = map[string]any{"category": "spot", "symbol": "BTCUSDT", "list": [][]string{row}}
			}
			json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "time": now.UnixMilli(), "result": result})
		}))
		provider, err := bybit.New(bybit.Options{RESTURL: server.URL, Now: func() time.Time { return now }, RequestInterval: time.Nanosecond})
		if err != nil {
			t.Fatal(err)
		}
		info, err := provider.Instrument(context.Background(), domain.Spot, "BTC/USDT")
		if err != nil {
			t.Fatal(err)
		}
		bars, err := provider.History(context.Background(), marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: start, To: start.Add(time.Minute)})
		if len(row) == 7 {
			if err != nil || len(bars) != 0 {
				t.Fatalf("forming candle claimed closed: %v %v", bars, err)
			}
		} else if err == nil {
			t.Fatal("malformed candle row accepted")
		}
		server.Close()
	}
}
