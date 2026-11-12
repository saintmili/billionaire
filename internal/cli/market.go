package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/saintmili/billionaire/internal/config"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/bybit"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/store/postgres"
)

type marketFlags struct {
	market, symbol, frame, fixture, from, to, provider string
	refresh                                            bool
	lookback, retries                                  int
}

func (f *marketFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.market, "market", "spot", "spot or linear")
	cmd.Flags().StringVar(&f.symbol, "symbol", "BTC/USDT", "instrument BASE/USDT")
	cmd.Flags().StringVar(&f.frame, "timeframe", "1m", "native interval or exact UTC aggregation interval")
	cmd.Flags().StringVar(&f.fixture, "fixture", "", "explicit offline dataset; never falls back from a network failure")
}
func (f marketFlags) source(cfg config.Config) (marketdata.Provider, func() time.Time, error) {
	if f.fixture != "" {
		p, err := fixture.Open(f.fixture)
		if err != nil {
			return nil, nil, err
		}
		return p, p.Now, nil
	}
	p, err := bybit.New(bybit.Options{RESTURL: cfg.MarketData.RESTURL, SpotWSURL: cfg.MarketData.SpotWSURL, LinearWSURL: cfg.MarketData.LinearWSURL})
	return p, time.Now, err
}
func (f marketFlags) identity(provider string) (domain.Instrument, domain.Timeframe, error) {
	frame, err := domain.ParseTimeframe(f.frame)
	if err != nil {
		return domain.Instrument{}, "", err
	}
	parts := strings.Split(f.symbol, "/")
	if len(parts) != 2 {
		return domain.Instrument{}, "", errors.New("symbol must be BASE/USDT")
	}
	i := domain.Instrument{Provider: provider, Market: domain.MarketType(f.market), Base: parts[0], Quote: parts[1], ID: parts[0] + parts[1]}
	return i, frame, i.Validate()
}
func (o *options) addMarketCommands(root *cobra.Command) {
	market := &cobra.Command{Use: "market", Short: "Backfill, watch and inspect public market data", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() }}
	market.AddCommand(o.backfillCommand(), o.watchCommand(), o.feedStatusCommand())
	root.AddCommand(market)
	o.addProviderCommands(root)
}

type marketRequest struct {
	provider marketdata.Provider
	now      func() time.Time
	query    marketdata.Query
}

