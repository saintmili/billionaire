package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestM4CLIJSONSignalLifecycles(t *testing.T) {
	for _, test := range []struct{ action, side string }{{"manual_paper", "long"}, {"auto_paper", "short"}} {
		t.Run(test.action+"/"+test.side, func(t *testing.T) {
			t.Setenv("MILLIONAIRE_DATABASE_URL", testdb.New(t))
			dir := t.TempDir()
			cfg := filepath.Join(dir, "config.yaml")
			call := func(args ...string) ([]byte, error) {
				t.Helper()
				var out, logs bytes.Buffer
				err := cli.Execute(context.Background(), append(args, "--config", cfg, "--output", "json"), &out, &logs)
				return out.Bytes(), err
			}
			invoke := func(args ...string) []byte {
				t.Helper()
				data, err := call(args...)
				if err != nil {
					t.Fatalf("%v: %v %s", args, err, data)
				}
				return data
			}
			decode := func(data []byte, target any) {
				t.Helper()
				if err := json.Unmarshal(data, target); err != nil {
					t.Fatalf("decode %s: %v", data, err)
				}
			}
			write := func(name string, value any) string {
				t.Helper()
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			decimal := func(s string) domain.Decimal {
				d, err := domain.ParseDecimal(s)
				if err != nil {
					t.Fatal(err)
				}
				return d
			}
			invoke("init")
			var account paper.Account
			decode(invoke("paper", "account", "create", "--name", "acceptance", "--balance", "1000", "--request-id", "acceptance"), &account)
			at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
			clock := func(second int) string { return at.Add(time.Duration(second) * time.Second).Format(time.RFC3339) }
			info := marketdata.InstrumentInfo{Instrument: domain.Instrument{Provider: "fixture", Market: domain.Linear, Base: "ETH", Quote: "USDT", ID: "ETHUSDT"}, PriceStep: decimal("0.01"), QuantityStep: decimal("0.001"), MinQuantity: decimal("0.001"), MinNotional: decimal("5")}
			fixture := write("metadata.json", map[string]any{"metadata": info})
			strategyPath := filepath.Join(dir, "strategy.yaml")
			yaml := fmt.Sprintf("schema_version: 1\nid: m4-cli\nname: M4 CLI\nimplementation: rules\nmarkets: [linear]\ndirection: %s\nrisk_percent: '100'\nentry:\n  op: gt\n  left: {field: close}\n  right: {constant: '0'}\n", test.side)
			if err := os.WriteFile(strategyPath, []byte(yaml), 0600); err != nil {
				t.Fatal(err)
			}
			invoke("strategy", "add", strategyPath, "--instance", "m4-cli", "--provider", "fixture", "--market", "linear", "--symbol", "ETH/USDT", "--account", account.ID, "--action", test.action)
			invoke("strategy", "enable", "m4-cli")
			candle := domain.Candle{Instrument: info.Instrument, Timeframe: "1m", OpenTime: at.Add(-time.Minute), EndTime: at, Open: decimal("100"), High: decimal("110"), Low: decimal("90"), Close: decimal("100"), BaseVolume: decimal("1"), Source: "fixture", ObservedAt: at, Closed: true}
			eventPath := write("signal.json", candle)
			invoke("strategy", "evaluate", "m4-cli", "--event", eventPath, "--source-ready")
			var signals []strategy.Signal
			decode(invoke("signals", "list"), &signals)
			if len(signals) != 1 {
				t.Fatal(signals)
			}
			args := []string{"paper", "consume", "--fixture", fixture, "--at", clock(0), "--quantity", "2", "--leverage", "5"}
			invoke(args...)
			if test.action == "manual_paper" {
				var orders []paper.Order
				decode(invoke("paper", "orders"), &orders)
				if len(orders) != 0 {
					t.Fatal("entry before approval")
				}
				args = []string{"paper", "approve", signals[0].ID, "--fixture", fixture, "--at", clock(0), "--quantity", "2", "--leverage", "5"}
				invoke(args...)
			}
			invoke(args...)
			observe := func(second int, price string, wantRejected bool) {
				t.Helper()
				at := at.Add(time.Duration(second) * time.Second)
				obs := paper.Observation{ID: fmt.Sprintf("m4-price-%d", second), Instrument: info.Instrument, Source: "fixture", Price: decimal(price), ObservedAt: at, AvailableAt: at, Ready: true}
				path := write("observe.json", map[string]any{"metadata": info, "observation": obs})
				for range 2 {
					data, err := call("paper", "observe", "--fixture", path, "--at", clock(second))
					if (err != nil) != wantRejected {
						t.Fatalf("observation %s %v", data, err)
					}
				}
			}
			observe(0, "100", false)
			var fills []paper.Fill
			decode(invoke("paper", "fills"), &fills)
			if len(fills) != 0 {
				t.Fatal("filled signal's known price")
			}
			observe(1, "100", false)
			var positions []paper.Position
			decode(invoke("paper", "positions"), &positions)
			if len(positions) != 1 {
				t.Fatal(positions)
			}
			pid := positions[0].ID
			stop, target := "90", "110"
			if test.side == "short" {
				stop, target = "110", "90"
			}
			invoke("paper", "adjust", pid, "--request-id", "partial-target", "--at", clock(1), "--stop-loss", stop, "--take-profit", target, "--target-quantity", "0.5")
			observe(2, target, false)
			decode(invoke("paper", "positions"), &positions)
			if positions[0].RemainingQuantity.String() != "1.5" || positions[0].ReservedMargin.String() != "30" {
				t.Fatal(positions)
			}
			rate := "0.01"
			funding := paper.FundingEvent{ID: "m4-cli-funding", Instrument: info.Instrument, EffectiveAt: at.Add(3 * time.Second), AvailableAt: at.Add(3 * time.Second), Source: "fixture", Provenance: "CLI acceptance remaining quantity", Rate: &rate, Price: decimal("100")}
			fundingPath := write("funding.json", funding)
			for range 2 {
				var result paper.FundingResult
				decode(invoke("paper", "funding", "apply", "--fixture", fundingPath), &result)
				payment := "-1.5"
				if test.side == "short" {
					payment = "1.5"
				}
				if len(result.Applications) != 1 || result.Applications[0].Payment.String() != payment {
					t.Fatal(result)
				}
			}
			wantCash, wantPnL := "988.11", "-11.89"
			if test.side == "short" {
				invoke("paper", "close", pid, "--quantity", "1.5", "--request-id", "competing", "--at", clock(4))
				wantCash, wantPnL = "974.755", "-25.245"
			}
			invoke("kill-switch", "on")
			invoke("strategy", "disable", "m4-cli")
			if test.side == "short" {
				observe(5, "150", true)
			} else {
				observe(5, stop, false)
			}
			decode(invoke("paper", "positions"), &positions)
			if positions[0].Status != "closed" || positions[0].RealizedPnL.String() != wantPnL || !positions[0].ReservedMargin.IsZero() {
				t.Fatal(positions)
			}
			var snapshot struct {
				Account paper.Account       `json:"account"`
				Ledger  []paper.LedgerEntry `json:"ledger"`
			}
			decode(invoke("paper", "account", "show", account.ID, "--at", clock(5)), &snapshot)
			sum := decimal("0")
			for _, entry := range snapshot.Ledger {
				sum = sum.Add(entry.Amount)
			}
			if snapshot.Account.Balance.String() != wantCash || !sum.Equal(snapshot.Account.Balance) {
				t.Fatalf("account %+v ledger %s", snapshot.Account, sum)
			}
			decode(invoke("paper", "fills"), &fills)
			if len(fills) != 3 {
				t.Fatal(fills)
			}
			var decisions []paper.SignalDecision
			decode(invoke("paper", "decisions"), &decisions)
			if len(decisions) != 1 || decisions[0].Order == nil || decisions[0].Order.Status != "filled" {
				t.Fatal(decisions)
			}
		})
	}
}

func TestM4CLIRejectsSpotShortAndLeverageWithoutMoneyMovement(t *testing.T) {
	t.Setenv("MILLIONAIRE_DATABASE_URL", testdb.New(t))
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	invoke := func(args ...string) ([]byte, error) {
		t.Helper()
		var out, logs bytes.Buffer
		err := cli.Execute(context.Background(), append(args, "--config", cfg, "--output", "json"), &out, &logs)
		return out.Bytes(), err
	}
	if _, err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	data, err := invoke("paper", "account", "create", "--name", "restricted", "--balance", "1000", "--request-id", "restricted")
	var account paper.Account
	if err != nil || json.Unmarshal(data, &account) != nil {
		t.Fatalf("account %s %v", data, err)
	}
	for _, pair := range []struct{ side, leverage string }{{"short", "1"}, {"long", "2"}} {
		if _, err := invoke("paper", "open", "--account", account.ID, "--market", "spot", "--symbol", "BTC/USDT", "--side", pair.side, "--leverage", pair.leverage, "--quantity", "2", "--fixture", "../../testdata/paper/spot-metadata.json", "--request-id", "invalid-"+pair.side, "--at", "2026-11-12T10:00:00Z"); err == nil {
			t.Fatalf("accepted %+v", pair)
		}
	}
	for _, kind := range []string{"orders", "fills", "positions"} {
		data, err := invoke("paper", kind)
		if err != nil || string(bytes.TrimSpace(data)) != "[]" {
			t.Fatalf("%s %s %v", kind, data, err)
		}
	}
	data, err = invoke("paper", "account", "show", account.ID)
	var snapshot struct {
		Account paper.Account       `json:"account"`
		Ledger  []paper.LedgerEntry `json:"ledger"`
	}
	if err != nil || json.Unmarshal(data, &snapshot) != nil || snapshot.Account.Balance.String() != "1000" || len(snapshot.Ledger) != 1 {
		t.Fatalf("money changed %s %v", data, err)
	}
}
