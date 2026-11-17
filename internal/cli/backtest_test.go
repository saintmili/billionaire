package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"time"

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
