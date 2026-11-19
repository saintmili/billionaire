package postgres_test

import (
	"bytes"
	"context"
	"github.com/saintmili/billionaire/internal/notify"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type telegramFixture struct{ url string }

func (f telegramFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme = "http"
	r.URL.Host = strings.TrimPrefix(f.url, "http://")
	return http.DefaultTransport.RoundTrip(r)
}
func TestTelegramDurableRateLimitRestartAndExhaustion(t *testing.T) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		w.Write([]byte(`{"ok":false,"description":"sensitive-token-chat","parameters":{"retry_after":7200}}`))
	}))
	defer server.Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := notify.Event{ID: "telegram-one", Kind: "engine_failure", At: at}
	for i := 0; i < 2; i++ {
		if err = s.RecordNotification(ctx, e, notify.Policy{Targets: []string{"telegram", "terminal"}}); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	senders := map[string]notify.Sender{"telegram": notify.NewTelegram("123:fixture", "-123456", telegramFixture{server.URL}), "terminal": notify.Terminal{Writer: &out}}
	if _, err = notify.Dispatch(ctx, s, senders, at, 10); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Notifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	for _, r := range rows {
		if r.Target == "telegram" && (r.State != "pending" || r.NextAttemptAt.Before(at.Add(2*time.Hour)) || r.Error != "telegram_rate_limited") {
			t.Fatalf("rate limit %+v", r)
		}
	}
	s.Close()
	s, err = postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := notify.Dispatch(ctx, s, senders, at.Add(time.Hour), 10); err != nil || n != 0 {
		t.Fatalf("early retry %d %v", n, err)
	}
	for i := 1; i <= 4; i++ {
		if _, err = notify.Dispatch(ctx, s, senders, at.Add(time.Duration(i)*3*time.Hour), 10); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = s.Notifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Target == "telegram" && (r.State != "failed" || r.Attempts != 5 || r.Error != "telegram_rate_limited") {
			t.Fatalf("exhaustion %+v", r)
		}
		if r.Target == "terminal" && (r.State != "delivered" || r.Attempts != 1) {
			t.Fatalf("terminal %+v", r)
		}
	}
}
