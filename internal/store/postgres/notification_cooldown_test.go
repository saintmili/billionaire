package postgres_test

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/notify"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
)

type rateLimitedFixture struct{ calls atomic.Int32 }

func (f *rateLimitedFixture) Send(context.Context, notify.Event) error {
	f.calls.Add(1)
	time.Sleep(80 * time.Millisecond)
	return &notify.DeliveryError{Category: "telegram_rate_limited", RetryAfter: time.Hour}
}

func TestNotificationTargetCooldownDefersBatchAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for _, id := range []string{"one", "two", "three"} {
		if err = s.RecordNotification(ctx, notify.Event{ID: id, Kind: "engine_failure", At: at}, notify.Policy{Targets: []string{"telegram", "terminal"}}); err != nil {
			t.Fatal(err)
		}
	}
	fixture := &rateLimitedFixture{}
	var out bytes.Buffer
	if _, err = notify.Dispatch(ctx, s, map[string]notify.Sender{"telegram": fixture, "terminal": notify.Terminal{Writer: &out}}, at, 10); err != nil {
		t.Fatal(err)
	}
	if fixture.calls.Load() != 1 {
		t.Fatalf("rate limited target sent %d times", fixture.calls.Load())
	}
	rows, err := s.Notifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	for _, d := range rows {
		if d.Target == "telegram" {
			attempts += d.Attempts
			if d.NextAttemptAt.Before(at.Add(time.Hour + 70*time.Millisecond)) {
				t.Fatalf("cooldown predates response: %+v", d)
			}
		} else if d.State != "delivered" {
			t.Fatalf("unrelated channel blocked: %+v", d)
		}
	}
	if attempts != 1 {
		t.Fatalf("deferred deliveries consumed attempts: %d", attempts)
	}
	s.Close()
	s, err = postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := notify.Dispatch(ctx, s, map[string]notify.Sender{"telegram": fixture}, at.Add(time.Minute), 10); err != nil || n != 0 {
		t.Fatalf("restart bypassed cooldown: %d %v", n, err)
	}
}

func TestNotificationCooldownStopsOtherWorkersAndOutlivesExhaustion(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	record := func(id string) {
		t.Helper()
		if err := s.RecordNotification(ctx, notify.Event{ID: id, Kind: "engine_failure", At: at}, notify.Policy{Targets: []string{"telegram"}}); err != nil {
			t.Fatal(err)
		}
	}
	record("first")
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := notify.Dispatch(ctx, s, map[string]notify.Sender{"telegram": blockingTerminal{started, release}}, at, 1)
		done <- err
	}()
	<-started
	record("limited")
	fixture := &rateLimitedFixture{}
	for i := 0; i < 5; i++ {
		when := at.Add(time.Duration(i) * 2 * time.Hour)
		if _, err := notify.Dispatch(ctx, s, map[string]notify.Sender{"telegram": fixture}, when, 1); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	}
	// The older admitted request was already in flight and may finish after the 429.
	record("later")
	other, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if n, err := notify.Dispatch(ctx, other, map[string]notify.Sender{"telegram": fixture}, at.Add(8*time.Hour+time.Minute), 10); err != nil || n != 0 {
		t.Fatalf("other worker bypassed exhausted target cooldown: %d %v", n, err)
	}
	rows, err := other.Notifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rows {
		if d.Event.ID == "limited" && (d.State != "failed" || d.Attempts != 5) {
			t.Fatalf("exhaustion %+v", d)
		}
		if d.Event.ID == "later" && d.Attempts != 0 {
			t.Fatalf("unused quota %+v", d)
		}
	}
}
