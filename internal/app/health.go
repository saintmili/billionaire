package app

import (
	"context"
	"fmt"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"sync"
	"time"
)

type FeedHealth struct {
	FundingMissingIntervals int64      `json:"funding_missing_intervals,omitempty"`
	FundingCoverage         string     `json:"funding_coverage,omitempty"`
	FundingThrough          *time.Time `json:"funding_through,omitempty"`
	marketdata.FeedState
	Clock                string    `json:"clock"`
	Status               string    `json:"status"`
	LastProcessed        time.Time `json:"last_successful_update"`
	LastValidObservation time.Time `json:"last_valid_observation"`
	EntriesReady         bool      `json:"entries_ready"`
}
type ExposureHealth struct {
	PositionID              string            `json:"position_id"`
	Instrument              domain.Instrument `json:"instrument"`
	Monitoring              string            `json:"monitoring"`
	Limitation              string            `json:"limitation,omitempty"`
	FundingMissingIntervals int64             `json:"funding_missing_intervals,omitempty"`
	FundingCoverage         string            `json:"funding_coverage,omitempty"`
	FundingThrough          *time.Time        `json:"funding_through,omitempty"`
}
type HealthSnapshot struct {
	ObservedAt      time.Time                   `json:"observed_at"`
	Session         string                      `json:"session"`
	Database        string                      `json:"database"`
	Ownership       string                      `json:"ownership"`
	EntriesReady    bool                        `json:"entries_ready"`
	Feeds           []FeedHealth                `json:"feeds"`
	Exposure        []ExposureHealth            `json:"exposure"`
	Counts          *postgres.OperationalCounts `json:"counts"`
	JournalDelivery string                      `json:"journal_delivery"`
}

// Health uses current-session processed states, never another session's persisted
// Connected bit. Each read rechecks ownership, database and subscription clocks.
type Health struct {
	store     *postgres.Store
	owner     *postgres.PaperOwner
	feeds     []Subscription
	ttl       time.Duration
	session   string
	mu        sync.RWMutex
	processed map[string]FeedHealth
}

func feedKey(i domain.Instrument, f domain.Timeframe) string { return fmt.Sprint(i, f) }
func NewHealth(s *postgres.Store, o *postgres.PaperOwner, feeds []Subscription, ttl time.Duration) *Health {
	return &Health{store: s, owner: o, feeds: append([]Subscription(nil), feeds...), ttl: ttl, session: fmt.Sprint(time.Now().UTC().UnixNano()), processed: map[string]FeedHealth{}}
}
func (h *Health) Processed(e marketdata.Event, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := feedKey(e.Feed.Instrument, e.Feed.Timeframe)
	old := h.processed[key]
	old.FeedState = e.Feed
	if e.Candle != nil {
		old.LastProcessed = at.UTC()
		if e.Kind != "recovered_candle" && !e.AvailableAt.After(at) && validPrice(e.Feed, at, h.ttl) {
			old.LastValidObservation = e.Candle.ObservedAt.UTC()
		}
	}
	h.processed[key] = old
}
func validPrice(s marketdata.FeedState, now time.Time, ttl time.Duration) bool {
	return s.Connected && !s.LastEvent.IsZero() && !now.Before(s.LastEvent) && now.Sub(s.LastEvent) <= ttl
}

