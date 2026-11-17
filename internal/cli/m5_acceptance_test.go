package cli_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestM5CLICombinedLinearAnalyticsFrozenExportAndTrainTest(t *testing.T) {
	t.Setenv("MILLIONAIRE_DATABASE_URL", testdb.New(t))
	path := filepath.Join(t.TempDir(), "config.yaml")
	invoke := func(args ...string) []byte {
		t.Helper()
		var out, logs bytes.Buffer
		if e := cli.Execute(context.Background(), append(args, "--config", path, "--output", "json"), &out, &logs); e != nil {
			t.Fatalf("%v: %s %v", args, out.String(), e)
		}
		return out.Bytes()
	}
	invoke("init")
	var added struct {
		Version strategy.Version `json:"version"`
	}
	if e := json.Unmarshal(invoke("strategy", "add", "../../examples/strategies/breakout.yaml"), &added); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../../testdata/market/m3.json")
	if e != nil {
		t.Fatal(e)
	}
	var dataset fixture.Dataset
	if e = json.Unmarshal(raw, &dataset); e != nil {
		t.Fatal(e)
	}
	end := time.Date(2026, 1, 1, 0, 22, 0, 0, time.UTC)
	var instrument domain.Instrument
	for i := range dataset.Candles {
		c := &dataset.Candles[i]
		if c.Instrument.Market == domain.Linear && c.Instrument.ID == "BTCUSDT" && c.EndTime.Equal(end) {
			c.Low, _ = domain.ParseDecimal("9")
			c.Close, _ = domain.ParseDecimal("12")
			c.High, _ = domain.ParseDecimal("13")
			instrument = c.Instrument
		}
	}
	fixturePath := filepath.Join(t.TempDir(), "partial-funding.json")
	encoded, e := json.Marshal(dataset)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(fixturePath, encoded, 0600); e != nil {
		t.Fatal(e)
	}
	rate := "0.01"
	funding := backtest.FundingDataset{Expected: []time.Time{end}, Events: []paper.FundingEvent{{ID: "acceptance-funding", Instrument: instrument, EffectiveAt: end, AvailableAt: end, Rate: &rate, Price: func() domain.Decimal { d, _ := domain.ParseDecimal("12"); return d }(), Source: "fixture", Provenance: "M5 joined acceptance: remaining 600 at mark 12"}}}
	fundingPath := filepath.Join(t.TempDir(), "funding.json")
	encoded, e = json.Marshal(funding)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(fundingPath, encoded, 0600); e != nil {
		t.Fatal(e)
	}
	base := []string{"backtest", "run", "--strategy", added.Version.ID, "--fixture", fixturePath, "--market", "linear", "--leverage", "2", "--funding-file", fundingPath, "--target-price", "12", "--target-quantity", "400", "--from", "2026-01-01T00:20:00Z", "--to", "2026-01-01T00:22:00Z", "--seed", "7", "--capital", "10000", "--fee-rate", "0.001", "--slippage-bps", "0"}
	decodeRun := func(raw []byte) backtest.Report {
		t.Helper()
		var r backtest.Report
		if e := json.Unmarshal(raw, &r); e != nil {
			t.Fatal(e)
		}
		return r
	}
	run := decodeRun(invoke(base...))
	if run.Status != "completed" || run.Balance.String() != "8486.2" || len(run.Fills) != 2 || run.Fills[1].Quantity.String() != "400" || run.Positions[0].RemainingQuantity.String() != "600" || run.Positions[0].ReservedMargin.String() != "2628" || run.FundingCoverage.Status != "complete" || run.Funding[0].Applications[0].Payment.String() != "-72" {
		t.Fatalf("partial funding %+v", run)
	}
	var trades analytics.Report
	if e = json.Unmarshal(invoke("backtest", "trades", run.ID), &trades); e != nil {
		t.Fatal(e)
	}
	if trades.ClosedTrades != 0 || trades.OpenPositions != 1 || trades.LedgerFees.String() != "13.8" || trades.LedgerFunding.String() != "-72" || trades.LedgerTotal.String() != "8486.2" || trades.LedgerCashReconciled == nil || !*trades.LedgerCashReconciled || trades.Return == nil || trades.Return.String() != "0.29142" {
		t.Fatalf("linear trade analytics %+v", trades)
	}
	var signals analytics.SignalReport
	if e = json.Unmarshal(invoke("backtest", "signals", run.ID, "--threshold-percent", "5", "--stop-percent", "50", "--window-bars", "1"), &signals); e != nil {
		t.Fatal(e)
	}
	if len(signals.Groups) != 1 || signals.Groups[0].Generated != 1 || signals.Groups[0].Entries != 1 || len(signals.Outcomes) != 1 || signals.Outcomes[0].Status != "success" {
		t.Fatalf("signal success %+v", signals)
	}
	// Identical rule/evidence on the rerun must reproduce retrospective outcomes.
	rerun := decodeRun(invoke("backtest", "run", "--source-run", run.ID))
	var rerunSignals analytics.SignalReport
	if e = json.Unmarshal(invoke("backtest", "signals", rerun.ID, "--threshold-percent", "5", "--stop-percent", "50", "--window-bars", "1"), &rerunSignals); e != nil {
		t.Fatal(e)
	}
	if rerun.ID == run.ID || rerun.AccountID == run.AccountID || rerun.Manifest != run.Manifest || rerun.Balance.String() != run.Balance.String() || rerunSignals.Outcomes[0].Status != "success" {
		t.Fatal("frozen economics/success changed")
	}
	// Equal immutable assumptions and equal selected rules compare without changes.
	var comparison backtest.Comparison
	if e = json.Unmarshal(invoke("backtest", "compare", run.ID, rerun.ID), &comparison); e != nil {
		t.Fatal(e)
	}
	if len(comparison.Differences) != 0 || comparison.Left.FundingStatus != "complete" || comparison.Left.Metrics.OpenPositions != 1 {
		t.Fatalf("frozen comparison %+v", comparison)
	}
	changed := decodeRun(invoke(append(append([]string(nil), base...), "--fee-rate", "0.002")...))
	if e = json.Unmarshal(invoke("backtest", "compare", run.ID, changed.ID), &comparison); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, d := range comparison.Differences {
		if d.Field == "config.fee_rate" {
			found = true
		}
	}
	if !found || comparison.DirectlyComparable || changed.Manifest.ConfigurationHash == run.Manifest.ConfigurationHash || changed.Manifest.ReproducibilityHash == run.Manifest.ReproducibilityHash || changed.Balance.String() != "8472.4" {
		t.Fatalf("changed fees %+v %+v", changed, comparison)
	}
	if e = os.Remove(fixturePath); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(fundingPath); e != nil {
		t.Fatal(e)
	}
	if retained := decodeRun(invoke("backtest", "report", run.ID)); retained.Manifest != run.Manifest || retained.Balance.String() != "8486.2" {
		t.Fatal("saved run changed")
	}
	for _, kind := range []string{"report", "trades", "equity"} {
		var exported backtest.Export
		if e = json.Unmarshal(invoke("backtest", "export", run.ID, "--kind", kind, "--format", "json"), &exported); e != nil {
			t.Fatal(e)
		}
		if len(exported.Rows) == 0 || exported.Context.FundingStatus != "complete" || len(exported.Context.SignalReports) != 1 || exported.Context.SignalReports[0].Rule.ThresholdPercent != "5" || exported.Context.Report.Config.Funding.Events[0].Provenance != funding.Events[0].Provenance {
			t.Fatalf("joined JSON export %+v", exported)
		}
		rows, e := csv.NewReader(bytes.NewReader(invoke("backtest", "export", run.ID, "--kind", kind, "--format", "csv"))).ReadAll()
		if e != nil {
			t.Fatal(e)
		}
		if len(rows) < 2 || len(rows[1]) != 6 {
			t.Fatalf("CSV %+v", rows)
		}
		var context backtest.Snapshot
		if e = json.Unmarshal([]byte(rows[1][4]), &context); e != nil || context.Report.Manifest != run.Manifest || context.Report.Funding[0].Applications[0].Payment.String() != "-72" {
			t.Fatalf("CSV funding context %+v %v", context, e)
		}
	}
	afterRemoval := decodeRun(invoke("backtest", "run", "--source-run", run.ID))
	if afterRemoval.Manifest != run.Manifest || afterRemoval.Balance.String() != "8486.2" {
		t.Fatal("source removal broke frozen replay")
	}

	t.Run("separate chronological train and test samples", func(t *testing.T) {
		// Same immutable parameters and fresh capital; test warmup includes earlier
		// observations, but earlier signals/trades/runtime are never carried across.
		train := decodeRun(invoke("backtest", "run", "--strategy", added.Version.ID, "--fixture", "../../testdata/market/m3.json", "--market", "spot", "--from", "2026-01-01T00:20:00Z", "--to", "2026-01-01T00:21:00Z"))
		test := decodeRun(invoke("backtest", "run", "--strategy", added.Version.ID, "--fixture", "../../testdata/market/m3.json", "--market", "spot", "--from", "2026-01-01T00:21:00Z", "--to", "2026-01-01T00:22:00Z"))
		if len(train.Signals) != 1 || len(train.Orders) != 1 || train.Orders[0].Status != "pending" || len(train.Fills) != 0 || len(test.Signals) != 0 || len(test.Fills) != 0 || train.Balance.String() != "10000" || test.Balance.String() != "10000" {
			t.Fatalf("split samples train%+v test%+v", train, test)
		}
		for _, r := range []backtest.Report{train, test} {
			var metrics analytics.Report
			if e = json.Unmarshal(invoke("backtest", "trades", r.ID), &metrics); e != nil || metrics.ClosedTrades != 0 || metrics.Return == nil || metrics.Return.String() != "0" || metrics.MaximumDrawdown.String() != "0" {
				t.Fatalf("sample %+v %v", metrics, e)
			}
		}
		if e = json.Unmarshal(invoke("backtest", "compare", train.ID, test.ID), &comparison); e != nil || comparison.MatchedPeriod != nil || comparison.DirectlyComparable {
			t.Fatalf("disjoint samples %+v %v", comparison, e)
		}
	})
}
