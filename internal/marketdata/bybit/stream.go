package bybit

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
)

// Stream is one connection. The synchronization service owns bounded reconnects
// and backfill. Callbacks run synchronously: no candle queue can silently drop a
// closed update. Non-native requests yield 1m source updates for aggregation.
func (c *Client) Stream(ctx context.Context, q marketdata.Query, consume func(domain.Candle) error) error {
	if err := q.Validate(); err != nil {
		return err
	}
	if q.Instrument.Provider != "bybit" {
		return errors.New("stream instrument provider mismatch")
	}
	if !Native(q.Timeframe) {
		q.Timeframe = "1m"
	}
	endpoint := c.opts.SpotWSURL
	if q.Instrument.Market == domain.Linear {
		endpoint = c.opts.LinearWSURL
	}
	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 10 * time.Second}
	// Production connections use system TLS roots; test servers use loopback ws.
	conn, response, err := dialer.DialContext(ctx, endpoint, nil)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("public WebSocket connection failed")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetReadLimit(1 << 20)
	topic := "kline." + intervals[q.Timeframe] + "." + q.Instrument.ID
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if conn.WriteJSON(map[string]any{"op": "subscribe", "args": []string{topic}}) != nil {
		return errors.New("public subscription write failed")
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(c.opts.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if conn.WriteJSON(map[string]string{"op": "ping"}) != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	subscribed := false
	// Bybit may send the initial snapshot before the subscription acknowledgment.
	// Retain a bounded set until success; never publish unacknowledged data.
	var pending []domain.Candle
	acknowledgmentDeadline := time.Now().Add(10 * time.Second)
	for {
		if !subscribed && !time.Now().Before(acknowledgmentDeadline) {
			return errors.New("public subscription acknowledgment timed out")
		}
		deadline := time.Now().Add(c.opts.ReadTimeout)
		if !subscribed && acknowledgmentDeadline.Before(deadline) {
			deadline = acknowledgmentDeadline
		}
		conn.SetReadDeadline(deadline)
		var message struct {
			Op, Topic string
			Success   *bool
			TS        int64 `json:"ts"`
			Data      []struct {
				Start, End                               int64
				Interval, Open, High, Low, Close, Volume string
				Confirm                                  bool
			}
		}
		if err := conn.ReadJSON(&message); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("public WebSocket disconnected or invalid frame")
		}
		if message.Op == "subscribe" {
			if message.Success == nil || !*message.Success {
				return errors.New("public subscription rejected")
			}
			subscribed = true
			for _, candle := range pending {
				if err := consume(candle); err != nil {
					return err
				}
			}
			pending = nil
			continue
		}
		if message.Op == "ping" || message.Op == "pong" {
			continue
		}
		if message.Topic != topic || len(message.Data) == 0 {
			return errors.New("unexpected public candle message")
		}
		if err := c.checkClock(message.TS); err != nil {
			return err
		}
		for _, row := range message.Data {
			start := time.UnixMilli(row.Start).UTC()
			aligned, end := q.Timeframe.Bounds(start)
			if !aligned.Equal(start) || row.End != end.UnixMilli()-1 || row.Interval != intervals[q.Timeframe] || (row.Confirm && message.TS < end.UnixMilli()) {
				return errors.New("invalid streaming candle boundaries or closure")
			}
			candle, err := parseCandle(q, start, end, []string{row.Open, row.High, row.Low, row.Close, row.Volume}, row.Confirm, time.UnixMilli(message.TS).UTC())
			if err != nil {
				return err
			}
			if !subscribed {
				if len(pending) >= 64 {
					return errors.New("public subscription pre-ack candle limit exceeded")
				}
				pending = append(pending, candle)
			} else if err := consume(candle); err != nil {
				return err
			}
		}
	}
}