// readiness omits the owner ping so it can run inside the owner's control
// transaction. That transaction itself fences ownership and fails on DB loss.
func (h *Health) readiness(ctx context.Context, override *marketdata.Event) ([]FeedHealth, bool, error) {

	// Complete database reads before sampling live clocks, so a slow query cannot
	// make an earlier readiness decision appear current.
	required := []marketdata.Query{}
	instances, err := h.store.StrategyInstances(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, i := range instances {
		if !i.Enabled {
			continue
		}
		v, err := h.store.StrategyVersion(ctx, i.VersionID)
		if err != nil {
			return nil, false, err
		}
		for _, frame := range v.Definition.Timeframes {
			required = append(required, marketdata.Query{Instrument: i.Instrument, Timeframe: frame})
		}
	}
	h.mu.RLock()
	states := make(map[string]FeedHealth, len(h.processed))
	for k, v := range h.processed {
		states[k] = v
	}
	h.mu.RUnlock()
	if override != nil {
		key := feedKey(override.Feed.Instrument, override.Feed.Timeframe)
		v := states[key]
		v.FeedState = override.Feed
		states[key] = v
	}
	feeds := []FeedHealth{}
	ready := len(h.feeds) > 0
	known := map[string]bool{}
	for _, f := range h.feeds {
		key := feedKey(f.Query.Instrument, f.Query.Timeframe)
		known[key] = true
		v, exists := states[key]
		now := f.Now().UTC()
		if !exists {
			v.FeedState = marketdata.FeedState{Instrument: f.Query.Instrument, Timeframe: f.Query.Timeframe}
			v.Status = "recovering"
		} else if !v.Connected {
			v.Status = "disconnected"
		} else if !validPrice(v.FeedState, now, h.ttl) {
			v.Status = "stale"
		} else if !v.AllowsEntries(now, h.ttl) {
			v.Status = "recovering"
		} else {
			v.Status = "ready"
		}
		v.Clock = "wall"
		if f.Query.Instrument.Provider == "fixture" {
			v.Clock = "fixture"
		}
		v.EntriesReady = v.Status == "ready"
		ready = ready && v.EntriesReady
		feeds = append(feeds, v)
	}
	// Warmed historical filters do not establish ongoing monitoring coverage.
	for _, q := range required {
		key := feedKey(q.Instrument, q.Timeframe)
		if !known[key] {
			known[key] = true
			ready = false
			feeds = append(feeds, FeedHealth{FeedState: marketdata.FeedState{Instrument: q.Instrument, Timeframe: q.Timeframe}, Status: "unmonitored"})
		}
	}

	return feeds, ready, nil
}
func (h *Health) Snapshot(ctx context.Context) (HealthSnapshot, error) {
	r := HealthSnapshot{ObservedAt: time.Now().UTC(), Session: h.session, Database: "unavailable", Ownership: "unknown", Feeds: []FeedHealth{}, Exposure: []ExposureHealth{}, JournalDelivery: "not_implemented_pending_M7"}
	var err error
	r.Counts, err = h.store.OperationalCounts(ctx)
	if err != nil {
		return r, err
	}
	r.Database = "ready"
	if err = h.owner.Check(ctx); err != nil {
		r.Ownership = "lost"
		return r, err
	}
	r.Ownership = "held"
	r.Feeds, r.EntriesReady, err = h.readiness(ctx, nil)
	if err != nil {
		r.Database = "unavailable"
		r.Counts = nil
		r.EntriesReady = false
		return r, err
	}
	for index := range r.Feeds {
		f := &r.Feeds[index]
		if f.Instrument.Market != domain.Linear {
			continue
		}
		f.FundingCoverage = "unknown"
		funding, e := h.store.LatestMonitoringFunding(ctx, f.Instrument)
		if e != nil {
			r.Database = "unavailable"
			r.Counts = nil
			r.EntriesReady = false
			return r, e
		}
		f.FundingMissingIntervals, e = h.store.MissingFundingIntervals(ctx, f.Instrument)
		if e != nil {
			r.Database = "unavailable"
			r.Counts = nil
			r.EntriesReady = false
			return r, e
		}
		if funding != nil {
			at := funding.Event.EffectiveAt
			f.FundingThrough = &at
			f.FundingCoverage = "supplied"
			if funding.Warning != "" || f.FundingMissingIntervals > 0 {
				f.FundingCoverage = "missing"
			}
		}
	}
	positions, err := h.store.PaperPositions(ctx, "")
	if err != nil {
		r.Database = "unavailable"
		r.Counts = nil
		r.EntriesReady = false
		return r, err
	}
	for _, p := range positions {
		if p.Status != "open" {
			continue
		}
		v := ExposureHealth{PositionID: p.ID, Instrument: p.Instrument, Monitoring: "unmonitored", Limitation: "no current valid price: protection and pending closes wait for an eligible observation"}

		if p.Instrument.Market == domain.Linear {
			v.FundingCoverage = "unknown"
			funding, e := h.store.LatestMonitoringFunding(ctx, p.Instrument)
			if e != nil {
				r.Database = "unavailable"
				r.Counts = nil
				r.EntriesReady = false
				return r, e
			}
			v.FundingMissingIntervals, e = h.store.MissingFundingIntervals(ctx, p.Instrument)
			if e != nil {
				r.Database = "unavailable"
				r.Counts = nil
				r.EntriesReady = false
				return r, e
			}
			if funding != nil {
				at := funding.Event.EffectiveAt
				v.FundingThrough = &at
				v.FundingCoverage = "supplied"
				if funding.Warning != "" || v.FundingMissingIntervals > 0 {
					v.FundingCoverage = "missing"
				}
			}
		}
		r.Exposure = append(r.Exposure, v)
	}
	// Funding/position reads can block. Sample readiness again after all I/O,
	// without another database round trip that could age this decision.
	if err = h.owner.Check(ctx); err != nil {
		r.Ownership = "lost"
		r.EntriesReady = false
		return r, err
	}
	r.ObservedAt = time.Now().UTC()
	r.EntriesReady = len(h.feeds) > 0
	for index := range r.Feeds {
		f := &r.Feeds[index]
		if index < len(h.feeds) {
			now := h.feeds[index].Now().UTC()
			switch {
			case !f.Connected:
				if f.Status != "recovering" {
					f.Status = "disconnected"
				}
			case !validPrice(f.FeedState, now, h.ttl):
				f.Status = "stale"
			case !f.AllowsEntries(now, h.ttl):
				f.Status = "recovering"
			default:
				f.Status = "ready"
			}
		}
		f.EntriesReady = f.Status == "ready"
		r.EntriesReady = r.EntriesReady && f.EntriesReady
	}
	for index := range r.Exposure {
		exposure := &r.Exposure[index]
		exposure.Monitoring = "unmonitored"
		exposure.Limitation = "no current valid price: protection and pending closes wait for an eligible observation"
		for feedIndex, f := range h.feeds {
			if f.Query.Instrument != exposure.Instrument {
				continue
			}
			state := r.Feeds[feedIndex]
			now := f.Now().UTC()
			if validPrice(state.FeedState, now, h.ttl) && !state.LastValidObservation.IsZero() && !now.Before(state.LastValidObservation) && now.Sub(state.LastValidObservation) <= h.ttl {
				exposure.Monitoring = "monitored"
				exposure.Limitation = "price-point protection only; recovered historical observations do not execute exits"
			} else if exposure.Monitoring != "monitored" {
				exposure.Monitoring = state.Status
				if state.Status == "ready" || state.Status == "recovering" {
					exposure.Monitoring = "awaiting_valid_observation"
				}
			}
		}
	}
	return r, nil
}
