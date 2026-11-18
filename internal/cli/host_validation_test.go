package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/testdb"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saintmili/billionaire/internal/cli"
)

func TestHostValidationRequiresExplicitNetworkOptInAndBounds(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "--allow-network"},
		{[]string{"--allow-network", "--duration", "0s"}, "duration"},
		{[]string{"--allow-network", "--duration", "31m"}, "duration"},
		{[]string{"--allow-network", "--host-role", "unknown"}, "host-role"},
	} {
		var out, logs bytes.Buffer
		err := cli.Execute(context.Background(), append([]string{"market", "validate-host"}, tc.args...), &out, &logs)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: %v", tc.args, err)
		}
	}
}

func TestHostValidationRecordsEveryMarketAndSanitizesProviderFailure(t *testing.T) {
	dsn := testdb.New(t)
	t.Setenv("BILLIONAIRE_DATABASE_URL", dsn)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-BAPI-API-KEY") != "" {
			t.Error("unexpected authentication")
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "private-provider-response-marker")
	}))
	defer server.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	t.Setenv("BILLIONAIRE_REST_URL", server.URL)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(configs.Example), 0600); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	if err := cli.Execute(context.Background(), []string{"init", "--config", path}, &out, &logs); err != nil {
		t.Fatal(err)
	}
	for range 2 { // Restart repeats explicit evidence without turning a prior failure into acceptance.
		out.Reset()
		logs.Reset()
		err := cli.Execute(context.Background(), []string{"market", "validate-host", "--allow-network", "--duration", "10s", "--host-role", "intended-runtime", "--config", path, "--output", "json"}, &out, &logs)
		if err == nil {
			t.Fatal("blocked provider accepted")
		}
		var report struct {
			Passed bool   `json:"checks_passed"`
			Gate   string `json:"m2_actual_host_gate"`
			Live   bool   `json:"live_monitoring_enabled"`
			Checks []struct {
				Instrument struct {
					Market string `json:"market"`
					Base   string `json:"base"`
				} `json:"instrument"`
				Timeframe string `json:"timeframe"`
				Stage     string `json:"stage"`
				Error     string `json:"error"`
			} `json:"checks"`
		}
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if report.Passed || report.Gate != "pending" || report.Live || len(report.Checks) != 8 {
			t.Fatalf("misleading report: %s", out.String())
		}
		identities := map[string]bool{}
		for _, check := range report.Checks {
			if check.Stage != "metadata" || check.Error != "public market request: HTTP 403" {
				t.Fatalf("missing failure evidence: %s", out.String())
			}
			identities[check.Instrument.Market+check.Instrument.Base+check.Timeframe] = true
		}
		if len(identities) != 8 {
			t.Fatalf("market/timeframe identities collapsed: %s", out.String())
		}
		if strings.Contains(out.String()+logs.String()+err.Error(), "private-provider-response-marker") {
			t.Fatal("raw provider body leaked")
		}
	}
}
