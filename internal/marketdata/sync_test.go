package marketdata_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
)

type offlineProvider struct {
	marketdata.Provider
	disableHistory bool
}

func (p *offlineProvider) History(ctx context.Context, q marketdata.Query) ([]domain.Candle, error) {
	if p.disableHistory {
		return nil, errors.New("provider unavailable")
	}
	return p.Provider.History(ctx, q)
}

func TestSynchronizationCachesAndPersistsFixtureUpdatesOnce(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	source, err := fixture.Open("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	remote := &offlineProvider{Provider: source}
	syncer, err := marketdata.NewSynchronizer(remote, store, marketdata.SyncOptions{Now: source.Now, MaxRetries: 1, Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	info, err := source.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: from, To: from.Add(time.Hour)}
	result, err := syncer.Backfill(ctx, q, false)
	if err != nil || result.Candles != 60 || result.Changed != 60 {
		t.Fatalf("backfill: %+v %v", result, err)
	}
	remote.disableHistory = true
	result, err = syncer.Backfill(ctx, q, false)
	if err != nil || result.Changed != 0 {
		t.Fatalf("cache unavailable offline: %+v %v", result, err)
	}
	remote.disableHistory = false
	candles := 0
	allowed := false
	if err := syncer.Run(ctx, q, func(event marketdata.Event) error {
		if event.Candle != nil {
			candles++
			allowed = allowed || event.AllowsEntries
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if candles != 2 || !allowed {
		t.Fatalf("duplicate/partial handling: events=%d allowed=%v", candles, allowed)
	}
	q.To = from.Add(61 * time.Minute)
	bars, err := store.Candles(ctx, q)
	if err != nil || len(bars) != 61 {
		t.Fatalf("partial/duplicate persistence: %d %v", len(bars), err)
	}
	state, err := store.FeedState(ctx, q.Instrument, q.Timeframe)
	if err != nil || state.Connected || state.AllowsEntries(source.Now(), time.Minute) {
		t.Fatalf("completed fixture left a live feed: %+v %v", state, err)
	}
}
func TestFreshnessAndGapsGateFutureEntries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 1, 10, 0, time.UTC)
	state := marketdata.FeedState{Timeframe: "1m", Connected: true, LastEvent: now, LastClosed: now.Add(-10 * time.Second)}
	if !state.AllowsEntries(now, time.Minute) {
		t.Fatal("fresh complete feed blocked")
	}
	if state.AllowsEntries(now.Add(time.Minute), time.Minute) {
		t.Fatal("missing close accepted")
	}
	state.Gaps = 1
	if state.AllowsEntries(now, time.Minute) {
		t.Fatal("gapped feed accepted")
	}
	state.Gaps = 0
	state.Connected = false
	if state.AllowsEntries(now, time.Minute) {
		t.Fatal("disconnected feed accepted")
	}
}

func TestDerivedStreamFreshnessUsesSourceUpdates(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	provider, err := fixture.Open("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := provider.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	syncer, err := marketdata.NewSynchronizer(provider, store, marketdata.SyncOptions{Now: provider.Now})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := marketdata.Query{Instrument: info.Instrument, Timeframe: "15m", From: start, To: start.Add(time.Hour)}
	fresh := false
	candles := 0
	if err := syncer.Run(ctx, q, func(event marketdata.Event) error {
		fresh = fresh || event.AllowsEntries
		if event.Candle != nil {
			candles++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Fatal("active lower-interval observations did not refresh the derived feed")
	}
	if candles != 0 {
		t.Fatal("incomplete target bucket was emitted")
	}
}

func TestFailedConsumerReplaysStoredUnacknowledgedClose(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	provider, err := fixture.Open("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := provider.Instrument(ctx, domain.Spot, "BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	syncer, err := marketdata.NewSynchronizer(provider, store, marketdata.SyncOptions{Now: provider.Now})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := marketdata.Query{Instrument: info.Instrument, Timeframe: "1m", From: start, To: start.Add(time.Hour)}
	failure := errors.New("consumer unavailable")
	failedID := ""
	err = syncer.Run(ctx, q, func(event marketdata.Event) error {
		if event.Candle != nil && event.Candle.Closed {
			failedID = marketdata.EventID(*event.Candle)
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("consumer failure lost: %v", err)
	}
	state, err := store.FeedState(ctx, q.Instrument, q.Timeframe)
	if err != nil || !state.LastDelivered.Equal(q.To) {
		t.Fatalf("failed delivery was acknowledged: %+v %v", state, err)
	}
	replayed := 0
	if err := syncer.Run(ctx, q, func(event marketdata.Event) error {
		if event.Candle != nil && event.Candle.Closed {
			replayed++
			if event.Kind != "recovered_candle" || event.AllowsEntries || marketdata.EventID(*event.Candle) != failedID {
				t.Error("unacknowledged replay was not stable and gated")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if replayed != 1 {
		t.Fatalf("unacknowledged close not replayed once: %d", replayed)
	}
}

func TestFailedOlderCorrectionReplaysIndependentlyOfEndTime(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../testdata/market/m2.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset fixture.Dataset
	if json.Unmarshal(raw, &dataset) != nil {
		t.Fatal("invalid test dataset")
	}
	next := dataset.Updates[2]
	correction := dataset.Candles[59]
	correction.Close = correction.High
	correction.ObservedAt = next.ObservedAt.Add(10 * time.Second)
	dataset.Updates = []domain.Candle{next, correction}
	raw, err = json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "correction.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := fixture.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	syncer, err := marketdata.NewSynchronizer(provider, store, marketdata.SyncOptions{Now: provider.Now})
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	q := marketdata.Query{Instrument: correction.Instrument, Timeframe: "1m", From: from, To: from.Add(time.Hour)}
	failure := errors.New("correction consumer unavailable")
	failedID := ""
	err = syncer.Run(ctx, q, func(event marketdata.Event) error {
		if event.Candle != nil && event.Candle.OpenTime.Equal(correction.OpenTime) {
			failedID = marketdata.EventID(*event.Candle)
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("correction failure lost: %v", err)
	}
	state, err := store.FeedState(ctx, q.Instrument, q.Timeframe)
	if err != nil || !state.LastDelivered.After(correction.EndTime) {
		t.Fatal("test did not advance beyond the corrected candle")
	}
	replayed := 0
	err = syncer.Run(ctx, q, func(event marketdata.Event) error {
		if event.Candle != nil && marketdata.EventID(*event.Candle) == failedID {
			replayed++
			if event.Kind != "recovered_candle" || event.AllowsEntries {
				t.Error("pending correction replay was not gated")
			}
		}
		return nil
	})
	if err != nil || replayed != 1 {
		t.Fatalf("older unacknowledged correction was lost: count=%d err=%v", replayed, err)
	}
}
