package bybit_test

import (
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata/bybit"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestPublicFundingPreservesScheduleRatesMarksAndMissingCoverage(t *testing.T) {
	at := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	now := at.Add(17 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-BAPI-API-KEY") != "" {
			t.Error("authenticated funding")
		}
		q := r.URL.Query()
		if q.Get("category") != "linear" || q.Get("symbol") != "BTCUSDT" {
			t.Error("instrument request mismatch")
		}
		var result any
		switch r.URL.Path {
		case "/v5/market/instruments-info":
			result = map[string]any{"category": "linear", "list": []any{map[string]any{"symbol": "BTCUSDT", "contractType": "LinearPerpetual", "settleCoin": "USDT", "status": "Trading", "fundingInterval": 480}}}
		case "/v5/market/tickers":
			result = map[string]any{"category": "linear", "list": []any{map[string]any{"symbol": "BTCUSDT", "nextFundingTime": strconv.FormatInt(at.Add(24*time.Hour).UnixMilli(), 10)}}}
		case "/v5/market/funding/history":
			end, _ := strconv.ParseInt(q.Get("endTime"), 10, 64)
			rows := []any{}
			if end >= at.UnixMilli() {
				rows = append(rows, map[string]string{"symbol": "BTCUSDT", "fundingRate": "-0.001", "fundingRateTimestamp": strconv.FormatInt(at.UnixMilli(), 10)})
			}
			result = map[string]any{"category": "linear", "list": rows}
		case "/v5/market/mark-price-kline":
			result = map[string]any{"category": "linear", "symbol": "BTCUSDT", "list": [][]string{{q.Get("start"), "100", "999", "1", "999"}}}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "time": now.UnixMilli(), "result": result})
	}))
	defer server.Close()
	c, err := bybit.New(bybit.Options{RESTURL: server.URL, Now: func() time.Time { return now }, RequestInterval: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	i := domain.Instrument{Provider: "bybit", Market: domain.Linear, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	events, err := c.Funding(context.Background(), i, at, now)
	if err != nil || len(events) != 3 {
		t.Fatalf("events %+v: %v", events, err)
	}
	if events[0].Rate == nil || *events[0].Rate != "-0.001" || events[0].Price.String() != "100" || events[0].IntervalMinutes != 480 || !events[0].AvailableAt.Equal(now) {
		t.Fatalf("public funding %+v", events[0])
	}
	if events[1].Rate != nil || events[1].MissingReason == "" || events[2].Rate != nil {
		t.Fatal("missing intervals silently valued as zero")
	}
}

func TestPublicFundingWaitsForNextScheduleWithoutPollingHistory(t *testing.T) {
	at := time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var result any
		switch r.URL.Path {
		case "/v5/market/instruments-info":
			result = map[string]any{"category": "linear", "list": []any{map[string]any{"symbol": "BTCUSDT", "contractType": "LinearPerpetual", "settleCoin": "USDT", "status": "Trading", "fundingInterval": 480}}}
		case "/v5/market/tickers":
			result = map[string]any{"category": "linear", "list": []any{map[string]any{"symbol": "BTCUSDT", "nextFundingTime": strconv.FormatInt(at.Add(7*time.Hour).UnixMilli(), 10)}}}
		default:
			t.Error("history requested before any due interval")
		}
		json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "time": at.UnixMilli(), "result": result})
	}))
	defer server.Close()
	c, _ := bybit.New(bybit.Options{RESTURL: server.URL, Now: func() time.Time { return at }, RequestInterval: time.Nanosecond})
	i := domain.Instrument{Provider: "bybit", Market: domain.Linear, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	for range 3 {
		events, err := c.Funding(context.Background(), i, at.Add(-time.Minute), at)
		if err != nil || len(events) != 0 {
			t.Fatalf("not due %+v %v", events, err)
		}
	}
	if calls != 2 {
		t.Fatalf("expected cached schedule until due, got %d public requests", calls)
	}
}

