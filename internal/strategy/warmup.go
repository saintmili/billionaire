package strategy

import (
	"errors"
	"sort"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
)

func (b builtin) Requirements(d Definition) (map[domain.Timeframe]int, error) {
	result := map[domain.Timeframe]int{}
	for _, f := range d.Timeframes {
		result[f] = 1
	}
	switch b.name {
	case "breakout":
		n, err := IntParam(d, "lookback", 20, 1, 5000)
		if err != nil {
			return nil, err
		}
		result[d.Primary] = n + 1
	case "support_resistance":
		n, err := IntParam(d, "lookback", 50, 4, 5000)
		if err != nil {
			return nil, err
		}
		result[d.Primary] = n
	case "engulfing":
		result[d.Primary] = 2
	}
	var walk func(Rule)
	walk = func(r Rule) {
		extra := 0
		if r.Op == "crosses_above" || r.Op == "crosses_below" {
			extra = 1
		}
		for _, value := range []Value{r.Left, r.Right} {
			if value.Constant != nil || value.Field == "" {
				continue
			}
			period := 1
			if value.Field == "sma" {
				period = value.Period
			}
			n := period + value.Offset + extra
			frame := value.Timeframe
			if frame == "" {
				frame = d.Primary
			}
			if n > result[frame] {
				result[frame] = n
			}
		}
		for _, child := range r.All {
			walk(child)
		}
		for _, child := range r.Any {
			walk(child)
		}
		if r.Not != nil {
			walk(*r.Not)
		}
	}
	if d.Entry != nil {
		walk(*d.Entry)
	}
	if d.Exit != nil {
		walk(*d.Exit)
	}
	return result, nil
}

type Warmup struct {
	Query marketdata.Query
	Count int
}

func WarmupQueries(d Definition, i domain.Instrument, at time.Time, registry *Registry) ([]Warmup, error) {
	normalized, err := Prepare(d, registry)
	if err != nil {
		return nil, err
	}
	implementation, err := registry.Lookup(normalized.Implementation)
	if err != nil {
		return nil, err
	}
	required, err := implementation.Requirements(normalized)
	if err != nil {
		return nil, err
	}
	frames := make([]domain.Timeframe, 0, len(required))
	for frame := range required {
		frames = append(frames, frame)
	}
	sort.Slice(frames, func(a, b int) bool { return frames[a] < frames[b] })
	result := []Warmup{}
	for _, frame := range frames {
		count := required[frame]
		selected := false
		for _, f := range normalized.Timeframes {
			selected = selected || f == frame
		}
		if !selected || count < 1 || count > MaxBars {
			return nil, errors.New("strategy requirements exceed selected timeframe/window limits")
		}
		if frame == normalized.Primary {
			count--
			if count == 0 {
				continue
			}
		}
		end, _ := frame.Bounds(at)
		start := end
		for range count {
			start, _ = frame.Bounds(start.Add(-time.Nanosecond))
		}
		result = append(result, Warmup{Query: marketdata.Query{Instrument: i, Timeframe: frame, From: start, To: end}, Count: count})
	}
	return result, nil
}
