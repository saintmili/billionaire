package marketdata

import (
	"errors"
	"sort"

	"github.com/saintmili/billionaire/internal/domain"
)

// Aggregate emits only fully covered closed buckets; gaps and duplicate inputs
// are errors. Decimal addition/comparison never uses binary floating point.
func Aggregate(input []domain.Candle, target domain.Timeframe) ([]domain.Candle, error) {
	if _, err := domain.ParseTimeframe(string(target)); err != nil {
		return nil, err
	}
	bars := append([]domain.Candle(nil), input...)
	sort.Slice(bars, func(i, j int) bool { return bars[i].OpenTime.Before(bars[j].OpenTime) })
	result := []domain.Candle{}
	for len(bars) > 0 {
		first := bars[0]
		if len(result) > 0 && !first.OpenTime.Equal(result[len(result)-1].EndTime) {
			return nil, errors.New("aggregate is missing a whole target bucket")
		}
		start, end := target.Bounds(first.OpenTime)
		if !first.OpenTime.Equal(start) {
			return nil, errors.New("aggregate is missing the start of a target bucket")
		}
		merged := first
		merged.Timeframe = target
		merged.EndTime = end
		next := start
		used := 0
		for used < len(bars) && bars[used].OpenTime.Before(end) {
			c := bars[used]
			if err := c.Validate(); err != nil {
				return nil, err
			}
			if !c.Closed || c.Instrument != first.Instrument || c.Source != first.Source || c.Timeframe != first.Timeframe || !c.OpenTime.Equal(next) || c.EndTime.After(end) {
				return nil, errors.New("aggregate has a gap, duplicate, partial or incompatible candle")
			}
			if used > 0 {
				merged.BaseVolume = merged.BaseVolume.Add(c.BaseVolume)
			}
			if c.High.GreaterThan(merged.High) {
				merged.High = c.High
			}
			if c.Low.LessThan(merged.Low) {
				merged.Low = c.Low
			}
			merged.Close = c.Close
			if c.ObservedAt.After(merged.ObservedAt) {
				merged.ObservedAt = c.ObservedAt
			}
			next = c.EndTime
			used++
		}
		if !next.Equal(end) {
			return nil, errors.New("aggregate is missing closed source candles")
		}
		result = append(result, Stamp(merged))
		bars = bars[used:]
	}
	return result, nil
}
