package config_test

import (
	"strings"
	"testing"

	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/config"
)

func TestLoadPaperConfigWithEnvironmentOverrides(t *testing.T) {
	env := map[string]string{"BILLIONAIRE_LOG_LEVEL": "debug", "BILLIONAIRE_INITIAL_BALANCE": "12345.67"}
	cfg, err := config.Load(strings.NewReader(configs.Example), func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.Mode != "paper" || cfg.App.LogLevel != "debug" || cfg.Paper.InitialBalance != "12345.67" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if len(cfg.MarketData.Instruments) != 2 {
		t.Fatal("missing configured instruments")
	}
}

func TestLoadRejectsUnsafeOrInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, from, to, field string }{
		{"live mode", "mode: paper", "mode: live", "app.mode"},
		{"timezone", "timezone: UTC", "timezone: local", "app.timezone"},
		{"log level", "log_level: info", "log_level: noisy", "app.log_level"},
		{"schema", "schema_version: 1", "schema_version: 2", "schema_version"},
		{"storage", "driver: postgres", "driver: sqlite", "storage.driver"},
		{"timeout", "timeout: 5s", "timeout: -1s", "storage.timeout"},
		{"DSN literal", "dsn_env: BILLIONAIRE_DATABASE_URL", "dsn_env: postgres://secret@host/db", "storage.dsn_env"},
		{"provider", "provider: bybit", "provider: unknown", "market_data.provider"},
		{"URL credentials", "https://api.bybit.com", "https://secret@api.bybit.com", "market_data.rest_url"},
		{"stale duration", "stale_after: 60s", "stale_after: 0s", "market_data.stale_after"},
		{"market", "market: spot", "market: inverse", "market_data.instruments"},
		{"symbol", "BTC/USDT", "BTC/USD", "market_data.instruments"},
		{"timeframe", "[1m, 15m]", "[17s]", "market_data.instruments"},
		{"balance", `initial_balance: "10000"`, `initial_balance: "NaN"`, "paper.initial_balance"},
		{"quote", "quote_currency: USDT", "quote_currency: USD", "paper.quote_currency"},
		{"unknown key", "mode: paper", "mode: paper\n  api_key: secret", "config"},
		{"duplicate key", "mode: paper", "mode: paper\n  mode: live", "config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Replace(configs.Example, tc.from, tc.to, 1)
			_, err := config.Load(strings.NewReader(text), func(string) string { return "" })
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("expected %s error, got %v", tc.field, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error disclosed input: %v", err)
			}
		})
	}
}

func TestLoadRejectsPrivateSocketAndDuplicateInstrument(t *testing.T) {
	for _, text := range []string{
		strings.Replace(configs.Example, "/v5/public/spot", "/v5/private", 1),
		strings.Replace(configs.Example, "market: linear\n      symbol: ETH/USDT", "market: spot\n      symbol: BTC/USDT", 1),
		configs.Example + "\n---\napp: {}\n",
	} {
		if _, err := config.Load(strings.NewReader(text), func(string) string { return "" }); err == nil {
			t.Fatal("unsafe or ambiguous config accepted")
		}
	}
	if _, err := config.Load(strings.NewReader(configs.Example), func(k string) string {
		if k == "BILLIONAIRE_MODE" {
			return "live"
		}
		return ""
	}); err == nil {
		t.Fatal("live mode override accepted")
	}
}

func TestDecodeErrorsIdentifyFieldsWithoutEchoingValues(t *testing.T) {
	for _, tc := range []struct{ from, to, field string }{
		{"schema_version: 1", "schema_version: []", "schema_version"},
		{"mode: paper", "mode: paper\n  api_key: sensitive-value", "app.api_key"},
		{"mode: paper", "mode: paper\n  mode: sensitive-value", "app.mode"},
		{"instruments:", "instruments: sensitive-value\n  unused:", "market_data.instruments"},
	} {
		_, err := config.Load(strings.NewReader(strings.Replace(configs.Example, tc.from, tc.to, 1)), func(string) string { return "" })
		if err == nil || !strings.Contains(err.Error(), tc.field) || !strings.Contains(err.Error(), "line ") {
			t.Errorf("expected field/line for %s, got %v", tc.field, err)
		}
		if err != nil && strings.Contains(err.Error(), "sensitive-value") {
			t.Fatal("submitted value disclosed")
		}
	}
}

func TestTelegramReferencesAreValidatedWithoutResolvingCredentials(t *testing.T) {
	text := strings.ReplaceAll(strings.ReplaceAll(configs.Example, "BILLIONAIRE_TELEGRAM_BOT_TOKEN", "TEST_TELEGRAM_TOKEN"), "BILLIONAIRE_TELEGRAM_CHAT_ID", "TEST_TELEGRAM_CHAT")
	cfg, err := config.Load(strings.NewReader(text), func(k string) string {
		if strings.HasPrefix(k, "TEST_TELEGRAM") {
			t.Fatal("config resolved Telegram credential")
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = cfg
	for _, field := range []string{"TEST_TELEGRAM_TOKEN", "TEST_TELEGRAM_CHAT"} {
		_, err = config.Load(strings.NewReader(strings.Replace(text, field, "secret://literal", 1)), func(string) string { return "" })
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid reference: %v", err)
		}
	}
}
