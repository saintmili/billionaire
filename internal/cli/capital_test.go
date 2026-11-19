package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/control"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCapitalCLIExactDepositWithdrawalAndReplay(t *testing.T) {
	t.Setenv("BILLIONAIRE_DATABASE_URL", testdb.New(t))
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	invoke := func(args ...string) ([]byte, error) {
		var out, logs bytes.Buffer
		err := executeOffline(context.Background(), append(args, "--config", cfg, "--output", "json"), &out, &logs)
		return out.Bytes(), err
	}
	if _, err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	raw, err := invoke("paper", "account", "create", "--name", "Capital", "--balance", "1000", "--request-id", "cli-capital")
	if err != nil {
		t.Fatal(err)
	}
	var a paper.Account
	if err = json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	args := []string{"paper", "account", "adjust", a.ID, "--direction", "deposit", "--amount", "12.125", "--reason", "fixture", "--at", "2026-11-19T12:00:00Z", "--request-id", "cli-deposit"}
	raw, err = invoke(args...)
	if err != nil || json.Unmarshal(raw, &a) != nil || a.Balance.String() != "1012.125" {
		t.Fatalf("deposit %s %v", raw, err)
	}
	retry, err := invoke(args...)
	if err != nil || string(retry) != string(raw) {
		t.Fatalf("retry %s %v", retry, err)
	}
	args[5] = "withdraw"
	args[7] = "12.125"
	args[len(args)-1] = "cli-withdraw"
	raw, err = invoke(args...)
	if err != nil || json.Unmarshal(raw, &a) != nil || a.Balance.String() != "1000" {
		t.Fatalf("withdraw %s %v", raw, err)
	}
}

func TestCapitalCLIUsesHeldOwnerSocket(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	t.Setenv("BILLIONAIRE_DATABASE_URL", dsn)
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "socket-capital", Name: "Socket", Currency: "USDT", InitialBalance: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(cfg, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "owner.sock")
	server, err := control.Listen(socket, app.NewControl(o, nil, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var out, logs bytes.Buffer
	args := []string{"paper", "account", "adjust", a.ID, "--direction", "deposit", "--amount", "0.000000000000000001", "--currency", "USDT", "--reason", "socket fixture", "--at", "2026-11-19T12:00:00Z", "--request-id", "socket-deposit", "--control-socket", socket, "--config", cfg, "--output", "json"}
	if err = cli.Execute(ctx, args, &out, &logs); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(out.Bytes(), &a); err != nil || a.Balance.String() != "1000.000000000000000001" {
		t.Fatalf("deposit %s %v", out.Bytes(), err)
	}
	out.Reset()
	if err = cli.Execute(ctx, append(args, "--offline"), &out, &logs); err == nil {
		t.Fatal("offline bypassed held owner")
	}
}
