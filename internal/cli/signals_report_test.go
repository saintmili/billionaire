package cli_test

import (
	"bytes"
	"context"
	"github.com/saintmili/billionaire/internal/cli"
	"strings"
	"testing"
)

func TestSignalReportHelpAndRequiredRule(t *testing.T) {
	for _, args := range [][]string{{"report", "signals", "--help"}, {"backtest", "signals", "--help"}} {
		var out, logs bytes.Buffer
		if e := cli.Execute(context.Background(), args, &out, &logs); e != nil {
			t.Fatal(e)
		}
		for _, s := range []string{"--threshold-percent", "--stop-percent", "--window-bars", "--timeframe", "--strategy-version"} {
			if !strings.Contains(out.String(), s) {
				t.Fatalf("missing %s", s)
			}
		}
	}
	var out, logs bytes.Buffer
	if e := cli.Execute(context.Background(), []string{"report", "signals", "--scope", "backtest", "--run", "x"}, &out, &logs); e == nil {
		t.Fatal("missing hypothetical rule accepted")
	}
}
