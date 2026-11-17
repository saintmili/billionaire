package postgres_test

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
	"net/url"
	"testing"
	"time"
)

func TestBacktestSnapshotsPreserveMissingAndIncompleteEvidenceWithoutExecution(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	s, e := postgres.Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx, postgres.Up); e != nil {
		t.Fatal(e)
	}
	db, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close(ctx)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	capital, _ := domain.ParseDecimal("1000")
	version := strategy.Version{}
	version.ID = strategy.ContentHash(version.Definition)
	in := backtest.Inputs{Config: backtest.Config{VersionID: version.ID, InitialCapital: "1000", From: at, To: at.Add(time.Hour)}, Version: version}
	manifest := backtest.NewManifest(in)
	for _, tc := range []struct {
		id, status          string
		corrupt, incomplete bool
	}{{"empty", "completed", false, false}, {"incomplete", "completed", false, true}, {"corrupt", "completed", true, false}, {"legacy", "completed", false, false}, {"failed", "failed", false, false}, {"running", "running", false, false}, {"cancelled", "cancelled", false, false}} {
		r := backtest.Report{ID: tc.id, Status: tc.status, Scope: "backtest", Config: in.Config, Manifest: manifest, Balance: capital, Equity: []analytics.EquityPoint{{At: at, Equity: &capital}, {At: at.Add(time.Hour), Equity: &capital}}}
		if tc.incomplete {
			r.Equity[1].Equity = nil
			r.Equity[1].Reason = "missing mark"
		}
		if tc.id == "legacy" {
			r.Manifest.Model = "unrecognized-old-model"
		}
		if tc.corrupt {
			r.Manifest.DatasetHash = "lost"
		}
		inputs, _ := json.Marshal(in)
		report, _ := json.Marshal(r)
		m, _ := json.Marshal(r.Manifest)
		// Fixture deliberately has no execution schema. Read paths must use retained artifacts.
		if _, e = db.Exec(ctx, "INSERT INTO millionaire.backtest_runs(id,execution_schema,status,inputs,manifest,report) VALUES($1,$2,$3,$4,$5,$6)", tc.id, "absent_"+tc.id, tc.status, inputs, m, report); e != nil {
			t.Fatal(e)
		}
		got, e := s.BacktestSnapshot(ctx, tc.id)
		if e != nil {
			t.Fatal(e)
		}
		switch tc.id {
		case "empty":
			if got.Metrics == nil || got.Metrics.ClosedTrades != 0 || got.Metrics.Return == nil || !got.Metrics.Return.IsZero() || got.Metrics.MaximumDrawdown == nil || !got.Metrics.MaximumDrawdown.IsZero() {
				t.Fatalf("valid empty %+v", got)
			}
		case "legacy":
			if got.Metrics == nil || got.AmbiguousFills != nil || len(got.Warnings) == 0 {
				t.Fatalf("legacy ambiguity invented %+v", got)
			}
		case "incomplete":
			if got.Metrics == nil || got.Metrics.Return != nil || got.Metrics.MaximumDrawdown != nil || got.Metrics.ValuationStatus != "incomplete" {
				t.Fatalf("missing mark %+v", got)
			}
		default:
			if got.Metrics != nil || got.RejectedDecisions != nil || got.PendingOrders != nil || got.EvidenceStatus == "verified" {
				t.Fatalf("unknown treated as zero %+v", got)
			}
		}
		if got.SignalStatus != "not_evaluated/no_saved_rule" {
			t.Fatalf("invented signal evaluation %+v", got)
		}
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "on")
	u.RawQuery = q.Encode()
	readOnly, e := postgres.Open(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	defer readOnly.Close()
	if _, e = readOnly.CompareBacktests(ctx, "empty", "incomplete"); e != nil {
		t.Fatalf("comparison attempted writes: %v", e)
	}
	if _, e = readOnly.BacktestStrategies(ctx, postgres.StrategyReportFilter{}); e != nil {
		t.Fatalf("strategy report attempted writes: %v", e)
	}
	if _, e = s.BacktestStrategies(ctx, postgres.StrategyReportFilter{Range: backtest.Period{From: at.Add(time.Hour), To: at}}); e == nil {
		t.Fatal("invalid selection range accepted")
	}
	selected, e := s.BacktestStrategies(ctx, postgres.StrategyReportFilter{})
	if e != nil || len(selected) != 7 {
		t.Fatalf("list omitted unavailable runs %+v %v", selected, e)
	}
}
