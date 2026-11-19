package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/control"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExplicitLiveCLIRefreshesOwnerHealthAcrossSilenceAndRestart(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	t.Setenv("BILLIONAIRE_DATABASE_URL", dsn)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	var quiet atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-BAPI-API-KEY") != "" {
			t.Error("authenticated market access")
		}
		if r.URL.Path == "/v5/public/spot" {
			conn, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if e != nil {
				return
			}
			defer conn.Close()
			var request any
			if conn.ReadJSON(&request) != nil {
				return
			}
			conn.WriteJSON(map[string]any{"op": "subscribe", "success": true})
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					if _, _, e := conn.ReadMessage(); e != nil {
						return
					}
				}
			}()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			var lastClosed time.Time
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if quiet.Load() {
						continue
					}
					now := time.Now().UTC()
					start := now.Truncate(time.Minute)
					// Supply the real close when a minute boundary advances during startup;
					// partial observations cannot establish completion of that missing close.
					if !lastClosed.Equal(start) {
						if conn.WriteJSON(map[string]any{"topic": "kline.1.BTCUSDT", "ts": now.UnixMilli(), "data": []any{map[string]any{"start": start.Add(-time.Minute).UnixMilli(), "end": start.UnixMilli() - 1, "interval": "1", "open": "100", "high": "100", "low": "100", "close": "100", "volume": "1", "confirm": true}}}) != nil {
							return
						}
						lastClosed = start
					}
					if conn.WriteJSON(map[string]any{"topic": "kline.1.BTCUSDT", "ts": now.UnixMilli(), "data": []any{map[string]any{"start": start.UnixMilli(), "end": start.Add(time.Minute).UnixMilli() - 1, "interval": "1", "open": "100", "high": "100", "low": "100", "close": "100", "volume": "1", "confirm": false}}}) != nil {
						return
					}
				}
			}
		}
		result := map[string]any{}
		switch r.URL.Path {
		case "/v5/market/instruments-info":
			result = map[string]any{"category": "spot", "list": []any{map[string]any{"symbol": "BTCUSDT", "baseCoin": "BTC", "quoteCoin": "USDT", "status": "Trading", "priceFilter": map[string]string{"tickSize": "0.01"}, "lotSizeFilter": map[string]string{"basePrecision": "0.001", "minOrderAmt": "1"}}}}
		case "/v5/market/kline":
			from, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
			end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
			rows := [][]string{}
			for at := from; at <= end; at += 60000 {
				rows = append(rows, []string{fmt.Sprint(at), "100", "100", "100", "100", "1", "100"})
			}
			result = map[string]any{"category": "spot", "symbol": "BTCUSDT", "list": rows}
		default:
			t.Errorf("unexpected public path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "time": time.Now().UnixMilli(), "result": result})
	}))
	defer server.Close()
	t.Setenv("BILLIONAIRE_REST_URL", server.URL)
	t.Setenv("BILLIONAIRE_SPOT_WS_URL", strings.Replace(server.URL, "http", "ws", 1)+"/v5/public/spot")
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	socket := filepath.Join(dir, "live.sock")
	config := filepath.Join(dir, "config.yaml")
	cfg := strings.Replace(configs.Example, "stale_after: 60s", "stale_after: 1s", 1)
	start := strings.Index(cfg, "  instruments:")
	end := strings.Index(cfg, "paper:")
	cfg = cfg[:start] + "  instruments:\n    - market: spot\n      symbol: BTC/USDT\n      timeframes: [1m]\n" + cfg[end:] + "\nengine:\n  lookback: 1\n  queue_capacity: 1\n"
	if err = os.WriteFile(config, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	sessions := map[string]bool{}
	for attempt := 0; attempt < 2; attempt++ {
		quiet.Store(false)
		running, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- cli.Execute(running, []string{"start", "--live", "--config", config, "--control-socket", socket, "--output", "json"}, io.Discard, io.Discard)
		}()
		waitHealth := func(want string) app.HealthSnapshot {
			t.Helper()
			deadline := time.After(10 * time.Second)
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case e := <-done:
					cancel()
					t.Fatalf("live engine stopped: %v", e)
				case <-deadline:
					cancel()
					t.Fatalf("health never became %s", want)
				case <-ticker.C:
					snapshot, e := control.ReadHealth(ctx, socket)
					if e == nil && len(snapshot.Feeds) == 1 && snapshot.Feeds[0].Status == want {
						return *snapshot
					}
				}
			}
		}
		ready := waitHealth("ready")
		if !ready.EntriesReady || ready.Ownership != "held" || ready.Counts == nil || sessions[ready.Session] {
			t.Fatalf("live readiness %+v", ready)
		}
		sessions[ready.Session] = true
		quiet.Store(true)
		stale := waitHealth("stale")
		if stale.EntriesReady || stale.Feeds[0].LastProcessed.IsZero() {
			t.Fatalf("silent feed healthy: %+v", stale)
		}
		var out, logs bytes.Buffer
		if e := cli.Execute(ctx, []string{"status", "--config", config, "--control-socket", socket, "--output", "json"}, &out, &logs); e != nil {
			t.Fatal(e)
		}
		var state struct {
			EngineConnectivity string              `json:"engine_connectivity"`
			Health             *app.HealthSnapshot `json:"health"`
		}
		if e := json.Unmarshal(out.Bytes(), &state); e != nil {
			t.Fatal(e)
		}
		if state.EngineConnectivity != "connected" || state.Health == nil || state.Health.EntriesReady || state.Health.Session != ready.Session {
			t.Fatalf("status used stale/nonowner state: %s", out.String())
		}
		// A second start cannot acquire ownership while the live service runs.
		if e := cli.Execute(ctx, []string{"start", "--live", "--config", config, "--control-socket", filepath.Join(dir, "other.sock")}, io.Discard, io.Discard); e == nil {
			t.Fatal("competing live owner admitted")
		}
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("live shutdown timed out")
		}
		if _, e := os.Stat(socket); !os.IsNotExist(e) {
			t.Fatalf("live socket retained: %v", e)
		}
	}
}
