package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/saintmili/billionaire/internal/notify"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fixtureTransport struct{ server *httptest.Server }

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" || r.URL.Path != "/bot123:fixture/sendMessage" {
		panic("unexpected Telegram origin")
	}
	r = r.Clone(r.Context())
	r.URL.Scheme = "http"
	r.URL.Host = strings.TrimPrefix(f.server.URL, "http://")
	return http.DefaultTransport.RoundTrip(r)
}
func TestTelegramSendsBoundedPlaintextToFixedOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		text, _ := body["text"].(string)
		if r.Method != "POST" || body["chat_id"] != "-123456" || len(text) == 0 || len(text) > 4096 || body["parse_mode"] != nil || !strings.Contains(text, "engine_failure") {
			t.Errorf("invalid message: %v", body)
		}
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()
	sender := notify.NewTelegram("123:fixture", "-123456", fixtureTransport{server})
	if err := sender.Send(context.Background(), notify.Event{ID: "one", Kind: "engine_failure", Reason: strings.Repeat("🙂", 5000)}); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramFailuresAreClassifiedWithoutSensitiveData(t *testing.T) {
	for _, tc := range []struct {
		name           string
		status         int
		body, category string
		permanent      bool
		delay          time.Duration
	}{
		{"rate limit", 429, `{"ok":false,"description":"TOKEN -123456","parameters":{"retry_after":7200}}`, "telegram_rate_limited", false, 2 * time.Hour},
		{"overflow", 429, `{"parameters":{"retry_after":999999999999999999999999999}}`, "telegram_rate_limited", false, time.Duration((1<<63-1)/int64(time.Second)) * time.Second},
		{"blocked", 403, `{"ok":false,"description":"TOKEN -123456"}`, "telegram_rejected", true, 0},
		{"unauthorized", 401, `{"ok":false}`, "telegram_rejected", true, 0},
		{"server", 503, `TOKEN -123456`, "telegram_server", false, 0},
		{"malformed", 200, `TOKEN -123456`, "telegram_response", false, 0},
		{"oversized", 200, strings.Repeat("TOKEN", 20000), "telegram_response", false, 0},
		{"redirect", 307, `TOKEN -123456`, "telegram_redirect", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "http://invalid.test/TOKEN")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			err := notify.NewTelegram("123:fixture", "-123456", fixtureTransport{server}).Send(context.Background(), notify.Event{})
			var classified *notify.DeliveryError
			if !errors.As(err, &classified) || classified.Category != tc.category || classified.Permanent != tc.permanent || classified.RetryAfter != tc.delay || err.Error() != tc.category {
				t.Fatalf("failure: %+v", err)
			}
		})
	}
}
func TestTelegramTimeoutAndAbsentCredentialsStaySafe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := notify.NewTelegram("123:fixture", "-123456", fixtureTransport{server}).Send(ctx, notify.Event{})
	if err == nil || err.Error() != "telegram_transport" {
		t.Fatal(err)
	}
	err = notify.NewTelegram("", "", nil).Send(context.Background(), notify.Event{})
	if err == nil || err.Error() != "telegram_unavailable" {
		t.Fatal(err)
	}
}
