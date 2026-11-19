// Package bybit implements only public Bybit V5 market endpoints.
package bybit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
)

type Options struct {
	RESTURL, SpotWSURL, LinearWSURL         string
	HTTPClient                              *http.Client
	Now                                     func() time.Time
	PageSize                                int
	RequestInterval, Heartbeat, ReadTimeout time.Duration
}
type fundingSchedule struct {
	anchor   time.Time
	interval time.Duration
}
type Client struct {
	fundingSchedules map[string]fundingSchedule
	opts             Options
	http             *http.Client
	mu               sync.Mutex
	next             time.Time
}

func New(opts Options) (*Client, error) {
	if opts.RESTURL == "" {
		opts.RESTURL = "https://api.bybit.com"
	}
	if opts.SpotWSURL == "" {
		opts.SpotWSURL = "wss://stream.bybit.com/v5/public/spot"
	}
	if opts.LinearWSURL == "" {
		opts.LinearWSURL = "wss://stream.bybit.com/v5/public/linear"
	}
	for _, item := range []struct{ value, scheme, path string }{{opts.RESTURL, "https", ""}, {opts.SpotWSURL, "wss", "/v5/public/spot"}, {opts.LinearWSURL, "wss", "/v5/public/linear"}} {
		u, err := url.Parse(item.value)
		if err != nil {
			return nil, errors.New("invalid public provider URL")
		}
		loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
		localScheme := "http"
		if item.scheme == "wss" {
			localScheme = "ws"
		}
		if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Scheme != item.scheme && !(loopback && u.Scheme == localScheme)) || (item.path != "" && u.Path != item.path) || (item.path == "" && u.Path != "" && u.Path != "/") {
			return nil, errors.New("provider URLs must select public market endpoints without credentials")
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PageSize == 0 {
		opts.PageSize = 1000
	}
	if opts.PageSize < 1 || opts.PageSize > 1000 {
		return nil, errors.New("page size must be 1..1000")
	}
	if opts.RequestInterval <= 0 {
		opts.RequestInterval = 200 * time.Millisecond
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 20 * time.Second
	}
	if opts.ReadTimeout <= 0 {
		opts.ReadTimeout = 45 * time.Second
	}
	client := http.Client{Timeout: 15 * time.Second}
	if opts.HTTPClient != nil {
		client = *opts.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{opts: opts, http: &client}, nil
}
func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (c *Client) rateLimit(ctx context.Context) error {
	c.mu.Lock()
	at := time.Now()
	if c.next.After(at) {
		at = c.next
	}
	c.next = at.Add(c.opts.RequestInterval)
	c.mu.Unlock()
	return pause(ctx, time.Until(at))
}
func (c *Client) get(ctx context.Context, path string, query url.Values, dest any) error {
	for attempt := 0; attempt < 4; attempt++ {
		if err := c.rateLimit(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.opts.RESTURL, "/")+path+"?"+query.Encode(), nil)
		if err != nil {
			return errors.New("invalid public request")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("public market request failed")
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
		resp.Body.Close()
		if readErr != nil || len(data) > 2<<20 {
			return errors.New("invalid or oversized public response")
		}
		var envelope struct {
			Code *int  `json:"retCode"`
			Time int64 `json:"time"`
		}
		decodeErr := json.Unmarshal(data, &envelope)
		limited := resp.StatusCode == 429 || (decodeErr == nil && envelope.Code != nil && *envelope.Code == 10006)
		if limited && attempt < 3 {
			if err := pause(ctx, time.Duration(1<<attempt)*200*time.Millisecond+time.Duration(rand.IntN(100))*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("public market request: HTTP %d", resp.StatusCode)
		}
		if decodeErr != nil || envelope.Code == nil {
			return errors.New("invalid Bybit response envelope")
		}
		if *envelope.Code != 0 {
			return fmt.Errorf("Bybit public error: retCode=%d", *envelope.Code)
		}
		if err := c.checkClock(envelope.Time); err != nil {
			return err
		}
		if json.Unmarshal(data, dest) != nil {
			return errors.New("invalid Bybit response fields")
		}
		return nil
	}
	return errors.New("public rate limit exhausted")
}
func (c *Client) checkClock(ms int64) error {
	delta := c.opts.Now().Sub(time.UnixMilli(ms))
	if ms <= 0 || delta > 30*time.Second || delta < -30*time.Second {
		return errors.New("provider clock drift exceeds 30s")
	}
	return nil
}

func (c *Client) Instrument(ctx context.Context, market domain.MarketType, symbol string) (marketdata.InstrumentInfo, error) {
	var info marketdata.InstrumentInfo
	parts := strings.Split(symbol, "/")
	if len(parts) != 2 {
		return info, errors.New("symbol must be BASE/USDT")
	}
	info.Instrument = domain.Instrument{Provider: "bybit", Market: market, Base: parts[0], Quote: parts[1], ID: parts[0] + parts[1]}
	if err := info.Instrument.Validate(); err != nil {
		return info, err
	}
	var response struct {
		Result struct {
			Category string
			List     []struct {
				Symbol, BaseCoin, QuoteCoin, SettleCoin, Status, ContractType string
				PriceFilter                                                   struct{ TickSize string }
				LotSizeFilter                                                 struct{ BasePrecision, QtyStep, MinOrderQty, MinNotionalValue, MinOrderAmt string }
			}
		}
	}
	if err := c.get(ctx, "/v5/market/instruments-info", url.Values{"category": {string(market)}, "symbol": {info.Instrument.ID}}, &response); err != nil {
		return info, err
	}
	if response.Result.Category != string(market) || len(response.Result.List) != 1 {
		return info, errors.New("instrument metadata does not match request")
	}
	meta := response.Result.List[0]
	if meta.Symbol != info.Instrument.ID || meta.BaseCoin != parts[0] || meta.QuoteCoin != "USDT" || meta.Status != "Trading" || (market == domain.Linear && (meta.ContractType != "LinearPerpetual" || meta.SettleCoin != "USDT")) {
		return info, errors.New("instrument is not an active spot/USDT perpetual market")
	}
	step, minQty, minValue := meta.LotSizeFilter.QtyStep, meta.LotSizeFilter.MinOrderQty, meta.LotSizeFilter.MinNotionalValue
	if market == domain.Spot {
		step = meta.LotSizeFilter.BasePrecision
		minQty = "0"
		minValue = meta.LotSizeFilter.MinOrderAmt
	}
	values := []string{meta.PriceFilter.TickSize, step, minQty, minValue}
	targets := []*domain.Decimal{&info.PriceStep, &info.QuantityStep, &info.MinQuantity, &info.MinNotional}
	for i, text := range values {
		v, err := domain.ParseDecimal(text)
		if err != nil {
			return info, errors.New("invalid instrument precision/minima")
		}
		*targets[i] = v
	}
	return info, info.Validate()
}

var intervals = map[domain.Timeframe]string{"1m": "1", "3m": "3", "5m": "5", "15m": "15", "30m": "30", "1h": "60", "2h": "120", "4h": "240", "6h": "360", "12h": "720", "1d": "D", "1w": "W", "1M": "M"}

func Native(frame domain.Timeframe) bool { _, ok := intervals[frame]; return ok }
func (c *Client) History(ctx context.Context, q marketdata.Query) ([]domain.Candle, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	info, err := c.Instrument(ctx, q.Instrument.Market, q.Instrument.Base+"/"+q.Instrument.Quote)
	if err != nil {
		return nil, err
	}
	if info.Instrument != q.Instrument {
		return nil, errors.New("history instrument mismatch")
	}
	if !Native(q.Timeframe) {
		base := q
		base.Timeframe = "1m"
		bars, err := c.History(ctx, base)
		if err != nil {
			return nil, err
		}
		return marketdata.Aggregate(bars, q.Timeframe)
	}
	end := q.To.UnixMilli() - 1
	byOpen := map[int64]domain.Candle{}
	for end >= q.From.UnixMilli() {
		var response struct {
			Time   int64
			Result struct {
				Category, Symbol string
				List             [][]string
			}
		}
		query := url.Values{"category": {string(q.Instrument.Market)}, "symbol": {q.Instrument.ID}, "interval": {intervals[q.Timeframe]}, "start": {strconv.FormatInt(q.From.UnixMilli(), 10)}, "end": {strconv.FormatInt(end, 10)}, "limit": {strconv.Itoa(c.opts.PageSize)}}
		if err := c.get(ctx, "/v5/market/kline", query, &response); err != nil {
			return nil, err
		}
		if response.Result.Category != string(q.Instrument.Market) || response.Result.Symbol != q.Instrument.ID {
			return nil, errors.New("history response instrument mismatch")
		}
		if len(response.Result.List) == 0 {
			break
		}
		oldest := end + 1
		for _, row := range response.Result.List {
			if len(row) != 7 {
				return nil, errors.New("invalid candle row width")
			}
			ms, err := strconv.ParseInt(row[0], 10, 64)
			if err != nil || ms < q.From.UnixMilli() || ms > end {
				return nil, errors.New("history row outside requested page")
			}
			if ms < oldest {
				oldest = ms
			}
			at := time.UnixMilli(ms).UTC()
			start, finish := q.Timeframe.Bounds(at)
			if !at.Equal(start) {
				return nil, errors.New("unaligned provider candle")
			}
			bar, err := parseCandle(q, at, finish, row[1:6], finish.UnixMilli() <= response.Time, time.UnixMilli(response.Time).UTC())
			if err != nil {
				return nil, err
			}
			if !bar.Closed {
				continue
			}
			if previous, exists := byOpen[ms]; exists && previous.Revision != bar.Revision {
				return nil, errors.New("conflicting duplicate historical candle")
			}
			byOpen[ms] = bar
		}
		if oldest > end {
			return nil, errors.New("history pagination did not progress")
		}
		end = oldest - 1
	}
	bars := make([]domain.Candle, 0, len(byOpen))
	for _, bar := range byOpen {
		bars = append(bars, bar)
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].OpenTime.Before(bars[j].OpenTime) })
	return bars, nil
}
func parseCandle(q marketdata.Query, start, end time.Time, values []string, closed bool, observed time.Time) (domain.Candle, error) {
	c := domain.Candle{Instrument: q.Instrument, Timeframe: q.Timeframe, OpenTime: start, EndTime: end, Closed: closed, Source: "bybit", ObservedAt: observed}
	if observed.Before(start) {
		return c, errors.New("candle starts after provider observation time")
	}
	targets := []*domain.Decimal{&c.Open, &c.High, &c.Low, &c.Close, &c.BaseVolume}
	if len(values) != len(targets) {
		return c, errors.New("invalid candle fields")
	}
	for i, text := range values {
		value, err := domain.ParseDecimal(text)
		if err != nil {
			return c, errors.New("invalid candle decimal")
		}
		*targets[i] = value
	}
	c = marketdata.Stamp(c)
	return c, c.Validate()
}
