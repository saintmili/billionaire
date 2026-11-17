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
