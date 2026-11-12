package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestCLIProtectionAdjustAndFixtureExit(t *testing.T) {
	t.Setenv("MILLIONAIRE_DATABASE_URL", testdb.New(t))
	path := filepath.Join(t.TempDir(), "config.yaml")
	invoke := func(args ...string) ([]byte, error) {
		var out, logs bytes.Buffer
		err := cli.Execute(context.Background(), append(args, "--config", path, "--output", "json"), &out, &logs)
		return out.Bytes(), err
	}
	if _, err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	data, err := invoke("paper", "account", "create", "--name", "protected", "--balance", "1000", "--request-id", "account")
	var a paper.Account
	if err != nil || json.Unmarshal(data, &a) != nil {
		t.Fatalf("account: %s %v", data, err)
	}
	dec := func(s string) domain.Decimal { v, _ := domain.ParseDecimal(s); return v }
	info := marketdata.InstrumentInfo{Instrument: domain.Instrument{Provider: "fixture", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}, PriceStep: dec("0.01"), QuantityStep: dec("0.001"), MinQuantity: dec("0.001"), MinNotional: dec("5")}
	fixture := filepath.Join(t.TempDir(), "price.json")
	at := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	write := func(second int, price string) {
		t.Helper()
		now := at.Add(time.Duration(second) * time.Second)
		data, err := json.Marshal(map[string]any{"metadata": info, "observation": paper.Observation{ID: price, Instrument: info.Instrument, Source: "fixture", Price: dec(price), ObservedAt: now, AvailableAt: now, Ready: true}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(1, "100")
	if data, err := invoke("paper", "open", "--account", a.ID, "--quantity", "2", "--request-id", "entry", "--at", at.Format(time.RFC3339), "--fixture", fixture, "--stop-price", "90", "--take-profit", "120", "--target-quantity", "0.5"); err != nil {
		t.Fatalf("open: %s %v", data, err)
	}
	if _, err := invoke("paper", "observe", "--fixture", fixture, "--at", at.Add(time.Second).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	data, err = invoke("paper", "positions")
	var positions []paper.Position
	if err != nil || json.Unmarshal(data, &positions) != nil || len(positions) != 1 {
		t.Fatalf("positions: %s %v", data, err)
	}
	id := positions[0].ID
	args := []string{"paper", "adjust", id, "--stop-loss", "95", "--request-id", "adjust", "--at", at.Add(2 * time.Second).Format(time.RFC3339)}
	for range 2 {
		data, err = invoke(args...)
		var p paper.Position
		if err != nil || json.Unmarshal(data, &p) != nil || p.Protection.StopPrice != "95" || p.Protection.TargetPrice != "120" {
			t.Fatalf("adjust: %s %v", data, err)
		}
	}
	if _, err := invoke("paper", "adjust", id, "--stop-loss", "101", "--request-id", "bad", "--at", at.Add(2*time.Second).Format(time.RFC3339)); err == nil {
		t.Fatal("invalid stop accepted")
	}
	write(3, "94")
	if _, err := invoke("paper", "observe", "--fixture", fixture, "--at", at.Add(3*time.Second).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	data, err = invoke("paper", "fills")
	var fills []paper.Fill
	if err != nil || json.Unmarshal(data, &fills) != nil || len(fills) != 2 {
		t.Fatalf("fills: %s %v", data, err)
	}
	found := false
	for _, f := range fills {
		if f.ExitReason == "stop_loss" && f.Price.String() == "94" && f.Gap {
			found = true
		}
	}
	if !found {
		t.Fatalf("protective fill absent: %s", data)
	}
}
