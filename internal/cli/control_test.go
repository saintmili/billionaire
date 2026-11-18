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
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
	"io"
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
	if _, err = invoke("database", "migrate", "down"); err == nil {
		t.Fatal("migration bypassed held owner")
	}
	if _, err = invoke("kill-switch", "on", "--request-id", "cli-pause"); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke("kill-switch", "off", "--request-id", "cli-resume"); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke("kill-switch", "on", "--request-id", "cli-pause"); err != nil {
		t.Fatal(err)
	}
	paused, err := store.KillSwitch(ctx)
	if err != nil || paused {
		t.Fatalf("old receipt reapplied pause: %t %v", paused, err)
	}
	if _, err = invoke("paper", "risk", "set", "--account", account.ID, "--max-positions", "2", "--request-id", "cli-risk"); err != nil {
		t.Fatal(err)
	}
	policy, err := store.RiskPolicy(ctx, account.ID, "")
	if err != nil || policy.MaxPositions != 2 {
		t.Fatalf("risk policy %v %v", policy, err)
	}
	raw, _ := os.ReadFile("../../examples/strategies/breakout.yaml")
	definition, err := strategy.Parse(strings.NewReader(string(raw)), strategy.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	_, instance, err := owner.RegisterStrategy(ctx, definition, &strategy.Instance{ID: "control-strategy", Instrument: fixture.Metadata.Instrument, Account: account.ID, Action: strategy.ManualPaper})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = invoke("strategy", "enable", instance.ID, "--request-id", "cli-enable"); err != nil {
		t.Fatal(err)
	}
	actual, err := store.StrategyInstance(ctx, instance.ID)
	if err != nil || !actual.Enabled {
		t.Fatalf("instance %v %v", actual, err)
	}
	if _, err = invoke("strategy", "disable", instance.ID, "--request-id", "cli-disable"); err != nil {
		t.Fatal(err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(positions) != 1 {
		t.Fatalf("positions %v %v", positions, err)
	}
	if _, err = invoke("paper", "adjust", positions[0].ID, "--stop-loss", "95", "--at", "2026-11-12T10:00:02Z", "--request-id", "cli-adjust"); err != nil {
		t.Fatal(err)
	}
	if _, err = invoke("paper", "close", positions[0].ID, "--quantity", "0.5", "--at", "2026-11-12T10:00:03Z", "--request-id", "cli-close"); err != nil {
		t.Fatal(err)
	}

}

func TestForegroundHoldServesControlUntilGracefulCancellation(t *testing.T) {
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
	path := filepath.Join(dir, "hold.sock")
	cfg := filepath.Join(dir, "engine.yaml")
	if err = os.WriteFile(cfg, []byte(configs.Example+"\nengine:\n  queue_capacity: 1\n  lookback: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	running, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- cli.Execute(running, []string{"start", "--hold", "--fixture", "../../testdata/market/m2.json", "--config", cfg, "--control-socket", path}, io.Discard, io.Discard)
	}()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	raw, _ := json.Marshal(paper.CreateAccount{RequestID: "hold-account", Name: "Hold", Currency: "USDT", InitialBalance: "1000"})
	command := app.Command{ID: "hold-control", Kind: "account.create", Payload: raw}
	waiting := true
	for waiting {
		select {
		case err := <-done:
			t.Fatalf("owner stopped before control: %v", err)
		case <-deadline.C:
			t.Fatal("owner socket did not become available")
		case <-ticker.C:
			if _, err = control.Call(ctx, path, control.Request{Command: &command}); err == nil {
				waiting = false
			}
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("owner shutdown timed out")
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("owner socket retained: %v", err)
	}
	active, err := store.EngineRunning(ctx)
	if err != nil || active {
		t.Fatalf("owner lock retained: %t %v", active, err)
	}
}
