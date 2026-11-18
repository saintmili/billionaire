// Package app supervises the single-owner foreground application runtime.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
)

type Subscription struct {
	Provider marketdata.Provider
	Now      func() time.Time
	Query    marketdata.Query
}
type Options struct {
	KeepAlive     bool
	QueueCapacity int
	StaleAfter    time.Duration
	Quantity      string
	OwnerReady    func(*postgres.PaperOwner) (func(), error)
}
type Result struct {
	Event   marketdata.Event  `json:"event"`
	Signals []strategy.Signal `json:"signals"`
}
type delivery struct {
	event        marketdata.Event
	subscription Subscription
	syncer       *marketdata.Synchronizer
	ack          chan error
}

// Run applies backpressure at admission and waits for each event's durable
// outcome before acknowledging it to its feed. Cancellation leaves unacknowledged
// closed events in the synchronizer's persistent pending checkpoint.
func Run(ctx context.Context, store *postgres.Store, feeds []Subscription, opts Options, report func(Result) error) error {
	if len(feeds) == 0 || len(feeds) > 128 || opts.QueueCapacity < 1 || opts.QueueCapacity > 4096 || opts.StaleAfter <= 0 {
		return errors.New("engine requires 1..128 subscriptions, queue capacity 1..4096 and positive stale duration")
	}
	seen := map[string]bool{}
	for _, f := range feeds {
		key := fmt.Sprint(f.Query.Instrument, f.Query.Timeframe)
		if seen[key] {
			return errors.New("engine subscription duplicates an instrument/timeframe")
		}
		seen[key] = true
		if f.Provider == nil || f.Now == nil {
			return errors.New("engine subscription requires provider and clock")
		}
		if err := f.Query.Validate(); err != nil {
			return err
		}
	}
	state, err := store.Status(ctx)
	if err != nil {
		return err
	}
	if !state.Initialized {
		return errors.New("storage requires migration; run billionaire init")
	}
	owner, err := store.AcquireEngineOwner(ctx)
	if err != nil {
		return err
	}
	defer owner.Close()
	if opts.OwnerReady != nil {
		stop, err := opts.OwnerReady(owner)
		if err != nil {
			return err
		}
		defer stop()
	}
	running, cancel := context.WithCancel(ctx)
	defer cancel()
	queue := make(chan delivery, opts.QueueCapacity)
	done := make(chan error, len(feeds))
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	registry := strategy.DefaultRegistry()
	for _, f := range feeds {
		syncer, err := marketdata.NewSynchronizer(f.Provider, store, marketdata.SyncOptions{Now: f.Now, MaxRetries: 5, StaleAfter: opts.StaleAfter})
		if err != nil {
			return err
		}
		// Validate required history for every subscription before admitting events.
		earliest, err := strategy.WarmActive(running, store, syncer, f.Query, f.Now(), nil, registry)
		if err != nil {
			return fmt.Errorf("engine warm-up: %w", err)
		}
		if !earliest.IsZero() && earliest.Before(f.Query.From) {
			f.Query.From = earliest
		}
		workers.Add(1)
		go func(f Subscription, syncer *marketdata.Synchronizer) {
			defer workers.Done()
			err := syncer.Run(running, f.Query, func(event marketdata.Event) error {
				d := delivery{event: event, subscription: f, syncer: syncer, ack: make(chan error, 1)}
				select {
				case queue <- d:
				case <-running.Done():
					return running.Err()
				}
				select {
				case err := <-d.ack:
					return err
				case <-running.Done():
					return running.Err()
				}
			})
			done <- err
		}(f, syncer)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	remaining := len(feeds)
	for remaining > 0 || opts.KeepAlive {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			check, c := context.WithTimeout(running, 2*time.Second)
			err := owner.Check(check)
			c()
			if err != nil {
				return fmt.Errorf("engine ownership lost: %w", err)
			}
		case err := <-done:
			remaining--
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return fmt.Errorf("engine feed worker failed: %w", err)
			}
		case d := <-queue:
			if err := running.Err(); err != nil {
				return nil
			}
			result, err := process(running, store, owner, d, opts, registry)
			if err == nil && report != nil {
				err = report(result)
			}
			d.ack <- err
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("engine processing failed: %w", err)
			}
		}
	}
	return nil
}

func process(ctx context.Context, store *postgres.Store, owner *postgres.PaperOwner, d delivery, opts Options, registry *strategy.Registry) (Result, error) {
	e := d.event
	result := Result{Event: e, Signals: []strategy.Signal{}}
	if err := owner.Check(ctx); err != nil {
		return result, err
	}
	if e.Candle == nil {
		return result, nil
	}
	at, err := strategy.EventTime(e)
	if err != nil {
		return result, err
	}
	if _, err := strategy.WarmActive(ctx, store, d.syncer, d.subscription.Query, at, e.Candle, registry); err != nil {
		return result, err
	}
	info, err := d.subscription.Provider.Instrument(ctx, e.Candle.Instrument.Market, e.Candle.Instrument.Base+"/"+e.Candle.Instrument.Quote)
	if err != nil {
		return result, err
	}
	// Consume only earlier intents. Recovered prices never open new positions.
	observation := paper.Observation{ID: marketdata.EventID(*e.Candle), Instrument: e.Candle.Instrument, Source: e.Candle.Source, Price: e.Candle.Close, ObservedAt: e.Candle.ObservedAt, AvailableAt: at, Ready: e.AllowsEntries && e.Kind != "recovered_candle"}
	prior, err := store.RecordedPaperObservation(ctx, observation)
	if err != nil {
		return result, err
	}
	if prior != nil {
		observation = *prior
	}
	if _, err := owner.Observe(ctx, observation, paper.ExecutionGate{Now: at, StaleAfter: opts.StaleAfter, Enabled: true}); err != nil {
		return result, err
	}
	instances, err := store.StrategyInstances(ctx)
	if err != nil {
		return result, err
	}
	for _, i := range instances {
		if !i.Enabled || i.Instrument != e.Candle.Instrument {
			continue
		}
		v, err := store.StrategyVersion(ctx, i.VersionID)
		if err != nil {
			return result, err
		}
		if v.Definition.Primary != e.Candle.Timeframe || (v.Definition.Evaluation == strategy.OnClose && !e.Candle.Closed) {
			continue
		}
		evaluation, err := owner.EvaluateStrategyEvent(ctx, i.ID, e, registry)
		if err != nil {
			return result, err
		}
		result.Signals = append(result.Signals, evaluation.Signals...)
	}
	// This also recovers a crash between persisted evaluation and intent creation.
	for {
		pending, err := store.PendingSignalEntries(ctx, info, 1000)
		if err != nil {
			return result, err
		}
		for _, signal := range pending {
			decisionAt := at
			if signal.ObservedAt.After(decisionAt) {
				decisionAt = signal.ObservedAt
			}
			_, err := owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: signal.ID, At: decisionAt, Quantity: opts.Quantity}, info, true)
			if err != nil {
				return result, err
			}
		}
		if len(pending) < 1000 {
			break
		}
	}
	return result, nil
}
