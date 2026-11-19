package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/testdb"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartDocumentsOfflineGateAndRejectsLiveBeforeDatabase(t *testing.T) {
	var out, logs bytes.Buffer
	if err := executeOffline(context.Background(), []string{"start", "--help"}, &out, &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--fixture") || !strings.Contains(out.String(), "--live") {
		t.Fatalf("help missing fixture mode: %s", &out)
	}
}

func TestStartConfiguredFixtureSubscriptionsThroughCLI(t *testing.T) {
	t.Setenv("BILLIONAIRE_DATABASE_URL", testdb.New(t))
	path := filepath.Join(t.TempDir(), "engine.yaml")
	cfg := configs.Example + "\nengine:\n  queue_capacity: 1\n  lookback: 1\n"
	if err := os.WriteFile(path, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	if err := executeOffline(context.Background(), []string{"init", "--config", path}, &out, &logs); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	logs.Reset()
	if err := executeOffline(context.Background(), []string{"start", "--config", path, "--output", "json"}, &out, &logs); err == nil || !strings.Contains(err.Error(), "select exactly one") {
		t.Fatalf("live gate: %v", err)
	}
	out.Reset()
	logs.Reset()
	if err := executeOffline(context.Background(), []string{"start", "--config", path, "--fixture", "../../testdata/market/m2.json", "--output", "json"}, &out, &logs); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&out)
	seen := map[string]bool{}
	stopped := false
	for {
		var result map[string]any
		err := decoder.Decode(&result)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if result["engine"] == "stopped" {
			stopped = true
		}
		if event, ok := result["event"].(map[string]any); ok {
			feed := event["feed"].(map[string]any)
			instrument := feed["instrument"].(map[string]any)
			seen[instrument["instrument_id"].(string)+feed["timeframe"].(string)] = true
		}
	}
	if !stopped || len(seen) != 4 {
		t.Fatalf("engine did not complete configured feeds: stopped=%v feeds=%v", stopped, seen)
	}
}
