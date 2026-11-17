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

	"encoding/csv"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"path/filepath"
	"testing"

	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/paper"
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

	for _, args := range [][]string{{"backtest", "signals", r.ID}, {"report", "signals", "--scope", "backtest", "--run", r.ID}} {
		args = append(args, "--threshold-percent", "100", "--stop-percent", "99", "--window-bars", "2", "--timeframe", "1m", "--strategy-version", added.Version.ID)
		output, err := invoke(args...)
		var signals analytics.SignalReport
		if err != nil || json.Unmarshal(output, &signals) != nil || signals.Scope != "backtest" || signals.RunID != r.ID || len(signals.Groups) != 1 || signals.Groups[0].Generated != 1 || signals.Groups[0].Candidates != 1 || signals.Groups[0].SuppressionRate == nil || signals.Groups[0].SuppressionRate.String() != "0" || signals.Groups[0].ConversionRate.String() != "1" || signals.Outcomes[0].Status != "incomplete" {
			t.Fatalf("signal JSON %s %v", output, err)
		}
		read, err := invoke("report", "signals", "--saved", signals.ID)
		var retained analytics.SignalReport
		if err != nil || json.Unmarshal(read, &retained) != nil || retained.ID != signals.ID || retained.Rule.ThresholdPercent != "100" {
			t.Fatalf("retained signal report %s %v", read, err)
		}
	}

	t.Run("compare and export retained evidence", func(t *testing.T) {
		output, err := invoke("backtest", "compare", r.ID, r.ID)
		var comparison backtest.Comparison
		if err != nil || json.Unmarshal(output, &comparison) != nil || len(comparison.Differences) != 0 || comparison.MatchedPeriod == nil || comparison.Left.Metrics == nil || comparison.Left.Metrics.ClosedTrades != 1 || comparison.Left.Metrics.Return == nil || comparison.Left.Metrics.MaximumDrawdown == nil || len(comparison.Left.SignalReports) == 0 {
			t.Fatalf("comparison %s %v", output, err)
		}
		output, err = invoke("report", "strategies", "--scope", "backtest", "--run", r.ID, "--strategy-version", added.Version.ID, "--timeframe", "1m")
		var selected struct {
			Runs []backtest.Snapshot `json:"runs"`
		}
		if err != nil || json.Unmarshal(output, &selected) != nil || len(selected.Runs) != 1 {
			t.Fatalf("strategies %s %v", output, err)
		}
		for _, kind := range []string{"report", "trades", "equity"} {
			output, err = invoke("backtest", "export", r.ID, "--kind", kind, "--format", "json")
			var exported backtest.Export
			if err != nil || json.Unmarshal(output, &exported) != nil || exported.Context.Report.Manifest != r.Manifest || len(exported.Context.SignalReports) == 0 || len(exported.Rows) == 0 {
				t.Fatalf("export %s %v", output, err)
			}
			output, err = invoke("backtest", "export", r.ID, "--kind", kind, "--format", "csv")
			rows, errCSV := csv.NewReader(bytes.NewReader(output)).ReadAll()
			if err != nil || errCSV != nil || len(rows) < 2 || len(rows[1]) != 6 {
				t.Fatalf("CSV %s %v %v", output, err, errCSV)
			}
		}
	})
	b, e = invoke("backtest", "report", r.ID)
	if e != nil {
		t.Fatal(e)
	}
	var saved backtest.Report
	if json.Unmarshal(b, &saved) != nil || saved.ID != r.ID || saved.Balance.String() != r.Balance.String() {
		t.Fatalf("saved: %s", b)
	}

	for _, args := range [][]string{{"backtest", "trades", r.ID}, {"report", "trades", "--scope", "backtest", "--run", r.ID}} {
		output, e := invoke(args...)
		if e != nil {
			t.Fatal(e)
		}
		var metrics analytics.Report
		if json.Unmarshal(output, &metrics) != nil || metrics.Scope != "backtest" || metrics.RunID != r.ID || metrics.ClosedTrades != 1 || metrics.NetPnL.String() != "-117.9" || metrics.Fees.String() != "17.9" {
			t.Fatalf("trade JSON %s", output)
		}

		if len(metrics.Equity) < 4 || metrics.Return == nil || metrics.Return.String() != "-0.01179" || metrics.MaximumDrawdown.String() != "117.9" {
			t.Fatalf("trade equity JSON %s", output)
		}
	}
	b, e = invoke("backtest", "trades", r.ID, "--account", r.AccountID, "--strategy-version", added.Version.ID, "--provider", "fixture", "--market", "spot", "--symbol", "BTC/USDT", "--from", "2026-01-01T00:21:00Z", "--to", "2026-01-01T00:22:00Z")
	var filtered analytics.Report
	if e != nil || json.Unmarshal(b, &filtered) != nil || filtered.ClosedTrades != 1 || filtered.Return != nil {
		t.Fatalf("trade filters %s %v", b, e)
	}
	if _, e = invoke("report", "trades"); e == nil {
		t.Fatal("implicit scope accepted")
	}
	if _, e = invoke("backtest", "trades", r.ID, "--from", "bad"); e == nil {
		t.Fatal("invalid range accepted")
	}
	b, e = invoke("paper", "account", "create", "--request-id", "trade-forward", "--name", "Forward", "--balance", "1000")
	if e != nil {
		t.Fatal(e)
	}
	var account paper.Account
	if json.Unmarshal(b, &account) != nil {
		t.Fatal("account JSON")
	}
	b, e = invoke("report", "trades", "--scope", "forward-paper", "--account", account.ID)
	var forward analytics.Report
	if e != nil || json.Unmarshal(b, &forward) != nil || forward.Scope != "forward-paper" || forward.ClosedTrades != 0 || forward.CashBalance.String() != "1000" || forward.Trades == nil {
		t.Fatalf("forward JSON %s %v", b, e)
	}
	b, e = invoke("report", "signals", "--scope", "forward-paper", "--account", account.ID, "--threshold-percent", "1", "--stop-percent", "1", "--window-bars", "2")
	var signals analytics.SignalReport
	if e != nil || json.Unmarshal(b, &signals) != nil || signals.Scope != "forward-paper" || len(signals.Groups) != 0 {
		t.Fatalf("empty forward signals %s %v", b, e)
	}
	t.Run("explicit fixture selection is independent of populated cache", func(t *testing.T) {
		var changed fixture.Dataset
		if e := json.Unmarshal(fixtureBytes, &changed); e != nil {
			t.Fatal(e)
		}
		for i := range changed.Candles {
			c := &changed.Candles[i]
			if c.Instrument.Market == domain.Spot && c.Instrument.ID == "BTCUSDT" && c.OpenTime.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
				c.Close, _ = domain.ParseDecimal("9.6")
				c.ObservedAt = c.ObservedAt.Add(time.Hour)
			}
		}
		encoded, e := json.Marshal(changed)
		if e != nil {
			t.Fatal(e)
		}
		changedPath := filepath.Join(t.TempDir(), "changed.json")
		if e = os.WriteFile(changedPath, encoded, 0600); e != nil {
			t.Fatal(e)
		}
		output, e := invoke("backtest", "run", "--strategy", added.Version.ID, "--provider", "fixture", "--fixture", changedPath, "--from", "2026-01-01T00:20:00Z", "--to", "2026-01-01T00:22:00Z")
		var selected backtest.Report
		if e != nil || json.Unmarshal(output, &selected) != nil || selected.Manifest.DatasetHash == r.Manifest.DatasetHash {
			t.Fatalf("selected fixture silently replaced by cache: %s %v", output, e)
		}
	})
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
