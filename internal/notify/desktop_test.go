package notify_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/notify"
)

func TestDesktopDeliversLiteralEscapedEvent(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "arguments")
	t.Setenv("PATH", dir)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=fixture")
	t.Setenv("DESKTOP_ARGUMENTS", output)
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DESKTOP_ARGUMENTS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	event := notify.Event{ID: "event-1", Kind: "engine_failure", Reason: "--urgency=critical $(touch /tmp/never-create) <b>literal</b>"}
	if err := (notify.Desktop{}).Send(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.HasPrefix(got, "--app-name=Billionaire\n--\nBillionaire: engine_failure\n") || !strings.Contains(got, "$(touch /tmp/never-create)") || strings.Contains(got, "<b>") {
		t.Fatalf("unsafe or incomplete argv: %q", got)
	}
}

func TestDesktopClassifiesFacilityAndUnsafeInputFailures(t *testing.T) {
	for _, tc := range []struct{ name, script, session, reason, category string }{
		{name: "missing binary", session: "fixture", category: "desktop_missing_binary"},
		{name: "headless", script: "exit 0", category: "desktop_session_unavailable"},
		{name: "exit output is discarded", script: "echo secret-session-address >&2; exit 2", session: "fixture", category: "desktop_failed"},
		{name: "bounded execution", script: "exec /bin/sleep 20", session: "fixture", category: "desktop_timeout"},
		{name: "nul rejected", script: "exit 0", session: "fixture", reason: "unsafe\x00argument", category: "desktop_invalid_event"},
		{name: "controls rejected", script: "exit 0", session: "fixture", reason: "unsafe\x1b[31m", category: "desktop_invalid_event"},
		{name: "oversized rejected", script: "exit 0", session: "fixture", reason: strings.Repeat("x", 4097), category: "desktop_invalid_event"},
		{name: "invalid utf8 rejected", script: "exit 0", session: "fixture", reason: string([]byte{0xff}), category: "desktop_invalid_event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			before := time.Now()
			err := (notify.Desktop{}).Send(context.Background(), notify.Event{ID: "fixture", Kind: "engine_failure", Reason: tc.reason})
			var classified *notify.DeliveryError
			if !errors.As(err, &classified) || classified.Category != tc.category {
				t.Fatalf("failure %v, wanted %s", err, tc.category)
			}
			if time.Since(before) > 3*time.Second {
				t.Fatal("desktop execution was not bounded")
			}
		})
	}
}
