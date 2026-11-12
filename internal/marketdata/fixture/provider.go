// Package fixture provides deterministic, network-free market observations.
package fixture

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
)

type Dataset struct {
	Version     int                         `json:"version"`
	Instruments []marketdata.InstrumentInfo `json:"instruments"`
	Candles     []domain.Candle             `json:"candles"`
	Updates     []domain.Candle             `json:"updates"`
}
type Provider struct {
	data Dataset
	mu   sync.Mutex
	now  time.Time
}

func Open(path string) (*Provider, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open market fixture")
	}
	defer file.Close()
	bytes, err := io.ReadAll(io.LimitReader(file, (10<<20)+1))
	if err != nil || len(bytes) > 10<<20 {
		return nil, errors.New("invalid or oversized market fixture")
	}
	var data Dataset
	if json.Unmarshal(bytes, &data) != nil || data.Version != 1 || len(data.Instruments) == 0 || len(data.Candles) == 0 {
		return nil, errors.New("invalid market fixture dataset")
	}
	p := &Provider{data: data}
	for _, info := range data.Instruments {
		if info.Instrument.Provider != "fixture" {
			return nil, errors.New("fixture identities must name the fixture provider")
		}
		if err := info.Validate(); err != nil {
			return nil, err
		}
	}
	for _, bars := range [][]domain.Candle{p.data.Candles, p.data.Updates} {
		for idx, c := range bars {
			if c.Instrument.Provider != "fixture" || c.Source != "fixture" {
				return nil, errors.New("fixture candle provider/source mismatch")
			}
			c = marketdata.Stamp(c)
			if err := c.Validate(); err != nil {
				return nil, err
			}
			if c.ObservedAt.IsZero() {
				return nil, errors.New("fixture observation time is required")
			}
			bars[idx] = c
		}
	}
	for _, c := range p.data.Candles {
		if !c.Closed {
			return nil, errors.New("fixture history must be closed")
		}
		if c.ObservedAt.After(p.now) {
			p.now = c.ObservedAt
		}
	}
	return p, nil
}
func (p *Provider) Now() time.Time { p.mu.Lock(); defer p.mu.Unlock(); return p.now }
func (p *Provider) Instrument(ctx context.Context, market domain.MarketType, symbol string) (marketdata.InstrumentInfo, error) {
	if ctx.Err() != nil {
		return marketdata.InstrumentInfo{}, ctx.Err()
	}
	for _, info := range p.data.Instruments {
		if info.Instrument.Market == market && info.Instrument.Base+"/"+info.Instrument.Quote == symbol {
			return info, nil
		}
	}
	return marketdata.InstrumentInfo{}, errors.New("instrument unavailable in fixture")
}
func (p *Provider) History(ctx context.Context, q marketdata.Query) ([]domain.Candle, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	bars := []domain.Candle{}
	now := p.Now()
	seen := map[int64]domain.Candle{}
	for _, set := range [][]domain.Candle{p.data.Candles, p.data.Updates} {
		for _, c := range set {
			if c.Instrument == q.Instrument && c.Timeframe == "1m" && c.Closed && !c.OpenTime.Before(q.From) && !c.EndTime.After(q.To) && !c.EndTime.After(now) {
				seen[c.OpenTime.UnixMilli()] = c
			}
		}
	}
	for _, c := range seen {
		bars = append(bars, c)
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].OpenTime.Before(bars[j].OpenTime) })
	if q.Timeframe == "1m" {
		return bars, nil
	}
	return marketdata.Aggregate(bars, q.Timeframe)
}
func (p *Provider) Stream(ctx context.Context, q marketdata.Query, consume func(domain.Candle) error) error {
	if err := q.Validate(); err != nil {
		return err
	}
	for _, c := range p.data.Updates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if c.Instrument != q.Instrument || c.OpenTime.Before(q.From) {
			continue
		}
		p.mu.Lock()
		if c.ObservedAt.After(p.now) {
			p.now = c.ObservedAt
		}
		p.mu.Unlock()
		if err := consume(c); err != nil {
			return err
		}
	}
	return io.EOF
}
