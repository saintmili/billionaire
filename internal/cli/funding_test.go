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
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestCLIFundingFixturePersistsMissingCoverage(t *testing.T) {
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
	at := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	event := paper.FundingEvent{ID: "missing", Instrument: domain.Instrument{Provider: "fixture", Market: domain.Linear, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}, EffectiveAt: at, AvailableAt: at, Source: "fixture", Provenance: "offline interval", MissingReason: "no public rate"}
	payload, _ := json.Marshal(event)
	fixture := filepath.Join(t.TempDir(), "funding.json")
	if err := os.WriteFile(fixture, payload, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		data, err := invoke("paper", "funding", "apply", "--fixture", fixture)
		var result paper.FundingResult
		if err != nil || json.Unmarshal(data, &result) != nil || result.Warning == "" {
			t.Fatalf("funding %s %v", data, err)
		}
	}
	data, err := invoke("paper", "funding", "list")
	var results []paper.FundingResult
	if err != nil || json.Unmarshal(data, &results) != nil || len(results) != 1 || results[0].Event.MissingReason != "no public rate" {
		t.Fatalf("history %s %v", data, err)
	}
}
