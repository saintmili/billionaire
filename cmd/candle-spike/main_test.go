package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommandReadsHistoricalCandleOffline(t *testing.T) {
	var out, diagnostics bytes.Buffer
	err := run([]string{"--fixture", "../../testdata/market/bybit-spot-btcusdt-1m.json"}, &out, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"provider": "bybit", "market": "spot", "symbol": "BTCUSDT", "timeframe": "1m",
		"open_time": "2022-12-09T16:00:00Z", "end_time": "2022-12-09T16:01:00Z",
		"open": "17000.10", "high": "17010.20", "low": "16990.30", "close": "17005.40", "base_volume": "1.25000000", "closed": true,
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s: got %v, want %v", key, got[key], value)
		}
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("unexpected diagnostics: %s", &diagnostics)
	}
}

func TestCommandRejectsInvalidProviderResponses(t *testing.T) {
	valid, err := os.ReadFile("../../testdata/market/bybit-spot-btcusdt-1m.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, body string }{
		{"provider error", `{"retCode":10001,"retMsg":"invalid request"}`},
		{"missing result", `{"retCode":0}`},
		{"missing code", strings.Replace(string(valid), `"retCode": 0,`, "", 1)},
		{"short row", strings.Replace(string(valid), `"17000.10", `, "", 1)},
		{"invalid decimal", strings.Replace(string(valid), `"17000.10"`, `"NaN"`, 1)},
		{"negative volume", strings.Replace(string(valid), `"1.25000000"`, `"-1"`, 1)},
		{"wrong market", strings.Replace(string(valid), `"spot"`, `"linear"`, 1)},
		{"wrong symbol", strings.Replace(string(valid), `"BTCUSDT"`, `"ETHUSDT"`, 1)},
		{"wrong candle", strings.Replace(string(valid), `"1670601600000"`, `"1670601660000"`, 1)},
		{"partial candle", strings.Replace(string(valid), `"time": 1670601660000`, `"time": 1670601659999`, 1)},
		{"trailing document", string(valid) + `{}`},
		{"invalid JSON", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("command panicked: %v", p)
				}
			}()
			path := filepath.Join(t.TempDir(), "response.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			if err := run([]string{"--fixture", path}, &out, &diagnostics); err == nil {
				t.Fatal("expected response rejection")
			}
			if out.Len() != 0 {
				t.Fatalf("failed command emitted a candle: %s", &out)
			}
		})
	}
}

func TestCommandFetchesPublicCandle(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/market/bybit-spot-btcusdt-1m.json")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v5/market/kline" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		want := url.Values{"category": {"spot"}, "symbol": {"BTCUSDT"}, "interval": {"1"}, "start": {"1670601600000"}, "end": {"1670601659999"}, "limit": {"1"}}
		if !reflect.DeepEqual(r.URL.Query(), want) {
			t.Errorf("query: %v", r.URL.Query())
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-BAPI-API-KEY") != "" {
			t.Error("public request must not authenticate")
		}
		w.Write(fixture)
	}))
	defer server.Close()
	var out, diagnostics bytes.Buffer
	if err := run([]string{"--base-url", server.URL}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"close":"17005.40"`)) {
		t.Fatalf("unexpected output: %s", &out)
	}
}

func TestCommandReportsHTTPFailures(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/unexpected-redirect")
				w.WriteHeader(status)
				io.WriteString(w, "provider diagnostic body")
			}))
			defer server.Close()
			var out, diagnostics bytes.Buffer
			err := run([]string{"--base-url", server.URL}, &out, &diagnostics)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("unexpected error: %v", err)
			}
			if out.Len() != 0 {
				t.Fatalf("failed command emitted a candle: %s", &out)
			}
		})
	}
}