func (f marketFlags) prepare(cfg config.Config) (marketRequest, error) {
	p, now, err := f.source(cfg)
	if err != nil {
		return marketRequest{}, err
	}
	name := "bybit"
	if f.fixture != "" {
		name = "fixture"
	}
	i, frame, err := f.identity(name)
	return marketRequest{p, now, marketdata.Query{Instrument: i, Timeframe: frame}}, err
}
func (o *options) withSynchronizer(ctx context.Context, cfg config.Config, f marketFlags, request marketRequest, operation func(context.Context, *marketdata.Synchronizer) error) error {
	return o.withDatabase(ctx, cfg, func(ctx context.Context, store *postgres.Store) error {
		state, err := store.Status(ctx)
		if err != nil {
			return err
		}
		if !state.Initialized {
			return errors.New("storage requires migration; run millionaire init")
		}
		ttl, _ := time.ParseDuration(cfg.MarketData.StaleAfter)
		syncer, err := marketdata.NewSynchronizer(request.provider, store, marketdata.SyncOptions{Now: request.now, MaxRetries: f.retries, StaleAfter: ttl})
		if err != nil {
			return err
		}
		return operation(ctx, syncer)
	})
}
func (o *options) backfillCommand() *cobra.Command {
	f := &marketFlags{}
	cmd := &cobra.Command{Use: "backfill", Short: "Persist a closed-candle range (exclusive --to)", Args: cobra.NoArgs}
	f.bind(cmd)
	cmd.Flags().StringVar(&f.from, "from", "", "inclusive RFC3339 UTC boundary")
	cmd.MarkFlagRequired("from")
	cmd.Flags().StringVar(&f.to, "to", "", "exclusive RFC3339 UTC boundary")
	cmd.MarkFlagRequired("to")
	cmd.Flags().BoolVar(&f.refresh, "refresh", false, "refetch cached candles to record corrections")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		request, err := f.prepare(cfg)
		if err != nil {
			return err
		}
		from, err := time.Parse(time.RFC3339, f.from)
		if err != nil {
			return errors.New("from must be an RFC3339 timestamp")
		}
		to, err := time.Parse(time.RFC3339, f.to)
		if err != nil {
			return errors.New("to must be an RFC3339 timestamp")
		}
		request.query.From = from.UTC()
		request.query.To = to.UTC()
		if request.query.To.After(request.now()) {
			return errors.New("to must not include future or forming candles")
		}
		if err := request.query.Validate(); err != nil {
			return err
		}
		return o.withSynchronizer(cmd.Context(), cfg, *f, request, func(ctx context.Context, syncer *marketdata.Synchronizer) error {
			result, err := syncer.Backfill(ctx, request.query, f.refresh)
			if outputErr := o.write(result, fmt.Sprintf("Candles: %d, changed: %d, missing: %d", result.Candles, result.Changed, result.Missing)); outputErr != nil {
				return outputErr
			}
			return err
		})
	}
	return cmd
}
func (o *options) watchCommand() *cobra.Command {
	f := &marketFlags{}
	cmd := &cobra.Command{Use: "watch", Short: "Warm up, subscribe and recover public candle updates", Args: cobra.NoArgs}
	f.bind(cmd)
	cmd.Flags().IntVar(&f.lookback, "lookback", 20, "closed intervals to warm up")
	cmd.Flags().IntVar(&f.retries, "retries", 5, "bounded reconnect attempts, 0..10")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		request, err := f.prepare(cfg)
		if err != nil {
			return err
		}
		if f.lookback < 1 || f.lookback > 10000 || f.retries < 0 || f.retries > 10 {
			return errors.New("lookback must be 1..10000 and retries 0..10")
		}
		request.query.To, _ = request.query.Timeframe.Bounds(request.now())
		request.query.From = request.query.To
		for range f.lookback {
			request.query.From, _ = request.query.Timeframe.Bounds(request.query.From.Add(-time.Nanosecond))
		}
		if err := request.query.Validate(); err != nil {
			return err
		}
		return o.withSynchronizer(cmd.Context(), cfg, *f, request, func(ctx context.Context, syncer *marketdata.Synchronizer) error {
			return syncer.Run(ctx, request.query, func(event marketdata.Event) error {
				return o.write(event, fmt.Sprintf("%s: %s %s, gaps=%d, automatic entries allowed=%v", event.Kind, f.symbol, f.frame, event.Feed.Gaps, event.AllowsEntries))
			})
		})
	}
	return cmd
}
func (o *options) feedStatusCommand() *cobra.Command {
	f := &marketFlags{}
	cmd := &cobra.Command{Use: "status", Short: "Read persisted feed health and freshness", Args: cobra.NoArgs}
	f.bind(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		name := "bybit"
		if f.fixture != "" {
			name = "fixture"
		}
		instrument, frame, err := f.identity(name)
		if err != nil {
			return err
		}
		ttl, _ := time.ParseDuration(cfg.MarketData.StaleAfter)
		return o.withDatabase(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			state, err := store.FeedState(ctx, instrument, frame)
			if err != nil {
				return err
			}
			result := struct {
				marketdata.FeedState
				Allows bool `json:"allows_auto_entries"`
			}{state, state.AllowsEntries(time.Now(), ttl)}
			return o.write(result, fmt.Sprintf("Feed: %s, gaps: %d, automatic entries allowed: %v", state.Reason, state.Gaps, result.Allows))
		})
	}
	return cmd
}

func (o *options) addProviderCommands(root *cobra.Command) {
	provider := &cobra.Command{Use: "provider", Short: "Inspect public market-data providers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() }}
	provider.AddCommand(&cobra.Command{Use: "list", Short: "List available read-only providers", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return o.write([]map[string]any{{"name": "bybit", "public_only": true, "markets": []string{"spot", "linear"}}, {"name": "fixture", "public_only": true, "selection": "--fixture PATH"}}, "bybit (public spot/linear)\nfixture (offline, --fixture PATH)")
	}})
	f := &marketFlags{}
	test := &cobra.Command{Use: "test", Short: "Verify public instrument metadata and one closed historical candle", Args: cobra.NoArgs}
	f.bind(test)
	test.Flags().StringVar(&f.provider, "provider", "bybit", "bybit or fixture (requires --fixture)")
	test.RunE = func(cmd *cobra.Command, args []string) error {
		if f.provider != "bybit" && f.provider != "fixture" {
			return errors.New("unsupported provider")
		}
		if f.provider == "fixture" && f.fixture == "" {
			return errors.New("fixture provider requires --fixture PATH")
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		p, now, err := f.source(cfg)
		if err != nil {
			return err
		}
		providerName := "bybit"
		if f.fixture != "" {
			providerName = "fixture"
		}
		_, frame, err := f.identity(providerName)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		info, err := p.Instrument(ctx, domain.MarketType(f.market), f.symbol)
		if err != nil {
			return err
		}
		end, _ := frame.Bounds(now())
		start, _ := frame.Bounds(end.Add(-time.Nanosecond))
		bars, err := p.History(ctx, marketdata.Query{Instrument: info.Instrument, Timeframe: frame, From: start, To: end})
		if err != nil {
			return err
		}
		if len(bars) != 1 {
			return errors.New("provider did not return the required closed candle")
		}
		return o.write(map[string]any{"ok": true, "instrument": info, "candle": bars[0]}, "Public instrument and closed historical candle verified")
	}
	provider.AddCommand(test)
	root.AddCommand(provider)
}
