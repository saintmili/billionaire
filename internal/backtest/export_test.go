package backtest_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"io"
	"testing"
	"time"
)

func TestExportRetainsExactContextAndEmptyRows(t *testing.T) {
	s := backtest.Snapshot{Report: backtest.Report{ID: "a", Config: backtest.Config{InitialCapital: "9007199254740993.123456789012345678"}, Warnings: []string{"quote\",comma,\nnewline"}}}
	var out bytes.Buffer
	if e := backtest.WriteExport(&out, "csv", "trades", s); e != nil {
		t.Fatal(e)
	}
	rows, e := csv.NewReader(&out).ReadAll()
	if e != nil || len(rows) != 2 || rows[1][2] != "context" {
		t.Fatalf("empty context: %v %v", rows, e)
	}
	var got backtest.Snapshot
	if e = json.Unmarshal([]byte(rows[1][4]), &got); e != nil || got.Report.Config.InitialCapital != "9007199254740993.123456789012345678" || got.Report.Warnings[0] != s.Report.Warnings[0] {
		t.Fatalf("precision/context lost: %+v %v", got, e)
	}
}

type brokenWriter struct{}

func (brokenWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }
func TestExportPreservesEquityPhaseSequenceAndReturnsWriterErrors(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	value, _ := domain.ParseDecimal("-0.123456789012345678")
	s := backtest.Snapshot{Metrics: &analytics.Report{Equity: []analytics.EquityPoint{{At: at, Equity: &value}, {At: at, Equity: nil, Reason: "missing"}}}}
	var out bytes.Buffer
	if e := backtest.WriteExport(&out, "csv", "equity", s); e != nil {
		t.Fatal(e)
	}
	rows, e := csv.NewReader(&out).ReadAll()
	if e != nil || len(rows) != 3 || rows[1][3] != "0" || rows[2][3] != "1" {
		t.Fatalf("phase order %v %v", rows, e)
	}
	var p analytics.EquityPoint
	if e = json.Unmarshal([]byte(rows[1][5]), &p); e != nil || !p.At.Equal(at) || p.Equity.String() != "-0.123456789012345678" {
		t.Fatalf("exact equity %+v %v", p, e)
	}
	for _, format := range []string{"csv", "json"} {
		if e = backtest.WriteExport(brokenWriter{}, format, "equity", s); !errors.Is(e, io.ErrClosedPipe) {
			t.Fatalf("lost output error %v", e)
		}
	}
}
