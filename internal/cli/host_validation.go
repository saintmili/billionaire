package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/bybit"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

type hostValidation struct {
	SchemaVersion  int               `json:"schema_version"`
	Host           string            `json:"host"`
	HostRole       string            `json:"host_role"`
	Started        time.Time         `json:"started_at"`
	Finished       time.Time         `json:"finished_at"`
	Command        string            `json:"command"`
	Endpoints      map[string]string `json:"public_endpoints"`
	Passed         bool              `json:"checks_passed"`
	Gate           string            `json:"m2_actual_host_gate"`
	LiveMonitoring bool              `json:"live_monitoring_enabled"`
	Error          string            `json:"error,omitempty"`
	Checks         []hostStreamCheck `json:"checks"`
}
type hostStreamCheck struct {
	Instrument           domain.Instrument          `json:"instrument"`
	Timeframe            domain.Timeframe           `json:"timeframe"`
	From                 time.Time                  `json:"history_from"`
	To                   time.Time                  `json:"history_to"`
	Stage                string                     `json:"stage"`
	Error                string                     `json:"error,omitempty"`
	Metadata             *marketdata.InstrumentInfo `json:"metadata,omitempty"`
	History              int                        `json:"persisted_history_candles"`
	Sample               *domain.Candle             `json:"history_sample,omitempty"`
	DuplicateVerified    bool                       `json:"duplicate_write_unchanged"`
	PartialSample        *domain.Candle             `json:"partial_sample,omitempty"`
	ClosedSample         *domain.Candle             `json:"closed_sample,omitempty"`
	StreamPersisted      bool                       `json:"stream_close_persisted"`
	Partial              int                        `json:"partial_updates"`
	Closed               int                        `json:"live_closed_updates"`
	Recovered            int                        `json:"recovered_closes"`
	Connections          int                        `json:"stream_connections"`
	ForcedDisconnect     bool                       `json:"forced_disconnect"`
	StaleRejected        bool                       `json:"synthetic_stale_rejected"`
	GapRejected          bool                       `json:"synthetic_gap_rejected"`
	DisconnectedRejected bool                       `json:"disconnected_rejected"`
	Passed               bool                       `json:"passed"`
}

func (o *options) hostValidationCommand() *cobra.Command {
	var allow bool
	var duration time.Duration
	var role string
	cmd := &cobra.Command{Use: "validate-host", Short: "Opt-in bounded public REST/stream/PostgreSQL evidence; never enables monitoring", Args: cobra.NoArgs}
	cmd.Flags().BoolVar(&allow, "allow-network", false, "explicitly permit public read-only network checks")
	cmd.Flags().DurationVar(&duration, "duration", 12*time.Minute, "total deadline, including all eight parallel checks (positive, at most 30m)")
	cmd.Flags().StringVar(&role, "host-role", "local", "local or intended-runtime; neither automatically accepts M2")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !allow {
			return errors.New("host validation requires --allow-network")
		}
		if duration <= 0 || duration > 30*time.Minute {
			return errors.New("duration must be positive and at most 30m")
		}
		if role != "local" && role != "intended-runtime" {
			return errors.New("host-role must be local or intended-runtime")
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		host, _ := os.Hostname()
		report := hostValidation{SchemaVersion: 1, Host: host, HostRole: role, Started: time.Now().UTC(), Gate: "pending", Command: fmt.Sprintf("billionaire market validate-host --allow-network --host-role %s --duration %s --output json", role, duration), Endpoints: map[string]string{"rest": cfg.MarketData.RESTURL, "spot_ws": cfg.MarketData.SpotWSURL, "linear_ws": cfg.MarketData.LinearWSURL}}
		ctx, cancel := context.WithTimeout(cmd.Context(), duration)
		defer cancel()
		provider, err := bybit.New(bybit.Options{RESTURL: cfg.MarketData.RESTURL, SpotWSURL: cfg.MarketData.SpotWSURL, LinearWSURL: cfg.MarketData.LinearWSURL})
		if err == nil {
			err = o.withDatabase(ctx, cfg, func(ctx context.Context, store *postgres.Store) error {
				status, e := store.Status(ctx)
				if e != nil {
					return e
				}
				if !status.Initialized {
					return errors.New("storage requires migration")
				}
				for _, market := range []domain.MarketType{domain.Spot, domain.Linear} {
					for _, base := range []string{"BTC", "ETH"} {
						for _, frame := range []domain.Timeframe{"1m", "3m"} {
							report.Checks = append(report.Checks, hostStreamCheck{Instrument: domain.Instrument{Provider: "bybit", Market: market, Base: base, Quote: "USDT", ID: base + "USDT"}, Timeframe: frame, Stage: "pending"})
						}
					}
				}
				ttl, _ := time.ParseDuration(cfg.MarketData.StaleAfter)
				var group sync.WaitGroup
				for index := range report.Checks {
					group.Add(1)
					go func(index int) {
						defer group.Done()
						validateHostStream(ctx, provider, store, ttl, &report.Checks[index])
					}(index)
				}
				group.Wait()
				return nil
			})
		}
		report.Passed = err == nil && len(report.Checks) == 8
		for _, check := range report.Checks {
			report.Passed = report.Passed && check.Passed
		}
		if err != nil {
			report.Error = hostCheckError(err)
		}
		report.Finished = time.Now().UTC()
		if e := o.write(report, fmt.Sprintf("Host checks passed: %t; M2 gate pending; live monitoring disabled (use --output json for evidence)", report.Passed)); e != nil {
			return e
		}
		if !report.Passed {
			return errors.New("host validation incomplete or failed; inspect structured evidence; M2 gate pending")
		}
		return nil
	}
	return cmd
}

