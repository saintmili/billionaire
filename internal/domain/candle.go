package domain

import (
	"errors"
	"time"
)

// Candle has UTC boundaries and an exclusive end. Source and revision preserve
// provenance; provider adapters decide when a candle is available for evaluation.
type Candle struct {
	Instrument Instrument `json:"instrument"`
	Timeframe  Timeframe  `json:"timeframe"`
	OpenTime   time.Time  `json:"open_time"`
	EndTime    time.Time  `json:"end_time"`
	Open       Decimal    `json:"open"`
	High       Decimal    `json:"high"`
	Low        Decimal    `json:"low"`
	Close      Decimal    `json:"close"`
	BaseVolume Decimal    `json:"base_volume"`
	Closed     bool       `json:"closed"`
	Source     string     `json:"source"`
	Revision   string     `json:"revision"`
}

func (c Candle) Validate() error {
	if err := c.Instrument.Validate(); err != nil {
		return err
	}
	if _, err := ParseTimeframe(string(c.Timeframe)); err != nil {
		return err
	}
	_, openOffset := c.OpenTime.Zone()
	_, endOffset := c.EndTime.Zone()
	if c.OpenTime.IsZero() || c.EndTime.IsZero() || openOffset != 0 || endOffset != 0 || !c.EndTime.After(c.OpenTime) {
		return errors.New("candle requires UTC open_time and a later exclusive end_time")
	}
	if c.Source == "" || c.Revision == "" {
		return errors.New("candle source and revision are required")
	}
	if !c.Open.IsPositive() || !c.High.IsPositive() || !c.Low.IsPositive() || !c.Close.IsPositive() || c.BaseVolume.IsNegative() {
		return errors.New("candle prices must be positive and volume nonnegative")
	}
	if c.High.LessThan(c.Low) || c.High.LessThan(c.Open) || c.High.LessThan(c.Close) || c.Low.GreaterThan(c.Open) || c.Low.GreaterThan(c.Close) {
		return errors.New("candle OHLC bounds are inconsistent")
	}
	return nil
}
