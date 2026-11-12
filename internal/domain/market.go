package domain

import (
	"errors"
	"regexp"
)

type MarketType string

const (
	Spot   MarketType = "spot"
	Linear MarketType = "linear"
)

type Instrument struct {
	Provider string     `json:"provider"`
	Market   MarketType `json:"market"`
	Base     string     `json:"base"`
	Quote    string     `json:"quote"`
	ID       string     `json:"instrument_id"`
}

var assetCode = regexp.MustCompile(`^[A-Z0-9]+$`)

func (i Instrument) Validate() error {
	if i.Provider == "" || i.ID == "" {
		return errors.New("instrument provider and instrument_id are required")
	}
	if i.Market != Spot && i.Market != Linear {
		return errors.New("instrument market must be spot or linear")
	}
	if !assetCode.MatchString(i.Base) || i.Quote != "USDT" || i.Base == i.Quote {
		return errors.New("instrument requires an uppercase base and USDT quote")
	}
	return nil
}

// Timeframe distinguishes calendar months (1M) from minutes (1m).
// Aggregation and provider capability resolution belong to the market adapter.
type Timeframe string

func ParseTimeframe(text string) (Timeframe, error) {
	switch text {
	case "1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "12h", "1d", "1w", "1M":
		return Timeframe(text), nil
	default:
		return "", errors.New("unsupported timeframe: choose a documented native interval")
	}
}
