package backtest

import (
	"errors"
	"fmt"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
	"sort"
	"time"
)

// FundingDataset freezes the supplied public/fixture events separately from the
// explicit expected schedule. An empty schedule means unknown coverage, not zero.
type FundingDataset struct {
	Expected []time.Time          `json:"expected_intervals"`
	Events   []paper.FundingEvent `json:"events"`
}
type FundingCoverage struct {
	Status      string                 `json:"status"`
	Expected    int                    `json:"expected"`
	Supplied    int                    `json:"supplied_rates"`
	Missing     []time.Time            `json:"missing_intervals"`
	Liquidation paper.LiquidationModel `json:"approximate_liquidation_model"`
}

func (c Config) FundingCoverage() FundingCoverage {
	out := FundingCoverage{Status: "unknown", Expected: len(c.Funding.Expected), Missing: []time.Time{}, Liquidation: paper.DefaultLiquidationModel()}
	if c.Instrument.Instrument.Market != domain.Linear {
		out.Status = "not_applicable"
		return out
	}
	if len(c.Funding.Expected) > 0 {
		out.Status = "complete"
	}
	for _, at := range c.Funding.Expected {
		present := false
		for _, e := range c.Funding.Events {
			if e.EffectiveAt.Equal(at) && e.Rate != nil {
				present = true
				break
			}
		}
		if present {
			out.Supplied++
		} else {
			out.Missing = append(out.Missing, at)
			out.Status = "missing"
		}
	}
	return out
}
func freezeFunding(c *Config, v strategy.Version) error {
	f := c.Funding
	if c.ClockMode == "recorded-close" && len(f.Events) > 0 {
		return errors.New("funding with delayed recorded candle intervals is unsupported; select close clock and aligned primary observations")
	}
	f.Expected = append([]time.Time(nil), f.Expected...)
	f.Events = append([]paper.FundingEvent(nil), f.Events...)
	if c.Instrument.Instrument.Market != domain.Linear && (len(f.Expected) > 0 || len(f.Events) > 0) {
		return errors.New("funding inputs require linear market")
	}
	sort.Slice(f.Expected, func(i, j int) bool { return f.Expected[i].Before(f.Expected[j]) })
	for i, at := range f.Expected {
		at = at.UTC()
		f.Expected[i] = at
		if at.Before(c.From) || at.After(c.To) || (i > 0 && at.Equal(f.Expected[i-1])) {
			return errors.New("expected funding intervals must be unique and within replay range")
		}
		start, _ := v.Definition.Primary.Bounds(at)
		if !start.Equal(at) {
			return errors.New("funding inside a primary OHLC interval is unsupported; choose an aligned lower timeframe with finer observations")
		}
	}
	sort.Slice(f.Events, func(i, j int) bool { return f.Events[i].EffectiveAt.Before(f.Events[j].EffectiveAt) })
	for i, e := range f.Events {
		if err := e.Validate(); err != nil {
			return err
		}
		e.EffectiveAt = e.EffectiveAt.UTC()
		e.AvailableAt = e.AvailableAt.UTC()
		f.Events[i] = e
		if e.Instrument != c.Instrument.Instrument || !e.AvailableAt.Equal(e.EffectiveAt) {
			return errors.New("historical funding requires matching instrument and valuation available exactly at effective time; delayed retroactive funding is unsupported")
		}
		found := false
		for _, at := range f.Expected {
			if at.Equal(e.EffectiveAt) {
				found = true
			}
		}
		if !found {
			return errors.New("every supplied funding event must belong to the explicit expected schedule")
		}
		if i > 0 && e.EffectiveAt.Equal(f.Events[i-1].EffectiveAt) {
			return fmt.Errorf("duplicate/conflicting funding interval %s", e.EffectiveAt)
		}
	}
	c.Funding = f
	return nil
}
