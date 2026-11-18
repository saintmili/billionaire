package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotificationsKeepCommandJSONSeparateFromTerminalAlerts(t *testing.T) {
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
	if err = s.RecordEngineFailure(ctx, "fixture", time.Now().Add(-time.Minute), "bad\x1b[31m\nline"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err = cli.Execute(ctx, []string{"notifications", "dispatch", "--config", path, "--output", "json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Attempted int `json:"attempted"`
	}
	if err = json.Unmarshal(out.Bytes(), &result); err != nil || result.Attempted != 1 {
		t.Fatalf("command output %q %v", out.String(), err)
	}
	if !strings.Contains(diagnostics.String(), `\u001b`) || strings.ContainsRune(diagnostics.String(), '\x1b') {
		t.Fatalf("unsafe terminal output %q", diagnostics.String())
	}
	out.Reset()
	diagnostics.Reset()
	if err = cli.Execute(ctx, []string{"notifications", "status", "--config", path, "--output", "json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		State string `json:"state"`
	}
	if err = json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].State != "delivered" {
		t.Fatalf("status %q %v", out.String(), err)
	}
}
