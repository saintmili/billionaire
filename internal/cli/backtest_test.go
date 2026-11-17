package cli_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"encoding/json"
	"github.com/jackc/pgx/v5"
	"path/filepath"
	"testing"

	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestBacktestCLICompleteFixtureJourney(t *testing.T) {
	t.Setenv("MILLIONAIRE_DATABASE_URL", testdb.New(t))
	path := filepath.Join(t.TempDir(), "config.yaml")
	invoke := func(args ...string) ([]byte, error) {
		var out, logs bytes.Buffer
		e := cli.Execute(context.Background(), append(args, "--config", path, "--output", "json"), &out, &logs)
		return out.Bytes(), e
	}
	if _, e := invoke("init"); e != nil {
		t.Fatal(e)
	}
	b, e := invoke("strategy", "add", "../../examples/strategies/breakout.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var added struct {
		Version strategy.Version `json:"version"`
	}
	if e = json.Unmarshal(b, &added); e != nil {
		t.Fatal(e)
	}
	fixturePath := filepath.Join(t.TempDir(), "candles.json")
	fixtureBytes, e := os.ReadFile("../../testdata/market/m3.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(fixturePath, fixtureBytes, 0600); e != nil {
		t.Fatal(e)
	}
	b, e = invoke("backtest", "run", "--strategy", added.Version.ID, "--provider", "fixture", "--fixture", fixturePath, "--market", "spot", "--symbol", "BTC/USDT", "--from", "2026-01-01T00:20:00Z", "--to", "2026-01-01T00:22:00Z", "--timeframes", "1m", "--seed", "7", "--capital", "10000", "--fee-rate", "0.001", "--slippage-bps", "0")
	if e != nil {
		t.Fatalf("run: %s %v", b, e)
	}
	var r backtest.Report
	if e = json.Unmarshal(b, &r); e != nil {
		t.Fatal(e)
	}
	if r.Status != "completed" || len(r.Fills) != 2 || r.Balance.String() != "9882.1" {
		t.Fatalf("run: %s", b)
	}
	b, e = invoke("backtest", "report", r.ID)
	if e != nil {
		t.Fatal(e)
	}
	var saved backtest.Report
	if json.Unmarshal(b, &saved) != nil || saved.ID != r.ID || saved.Balance.String() != r.Balance.String() {
		t.Fatalf("saved: %s", b)
	}
	if e = os.Remove(fixturePath); e != nil {
		t.Fatal(e)
	}

	b, e = invoke("backtest", "run", "--strategy", added.Version.ID, "--provider", "fixture", "--cached", "--from", "2026-01-01T00:20:00Z", "--to", "2026-01-01T00:22:00Z")
	var cached backtest.Report
	if e != nil || json.Unmarshal(b, &cached) != nil || cached.Balance.String() != r.Balance.String() || cached.Config.Source != "cache-only" {
		t.Fatalf("cache-only after fixture removal: %s %v", b, e)
	}
	if _, e = invoke("backtest", "run", "--source-run", r.ID, "--cached"); e == nil {
		t.Fatal("source-run input flag accepted")
	}
	if _, e = invoke("backtest", "run", "--strategy", added.Version.ID, "--provider", "fixture", "--cached", "--symbol", "ETH/USDT", "--from", "2026-01-01T00:20:00Z", "--to", "2026-01-01T00:22:00Z"); e == nil {
		t.Fatal("missing cached metadata accepted")
	}
	b, e = invoke("backtest", "run", "--source-run", r.ID)
	if e != nil {
		t.Fatal(e)
	}
	var rerun backtest.Report
	if json.Unmarshal(b, &rerun) != nil || rerun.ID == r.ID || rerun.Manifest.ReproducibilityHash != r.Manifest.ReproducibilityHash || rerun.Balance.String() != r.Balance.String() {
		t.Fatalf("frozen rerun: %s", b)
	}
	if _, e = invoke("backtest", "run", "--source-run", r.ID, "--capital", "10"); e == nil {
		t.Fatal("conflicting frozen inputs accepted")
	}

	t.Run("public historical selection and network-free cached replay", func(t *testing.T) {
		source, e := fixture.Open("../../testdata/market/m3.json")
		if e != nil {
			t.Fatal(e)
		}
		metadata, e := source.Instrument(context.Background(), domain.Spot, "BTC/USDT")
		if e != nil {
			t.Fatal(e)
		}
		bars, e := source.History(context.Background(), marketdata.Query{Instrument: metadata.Instrument, Timeframe: "1m", From: source.Now().Add(-22 * time.Minute), To: source.Now()})
		if e != nil {
			t.Fatal(e)
		}
		var requests atomic.Int64
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			requests.Add(1)
			if req.Header.Get("Authorization") != "" || req.Header.Get("X-BAPI-API-KEY") != "" {
				t.Error("authenticated trading access")
			}
			var result any
			switch req.URL.Path {
			case "/v5/market/instruments-info":
				result = map[string]any{"category": "spot", "list": []any{map[string]any{"symbol": "BTCUSDT", "baseCoin": "BTC", "quoteCoin": "USDT", "status": "Trading", "priceFilter": map[string]string{"tickSize": "0.01"}, "lotSizeFilter": map[string]string{"basePrecision": "0.001", "minOrderAmt": "5"}}}}
			case "/v5/market/kline":
				end, _ := strconv.ParseInt(req.URL.Query().Get("end"), 10, 64)
				rows := [][]string{}
				for i := len(bars) - 1; i >= 0; i-- {
					b := bars[i]
					if b.OpenTime.UnixMilli() <= end {
						rows = append(rows, []string{strconv.FormatInt(b.OpenTime.UnixMilli(), 10), b.Open.String(), b.High.String(), b.Low.String(), b.Close.String(), b.BaseVolume.String(), "0"})
					}
				}
				result = map[string]any{"category": "spot", "symbol": "BTCUSDT", "list": rows}
			default:
				t.Errorf("unexpected public path %s", req.URL.Path)
			}
			json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "time": time.Now().UnixMilli(), "result": result})
		}))
		defer server.Close()
		previousTransport := http.DefaultTransport
		http.DefaultTransport = server.Client().Transport
		defer func() { http.DefaultTransport = previousTransport }()
		t.Setenv("MILLIONAIRE_REST_URL", server.URL)
		args := []string{"backtest", "run", "--strategy", added.Version.ID, "--provider", "bybit", "--from", "2026-01-01T00:20:00Z", "--to", "2026-01-01T00:22:00Z"}
		output, e := invoke(args...)
		var public backtest.Report
		if e != nil || json.Unmarshal(output, &public) != nil || public.Balance.String() != r.Balance.String() {
			t.Fatalf("public run: %s %v", output, e)
		}
		before := requests.Load()
		server.Close()
		output, e = invoke(append(args, "--cached")...)
		var offline backtest.Report
		if e != nil || json.Unmarshal(output, &offline) != nil || offline.Balance.String() != public.Balance.String() || requests.Load() != before {
			t.Fatalf("offline run: %s %v", output, e)
		}
	})
	blocker, e := pgx.Connect(context.Background(), os.Getenv("MILLIONAIRE_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Close(context.Background())
	if _, e = blocker.Exec(context.Background(), "SELECT pg_advisory_lock(7341928601)"); e != nil {
		t.Fatal(e)
	}
	defer blocker.Exec(context.Background(), "SELECT pg_advisory_unlock(7341928601)")
	cancelled, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var failedOut, failedLogs bytes.Buffer
	e = cli.Execute(cancelled, []string{"backtest", "run", "--source-run", r.ID, "--config", path, "--output", "json"}, &failedOut, &failedLogs)
	var stopped backtest.Report
	if !errors.Is(e, context.DeadlineExceeded) || json.Unmarshal(failedOut.Bytes(), &stopped) != nil || stopped.ID == "" || stopped.Status != "cancelled" {
		t.Fatalf("CLI cancelled result: %s %v", &failedOut, e)
	}
	if _, e = invoke("backtest", "report", "missing"); e == nil {
		t.Fatal("missing run succeeded")
	}
}
