package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	b, e = invoke("backtest", "run", "--strategy", added.Version.ID, "--provider", "fixture", "--fixture", "../../testdata/market/m3.json", "--market", "spot", "--symbol", "BTC/USDT", "--from", "2026-01-01T00:20:00Z", "--to", "2026-01-01T00:22:00Z", "--timeframes", "1m", "--seed", "7", "--capital", "10000", "--fee-rate", "0.001", "--slippage-bps", "0")
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
	if _, e = invoke("backtest", "report", "missing"); e == nil {
		t.Fatal("missing run succeeded")
	}
}
