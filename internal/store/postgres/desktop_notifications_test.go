package postgres_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/notify"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestDesktopFailuresAreDurableAndRetriesBounded(t *testing.T) {
	for _, tc := range []struct {
		name, script, session, category string
		attempts                        int
	}{
		{"missing binary", "", "fixture", "desktop_missing_binary", 1},
		{"headless", "exit 0", "", "desktop_session_unavailable", 1},
		{"nonzero", "echo secret-session-output >&2; exit 2", "fixture", "desktop_failed", 5},
		{"timeout", "exec /bin/sleep 20", "fixture", "desktop_timeout", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dsn := testdb.New(t)
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", tc.session)
			t.Setenv("DISPLAY", "")
			t.Setenv("WAYLAND_DISPLAY", "")
			if tc.script != "" {
				if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			s, err := postgres.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Migrate(ctx, postgres.Up); err != nil {
				s.Close()
				t.Fatal(err)
			}
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			if err = s.RecordNotification(ctx, notify.Event{ID: "desktop-fixture", Kind: "engine_failure", At: at, Reason: "fixture"}, notify.Policy{Targets: []string{"desktop", "terminal"}}); err != nil {
				s.Close()
				t.Fatal(err)
			}
			senders := map[string]notify.Sender{"desktop": notify.Desktop{}, "terminal": notify.Terminal{Writer: io.Discard}}
			for attempt := 1; attempt <= tc.attempts; attempt++ {
				if _, err = notify.Dispatch(ctx, s, senders, at.Add(time.Duration(attempt)*time.Hour), 10); err != nil {
					s.Close()
					t.Fatal(err)
				}
				rows, e := s.Notifications(ctx, 10)
				if e != nil {
					s.Close()
					t.Fatal(e)
				}
				for _, row := range rows {
					if row.Target == "terminal" {
						if row.State != "delivered" {
							t.Errorf("desktop failure prevented terminal delivery: %+v", row)
						}
						continue
					}
					want := "pending"
					if attempt == tc.attempts {
						want = "failed"
					}
					if row.State != want || row.Attempts != attempt || row.Error != tc.category {
						t.Errorf("durable desktop failure %+v", row)
					}
				}
				// Restart the application-facing store between claims: pending failures survive.
				s.Close()
				s, err = postgres.Open(ctx, dsn)
				if err != nil {
					t.Fatal(err)
				}
			}
			defer s.Close()
			if count, e := notify.Dispatch(ctx, s, senders, at.Add(24*time.Hour), 10); e != nil || count != 0 {
				t.Fatalf("exhausted delivery retried: %d %v", count, e)
			}
		})
	}
}
