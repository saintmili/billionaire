package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/saintmili/billionaire/configs"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/config"
	"github.com/saintmili/billionaire/internal/control"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/store/postgres"
)

func (o *options) addStorageCommands(root *cobra.Command) {
	root.AddCommand(&cobra.Command{Use: "init", Short: "Create a paper config if missing and apply database migrations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		created, err := createConfig(o.configPath)
		if err != nil {
			return err
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		return o.database(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			if err := store.Migrate(ctx, postgres.Up); err != nil {
				return err
			}
			state, err := store.Status(ctx)
			if err != nil {
				return err
			}
			return o.write(map[string]any{"initialized": state.Initialized, "schema_version": state.Version, "config_created": created, "mode": "paper"}, "Initialized paper-only storage")
		})
	}})
	root.AddCommand(&cobra.Command{Use: "status", Short: "Check configuration, storage readiness and engine ownership", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		result := struct {
			postgres.Status
			Mode               string              `json:"mode"`
			Database           string              `json:"database"`
			Engine             string              `json:"engine"`
			KillSwitch         bool                `json:"kill_switch"`
			ObservedAt         time.Time           `json:"observed_at"`
			EngineConnectivity string              `json:"engine_connectivity"`
			Health             *app.HealthSnapshot `json:"health"`
		}{Status: postgres.Status{RequiredVersion: postgres.LatestVersion}, Mode: "paper", Database: "unavailable", Engine: "unavailable"}
		result.ObservedAt = time.Now().UTC()
		result.EngineConnectivity = "unavailable"
		if snapshot, e := control.ReadHealth(cmd.Context(), o.controlSocket); snapshot != nil {
			result.Health = snapshot
			if e == nil {
				result.EngineConnectivity = "connected"
			}
		}
		err = o.database(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			state, err := store.Status(ctx)
			if err != nil {
				return err
			}
			result.Status = state
			if !state.Initialized {
				result.Health = nil
				result.EngineConnectivity = "unavailable"
			}
			result.Database = "uninitialized"
			if state.Initialized {
				result.Database = "ready"
				running, err := store.EngineRunning(ctx)
				if err != nil {
					return err
				}
				result.Engine = "stopped"
				if running {
					result.Engine = "running"
				}
				on, err := store.KillSwitch(ctx)
				if err != nil {
					return err
				}
				result.KillSwitch = on
				counts, e := store.OperationalCounts(ctx)
				if e != nil {
					return e
				}
				if result.Health != nil && (!running || result.Health.Counts == nil || result.Health.Counts.DatabaseIdentity != counts.DatabaseIdentity) {
					result.Health = nil
					result.EngineConnectivity = "unavailable_or_configuration_mismatch"
				}
				if result.Health == nil {
					result.Health = &app.HealthSnapshot{ObservedAt: time.Now().UTC(), Database: "ready", Ownership: "none", Counts: counts, Feeds: []app.FeedHealth{}, Exposure: []app.ExposureHealth{}, JournalDelivery: "not_implemented_pending_M7"}
					if running {
						result.Health.Ownership = "held_health_unavailable"
					}
					for _, instrument := range cfg.MarketData.Instruments {
						for _, frame := range instrument.Timeframes {
							f := marketFlags{market: instrument.Market, symbol: instrument.Symbol, frame: frame}
							i, tf, e := f.identity("bybit")
							if e != nil {
								return e
							}
							result.Health.Feeds = append(result.Health.Feeds, app.FeedHealth{FeedState: marketdata.FeedState{Instrument: i, Timeframe: tf}, Status: "unmonitored"})
						}
					}
					positions, e := store.PaperPositions(ctx, "")
					if e != nil {
						return e
					}
					for _, p := range positions {
						if p.Status == "open" {
							result.Health.Exposure = append(result.Health.Exposure, app.ExposureHealth{PositionID: p.ID, Instrument: p.Instrument, Monitoring: "unmonitored", Limitation: "no authoritative owner health; valid ongoing observations are unconfirmed"})
						}
					}
				}
			}
			return nil
		})
		if err != nil && result.Health != nil {
			result.Health.Database = "unavailable"
			result.Health.Counts = nil
			result.Health.EntriesReady = false
		}
		if errors.Is(err, postgres.ErrSchema) {
			result.Database = "incompatible"
		}
		if outputErr := o.write(result, fmt.Sprintf("Mode: paper\nDatabase: %s (schema %d/%d)\nEngine: %s\nKill switch: %t\nOwner health: %s\nEntries ready: %t", result.Database, result.Version, result.RequiredVersion, result.Engine, result.KillSwitch, result.EngineConnectivity, result.Health != nil && result.Health.EntriesReady)+healthText(result.Health)); outputErr != nil {
			return outputErr
		}
		if err != nil {
			return err
		}
		if !result.Initialized {
			return errors.New("database is not initialized; run billionaire init")
		}
		return nil
	}})
	database := &cobra.Command{Use: "database", Short: "Manage application-owned storage", Args: cobra.NoArgs}
	database.AddCommand(&cobra.Command{Use: "migrate up|down", Short: "Apply pending migrations or roll back the last version", ValidArgs: []string{"up", "down"}, Args: cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		return o.database(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			if err := store.Migrate(ctx, postgres.Direction(args[0])); err != nil {
				return err
			}
			state, err := store.Status(ctx)
			if err != nil {
				return err
			}
			return o.write(state, fmt.Sprintf("Database schema version %d", state.Version))
		})
	}})
	database.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
	root.AddCommand(database)
}

