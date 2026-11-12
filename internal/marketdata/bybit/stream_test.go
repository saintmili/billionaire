package bybit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/bybit"
)

func TestPublicSocketSubscribesPingsAndDistinguishesClosure(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request struct {
			Op   string
			Args []string
		}
		if conn.ReadJSON(&request) != nil {
			return
		}
		if request.Op != "subscribe" || len(request.Args) != 1 || request.Args[0] != "kline.1.BTCUSDT" {
			t.Error("incorrect public subscription")
		}
		conn.WriteJSON(map[string]any{"op": "subscribe", "success": true})
		send := func(closed bool, at time.Time) {
			conn.WriteJSON(map[string]any{"topic": "kline.1.BTCUSDT", "ts": at.UnixMilli(), "data": []any{map[string]any{"start": start.UnixMilli(), "end": start.Add(time.Minute).UnixMilli() - 1, "interval": "1", "open": "10", "high": "12", "low": "9", "close": "11", "volume": "1", "confirm": closed}}})
		}
		send(false, start.Add(40*time.Second))
		var ping map[string]any
		if conn.ReadJSON(&ping) != nil {
			return
		}
		if ping["op"] != "ping" {
			t.Error("missing heartbeat")
		}
		conn.WriteJSON(map[string]any{"op": "ping", "success": true, "ret_msg": "pong"})
		send(true, start.Add(time.Minute))
		conn.ReadMessage()
	}))
	defer server.Close()
	client, err := bybit.New(bybit.Options{SpotWSURL: strings.Replace(server.URL, "http", "ws", 1) + "/v5/public/spot", Now: func() time.Time { return start.Add(time.Minute) }, Heartbeat: 10 * time.Millisecond, ReadTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	i := domain.Instrument{Provider: "bybit", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	q := marketdata.Query{Instrument: i, Timeframe: "1m", From: start, To: start.Add(time.Minute)}
	stop := errors.New("consumer complete")
	events := []domain.Candle{}
	err = client.Stream(context.Background(), q, func(c domain.Candle) error {
		events = append(events, c)
		if len(events) == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || len(events) != 2 || events[0].Closed || !events[1].Closed || !events[1].EndTime.Equal(start.Add(time.Minute)) {
		t.Fatalf("stream closure: %+v %v", events, err)
	}
}
