package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestConfigValidateIsOfflineAndMachineReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "billionaire.yaml")
	if err := os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BILLIONAIRE_DATABASE_URL", "")
	var out, logs bytes.Buffer
	if err := executeOffline(context.Background(), []string{"config", "validate", "--config", path, "--output", "json"}, &out, &logs); err != nil {
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
	t.Setenv("BILLIONAIRE_DATABASE_URL", dsn)
	path := filepath.Join(t.TempDir(), "config", "billionaire.yaml")
	invoke := func(args ...string) (map[string]any, error) {
		t.Helper()
		var out, logs bytes.Buffer
		args = append(args, "--config", path, "--output", "json")
		err := executeOffline(context.Background(), args, &out, &logs)
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
	if err != nil || state["database"] != "ready" || state["engine"] != "stopped" {
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
	if err := executeOffline(ctx, []string{"init", "--config", path}, &out, &logs); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled command changed filesystem")
	}
	for _, args := range [][]string{{"unknown"}, {"config", "unknown"}, {"database", "unknown"}, {"config", "validate", "extra"}, {"status", "--output", "xml"}} {
		out.Reset()
		logs.Reset()
		if err := executeOffline(context.Background(), args, &out, &logs); err == nil {
			t.Errorf("accepted arguments %v", args)
		}
	}
}

func TestUnavailableStatusDoesNotDiscloseDatabaseSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "billionaire.yaml")
	if err := os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BILLIONAIRE_DATABASE_URL", "postgres://sensitive-marker:%not-a-url@host/db")
	var out, logs bytes.Buffer
	err := executeOffline(context.Background(), []string{"status", "--config", path, "--output", "json"}, &out, &logs)
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

func TestMarketBackfillWatchAndStatusUseOfflineFixture(t *testing.T) {
	dsn := testdb.New(t)
	t.Setenv("BILLIONAIRE_DATABASE_URL", dsn)
	path := filepath.Join(t.TempDir(), "billionaire.yaml")
	if err := os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	invoke := func(args ...string) error {
		out.Reset()
		logs.Reset()
		return executeOffline(context.Background(), append(args, "--config", path, "--output", "json"), &out, &logs)
	}
	if err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	fixturePath := "../../testdata/market/m2.json"
	for _, market := range []string{"spot", "linear"} {
		for _, symbol := range []string{"BTC/USDT", "ETH/USDT"} {
			for _, frame := range []string{"1m", "15m"} {
				if err := invoke("market", "backfill", "--market", market, "--symbol", symbol, "--timeframe", frame, "--from", "2026-01-01T00:00:00Z", "--to", "2026-01-01T01:00:00Z", "--fixture", fixturePath); err != nil {
					t.Fatal(err)
				}
				var result map[string]any
				if json.Unmarshal(out.Bytes(), &result) != nil || result["missing"] != float64(0) {
					t.Fatalf("invalid backfill result: %s", &out)
				}
			}
		}
	}
	if err := invoke("market", "watch", "--fixture", fixturePath, "--lookback", "20"); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&out)
	events := 0
	for {
		var event map[string]any
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event["kind"] == "candle" {
			events++
		}
	}
	if events != 2 {
		t.Fatalf("watch emitted duplicate closed candles: %d", events)
	}
	if err := invoke("market", "status", "--fixture", fixturePath); err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if json.Unmarshal(out.Bytes(), &state) != nil || state["allows_auto_entries"] != false {
		t.Fatalf("fixture completion claimed live health: %s", &out)
	}
}

