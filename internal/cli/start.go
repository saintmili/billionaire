package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/saintmili/billionaire/internal/control"
	"github.com/saintmili/billionaire/internal/marketdata"
	"os"
	"path/filepath"
	"time"

	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/notify"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

func (o *options) startCommand() *cobra.Command {
	var path string
	var hold bool
	cmd := &cobra.Command{Use: "start", Short: "Run the single-owner foreground paper engine", Long: "Run configured subscriptions, strategies and paper processing. Offline --fixture is required while the M2 actual-host validation gate remains pending.", Args: cobra.NoArgs}
	cmd.Flags().BoolVar(&hold, "hold", false, "keep the fixture owner and control socket running until interrupted")
	cmd.Flags().StringVar(&path, "fixture", "", "offline market dataset; live monitoring remains gated")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		if path == "" {
			return errors.New("live monitoring disabled: M2 actual-host acceptance remains pending; select --fixture PATH")
		}
		capacity := cfg.Engine.QueueCapacity
		if capacity == 0 {
			capacity = 64
		}
		lookback := cfg.Engine.Lookback
		if lookback == 0 {
			lookback = 20
		}
		ttl, _ := time.ParseDuration(cfg.MarketData.StaleAfter)
		feeds := []app.Subscription{}
		for _, instrument := range cfg.MarketData.Instruments {
			for _, frame := range instrument.Timeframes {
				f := marketFlags{fixture: path, market: instrument.Market, symbol: instrument.Symbol, frame: frame}
				request, err := f.prepare(cfg)
				if err != nil {
					return err
				}
				// Each subscription has its own deterministic fixture clock.
				request.query.To, _ = request.query.Timeframe.Bounds(request.now())
				request.query.From = request.query.To
				for range lookback {
					request.query.From, _ = request.query.Timeframe.Bounds(request.query.From.Add(-time.Nanosecond))
				}
				if _, err := request.provider.Instrument(cmd.Context(), domain.MarketType(instrument.Market), instrument.Symbol); err != nil {
					return fmt.Errorf("configured subscription %s %s: %w", instrument.Symbol, frame, err)
				}
				feeds = append(feeds, app.Subscription{Provider: request.provider, Now: request.now, Query: request.query})
			}
		}
		return o.withDatabase(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			if err := o.write(map[string]any{"engine": "starting", "fixture": true, "subscriptions": len(feeds), "queue_capacity": capacity, "live_monitoring": false}, fmt.Sprintf("Starting fixture engine with %d subscriptions", len(feeds))); err != nil {
				return err
			}
			err := app.Run(ctx, store, feeds, app.Options{KeepAlive: hold, QueueCapacity: capacity, StaleAfter: ttl, Quantity: cfg.Engine.Quantity, OwnerReady: func(owner *postgres.PaperOwner) (func(), error) {
				if err := os.MkdirAll(filepath.Dir(o.controlSocket), 0700); err != nil {
					return nil, err
				}
				metadata := []marketdata.InstrumentInfo{}
				for _, feed := range feeds {
					info, err := feed.Provider.Instrument(ctx, feed.Query.Instrument.Market, feed.Query.Instrument.Base+"/"+feed.Query.Instrument.Quote)
					if err != nil {
						return nil, err
					}
					metadata = append(metadata, info)
				}
				server, err := control.Listen(o.controlSocket, app.NewControl(owner, metadata, ttl))
				if err != nil {
					return nil, err
				}
				cooldown, _ := time.ParseDuration(cfg.Notifications.Cooldown)
				if err := store.SetNotificationPolicy(ctx, notify.Policy{Targets: cfg.Notifications.Targets, Cooldown: cooldown}); err != nil {
					server.Close()
					return nil, err
				}
				deliveryCtx, stopDelivery := context.WithCancel(ctx)
				deliveryDone := make(chan struct{})
				go func() {
					defer close(deliveryDone)
					notify.Run(deliveryCtx, store, o.notificationSenders(cfg), o.diagnostics)
				}()
				return func() {
					server.Close()
					stopDelivery()
					<-deliveryDone
					finalCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					if _, err := notify.Dispatch(finalCtx, store, o.notificationSenders(cfg), time.Now().UTC(), 16); err != nil {
						fmt.Fprintln(o.diagnostics, "notification final dispatch unavailable; pending work remains durable")
					}
				}, nil

			}}, func(result app.Result) error {
				return o.write(result, fmt.Sprintf("%s: %s %s, signals=%d", result.Event.Kind, result.Event.Feed.Instrument.ID, result.Event.Feed.Timeframe, len(result.Signals)))
			})
			if err != nil {
				return err
			}
			return o.write(map[string]any{"engine": "stopped", "fixture": true, "live_monitoring": false}, "Fixture engine stopped; ownership released")
		})
	}
	return cmd
}