func (o *options) database(ctx context.Context, cfg config.Config, operation func(context.Context, *postgres.Store) error) error {
	timeout, _ := time.ParseDuration(cfg.Storage.Timeout)
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return o.withDatabase(bounded, cfg, operation)
}

// withDatabase bounds connection setup but leaves long-running watch operations
// under the caller's cancellation context rather than the short storage timeout.
func (o *options) withDatabase(ctx context.Context, cfg config.Config, operation func(context.Context, *postgres.Store) error) error {
	dsn, err := cfg.DatabaseURL(os.Getenv)
	if err != nil {
		return err
	}
	timeout, _ := time.ParseDuration(cfg.Storage.Timeout)
	connecting, cancel := context.WithTimeout(ctx, timeout)
	store, err := postgres.Open(connecting, dsn)
	cancel()
	if err != nil {
		return err
	}
	defer store.Close()
	return operation(ctx, store)
}

func createConfig(path string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return false, errors.New("config: cannot create parent directory")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("config: cannot create file")
	}
	_, writeErr := file.WriteString(configs.Example)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(path)
		return false, errors.New("config: cannot finish writing file")
	}
	return true, nil
}

func healthText(h *app.HealthSnapshot) string {
	if h == nil {
		return "\nOperational aggregates: unknown"
	}
	text := fmt.Sprintf("\nHealth observed: %s; ownership: %s", h.ObservedAt.Format(time.RFC3339Nano), h.Ownership)
	for _, f := range h.Feeds {
		text += fmt.Sprintf("\n%s %s %s: %s, connected=%t gaps=%d last_processed=%s", f.Instrument.Market, f.Instrument.ID, f.Timeframe, f.Status, f.Connected, f.Gaps, f.LastProcessed.Format(time.RFC3339Nano))
	}
	for _, p := range h.Exposure {
		text += fmt.Sprintf("\nPosition %s: %s — %s", p.PositionID, p.Monitoring, p.Limitation)
	}
	if c := h.Counts; c != nil {
		text += fmt.Sprintf("\nActive instances: %d; signals: %d; open positions: %d; pending orders: %d\nBacktests running/completed/failed/cancelled: %d/%d/%d/%d\nNotification failures: %d; pending/inflight: %d\nJournal pending: %d (delivery pending M7)", c.ActiveInstances, c.Signals, c.OpenPositions, c.PendingOrders, c.BacktestsRunning, c.BacktestsCompleted, c.BacktestsFailed, c.BacktestsCancelled, c.NotificationFailures, c.NotificationsPending, c.JournalPending)
	} else {
		text += "\nOperational aggregates: unknown"
	}
	return text
}
