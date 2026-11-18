// Package config loads schema-versioned, paper-only application configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/yamlutil"
)

type Config struct {
	Notifications struct {
		Targets  []string `yaml:"targets"`
		Cooldown string   `yaml:"cooldown"`
	} `yaml:"notifications"`
	Engine struct {
		QueueCapacity int    `yaml:"queue_capacity"`
		Lookback      int    `yaml:"lookback"`
		Quantity      string `yaml:"quantity"`
	} `yaml:"engine"`
	SchemaVersion int `yaml:"schema_version"`
	App           struct {
		Mode     string `yaml:"mode"`
		Timezone string `yaml:"timezone"`
		LogLevel string `yaml:"log_level"`
	} `yaml:"app"`
	Storage struct {
		Driver  string `yaml:"driver"`
		DSNEnv  string `yaml:"dsn_env"`
		Timeout string `yaml:"timeout"`
	} `yaml:"storage"`
	MarketData struct {
		Provider    string       `yaml:"provider"`
		RESTURL     string       `yaml:"rest_url"`
		SpotWSURL   string       `yaml:"spot_ws_url"`
		LinearWSURL string       `yaml:"linear_ws_url"`
		StaleAfter  string       `yaml:"stale_after"`
		Instruments []Instrument `yaml:"instruments"`
	} `yaml:"market_data"`
	Paper struct {
		InitialBalance string `yaml:"initial_balance"`
		QuoteCurrency  string `yaml:"quote_currency"`
	} `yaml:"paper"`
}

type Instrument struct {
	Market     string   `yaml:"market"`
	Symbol     string   `yaml:"symbol"`
	Timeframes []string `yaml:"timeframes"`
}

