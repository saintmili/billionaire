package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/testdb"
	"path/filepath"
	"testing"
)

func TestRiskCLIControlsPersistAndStatusShowsSwitch(t *testing.T) {
	t.Setenv("BILLIONAIRE_DATABASE_URL", testdb.New(t))
	path := filepath.Join(t.TempDir(), "config.yaml")
	invoke := func(args ...string) ([]byte, error) {
		var out, logs bytes.Buffer
		err := cli.Execute(context.Background(), append(args, "--config", path, "--output", "json"), &out, &logs)
		return out.Bytes(), err
	}
	if _, err := invoke("init"); err != nil {
		t.Fatal(err)
	}
	data, err := invoke("paper", "account", "create", "--name", "risk", "--request-id", "risk")
	var account paper.Account
	if err != nil || json.Unmarshal(data, &account) != nil {
		t.Fatalf("%s %v", data, err)
	}
	if _, err := invoke("paper", "risk", "set", "--account", account.ID, "--fixed-risk", "10", "--max-positions", "2"); err != nil {
		t.Fatal(err)
	}
	data, err = invoke("paper", "risk", "show", "--account", account.ID)
	var p paper.RiskPolicy
	if err != nil || json.Unmarshal(data, &p) != nil || p.FixedRisk != "10" || p.MaxPositions != 2 {
		t.Fatalf("%s %v", data, err)
	}
	if _, err := invoke("kill-switch", "on"); err != nil {
		t.Fatal(err)
	}
	data, err = invoke("status")
	var status struct {
		KillSwitch bool `json:"kill_switch"`
	}
	if err != nil || json.Unmarshal(data, &status) != nil || !status.KillSwitch {
		t.Fatalf("%s %v", data, err)
	}
	if _, err := invoke("kill-switch", "off"); err != nil {
		t.Fatal(err)
	}
	data, err = invoke("kill-switch", "status")
	if err != nil || json.Unmarshal(data, &status) != nil || status.KillSwitch {
		t.Fatalf("%s %v", data, err)
	}
}
