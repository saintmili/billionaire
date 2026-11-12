// Package marketdata normalizes public market observations and synchronizes candles.
package marketdata

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
)

type Query struct {
	Instrument domain.Instrument
	Timeframe  domain.Timeframe
	From, To   time.Time
}

func (q Query) Validate() error {
	if err := q.Instrument.Validate(); err != nil {
		return err
	}
	if _, err := domain.ParseTimeframe(string(q.Timeframe)); err != nil {
		return err
	}
	start, _ := q.Timeframe.Bounds(q.From)
	end, _ := q.Timeframe.Bounds(q.To)
	if q.From.IsZero() || !q.To.After(q.From) || !start.Equal(q.From) || !end.Equal(q.To) {
		return errors.New("range must use aligned inclusive from/exclusive to UTC boundaries")
	}
	count := 0
	for at := q.From; at.Before(q.To); {
		_, at = q.Timeframe.Bounds(at)
		count++
		if count > 100000 {
			return errors.New("range exceeds 100000 candles; split the request")
		}
	}
	return nil
}

type InstrumentInfo struct {
	Instrument   domain.Instrument `json:"instrument"`
	PriceStep    domain.Decimal    `json:"price_step"`
	QuantityStep domain.Decimal    `json:"quantity_step"`
	MinQuantity  domain.Decimal    `json:"min_quantity"`
	MinNotional  domain.Decimal    `json:"min_notional"`
}

func (i InstrumentInfo) Validate() error {
	if err := i.Instrument.Validate(); err != nil {
		return err
	}
	if !i.PriceStep.IsPositive() || !i.QuantityStep.IsPositive() || i.MinQuantity.IsNegative() || !i.MinNotional.IsPositive() {
		return errors.New("instrument precision/minima are invalid")
	}
	return nil
}

type Provider interface {
	Instrument(context.Context, domain.MarketType, string) (InstrumentInfo, error)
	History(context.Context, Query) ([]domain.Candle, error)
	Stream(context.Context, Query, func(domain.Candle) error) error
}

type FeedState struct {
	Instrument    domain.Instrument `json:"instrument"`
	Timeframe     domain.Timeframe  `json:"timeframe"`
	Connected     bool              `json:"connected"`
	LastEvent     time.Time         `json:"last_event"`
	LastClosed    time.Time         `json:"last_closed_end"`
	LastDelivered time.Time         `json:"delivered_through"`
	Gaps          int               `json:"gap_count"`
	Reason        string            `json:"reason"`
}

func (s FeedState) AllowsEntries(now time.Time, staleAfter time.Duration) bool {
	required, _ := s.Timeframe.Bounds(now)
	return !required.IsZero() && !s.LastClosed.Before(required) && staleAfter > 0 && s.Connected && s.Gaps == 0 && !s.LastEvent.IsZero() && !s.LastClosed.IsZero() && !now.Before(s.LastEvent.Add(-30*time.Second)) && now.Sub(s.LastEvent) <= staleAfter
}

// Stamp hashes the normalized content. ObservedAt remains separate so identical
// REST/backfill and socket closes share a revision while distinct updates retain time.
func Stamp(c domain.Candle) domain.Candle {
	c.Revision = ""
	observed := c.ObservedAt
	c.ObservedAt = time.Time{}
	data, _ := json.Marshal(c)
	c.Revision = fmt.Sprintf("%x", sha256.Sum256(data))
	c.ObservedAt = observed
	return c
}
func EventID(c domain.Candle) string {
	if c.Closed {
		return c.Revision
	}
	return fmt.Sprintf("%s:%s", c.Revision, c.ObservedAt.UTC().Format(time.RFC3339Nano))
}
