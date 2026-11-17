package cli_test

import (
	"bytes"
	"context"
	"github.com/saintmili/billionaire/internal/cli"
	"strings"
	"testing"
)

func TestTradeReportHelpDocumentsScopesAndFilters(t *testing.T) {
	for _, args := range [][]string{{"report", "trades", "--help"}, {"backtest", "trades", "--help"}} {
		var out, logs bytes.Buffer
		if e := cli.Execute(context.Background(), args, &out, &logs); e != nil {
			t.Fatal(e)
		}
		for _, term := range []string{"--account", "--strategy-version", "--from", "--to", "--provider", "--market", "--symbol"} {
			if !strings.Contains(out.String(), term) {
				t.Fatalf("missing %s: %s", term, out.String())
			}
		}
	}
}
