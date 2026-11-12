package strategy

import (
	"context"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"time"
)

type HistoryRepository interface {
	ActiveStrategyVersions(context.Context, domain.Instrument, domain.Timeframe) ([]Version, error)
	Candles(context.Context, marketdata.Query) ([]domain.Candle, error)
}
type HistoryBackfiller interface {
	Backfill(context.Context, marketdata.Query, bool) (marketdata.SyncResult, error)
}

// WarmActive prepares required history for enabled versions before evaluation.
// Transport callers provide repository and market-data ports; policies live here.
func WarmActive(ctx context.Context, store HistoryRepository, syncer HistoryBackfiller, q marketdata.Query, at time.Time, primaryEvent *domain.Candle, registry *Registry) (time.Time, error) {
	versions, err := store.ActiveStrategyVersions(ctx, q.Instrument, q.Timeframe)
	if err != nil {
		return time.Time{}, err
	}
	earliest := time.Time{}
	for _, version := range versions {
		queries, err := WarmupQueries(version.Definition, q.Instrument, at, registry)
		if err != nil {
			return earliest, err
		}
		for _, required := range queries {
			if primaryEvent != nil && required.Query.Timeframe == q.Timeframe {
				required.Query.To = primaryEvent.OpenTime
				required.Query.From = required.Query.To
				for range required.Count {
					required.Query.From, _ = q.Timeframe.Bounds(required.Query.From.Add(-time.Nanosecond))
				}
			}
			if required.Query.Timeframe == q.Timeframe && (earliest.IsZero() || required.Query.From.Before(earliest)) {
				earliest = required.Query.From
			}
			cached, err := store.Candles(ctx, required.Query)
			if err != nil {
				return earliest, err
			}
			if len(cached) == required.Count {
				continue
			}
			if _, err := syncer.Backfill(ctx, required.Query, false); err != nil {
				return earliest, err
			}
		}
	}
	return earliest, nil
}
