package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestManualSpotCLIEntryPartialAndFinalClose(t *testing.T) {
	t.Setenv("MILLIONAIRE_DATABASE_URL", testdb.New(t))
	path := filepath.Join(t.TempDir(), "millionaire.yaml")
	invoke := func(args ...string) ([]byte, error) {
		t.Helper()
		var out, logs bytes.Buffer
		err := cli.Execute(context.Background(), append(args, "--config", path, "--output", "json"), &out, &logs)
		return out.Bytes(), err
	}
	if _, err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	data, err := invoke("paper", "account", "create", "--name", "demo", "--balance", "1000", "--request-id", "demo")
	var account paper.Account
	if err != nil || json.Unmarshal(data, &account) != nil {
		t.Fatalf("account: %s %v", data, err)
	}
	decimal := func(s string) domain.Decimal { v, _ := domain.ParseDecimal(s); return v }
	info := marketdata.InstrumentInfo{Instrument: domain.Instrument{Provider: "fixture", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}, PriceStep: decimal("0.01"), QuantityStep: decimal("0.001"), MinQuantity: decimal("0.001"), MinNotional: decimal("5")}
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	fixture := filepath.Join(t.TempDir(), "spot.json")
	write := func(id, price string, second int) {
		t.Helper()
		observation := paper.Observation{ID: id, Instrument: info.Instrument, Source: "fixture", Price: decimal(price), ObservedAt: at.Add(time.Duration(second) * time.Second), AvailableAt: at.Add(time.Duration(second) * time.Second), Ready: true}
		data, err := json.Marshal(map[string]any{"metadata": info, "observation": observation})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("known", "100", 0)
	openArgs := []string{"paper", "open", "--account", account.ID, "--market", "spot", "--symbol", "BTC/USDT", "--side", "long", "--quantity", "2", "--fee-rate", "0.001", "--fixture", fixture, "--request-id", "buy", "--at", at.Format(time.RFC3339)}
	data, err = invoke(openArgs...)
	var order paper.Order
	if err != nil || json.Unmarshal(data, &order) != nil || order.Status != "pending" {
		t.Fatalf("open: %s %v", data, err)
	}
	if _, err := invoke("paper", "observe", "--fixture", fixture, "--at", at.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	data, err = invoke("paper", "positions")
	if err != nil || strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("known price filled: %s %v", data, err)
	}
	write("entry", "100", 1)
	for range 2 {
		if _, err := invoke("paper", "observe", "--fixture", fixture, "--at", at.Add(time.Second).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	data, err = invoke(openArgs...)
	if err != nil || json.Unmarshal(data, &order) != nil || order.Status != "filled" {
		t.Fatalf("retry: %s %v", data, err)
	}
	data, err = invoke("paper", "positions", "--account", account.ID)
	var positions []paper.Position
	if err != nil || json.Unmarshal(data, &positions) != nil || len(positions) != 1 {
		t.Fatalf("positions: %s %v", data, err)
	}
	positionID := positions[0].ID
	for _, step := range []struct {
		id, quantity, price string
		second              int
	}{{"partial", "0.5", "110", 2}, {"final", "1.5", "90", 4}} {
		data, err = invoke("paper", "close", positionID, "--quantity", step.quantity, "--request-id", step.id, "--fee-rate", "0.001", "--at", at.Add(time.Duration(step.second)*time.Second).Format(time.RFC3339))
		if err != nil || json.Unmarshal(data, &order) != nil || order.Status != "pending" {
			t.Fatalf("close: %s %v", data, err)
		}
		write(step.id, step.price, step.second+1)
		if _, err := invoke("paper", "observe", "--fixture", fixture, "--at", at.Add(time.Duration(step.second+1)*time.Second).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	data, err = invoke("paper", "account", "show", account.ID)
	var result struct {
		Account paper.Account       `json:"account"`
		Ledger  []paper.LedgerEntry `json:"ledger"`
	}
	if err != nil || json.Unmarshal(data, &result) != nil || result.Account.Balance.String() != "989.61" || len(result.Ledger) != 7 {
		t.Fatalf("reconciliation: %s %v", data, err)
	}
	data, err = invoke("paper", "positions")
	if err != nil || json.Unmarshal(data, &positions) != nil || positions[0].Status != "closed" || positions[0].RealizedPnL.String() != "-10.39" {
		t.Fatalf("closed: %s %v", data, err)
	}
	var out, logs bytes.Buffer
	if err := cli.Execute(context.Background(), []string{"paper", "positions", "--config", path}, &out, &logs); err != nil || !strings.Contains(out.String(), "closed") {
		t.Fatalf("text positions: %s %v", &out, err)
	}
}
