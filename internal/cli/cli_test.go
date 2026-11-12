package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestConfigValidateIsOfflineAndMachineReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "millionaire.yaml")
	if err := os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MILLIONAIRE_DATABASE_URL", "")
	var out, logs bytes.Buffer
	if err := cli.Execute(context.Background(), []string{"config", "validate", "--config", path, "--output", "json"}, &out, &logs); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Valid bool   `json:"valid"`
		Mode  string `json:"mode"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %s", &out)
	}
	if !result.Valid || result.Mode != "paper" {
		t.Fatalf("invalid result: %+v", result)
	}
	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("logs are not structured: %s", &logs)
	}
}

func TestInitAndStatusPersistAcrossCommandInvocations(t *testing.T) {
	dsn := testdb.New(t)
	t.Setenv("MILLIONAIRE_DATABASE_URL", dsn)
	path := filepath.Join(t.TempDir(), "config", "millionaire.yaml")
	invoke := func(args ...string) (map[string]any, error) {
		t.Helper()
		var out, logs bytes.Buffer
		args = append(args, "--config", path, "--output", "json")
		err := cli.Execute(context.Background(), args, &out, &logs)
		var result map[string]any
		if out.Len() > 0 {
			if e := json.Unmarshal(out.Bytes(), &result); e != nil {
				t.Fatalf("invalid JSON output: %s", &out)
			}
		}
		return result, err
	}
	initial, err := invoke("init")
	if err != nil || initial["initialized"] != true {
		t.Fatalf("init: %v %v", initial, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe config permissions: %v %v", info, err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.Replace(string(original), `initial_balance: "10000"`, `initial_balance: "42"`, 1)
	if err := os.WriteFile(path, []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != custom {
		t.Fatal("init overwrote an existing config")
	}
	state, err := invoke("status")
	if err != nil || state["database"] != "ready" || state["engine"] != "not_implemented" {
		t.Fatalf("status: %v %v", state, err)
	}
	if _, err := invoke("database", "migrate", "down"); err != nil {
		t.Fatal(err)
	}
	state, err = invoke("status")
	if err == nil || state["database"] != "uninitialized" {
		t.Fatalf("down status: %v %v", state, err)
	}
	if _, err := invoke("database", "migrate", "up"); err != nil {
		t.Fatal(err)
	}
}

func TestCommandCancellationAndInvalidArguments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "new.yaml")
	var out, logs bytes.Buffer
	if err := cli.Execute(ctx, []string{"init", "--config", path}, &out, &logs); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled command changed filesystem")
	}
	for _, args := range [][]string{{"unknown"}, {"config", "unknown"}, {"database", "unknown"}, {"config", "validate", "extra"}, {"status", "--output", "xml"}} {
		out.Reset()
		logs.Reset()
		if err := cli.Execute(context.Background(), args, &out, &logs); err == nil {
			t.Errorf("accepted arguments %v", args)
		}
	}
}

func TestUnavailableStatusDoesNotDiscloseDatabaseSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "millionaire.yaml")
	if err := os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MILLIONAIRE_DATABASE_URL", "postgres://sensitive-marker:%not-a-url@host/db")
	var out, logs bytes.Buffer
	err := cli.Execute(context.Background(), []string{"status", "--config", path, "--output", "json"}, &out, &logs)
	if err == nil {
		t.Fatal("unavailable database reported healthy")
	}
	if strings.Contains(err.Error()+logs.String()+out.String(), "sensitive-marker") {
		t.Fatal("secret disclosed")
	}
	var state map[string]any
	if err := json.Unmarshal(out.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state["database"] != "unavailable" || state["initialized"] != false {
		t.Fatalf("misleading status: %v", state)
	}
}