func TestPublicFundingOutageMarkLossAndMalformedRates(t *testing.T) {
	for _, failure := range []string{"history-outage", "mark-missing", "bad-rate", "wrong-instrument"} {
		t.Run(failure, func(t *testing.T) {
			at := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)
			now := at.Add(time.Minute)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var result any
				switch r.URL.Path {
				case "/v5/market/instruments-info":
					result = map[string]any{"category": "linear", "list": []any{map[string]any{"symbol": "BTCUSDT", "contractType": "LinearPerpetual", "settleCoin": "USDT", "status": "Trading", "fundingInterval": 480}}}
				case "/v5/market/tickers":
					result = map[string]any{"category": "linear", "list": []any{map[string]any{"symbol": "BTCUSDT", "nextFundingTime": strconv.FormatInt(at.Add(8*time.Hour).UnixMilli(), 10)}}}
				case "/v5/market/funding/history":
					if failure == "history-outage" {
						w.WriteHeader(503)
						return
					}
					rate, symbol := "0.001", "BTCUSDT"
					if failure == "bad-rate" {
						rate = "NaN"
					}
					if failure == "wrong-instrument" {
						symbol = "ETHUSDT"
					}
					result = map[string]any{"category": "linear", "list": []any{map[string]string{"symbol": symbol, "fundingRate": rate, "fundingRateTimestamp": strconv.FormatInt(at.UnixMilli(), 10)}}}
				case "/v5/market/mark-price-kline":
					result = map[string]any{"category": "linear", "symbol": "BTCUSDT", "list": [][]string{}}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "time": now.UnixMilli(), "result": result})
			}))
			defer server.Close()
			c, _ := bybit.New(bybit.Options{RESTURL: server.URL, Now: func() time.Time { return now }, RequestInterval: time.Nanosecond})
			events, err := c.Funding(context.Background(), domain.Instrument{Provider: "bybit", Market: domain.Linear, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}, at, now)
			if failure == "bad-rate" || failure == "wrong-instrument" {
				if err == nil {
					t.Fatal("invalid public funding accepted")
				}
				return
			}
			if err != nil || len(events) != 1 || events[0].Rate != nil || events[0].MissingReason == "" || !events[0].AvailableAt.Equal(now) {
				t.Fatalf("outage coverage %+v %v", events, err)
			}
		})
	}
}

func TestPublicFundingPaginatesAndOverlappingFetchesKeepSettlementIdentity(t *testing.T) {
	at := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	now := at.Add(201 * time.Minute)
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var result any
		switch r.URL.Path {
		case "/v5/market/instruments-info":
			result = map[string]any{"category": "linear", "list": []any{map[string]any{"symbol": "BTCUSDT", "contractType": "LinearPerpetual", "settleCoin": "USDT", "status": "Trading", "fundingInterval": 1}}}
		case "/v5/market/tickers":
			result = map[string]any{"category": "linear", "list": []any{map[string]any{"symbol": "BTCUSDT", "nextFundingTime": strconv.FormatInt(at.Add(202*time.Minute).UnixMilli(), 10)}}}
		case "/v5/market/funding/history":
			pages++
			end, _ := strconv.ParseInt(q.Get("endTime"), 10, 64)
			start, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
			rows := []any{}
			for n := 200; n >= 0; n-- {
				ms := at.Add(time.Duration(n) * time.Minute).UnixMilli()
				if ms <= end && ms >= start && len(rows) < 200 {
					rows = append(rows, map[string]string{"symbol": "BTCUSDT", "fundingRate": "0.001", "fundingRateTimestamp": strconv.FormatInt(ms, 10)})
				}
			}
			result = map[string]any{"category": "linear", "list": rows}
		case "/v5/market/mark-price-kline":
			result = map[string]any{"category": "linear", "symbol": "BTCUSDT", "list": [][]string{{q.Get("start"), "100", "100", "100", "100"}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "time": now.UnixMilli(), "result": result})
	}))
	defer server.Close()
	c, _ := bybit.New(bybit.Options{RESTURL: server.URL, Now: func() time.Time { return now }, RequestInterval: time.Nanosecond})
	i := domain.Instrument{Provider: "bybit", Market: domain.Linear, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	events, err := c.Funding(context.Background(), i, at, at.Add(200*time.Minute))
	if err != nil || len(events) != 201 || pages != 2 {
		t.Fatalf("paginated funding count=%d pages=%d %v", len(events), pages, err)
	}
	overlap, err := c.Funding(context.Background(), i, at.Add(199*time.Minute), at.Add(200*time.Minute))
	if err != nil || len(overlap) != 2 || overlap[0].ID != events[199].ID || overlap[1].ID != events[200].ID {
		t.Fatalf("overlap identity %+v %v", overlap, err)
	}
}