func TestStrategyCLIRegistersEnablesEvaluatesAndInspectsSignals(t *testing.T) {
	t.Setenv("BILLIONAIRE_DATABASE_URL", testdb.New(t))
	path := filepath.Join(t.TempDir(), "billionaire.yaml")
	if err := os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	invoke := func(args ...string) error {
		out.Reset()
		logs.Reset()
		return executeOffline(context.Background(), append(args, "--config", path, "--output", "json"), &out, &logs)
	}
	if err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("strategy", "validate", "../../examples/strategies/breakout.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("paper", "account", "create", "--name", "demo", "--request-id", "demo"); err != nil {
		t.Fatal(err)
	}
	var account struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(out.Bytes(), &account) != nil {
		t.Fatal("account output")
	}
	if err := invoke("strategy", "add", "../../examples/strategies/breakout.yaml", "--instance", "cli-breakout", "--provider", "fixture", "--market", "spot", "--symbol", "BTC/USDT", "--account", account.ID); err != nil {
		t.Fatal(err)
	}
	if err := invoke("strategy", "enable", "cli-breakout"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("market", "watch", "--fixture", "../../testdata/market/m3.json"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("signals", "list", "--instance", "cli-breakout"); err != nil {
		t.Fatal(err)
	}
	var signals []map[string]any
	if json.Unmarshal(out.Bytes(), &signals) != nil || len(signals) != 1 || signals[0]["kind"] != "entry" {
		t.Fatalf("signal pipeline: %s", &out)
	}
	if err := invoke("strategy", "disable", "cli-breakout"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("strategy", "list"); err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if json.Unmarshal(out.Bytes(), &state) != nil {
		t.Fatal("invalid strategy list JSON")
	}
}

func TestStrategyEvaluateCommandIsOfflineAndIdempotent(t *testing.T) {
	t.Setenv("BILLIONAIRE_DATABASE_URL", testdb.New(t))
	directory := t.TempDir()
	path := filepath.Join(directory, "billionaire.yaml")
	if err := os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	invoke := func(args ...string) error {
		out.Reset()
		logs.Reset()
		return executeOffline(context.Background(), append(args, "--config", path, "--output", "json"), &out, &logs)
	}
	if err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	if err := invoke("paper", "account", "create", "--name", "demo", "--request-id", "demo"); err != nil {
		t.Fatal(err)
	}
	var account struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(out.Bytes(), &account) != nil {
		t.Fatal("account output")
	}
	add := []string{"strategy", "add", "../../examples/strategies/breakout.yaml", "--instance", "offline-demo", "--provider", "fixture", "--account", account.ID}
	if err := invoke(add...); err != nil {
		t.Fatal(err)
	}
	if err := invoke("strategy", "enable", "offline-demo"); err != nil {
		t.Fatal(err)
	}
	if err := invoke(add...); err != nil {
		t.Fatal(err)
	}
	var registered struct {
		Instance struct {
			Enabled bool `json:"enabled"`
		} `json:"instance"`
	}
	if json.Unmarshal(out.Bytes(), &registered) != nil || !registered.Instance.Enabled {
		t.Fatal("idempotent add misreported enabled state")
	}
	if err := invoke("market", "backfill", "--fixture", "../../testdata/market/m3.json", "--from", "2026-01-01T00:00:00Z", "--to", "2026-01-01T00:22:00Z"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../testdata/market/m3.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset struct {
		Candles []json.RawMessage `json:"candles"`
	}
	if json.Unmarshal(raw, &dataset) != nil {
		t.Fatal("invalid dataset")
	}
	eventPath := filepath.Join(directory, "event.json")
	if err := os.WriteFile(eventPath, dataset.Candles[20], 0600); err != nil {
		t.Fatal(err)
	}
	if err := invoke("strategy", "evaluate", "offline-demo", "--event", eventPath); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Signals []map[string]any `json:"signals"`
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result.Signals) != 1 || result.Signals[0]["source_ready"] != false {
		t.Fatalf("offline evaluation result: %s", &out)
	}
	if err := invoke("strategy", "evaluate", "offline-demo", "--event", eventPath); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result.Signals) != 0 {
		t.Fatal("replayed event emitted twice")
	}
	invalid := strings.Replace(string(dataset.Candles[20]), `"close":"11"`, `"close":"1e1000000000"`, 1)
	if err := os.WriteFile(eventPath, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := invoke("strategy", "evaluate", "offline-demo", "--event", eventPath); err == nil {
		t.Fatal("unbounded exponent accepted")
	}
}
