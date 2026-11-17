package backtest_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"github.com/saintmili/billionaire/internal/backtest"
	"testing"
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
