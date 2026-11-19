package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestSignalEntryFixtureCLI(t *testing.T) {
	for _, action := range []string{"manual_paper", "auto_paper", "alert_only"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("BILLIONAIRE_DATABASE_URL", testdb.New(t))
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			invoke := func(args ...string) []byte {
				t.Helper()
				var out, logs bytes.Buffer
				err := executeOffline(context.Background(), append(args, "--config", configPath, "--output", "json"), &out, &logs)
				if err != nil {
					t.Fatalf("%v: %v; %s", args, err, &out)
				}
				return out.Bytes()
			}
			invoke("init")
			var account paper.Account
			if json.Unmarshal(invoke("paper", "account", "create", "--name", "signals", "--balance", "1000", "--request-id", "signals"), &account) != nil {
				t.Fatal("account output")
			}
			strategyPath := filepath.Join(t.TempDir(), "strategy.yaml")
			if err := os.WriteFile(strategyPath, []byte("schema_version: 1\nid: entry-demo\nname: Entry demo\nimplementation: rules\nentry:\n  op: gt\n  left: {field: close}\n  right: {constant: '99'}\nexit:\n  op: lt\n  left: {field: close}\n  right: {constant: '99'}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			invoke("strategy", "add", strategyPath, "--instance", "entry-demo", "--provider", "fixture", "--account", account.ID, "--action", action)
			invoke("strategy", "enable", "entry-demo")
			invoke("strategy", "evaluate", "entry-demo", "--event", "../../testdata/paper/signal-entry.json", "--source-ready")
			var signals []strategy.Signal
			if json.Unmarshal(invoke("signals", "list"), &signals) != nil || len(signals) != 1 {
				t.Fatalf("signals: %+v", signals)
			}
			at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
			args := []string{"paper", "consume", "--fixture", "../../testdata/paper/spot-metadata.json", "--at", at.Format(time.RFC3339)}
			invoke(args...)
			invoke(args...)
			var orders []paper.Order
			if json.Unmarshal(invoke("paper", "orders"), &orders) != nil {
				t.Fatal("orders output")
			}
			if action == "auto_paper" {
				if len(orders) != 1 || orders[0].Status != "pending" {
					t.Fatalf("auto: %+v", orders)
				}
			} else if len(orders) != 0 {
				t.Fatalf("premature entry: %+v", orders)
			}
			if action == "manual_paper" {
				approve := []string{"paper", "approve", signals[0].ID, "--fixture", "../../testdata/paper/spot-metadata.json", "--at", at.Format(time.RFC3339)}
				invoke(approve...)
				invoke(approve...)
			}
			invoke("paper", "observe", "--fixture", "../../testdata/paper/spot-entry.json", "--at", at.Add(time.Second).Format(time.RFC3339))
			var fills []paper.Fill
			if json.Unmarshal(invoke("paper", "fills"), &fills) != nil {
				t.Fatal("fills output")
			}
			want := 1
			if action == "alert_only" {
				want = 0
			}
			if len(fills) != want {
				t.Fatalf("fills: %+v", fills)
			}
			var decisions []paper.SignalDecision
			if json.Unmarshal(invoke("paper", "decisions"), &decisions) != nil || len(decisions) != 1 {
				t.Fatalf("decisions: %+v", decisions)
			}
			if want == 1 && (decisions[0].Order == nil || decisions[0].Order.Status != "filled") {
				t.Fatalf("outcome: %+v", decisions)
			}
			// Exit evaluation has a later availability clock; explicit approval
			// and repeat invocations travel through the same CLI workflow.
			invoke("strategy", "evaluate", "entry-demo", "--event", "../../testdata/paper/signal-exit.json", "--source-ready")
			if json.Unmarshal(invoke("signals", "list"), &signals) != nil || len(signals) != 2 {
				t.Fatalf("exit signals: %+v", signals)
			}
			var exit strategy.Signal
			for _, signal := range signals {
				if signal.Kind == "exit" {
					exit = signal
				}
			}
			if exit.ID == "" {
				t.Fatal("missing exit")
			}
			invoke("kill-switch", "on")
			invoke("paper", "consume", "--fixture", "../../testdata/paper/spot-metadata.json", "--at", exit.ObservedAt.Format(time.RFC3339))
			if action == "manual_paper" {
				approve := []string{"paper", "approve", exit.ID, "--fixture", "../../testdata/paper/spot-metadata.json", "--at", exit.ObservedAt.Format(time.RFC3339), "--quantity", "0.1"}
				invoke(approve...)
				invoke(approve...)
			}
			invoke("paper", "observe", "--fixture", "../../testdata/paper/spot-exit.json", "--at", at.Add(3*time.Second).Format(time.RFC3339))
			if json.Unmarshal(invoke("paper", "fills"), &fills) != nil {
				t.Fatal("exit fills")
			}
			if action == "alert_only" {
				if len(fills) != 0 {
					t.Fatal(fills)
				}
			} else {
				if len(fills) != 2 {
					t.Fatal(fills)
				}
				var positions []paper.Position
				if json.Unmarshal(invoke("paper", "positions"), &positions) != nil || len(positions) != 1 {
					t.Fatal(positions)
				}
				if action == "auto_paper" && positions[0].Status != "closed" {
					t.Fatal(positions)
				}
				if action == "manual_paper" && (positions[0].Status != "open" || positions[0].RemainingQuantity.String() != "0.9") {
					t.Fatal(positions)
				}
			}

		})
	}
}
