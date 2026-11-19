package bybit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
)

// Funding retrieves expected settlements in the inclusive window. The current
// instrument interval and ticker anchor are a declared schedule approximation,
// not evidence of historical schedule changes. Only the settlement minute's
// mark OPEN is used; its eventual high/low/close would leak future prices.
func (c *Client) Funding(ctx context.Context, i domain.Instrument, from, through time.Time) ([]paper.FundingEvent, error) {
	if i.Validate() != nil || i.Provider != "bybit" || i.Market != domain.Linear || from.IsZero() || through.Before(from) || through.After(c.opts.Now()) {
		return nil, errors.New("public funding requires a due linear Bybit interval")
	}
	c.mu.Lock()
	cached, ok := c.fundingSchedules[i.ID]
	c.mu.Unlock()
	if ok && from.After(cached.anchor.Add(-cached.interval)) && through.Before(cached.anchor) {
		return []paper.FundingEvent{}, nil
	}
	query := url.Values{"category": {"linear"}, "symbol": {i.ID}}
	var meta struct {
		Result struct {
			Category string
			List     []struct {
				Symbol, ContractType, SettleCoin, Status string
				FundingInterval                          int
			}
		}
	}
	if err := c.get(ctx, "/v5/market/instruments-info", query, &meta); err != nil {
		return nil, fmt.Errorf("funding schedule unavailable: %w", err)
	}
	if meta.Result.Category != "linear" || len(meta.Result.List) != 1 {
		return nil, errors.New("funding schedule instrument mismatch")
	}
	m := meta.Result.List[0]
	if m.Symbol != i.ID || m.ContractType != "LinearPerpetual" || m.SettleCoin != "USDT" || m.Status != "Trading" || m.FundingInterval < 1 || m.FundingInterval > 1440 {
		return nil, errors.New("unsupported public funding schedule")
	}
	var ticker struct {
		Result struct {
			Category string
			List     []struct{ Symbol, NextFundingTime string }
		}
	}
	if err := c.get(ctx, "/v5/market/tickers", query, &ticker); err != nil {
		return nil, fmt.Errorf("funding anchor unavailable: %w", err)
	}
	if ticker.Result.Category != "linear" || len(ticker.Result.List) != 1 || ticker.Result.List[0].Symbol != i.ID {
		return nil, errors.New("funding anchor instrument mismatch")
	}
	ms, err := strconv.ParseInt(ticker.Result.List[0].NextFundingTime, 10, 64)
	anchor := time.UnixMilli(ms).UTC()
	step := time.Duration(m.FundingInterval) * time.Minute
	if err != nil || ms <= 0 || !anchor.After(through) || anchor.After(c.opts.Now().Add(step+time.Minute)) || ms%60000 != 0 {
		return nil, errors.New("invalid public next funding anchor")
	}
	first := anchor.Add(-time.Duration(int64(anchor.Sub(from)/step)) * step)
	if first.Before(from) {
		first = first.Add(step)
	}
	if through.Sub(first)/step >= 10000 {
		return nil, errors.New("funding window exceeds 10000 intervals")
	}
	unit, _ := domain.ParseDecimal("1")
	c.mu.Lock()
	if c.fundingSchedules == nil {
		c.fundingSchedules = map[string]fundingSchedule{}
	}
	c.fundingSchedules[i.ID] = fundingSchedule{anchor: anchor, interval: step}
	c.mu.Unlock()
	if first.After(through) {
		return []paper.FundingEvent{}, nil
	}
	rates := map[int64]string{}
	unavailable := ""
	end := through.UnixMilli()
	for end >= from.UnixMilli() {
		var response struct {
			Result struct {
				Category string
				List     []struct{ Symbol, FundingRate, FundingRateTimestamp string }
			}
		}
		q := url.Values{"category": {"linear"}, "symbol": {i.ID}, "startTime": {strconv.FormatInt(from.UnixMilli(), 10)}, "endTime": {strconv.FormatInt(end, 10)}, "limit": {"200"}}
		if err := c.get(ctx, "/v5/market/funding/history", q, &response); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			unavailable = "public funding history unavailable"
			break
		}
		if response.Result.Category != "linear" {
			return nil, errors.New("funding history category mismatch")
		}
		if len(response.Result.List) == 0 {
			break
		}
		oldest := end + 1
		for _, row := range response.Result.List {
			at, e := strconv.ParseInt(row.FundingRateTimestamp, 10, 64)
			if e != nil || row.Symbol != i.ID || at < from.UnixMilli() || at > end {
				return nil, errors.New("invalid public funding history identity or clock")
			}
			if _, e = paper.FundingPayment(paper.Position{}, row.FundingRate, unit); e != nil {
				return nil, e
			}
			if prior, ok := rates[at]; ok && prior != row.FundingRate {
				return nil, errors.New("conflicting public funding rates")
			}
			rates[at] = row.FundingRate
			if at < oldest {
				oldest = at
			}
		}
		end = oldest - 1
	}
	events := []paper.FundingEvent{}
	for at := first; !at.After(through); at = at.Add(step) {
		event := paper.FundingEvent{ID: paper.Identity("public-funding", fmt.Sprint(i, at.UnixMilli())), Instrument: i, EffectiveAt: at, Source: "bybit", IntervalMinutes: m.FundingInterval, ScheduleAnchor: anchor, Provenance: "Bybit V5 public instruments-info fundingInterval + tickers nextFundingTime; current schedule extrapolated; funding/history; mark-price-kline 1m open; approximate isolated model"}
		rate, ok := rates[at.UnixMilli()]
		if !ok {
			event.MissingReason = "expected interval absent from public funding history"
			if unavailable != "" {
				event.MissingReason = unavailable
			}
		} else {
			var mark struct {
				Result struct {
					Category, Symbol string
					List             [][]string
				}
			}
			q := url.Values{"category": {"linear"}, "symbol": {i.ID}, "interval": {"1"}, "start": {strconv.FormatInt(at.UnixMilli(), 10)}, "end": {strconv.FormatInt(at.UnixMilli(), 10)}, "limit": {"1"}}
			err := c.get(ctx, "/v5/market/mark-price-kline", q, &mark)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil || mark.Result.Category != "linear" || mark.Result.Symbol != i.ID || len(mark.Result.List) != 1 || len(mark.Result.List[0]) != 5 || mark.Result.List[0][0] != strconv.FormatInt(at.UnixMilli(), 10) {
				event.MissingReason = "public settlement mark unavailable"
			} else {
				price, e := domain.ParseDecimal(mark.Result.List[0][1])
				if e != nil || !price.IsPositive() {
					return nil, errors.New("invalid public funding mark")
				}
				event.Price = price
				event.Rate = &rate
			}
		}
		event.AvailableAt = c.opts.Now().UTC()
		if err := event.Validate(); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}