// Only allowlisted diagnostic forms reach evidence; never echo provider bodies,
// database diagnostics, environment values, or arbitrary wrapped transport errors.
func hostCheckError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	text := err.Error()
	if regexp.MustCompile(`^public market request: HTTP [0-9]{3}$|^Bybit public error: retCode=[0-9]+$`).MatchString(text) {
		return text
	}
	switch text {
	case "public market request failed", "public WebSocket connection failed", "provider clock drift exceeds 30s", "storage requires migration":
		return text
	}
	return "check failed (details withheld; inspect stage and counts)"
}

// The wrapper deliberately interrupts the first connection after a close. Waiting
// longer than its interval guarantees a missed close for the real REST backfill.
// This is an injected outage, not evidence of a spontaneous provider outage.
type interruptionProbe struct {
	marketdata.Provider
	check *hostStreamCheck
}

func (p *interruptionProbe) Stream(ctx context.Context, q marketdata.Query, consume func(domain.Candle) error) error {
	p.check.Connections++
	injected := false
	err := p.Provider.Stream(ctx, q, func(c domain.Candle) error {
		if err := consume(c); err != nil {
			return err
		}
		if p.check.Connections == 1 && c.Closed {
			injected = true
			return errors.New("host validation injected disconnect")
		}
		return nil
	})
	if injected {
		p.check.ForcedDisconnect = true
		start, end := q.Timeframe.Bounds(time.Now())
		timer := time.NewTimer(end.Sub(start) + 3*time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func validateHostStream(ctx context.Context, provider marketdata.Provider, store *postgres.Store, ttl time.Duration, check *hostStreamCheck) {
	fail := func(err error) { check.Error = hostCheckError(err) }
	check.Stage = "metadata"
	info, err := provider.Instrument(ctx, check.Instrument.Market, check.Instrument.Base+"/USDT")
	if err != nil {
		fail(err)
		return
	}
	if info.Instrument != check.Instrument {
		fail(errors.New("metadata mismatch"))
		return
	}
	check.Metadata = &info
	check.To, _ = check.Timeframe.Bounds(time.Now())
	check.From = check.To
	for range 3 {
		check.From, _ = check.Timeframe.Bounds(check.From.Add(-time.Nanosecond))
	}
	q := marketdata.Query{Instrument: check.Instrument, Timeframe: check.Timeframe, From: check.From, To: check.To}
	probe := &interruptionProbe{Provider: provider, check: check}
	syncer, _ := marketdata.NewSynchronizer(probe, store, marketdata.SyncOptions{MaxRetries: 2, StaleAfter: ttl})
	check.Stage = "history_persistence"
	if _, err = syncer.Backfill(ctx, q, true); err != nil {
		fail(err)
		return
	}
	bars, err := store.Candles(ctx, q)
	if err != nil {
		fail(err)
		return
	}
	check.History = len(bars)
	if len(bars) != 3 {
		fail(errors.New("history incomplete"))
		return
	}
	check.Sample = &bars[0]
	check.Stage = "duplicate_persistence"
	changed, err := store.SaveCandles(ctx, bars)
	if err != nil {
		fail(err)
		return
	}
	check.DuplicateVerified = changed == 0
	if !check.DuplicateVerified {
		fail(errors.New("duplicate changed candles"))
		return
	}
	check.Stage = "stream_reconnect_backfill"
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	completed := false
	err = syncer.Run(runCtx, q, func(event marketdata.Event) error {
		if event.Candle == nil {
			return nil
		}
		if event.Kind == "recovered_candle" {
			check.Recovered++
		} else if event.Candle.Closed {
			check.Closed++
			if check.ClosedSample == nil {
				copy := *event.Candle
				check.ClosedSample = &copy
			}
		} else {
			check.Partial++
			if check.PartialSample == nil {
				copy := *event.Candle
				check.PartialSample = &copy
			}
		}
		if event.Feed.AllowsEntries(time.Now(), ttl) {
			stale := event.Feed
			stale.LastEvent = time.Now().Add(-ttl - time.Second)
			check.StaleRejected = !stale.AllowsEntries(time.Now(), ttl)
			gap := event.Feed
			gap.Gaps = 1
			check.GapRejected = !gap.AllowsEntries(time.Now(), ttl)
		}
		if check.Connections >= 2 && check.Recovered > 0 && check.Closed > 0 && check.Partial > 0 && check.StaleRejected && check.GapRejected && event.Kind == "candle" && !event.Candle.Closed {
			completed = true
			cancel()
		}
		return nil
	})
	if !completed {
		if err == nil {
			err = errors.New("stream evidence incomplete")
		}
		fail(err)
		return
	}
	// Run clears connection state synchronously on exit; read through the repository.
	cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	state, err := store.FeedState(cleanup, check.Instrument, check.Timeframe)
	if err != nil {
		fail(err)
		return
	}
	if state.PendingClosed != nil {
		fail(errors.New("unacknowledged close remains"))
		return
	}
	if check.ClosedSample != nil {
		closedQuery := q
		closedQuery.From, closedQuery.To = check.ClosedSample.OpenTime, check.ClosedSample.EndTime
		saved, e := store.Candles(cleanup, closedQuery)
		if e != nil {
			fail(e)
			return
		}
		// A later provider correction may replace content, but identity and the closed
		// boundary must still be retrievable through the persistence interface.
		check.StreamPersisted = len(saved) == 1 && saved[0].Closed && saved[0].Instrument == q.Instrument && saved[0].Timeframe == q.Timeframe
	}
	check.DisconnectedRejected = !state.Connected && !state.AllowsEntries(time.Now(), ttl)
	check.Passed = check.DisconnectedRejected && check.ForcedDisconnect && check.StreamPersisted
	if !check.Passed {
		fail(errors.New("disconnect state invalid"))
		return
	}
	check.Stage = "complete"
}
