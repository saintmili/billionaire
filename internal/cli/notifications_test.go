package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestDesktopNotificationsUseCLIAndDurableStatus(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	t.Setenv("BILLIONAIRE_DATABASE_URL", dsn)
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=fixture")
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	event := notify.Event{ID: "desktop-success", Kind: "engine_failure", At: time.Now().Add(-time.Minute), Reason: "fixture"}
	policy := notify.Policy{Targets: []string{"desktop"}, Key: "fixture", Cooldown: time.Minute}
	for _, id := range []string{"desktop-success", "desktop-success", "desktop-suppressed"} {
		event.ID = id
		if err = s.RecordNotification(ctx, event, policy); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(path, []byte(strings.Replace(configs.Example, "targets: [terminal]", "targets: [desktop]", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err = cli.Execute(ctx, []string{"notifications", "dispatch", "--config", path, "--output", "json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"attempted":1`) && !strings.Contains(out.String(), `"attempted": 1`) {
		t.Fatalf("dispatch %q", out.String())
	}
	out.Reset()
	if err = cli.Execute(ctx, []string{"notifications", "status", "--config", path, "--output", "json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var rows []notify.Delivery
	if err = json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.State]++
	}
	if len(rows) != 2 || counts["delivered"] != 1 || counts["suppressed"] != 1 {
		t.Fatalf("status %q stderr %q", out.String(), diagnostics.String())
	}
	// A headless deployment remains operational and exposes the safe failure category.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	event.ID = "headless"
	event.At = time.Now().Add(-time.Second)
	if err = s.RecordNotification(ctx, event, notify.Policy{Targets: []string{"desktop"}}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = cli.Execute(ctx, []string{"notifications", "dispatch", "--config", path, "--output", "json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err = cli.Execute(ctx, []string{"notifications", "status", "--config", path, "--output", "json"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `desktop_session_unavailable`) {
		t.Fatalf("headless status %q", out.String())
	}
}

func TestNotificationStatusPaginatesBeyondThousandHistoricalDeliveries(t *testing.T) {
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
	at := time.Now().Add(-time.Hour)
	for i := 0; i < 1001; i++ {
		policy := notify.Policy{Targets: []string{"desktop"}, Key: "history", Cooldown: 24 * time.Hour}
		if i == 1000 {
			policy = notify.Policy{Targets: []string{"desktop"}}
		}
		if err = s.RecordNotification(ctx, notify.Event{ID: fmt.Sprintf("history-%04d", i), Kind: "engine_failure", At: at.Add(time.Duration(i) * time.Millisecond)}, policy); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = notify.Dispatch(ctx, s, nil, time.Now(), 100); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err = cli.Execute(ctx, []string{"notifications", "status", "--config", path, "--output", "json", "--offset", "1000", "--limit", "1"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var rows []notify.Delivery
	if err = json.Unmarshal(out.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0].Event.ID != "history-1000" || rows[0].State != "failed" {
		t.Fatalf("later failure inaccessible: %s %v", out.String(), err)
	}
}
