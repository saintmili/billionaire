package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestPaperAccountCLI(t *testing.T) {
	t.Setenv("BILLIONAIRE_DATABASE_URL", testdb.New(t))
	path := filepath.Join(t.TempDir(), "config.yaml")
	invoke := func(args ...string) ([]byte, error) {
		t.Helper()
		var out, logs bytes.Buffer
		err := cli.Execute(context.Background(), append(args, "--config", path, "--output", "json"), &out, &logs)
		return out.Bytes(), err
	}
	if _, err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	data, err := invoke("paper", "account", "create", "--name", "demo", "--balance", "10000.125", "--request-id", "demo-opening")
	if err != nil {
		t.Fatal(err)
	}
	var account paper.Account
	if err := json.Unmarshal(data, &account); err != nil {
		t.Fatal(err)
	}
	if account.ID == "" || account.Balance.String() != "10000.125" || account.Currency != "USDT" {
		t.Fatalf("account: %s", data)
	}
	retry, err := invoke("paper", "account", "create", "--name", "demo", "--balance", "10000.1250", "--request-id", "demo-opening")
	if err != nil || string(retry) != string(data) {
		t.Fatalf("retry: %s %v", retry, err)
	}
	data, err = invoke("paper", "accounts")
	var accounts []paper.Account
	if err != nil || json.Unmarshal(data, &accounts) != nil || len(accounts) != 1 {
		t.Fatalf("list: %s %v", data, err)
	}
	data, err = invoke("paper", "account", "show", account.ID)
	var result struct {
		Account paper.Account       `json:"account"`
		Ledger  []paper.LedgerEntry `json:"ledger"`
	}
	if err != nil || json.Unmarshal(data, &result) != nil || result.Account.ID != account.ID || len(result.Ledger) != 1 || result.Ledger[0].Amount.String() != "10000.125" {
		t.Fatalf("show: %s %v", data, err)
	}
	for _, args := range [][]string{
		{"paper", "account", "create", "--name", "default", "--request-id", "default-opening"},
	} {
		data, err = invoke(args...)
		if err != nil || json.Unmarshal(data, &account) != nil || account.Balance.String() != "10000" {
			t.Fatalf("default: %s %v", data, err)
		}
	}
	var out, logs bytes.Buffer
	if err := cli.Execute(context.Background(), []string{"paper", "accounts", "--config", path}, &out, &logs); err != nil || !strings.Contains(out.String(), "10000.125 USDT") {
		t.Fatalf("text: %s %v", &out, err)
	}
}

func TestPaperAccountCLIRejectsInvalidInputsOffline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.yaml")
	for _, args := range [][]string{
		{"paper", "account", "create", "--name", "demo"},
		{"paper", "account", "create", "--name", " ", "--balance", "1", "--request-id", "x"},
		{"paper", "account", "create", "--name", "demo", "--currency", "USD", "--balance", "1", "--request-id", "x"},
		{"paper", "account", "create", "--name", "demo", "--balance", "NaN", "--request-id", "x"},
		{"paper", "account", "create", "--name", "demo", "--balance", "0", "--request-id", "x"},
		{"paper", "account", "create", "--name", "demo", "--balance", "-1", "--request-id", "x"},
		{"paper", "account", "create", "--name", "demo", "--balance", "1000000000000000001", "--request-id", "x"},
		{"paper", "account", "create", "--name", "demo", "--balance", "0.0000000000000000001", "--request-id", "x"},
	} {
		var out, logs bytes.Buffer
		err := cli.Execute(context.Background(), append(args, "--config", path), &out, &logs)
		if err == nil || strings.Contains(err.Error(), "cannot open file") || strings.Contains(err.Error(), "unknown") {
			t.Fatalf("validation reached config for %v: %v", args, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("validation created config")
	}
}

func TestPaperBalanceConfigurationRejectsUnsupportedPrecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	for _, balance := range []string{"0.0000000000000000001", "1000000000000000001"} {
		data := strings.Replace(configs.Example, `initial_balance: "10000"`, `initial_balance: "`+balance+`"`, 1)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		var out, logs bytes.Buffer
		err := cli.Execute(context.Background(), []string{"config", "validate", "--config", path}, &out, &logs)
		if err == nil {
			t.Fatalf("unsupported balance %q accepted", balance)
		}
	}
}
