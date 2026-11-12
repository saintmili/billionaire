package marketdata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
)

type Repository interface {
	SaveInstrument(context.Context, InstrumentInfo) error
	SaveCandles(context.Context, []domain.Candle) (int, error)
	SaveCandleEvent(context.Context, domain.Candle, FeedState) (int, bool, error)
	Candles(context.Context, Query) ([]domain.Candle, error)
	SaveFeedState(context.Context, FeedState) error
	FeedState(context.Context, domain.Instrument, domain.Timeframe) (FeedState, error)
}
type SyncResult struct {
	Candles int `json:"candles"`
	Changed int `json:"changed"`
	Missing int `json:"missing"`
}
type GapError struct{ Missing int }

func (e *GapError) Error() string { return fmt.Sprintf("historical candle gaps remain: %d", e.Missing) }

type SyncOptions struct {
	Now                 func() time.Time
	MaxRetries          int
	Backoff, StaleAfter time.Duration
}
type Synchronizer struct {
	provider   Provider
	repository Repository
	opts       SyncOptions
}

func NewSynchronizer(provider Provider, repository Repository, opts SyncOptions) (*Synchronizer, error) {
	if provider == nil || repository == nil || opts.MaxRetries < 0 || opts.MaxRetries > 10 {
		return nil, errors.New("invalid synchronization dependencies or retry limit")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Backoff <= 0 {
		opts.Backoff = time.Second
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = time.Minute
	}
	return &Synchronizer{provider: provider, repository: repository, opts: opts}, nil
}
func (s *Synchronizer) Backfill(ctx context.Context, q Query, refresh bool) (SyncResult, error) {
	result := SyncResult{}
	if err := q.Validate(); err != nil {
		return result, err
	}
	info, err := s.provider.Instrument(ctx, q.Instrument.Market, q.Instrument.Base+"/"+q.Instrument.Quote)
	if err != nil {
		return result, err
	}
	if info.Instrument != q.Instrument {
		return result, errors.New("requested instrument does not match provider metadata")
	}
	if err := s.repository.SaveInstrument(ctx, info); err != nil {
		return result, err
	}
	cached, err := s.repository.Candles(ctx, q)
	if err != nil {
		return result, err
	}
	present := map[int64]bool{}
	for _, c := range cached {
		present[c.OpenTime.UnixMilli()] = true
	}
	ranges := []Query{}
	for at := q.From; at.Before(q.To); {
		if present[at.UnixMilli()] && !refresh {
			_, at = q.Timeframe.Bounds(at)
			continue
		}
		from := at
		for at.Before(q.To) && (refresh || !present[at.UnixMilli()]) {
			_, at = q.Timeframe.Bounds(at)
		}
		missing := q
		missing.From = from
		missing.To = at
		ranges = append(ranges, missing)
	}
	for _, missing := range ranges {
		bars, err := s.provider.History(ctx, missing)
		if err != nil {
			return result, err
		}
		for _, c := range bars {
			if c.Instrument != q.Instrument || c.Timeframe != q.Timeframe || c.OpenTime.Before(missing.From) || !c.EndTime.After(c.OpenTime) || c.EndTime.After(missing.To) {
				return result, errors.New("provider returned candles outside the missing range")
			}
		}
		changed, err := s.repository.SaveCandles(ctx, bars)
		if err != nil {
			return result, err
		}
		result.Changed += changed
	}
	bars, err := s.repository.Candles(ctx, q)
	if err != nil {
		return result, err
	}
	result.Candles = len(bars)
	present = map[int64]bool{}
	for _, c := range bars {
		present[c.OpenTime.UnixMilli()] = true
	}
	for at := q.From; at.Before(q.To); {
		if !present[at.UnixMilli()] {
			result.Missing++
		}
		_, at = q.Timeframe.Bounds(at)
	}
	if result.Missing > 0 {
		return result, &GapError{result.Missing}
	}
	return result, nil
}

type Event struct {
	AvailableAt   time.Time      `json:"available_at"`
	Kind          string         `json:"kind"`
	Candle        *domain.Candle `json:"candle,omitempty"`
	Feed          FeedState      `json:"feed"`
	AllowsEntries bool           `json:"allows_auto_entries"`
}

// Run backfills before every subscription, including reconnect. Each callback is
// synchronous; failure stops delivery and leaves subsequent closed bars for REST
// recovery. No unbounded channel or silent overflow is used.
func (s *Synchronizer) Run(ctx context.Context, q Query, consume func(Event) error) error {
	if err := q.Validate(); err != nil {
		return err
	}
	state, err := s.repository.FeedState(ctx, q.Instrument, q.Timeframe)
	if err != nil {
		return err
	}
	availability := func(c *domain.Candle) time.Time {
		at := s.opts.Now().UTC()
		if c != nil && c.ObservedAt.After(at) {
			at = c.ObservedAt
		}
		if state.AvailabilityClock.After(at) {
			at = state.AvailabilityClock
		}
		return at
	}
	publish := func(c *domain.Candle, kind string) error {
		available := availability(c)
		if c != nil && c.Closed && state.PendingClosed != nil && EventID(*state.PendingClosed) == EventID(*c) && !state.PendingAvailableAt.IsZero() {
			available = state.PendingAvailableAt
		}
		if c != nil {
			state.AvailabilityClock = available
		}

		if c != nil && c.Closed && (state.PendingClosed == nil || EventID(*state.PendingClosed) != EventID(*c)) {
			copy := *c
			state.PendingClosed = &copy
			state.PendingAvailableAt = available
			if err := s.repository.SaveFeedState(ctx, state); err != nil {
				return err
			}
		}
		if c == nil {
			if err := s.repository.SaveFeedState(ctx, state); err != nil {
				return err
			}
		}
		if err := consume(Event{Kind: kind, Candle: c, Feed: state, AvailableAt: available, AllowsEntries: state.AllowsEntries(s.opts.Now(), s.opts.StaleAfter)}); err != nil {
			return err
		}
		// Closed-event acknowledgment follows successful synchronous consumption.
		// A crash between callback and checkpoint can replay; EventID stays stable.
		if c != nil {
			if c.Closed && c.EndTime.After(state.LastDelivered) {
				state.LastDelivered = c.EndTime
			}
			if c.Closed {
				state.PendingClosed = nil
				state.PendingAvailableAt = time.Time{}
			}
			return s.repository.SaveFeedState(ctx, state)
		}
		return nil
	}
	recoverCloses := func(from, to time.Time) error {
		if !to.After(from) {
			return nil
		}
		recovery := q
		recovery.From = from
		recovery.To = to
		bars, err := s.repository.Candles(ctx, recovery)
		if err != nil {
			return err
		}
		// All fetched pages are available before chronological recovery starts.
		for _, c := range bars {
			if c.ObservedAt.After(state.AvailabilityClock) {
				state.AvailabilityClock = c.ObservedAt
			}
		}
		next := from
		for _, c := range bars {
			if !c.OpenTime.Equal(next) {
				return errors.New("recovered event range has a gap")
			}
			state.Connected = false
			state.Reason = "delivering recovered closes"
			if err := publish(&c, "recovered_candle"); err != nil {
				return err
			}
			next = c.EndTime
		}
		if !next.Equal(to) {
			return errors.New("recovered event range is incomplete")
		}
		return nil
	}

	// Always clear persisted connection ownership when this run exits, even if its
	// context was canceled. A crashed process is additionally gated by freshness.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		state.Connected = false
		if state.Reason == "" {
			state.Reason = "stopped"
		}
		_ = s.repository.SaveFeedState(cleanup, state)
	}()
	seen := map[int64]string{}
	var lastErr error
	for attempt := 0; attempt <= s.opts.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		state.Connected = false
		state.Reason = "warming up or reconnecting"
		if err := publish(nil, "feed"); err != nil {
			return err
		}
		if state.PendingClosed != nil {
			pending := *state.PendingClosed
			if _, err := s.repository.SaveCandles(ctx, []domain.Candle{pending}); err != nil {
				return err
			}
			state.Reason = "redelivering pending closed revision"
			if err := publish(&pending, "recovered_candle"); err != nil {
				return err
			}
		}
		current := q
		current.To, _ = q.Timeframe.Bounds(s.opts.Now())
		if !state.LastDelivered.IsZero() && state.LastDelivered.Before(current.From) {
			current.From = state.LastDelivered
		}
		result, err := s.Backfill(ctx, current, false)
		state.Gaps = result.Missing
		if err == nil {
			state.LastClosed = current.To
			if state.LastDelivered.IsZero() {
				state.LastDelivered = current.To
			} else {
				if err := recoverCloses(state.LastDelivered, current.To); err != nil {
					return err
				}
			}
			state.Reason = "warm-up complete; awaiting live observation"
			if err := publish(nil, "feed"); err != nil {
				return err
			}
			var processErr error
			err = s.provider.Stream(ctx, q, func(c domain.Candle) error {
				process := func() error {
					if c.Instrument != q.Instrument {
						return errors.New("stream instrument mismatch")
					}
					if err := c.Validate(); err != nil {
						return err
					}
					if c.ObservedAt.IsZero() || c.ObservedAt.Before(c.OpenTime) || c.ObservedAt.After(s.opts.Now().Add(30*time.Second)) || s.opts.Now().Sub(c.ObservedAt) > 30*time.Second {
						return errors.New("stale or clock-skewed streaming update")
					}
					if !c.Closed && !c.EndTime.After(state.LastClosed) {
						return nil
					}
					if c.Timeframe != q.Timeframe {
						if c.Timeframe != "1m" {
							return errors.New("unexpected stream timeframe")
						}
						start, end := q.Timeframe.Bounds(c.OpenTime)
						through := c.OpenTime
						if c.Closed {
							if _, err := s.repository.SaveCandles(ctx, []domain.Candle{c}); err != nil {
								return err
							}
							through = c.EndTime
						}
						base := Query{Instrument: q.Instrument, Timeframe: "1m", From: start, To: through}
						if through.After(start) {
							recovered, err := s.Backfill(ctx, base, false)
							state.Gaps = recovered.Missing
							if err != nil {
								return err
							}
						}
						if !c.Closed || !c.EndTime.Equal(end) {
							if c.ObservedAt.After(state.LastEvent) {
								state.LastEvent = c.ObservedAt
							}
							state.Connected = true
							state.Reason = ""
							state.Gaps = 0
							return publish(nil, "feed")
						}
						bars, err := s.repository.Candles(ctx, base)
						if err != nil {
							return err
						}
						aggregate, err := Aggregate(bars, q.Timeframe)
						if err != nil || len(aggregate) != 1 {
							return errors.New("stream aggregate has missing source candles")
						}
						c = aggregate[0]
					}
					if !c.Closed && !c.EndTime.After(state.LastClosed) {
						return nil
					}
					if c.Closed && c.OpenTime.After(state.LastClosed) {
						state.Connected = false
						state.Reason = "recovering missing closed candles"
						state.Gaps = 1
						if err := publish(nil, "feed"); err != nil {
							return err
						}
						gap := q
						gap.From = state.LastClosed
						gap.To = c.OpenTime
						recovered, err := s.Backfill(ctx, gap, false)
						state.Gaps = recovered.Missing
						if err != nil {
							return err
						}
						state.LastClosed = gap.To
						if err := recoverCloses(state.LastDelivered, gap.To); err != nil {
							return err
						}
					}
					duplicate := seen[c.OpenTime.UnixMilli()] == EventID(c)
					if c.Closed {
						prepared := state
						prepared.PendingAvailableAt = availability(&c)
						changed, pending, err := s.repository.SaveCandleEvent(ctx, c, prepared)
						if pending {
							copy := c
							state.PendingClosed = &copy
							state.PendingAvailableAt = prepared.PendingAvailableAt
						}
						if err != nil {
							return err
						}
						duplicate = changed == 0 && !c.EndTime.After(state.LastDelivered)
						if c.EndTime.After(state.LastClosed) {
							state.LastClosed = c.EndTime
						}
					} else if c.ObservedAt.Before(state.LastEvent) {
						return nil
					}
					if c.ObservedAt.After(state.LastEvent) {
						state.LastEvent = c.ObservedAt
					}
					state.Connected = true
					state.Gaps = 0
					state.Reason = ""
					seen[c.OpenTime.UnixMilli()] = EventID(c)
					if len(seen) > 1024 {
						oldest := c.OpenTime.UnixMilli()
						for at := range seen {
							if at < oldest {
								oldest = at
							}
						}
						delete(seen, oldest)
					}
					if duplicate {
						return s.repository.SaveFeedState(ctx, state)
					}
					return publish(&c, "candle")
				}
				processErr = process()
				return processErr
			})
			if processErr != nil {
				state.Reason = processErr.Error()
				return processErr
			}
		}
		state.Connected = false
		if errors.Is(err, io.EOF) {
			state.Reason = "fixture complete"
			return publish(nil, "feed")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			err = errors.New("public stream ended unexpectedly")
		}
		lastErr = err
		state.Reason = err.Error()
		if err := publish(nil, "feed"); err != nil {
			return err
		}
		if attempt < s.opts.MaxRetries {
			delay := s.opts.Backoff * time.Duration(1<<attempt)
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			delay += time.Duration(rand.Int64N(int64(delay/4) + 1))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return fmt.Errorf("market reconnect attempts exhausted: %w", lastErr)
}
