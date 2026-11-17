package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
)

// InstrumentInfo reads cached metadata without constructing a provider.
func (s *Store) InstrumentInfo(ctx context.Context, i domain.Instrument) (marketdata.InstrumentInfo, error) {
	var payload []byte
	var info marketdata.InstrumentInfo
	e := s.pool.QueryRow(ctx, `SELECT metadata FROM millionaire.market_instruments WHERE provider=$1 AND market=$2 AND instrument_id=$3 AND base=$4 AND quote=$5`, i.Provider, i.Market, i.ID, i.Base, i.Quote).Scan(&payload)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && len(payload) == 0 {
		return info, errors.New("cached instrument metadata unavailable")
	}
	if e != nil {
		return info, safeError(ctx, ErrUnavailable)
	}
	if json.Unmarshal(payload, &info) != nil {
		return info, ErrSchema
	}
	return info, info.Validate()
}

// SelectBacktestDataset acquires through the shared historical synchronizer, or
// reads only PostgreSQL when provider is nil. Replay consumes the returned frozen
// values, never the cache. Source observations remain untouched.
func (s *Store) SelectBacktestDataset(ctx context.Context, cfg backtest.Config, p marketdata.Provider, refresh bool) (backtest.Inputs, error) {
	v, e := s.StrategyVersion(ctx, cfg.VersionID)
	if e != nil {
		return backtest.Inputs{}, e
	}
	queries, e := backtest.Queries(cfg, v)
	if e != nil {
		return backtest.Inputs{}, e
	}
	if p == nil {
		metadata, e := s.InstrumentInfo(ctx, cfg.Instrument.Instrument)
		if e != nil {
			return backtest.Inputs{}, e
		}
		cfg.Instrument = metadata
	}
	var syncer *marketdata.Synchronizer
	if p != nil {
		syncer, e = marketdata.NewSynchronizer(p, s, marketdata.SyncOptions{MaxRetries: 2})
		if e != nil {
			return backtest.Inputs{}, e
		}
	}
	bars := []domain.Candle{}
	for _, q := range queries {
		if syncer != nil {
			_, e = syncer.Backfill(ctx, q, refresh)
			var gap *marketdata.GapError
			if e != nil && !(cfg.AcknowledgeGaps && errors.As(e, &gap)) {
				return backtest.Inputs{}, e
			}
		}
		selected, e := s.Candles(ctx, q)
		if e != nil {
			return backtest.Inputs{}, e
		}
		bars = append(bars, selected...)
	}
	in, _, e := backtest.Freeze(cfg, v, bars)
	return in, e
}
