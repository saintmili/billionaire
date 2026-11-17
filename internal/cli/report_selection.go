package cli

import (
	"errors"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
)

func validateReportScope(scope, run, account string) error {
	if scope != "backtest" && scope != "forward-paper" {
		return errors.New("--scope must explicitly select backtest or forward-paper")
	}
	if scope == "backtest" && run == "" {
		return errors.New("backtest scope requires --run")
	}
	if scope == "forward-paper" && (run != "" || account == "") {
		return errors.New("forward-paper scope requires --account and excludes --run")
	}
	return nil
}

func optionalReportDate(value, flag string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New(flag + " must be RFC3339")
	}
	return at, nil
}

func optionalReportInstrument(provider, market, symbol string) (*domain.Instrument, error) {
	if provider == "" && market == "" && symbol == "" {
		return nil, nil
	}
	identity, _, err := (marketFlags{provider: provider, market: market, symbol: symbol, frame: "1m"}).identity(provider)
	if err != nil {
		return nil, err
	}
	return &identity, nil
}
