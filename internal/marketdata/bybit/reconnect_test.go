package bybit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/bybit"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestReconnectBackfillsLostClosedCandlesBeforeResuming(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(start.Add(2*time.Minute + 10*time.Second).UnixMilli())
	var sockets atomic.Int32
	var recovered atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/public/spot" {
			conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			var request any
			if conn.ReadJSON(&request) != nil {
				return
			}
			conn.WriteJSON(map[string]any{"op": "subscribe", "success": true})
			if sockets.Add(1) == 1 {
				clock.Store(start.Add(4*time.Minute + 10*time.Second).UnixMilli())
				return
			}
			clock.Store(start.Add(5 * time.Minute).UnixMilli())
			conn.WriteJSON(map[string]any{"topic": "kline.1.BTCUSDT", "ts": clock.Load(), "data": []any{map[string]any{"start": start.Add(4 * time.Minute).UnixMilli(), "end": clock.Load() - 1, "interval": "1", "open": "10", "high": "12", "low": "9", "close": "11", "volume": "1", "confirm": true}}})
			conn.ReadMessage()
			return
		}
		var result any
		if r.URL.Path == "/v5/market/instruments-info" {
			result = map[string]any{"category": "spot", "list": []any{map[string]any{"symbol": "BTCUSDT", "baseCoin": "BTC", "quoteCoin": "USDT", "status": "Trading", "priceFilter": map[string]string{"tickSize": "0.01"}, "lotSizeFilter": map[string]string{"basePrecision": "0.001", "minOrderAmt": "5"}}}}
		} else {
			from, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
			end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
			if from == start.Add(2*time.Minute).UnixMilli() {
				recovered.Store(true)
			}
			list := [][]string{}
			for n := 3; n >= 0; n-- {
				at := start.Add(time.Duration(n) * time.Minute).UnixMilli()
				if at >= from && at <= end {
					list = append(list, []string{strconv.FormatInt(at, 10), "10", "12", "9", "11", "1", "11"})
				}
			}
			result = map[string]any{"category": "spot", "symbol": "BTCUSDT", "list": list}
		}
		json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "time": clock.Load(), "result": result})
	}))
	defer server.Close()
	now := func() time.Time { return time.UnixMilli(clock.Load()).UTC() }
	provider, err := bybit.New(bybit.Options{RESTURL: server.URL, SpotWSURL: strings.Replace(server.URL, "http", "ws", 1) + "/v5/public/spot", Now: now, RequestInterval: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	info, err := provider.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	q := marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: start, To: start.Add(2 * time.Minute)}
	syncer, err := marketdata.NewSynchronizer(provider, store, marketdata.SyncOptions{Now: now, MaxRetries: 1, Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	emitted := 0
	recoveredEvents := 0
	observedOpens := []time.Time{}
	err = syncer.Run(ctx, q, func(event marketdata.Event) error {
		if event.Candle != nil {
			emitted++
			observedOpens = append(observedOpens, event.Candle.OpenTime)
			if event.Kind == "recovered_candle" {
				recoveredEvents++
				if event.AllowsEntries {
					t.Error("recovery event allowed new entries before source resubscription")
				}
			} else {
				if !event.AllowsEntries {
					t.Error("recovered source remained blocked")
				}
				cancel()
			}
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || sockets.Load() != 2 || !recovered.Load() || emitted != 3 || recoveredEvents != 2 {
		t.Fatalf("reconnect: sockets=%d recovered=%v emitted=%d recovered events=%d err=%v", sockets.Load(), recovered.Load(), emitted, recoveredEvents, err)
	}
	for n, at := range observedOpens {
		if !at.Equal(start.Add(time.Duration(n+2) * time.Minute)) {
			t.Fatal("recovered events were not delivered in order")
		}
	}

	q.To = start.Add(5 * time.Minute)
	bars, err := store.Candles(context.Background(), q)
	if err != nil || len(bars) != 5 {
		t.Fatalf("lost closed bars: %d %v", len(bars), err)
	}
}
