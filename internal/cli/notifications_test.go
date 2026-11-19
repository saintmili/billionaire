package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/cli"
	"github.com/saintmili/billionaire/internal/notify"
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

func TestTelegramAbsentCredentialsDoNotBlockTerminal(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	t.Setenv("BILLIONAIRE_DATABASE_URL", dsn)
	// Empty credentials guarantee the adapter returns before constructing any request.
	t.Setenv("BILLIONAIRE_TELEGRAM_BOT_TOKEN", "")
	t.Setenv("BILLIONAIRE_TELEGRAM_CHAT_ID", "")
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute)
	if err = s.RecordNotification(ctx, notify.Event{ID: "optional", Kind: "engine_failure", At: at}, notify.Policy{Targets: []string{"terminal", "telegram"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	text := strings.Replace(configs.Example, "targets: [terminal]", "targets: [terminal, telegram]", 1)
	if err = os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err = cli.Execute(ctx, []string{"notifications", "dispatch", "--config", path, "--output", "json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Notifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Target == "terminal" && r.State != "delivered" {
			t.Fatal(r)
		}
		if r.Target == "telegram" && (r.State != "failed" || r.Error != "telegram_unavailable") {
			t.Fatal(r)
		}
	}
}
func TestTelegramConfigValidationDoesNotExposeEnvironmentSecrets(t *testing.T) {
	// Offline validation never constructs a sender or sends a request.
	t.Setenv("BILLIONAIRE_TELEGRAM_BOT_TOKEN", "sensitive-token")
	t.Setenv("BILLIONAIRE_TELEGRAM_CHAT_ID", "sensitive-chat")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err := cli.Execute(context.Background(), []string{"config", "validate", "--config", path, "--output", "json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sensitive-token", "sensitive-chat"} {
		if strings.Contains(out.String()+diagnostics.String(), secret) {
			t.Fatal("CLI leaked credential")
		}
	}
}
