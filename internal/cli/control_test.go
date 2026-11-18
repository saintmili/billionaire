package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/control"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIControlsHeldOwnerAndPreservesPendingReceiptAfterFill(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	t.Setenv("BILLIONAIRE_DATABASE_URL", dsn)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "owner.sock")
	cfg := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, []byte(configs.Example), 0600)
	invoke := func(args ...string) ([]byte, error) {
		var out, logs bytes.Buffer
		err := cli.Execute(ctx, append(args, "--config", cfg, "--control-socket", path, "--output", "json"), &out, &logs)
		return out.Bytes(), err
	}
	create := []string{"paper", "account", "create", "--name", "Control", "--balance", "1000", "--request-id", "cli-account"}
	if _, err = invoke(create...); err == nil || !strings.Contains(err.Error(), "owner unavailable") {
		t.Fatalf("absent owner: %v", err)
	}
	accounts, _ := store.PaperAccounts(ctx)
	if len(accounts) != 0 {
		t.Fatal("absent-owner CLI mutated database")
	}
	owner, err := store.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	data, _ := os.ReadFile("../../testdata/paper/spot-entry.json")
	var fixture struct {
		Metadata    marketdata.InstrumentInfo `json:"metadata"`
		Observation paper.Observation         `json:"observation"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	server, err := control.Listen(path, app.NewControl(owner, []marketdata.InstrumentInfo{fixture.Metadata}, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	data, err = invoke(create...)
	if err != nil {
		t.Fatal(err)
	}
	var account paper.Account
	json.Unmarshal(data, &account)
	if _, err = invoke(append(create, "--offline")...); err == nil {
		t.Fatal("offline mutation bypassed held owner")
	}
	args := []string{"paper", "open", "--account", account.ID, "--request-id", "cli-order", "--quantity", "1", "--at", "2026-11-12T10:00:00Z", "--fixture", "../../testdata/paper/spot-entry.json"}
	data, err = invoke(args...)
	if err != nil {
		t.Fatal(err)
	}
	var pending paper.Order
	json.Unmarshal(data, &pending)
	if pending.Status != "pending" {
		t.Fatalf("intent %s", data)
	}
	if _, err = owner.Observe(ctx, fixture.Observation, paper.ExecutionGate{Now: fixture.Observation.AvailableAt, StaleAfter: time.Minute, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	data, err = invoke("control", "outcome", "cli-order")
	if err != nil {
		t.Fatal(err)
	}
	var receipt paper.ControlOutcome
	json.Unmarshal(data, &receipt)
	if receipt.Status != "pending" {
		t.Fatalf("receipt changed with fill: %s", data)
	}
	data, err = invoke(args...)
	if err != nil {
		t.Fatal(err)
	}
	var retry paper.Order
	json.Unmarshal(data, &retry)
	if retry.Status != "pending" {
		t.Fatalf("retry changed with fill: %s", data)
	}
	orders, _ := store.PaperOrders(ctx, account.ID)
	if len(orders) != 1 || orders[0].Status != "filled" {
		t.Fatalf("current orders %v", orders)
	}
}
