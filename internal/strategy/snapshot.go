package strategy

import (
	"errors"
	"sort"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
)

const MaxBars = 5001

var ErrWarmup = errors.New("insufficient warm-up or missing candles")

type Snapshot struct {
	At     time.Time
	Event  domain.Candle
	frames map[domain.Timeframe][]domain.Candle
}

func NewSnapshot(event domain.Candle, history []domain.Candle, frames []domain.Timeframe) (Snapshot, error) {
	return NewSnapshotAt(event, history, frames, event.ObservedAt)
}
func NewSnapshotAt(event domain.Candle, history []domain.Candle, frames []domain.Timeframe, at time.Time) (Snapshot, error) {
	s := Snapshot{At: at.UTC(), Event: event, frames: map[domain.Timeframe][]domain.Candle{}}
	if at.IsZero() || at.Before(event.ObservedAt) {
		return s, errors.New("snapshot availability precedes source observation")
	}
	if err := event.Validate(); err != nil {
		return s, err
	}
	if event.Source != event.Instrument.Provider {
		return s, errors.New("event source must match the canonical provider")
	}
	if event.ObservedAt.IsZero() || event.ObservedAt.Before(event.OpenTime) || (event.Closed && event.ObservedAt.Before(event.EndTime)) || (!event.Closed && !event.ObservedAt.Before(event.EndTime)) || marketdata.Stamp(event).Revision != event.Revision {
		return s, errors.New("invalid event availability, closure or revision")
	}
	selected := map[domain.Timeframe]bool{}
	for _, f := range frames {
		selected[f] = true
	}
	if !selected[event.Timeframe] {
		return s, errors.New("event timeframe is not selected")
	}
	byFrame := map[domain.Timeframe]map[int64]domain.Candle{}
	for _, c := range history {
		if c.Source != event.Source || c.Instrument != event.Instrument || !selected[c.Timeframe] || !c.Closed || c.ObservedAt.IsZero() || c.ObservedAt.After(s.At) || c.EndTime.After(s.At) || (c.Timeframe == event.Timeframe && !c.OpenTime.Before(event.OpenTime)) {
			continue
		}
		if err := c.Validate(); err != nil {
			return s, err
		}
		if c.ObservedAt.Before(c.EndTime) || marketdata.Stamp(c).Revision != c.Revision {
			return s, errors.New("invalid historical candle availability/revision")
		}
		if byFrame[c.Timeframe] == nil {
			byFrame[c.Timeframe] = map[int64]domain.Candle{}
		}
		prior, ok := byFrame[c.Timeframe][c.OpenTime.Unix()]
		if ok && prior.ObservedAt.Equal(c.ObservedAt) && prior.Revision != c.Revision {
			return s, errors.New("ambiguous candle revisions at evaluation time")
		}
		if !ok || c.ObservedAt.After(prior.ObservedAt) {
			byFrame[c.Timeframe][c.OpenTime.Unix()] = c
		}
	}
	for frame, items := range byFrame {
		for _, c := range items {
			s.frames[frame] = append(s.frames[frame], c)
		}
		sort.Slice(s.frames[frame], func(i, j int) bool { return s.frames[frame][i].OpenTime.Before(s.frames[frame][j].OpenTime) })
	}
	s.frames[event.Timeframe] = append(s.frames[event.Timeframe], event)
	return s, nil
}

// Need returns an independent contiguous suffix. Higher intervals contain only
// completed candles available at At; the primary suffix can end in Event partial.
func (s Snapshot) Need(frame domain.Timeframe, n int) ([]domain.Candle, error) {
	bars := s.frames[frame]
	if n < 1 || n > MaxBars || len(bars) < n {
		return nil, ErrWarmup
	}
	bars = bars[len(bars)-n:]
	for i := 1; i < len(bars); i++ {
		if !bars[i-1].EndTime.Equal(bars[i].OpenTime) {
			return nil, ErrWarmup
		}
	}
	if frame != s.Event.Timeframe {
		expected, _ := frame.Bounds(s.At)
		if !bars[len(bars)-1].EndTime.Equal(expected) {
			return nil, ErrWarmup
		}
	}
	return append([]domain.Candle(nil), bars...), nil
}