// Load does not access PostgreSQL or resolve DSN values. lookup can be os.Getenv.
func Load(input io.Reader, lookup func(string) string) (Config, error) {
	var cfg Config
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil {
		return cfg, errors.New("config: could not read YAML")
	}
	if len(data) > 1<<20 {
		return cfg, errors.New("config: file exceeds 1 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, yamlutil.DecodeError("config", data, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("config: expected one YAML document")
	}
	for name, target := range map[string]*string{
		"BILLIONAIRE_MODE":             &cfg.App.Mode,
		"BILLIONAIRE_LOG_LEVEL":        &cfg.App.LogLevel,
		"BILLIONAIRE_DATABASE_TIMEOUT": &cfg.Storage.Timeout,
		"BILLIONAIRE_PROVIDER":         &cfg.MarketData.Provider,
		"BILLIONAIRE_REST_URL":         &cfg.MarketData.RESTURL,
		"BILLIONAIRE_SPOT_WS_URL":      &cfg.MarketData.SpotWSURL,
		"BILLIONAIRE_LINEAR_WS_URL":    &cfg.MarketData.LinearWSURL,
		"BILLIONAIRE_STALE_AFTER":      &cfg.MarketData.StaleAfter,
		"BILLIONAIRE_INITIAL_BALANCE":  &cfg.Paper.InitialBalance,
	} {
		if value := lookup(name); value != "" {
			*target = value
		}
	}
	return cfg, cfg.Validate()
}

func (c Config) DatabaseURL(lookup func(string) string) (string, error) {
	value := strings.TrimSpace(lookup(c.Storage.DSNEnv))
	if value == "" {
		return "", errors.New("storage.dsn_env: referenced environment variable is unset or empty")
	}
	return value, nil
}

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func (c Config) Validate() error {
	if c.Notifications.Cooldown != "" {
		d, err := time.ParseDuration(c.Notifications.Cooldown)
		if err != nil || d < 0 || d > 24*time.Hour {
			return errors.New("notifications.cooldown must be between 0s and 24h")
		}
	}
	if len(c.Notifications.Targets) > 3 {
		return errors.New("too many notification targets")
	}
	for _, target := range c.Notifications.Targets {
		if target != "terminal" && target != "desktop" && target != "telegram" {
			return errors.New("invalid notification target")
		}
	}

	if c.Engine.QueueCapacity < 0 || c.Engine.QueueCapacity > 4096 || c.Engine.Lookback < 0 || c.Engine.Lookback > 10000 {
		return errors.New("engine.queue_capacity must be 0..4096 and engine.lookback 0..10000; zero selects defaults")
	}
	if c.Engine.Quantity != "" && c.Engine.Quantity != "risk" {
		q, err := domain.ParseDecimal(c.Engine.Quantity)
		if err != nil || !q.IsPositive() {
			return errors.New("engine.quantity must be risk or a positive base quantity")
		}
	}
	if c.SchemaVersion != 1 {
		return errors.New("schema_version must be 1")
	}
	if c.App.Mode != "paper" {
		return errors.New("app.mode must be paper")
	}
	if c.App.Timezone != "UTC" {
		return errors.New("app.timezone must be UTC")
	}
	switch c.App.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("app.log_level must be debug, info, warn or error")
	}
	if c.Storage.Driver != "postgres" {
		return errors.New("storage.driver must be postgres")
	}
	if !envName.MatchString(c.Storage.DSNEnv) {
		return errors.New("storage.dsn_env must name an environment variable")
	}
	if d, err := time.ParseDuration(c.Storage.Timeout); err != nil || d <= 0 || d > time.Minute {
		return errors.New("storage.timeout must be positive and at most 1m")
	}
	if c.MarketData.Provider != "bybit" {
		return errors.New("market_data.provider must be bybit; other providers are not implemented")
	}
	for _, item := range []struct{ field, value, scheme string }{
		{"market_data.rest_url", c.MarketData.RESTURL, "https"},
		{"market_data.spot_ws_url", c.MarketData.SpotWSURL, "wss"},
		{"market_data.linear_ws_url", c.MarketData.LinearWSURL, "wss"},
	} {
		u, err := url.Parse(item.value)
		if err != nil || u.Hostname() == "" || u.Scheme != item.scheme || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return fmt.Errorf("%s must be a public %s URL without credentials, query or fragment", item.field, item.scheme)
		}
		if item.scheme == "wss" {
			expected := "/v5/public/spot"
			if item.field == "market_data.linear_ws_url" {
				expected = "/v5/public/linear"
			}
			if u.Path != expected {
				return fmt.Errorf("%s must select its public market socket", item.field)
			}
		}
		if item.scheme == "https" && u.Path != "" && u.Path != "/" {
			return errors.New("market_data.rest_url must be an origin without a path")
		}
	}
	if d, err := time.ParseDuration(c.MarketData.StaleAfter); err != nil || d <= 0 {
		return errors.New("market_data.stale_after must be a positive duration")
	}
	if len(c.MarketData.Instruments) == 0 {
		return errors.New("market_data.instruments must not be empty")
	}
	seen := map[string]bool{}
	for idx, item := range c.MarketData.Instruments {
		field := fmt.Sprintf("market_data.instruments[%d]", idx)
		parts := strings.Split(item.Symbol, "/")
		if len(parts) != 2 {
			return fmt.Errorf("%s.symbol must be BASE/USDT", field)
		}
		instrument := domain.Instrument{Provider: c.MarketData.Provider, Market: domain.MarketType(item.Market), Base: parts[0], Quote: parts[1], ID: parts[0] + parts[1]}
		if err := instrument.Validate(); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		key := item.Market + ":" + item.Symbol
		if seen[key] {
			return fmt.Errorf("%s duplicates an instrument", field)
		}
		seen[key] = true
		if len(item.Timeframes) == 0 {
			return fmt.Errorf("%s.timeframes must not be empty", field)
		}
		intervals := map[string]bool{}
		for _, interval := range item.Timeframes {
			if _, err := domain.ParseTimeframe(interval); err != nil {
				return fmt.Errorf("%s.timeframes: %w", field, err)
			}
			if intervals[interval] {
				return fmt.Errorf("%s.timeframes contains a duplicate", field)
			}
			intervals[interval] = true
		}
	}
	if _, err := paper.ValidateInitialBalance(c.Paper.InitialBalance); err != nil {
		return fmt.Errorf("paper.initial_balance: %w", err)
	}
	if c.Paper.QuoteCurrency != "USDT" {
		return errors.New("paper.quote_currency must be USDT")
	}
	return nil
}
