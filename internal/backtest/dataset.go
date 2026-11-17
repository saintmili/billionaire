package backtest

import (
	"errors"
	"fmt"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/strategy"
	"sort"
	"time"
)

// Queries includes the exact lookback required at the first primary evaluation.
// Calendar intervals are stepped through Bounds, never fixed durations.
func Queries(c Config, v strategy.Version) ([]marketdata.Query, error) {
	if e := c.Validate(v); e != nil {
		return nil, e
	}
	_, first := v.Definition.Primary.Bounds(c.From)
	primary, e := strategy.WarmupQueries(v.Definition, c.Instrument.Instrument, c.From, strategy.DefaultRegistry())
	if e != nil {
		return nil, e
	}
	higher, e := strategy.WarmupQueries(v.Definition, c.Instrument.Instrument, first, strategy.DefaultRegistry())
	if e != nil {
		return nil, e
	}
	starts := map[domain.Timeframe]time.Time{}
	for _, w := range primary {
		if w.Query.Timeframe == v.Definition.Primary {
			starts[w.Query.Timeframe] = w.Query.From
		}
	}
	for _, w := range higher {
		if w.Query.Timeframe != v.Definition.Primary {
			starts[w.Query.Timeframe] = w.Query.From
		}
	}
	frames := append([]domain.Timeframe(nil), c.Timeframes...)
	sort.Slice(frames, func(i, j int) bool { return frames[i] < frames[j] })
	result := []marketdata.Query{}
	for _, f := range frames {
		start, _ := f.Bounds(c.From)
		if s, ok := starts[f]; ok {
			start = s
		}
		end, _ := f.Bounds(c.To)
		if !start.Before(end) {
			continue
		}
		q := marketdata.Query{Instrument: c.Instrument.Instrument, Timeframe: f, From: start, To: end}
		if e := q.Validate(); e != nil {
			return nil, e
		}
		result = append(result, q)
	}
	return result, nil
}

// Availability is the explicit virtual publication time; exact provider clocks
// remain in Inputs and therefore in the dataset identity.
func Availability(c Config, b domain.Candle) time.Time {
	if c.ClockMode == "recorded-close" {
		return b.ObservedAt
	}
	return b.EndTime
}

// Freeze validates and canonically orders exact source payloads before trading.
func Freeze(c Config, v strategy.Version, bars []domain.Candle) (Inputs, []string, error) {
	queries, e := Queries(c, v)
	if e != nil {
		return Inputs{}, nil, e
	}
	c.Timeframes = append([]domain.Timeframe(nil), c.Timeframes...)
	sort.Slice(c.Timeframes, func(i, j int) bool { return c.Timeframes[i] < c.Timeframes[j] })
	selected := []domain.Candle{}
	for _, b := range bars {
		for _, q := range queries {
			if b.Instrument == q.Instrument && b.Timeframe == q.Timeframe && !b.OpenTime.Before(q.From) && b.OpenTime.Before(q.To) {
				if e := b.Validate(); e != nil {
					return Inputs{}, nil, e
				}
				start, end := b.Timeframe.Bounds(b.OpenTime)
				if !b.Closed || !start.Equal(b.OpenTime) || !end.Equal(b.EndTime) || b.Source != b.Instrument.Provider || b.ObservedAt.Before(b.EndTime) || marketdata.Stamp(b).Revision != b.Revision {
					return Inputs{}, nil, errors.New("invalid historical candle provenance")
				}
				selected = append(selected, b)
				break
			}
		}
	}
	sort.Slice(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		if a.Timeframe != b.Timeframe {
			return a.Timeframe < b.Timeframe
		}
		if !a.OpenTime.Equal(b.OpenTime) {
			return a.OpenTime.Before(b.OpenTime)
		}
		if !a.ObservedAt.Equal(b.ObservedAt) {
			return a.ObservedAt.Before(b.ObservedAt)
		}
		return a.Revision < b.Revision
	})
	unique := []domain.Candle{}
	for _, b := range selected {
		if len(unique) > 0 {
			a := unique[len(unique)-1]
			if a.Timeframe == b.Timeframe && a.OpenTime.Equal(b.OpenTime) {
				if a.ObservedAt.Equal(b.ObservedAt) && a.Revision != b.Revision {
					return Inputs{}, nil, errors.New("conflicting candle revisions at equal observation time")
				}
				if a.Revision == b.Revision {
					if b.ObservedAt.After(a.ObservedAt) {
						unique[len(unique)-1] = b
					}
					continue
				}
				if c.ClockMode != "recorded-close" {
					return Inputs{}, nil, errors.New("multiple known revisions require recorded-close availability")
				}
			}
		}
		unique = append(unique, b)
	}
	_, first := v.Definition.Primary.Bounds(c.From)
	warnings := []string{}
	for _, q := range queries {
		byOpen := map[time.Time]bool{}
		for _, b := range unique {
			if b.Timeframe == q.Timeframe {
				if b.OpenTime.Before(c.From) && Availability(c, b).After(first) {
					continue
				}
				byOpen[b.OpenTime] = true
			}
		}
		for at := q.From; at.Before(q.To); {
			_, end := q.Timeframe.Bounds(at)
			if !byOpen[at] {
				warm := at.Before(c.From)
				if q.Timeframe != v.Definition.Primary {
					warm = !end.After(first)
				}
				if warm {
					return Inputs{}, nil, fmt.Errorf("%w: %s %s", strategy.ErrWarmup, q.Timeframe, at.Format(time.RFC3339))
				}
				if !c.AcknowledgeGaps {
					return Inputs{}, nil, fmt.Errorf("unacknowledged data gap: %s %s", q.Timeframe, at.Format(time.RFC3339))
				}
				warnings = append(warnings, fmt.Sprintf("Acknowledged gap: %s %s; no synthetic open; strategy remains unready until contiguous lookback resumes", q.Timeframe, at.Format(time.RFC3339)))
			}
			at = end
		}
	}
	sort.Slice(unique, func(i, j int) bool {
		a, b := unique[i], unique[j]
		aa, bb := Availability(c, a), Availability(c, b)
		if !aa.Equal(bb) {
			return aa.Before(bb)
		}
		if a.Timeframe != b.Timeframe {
			return a.Timeframe < b.Timeframe
		}
		if !a.OpenTime.Equal(b.OpenTime) {
			return a.OpenTime.Before(b.OpenTime)
		}
		return a.Revision < b.Revision
	})
	return Inputs{Config: c, Version: v, Candles: unique}, warnings, nil
}
