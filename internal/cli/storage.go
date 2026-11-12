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
	"github.com/saintmili/billionaire/internal/config"
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
	root.AddCommand(&cobra.Command{Use: "status", Short: "Check configuration and storage readiness (engine is not yet implemented)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		result := struct {
			postgres.Status
			Mode       string `json:"mode"`
			Database   string `json:"database"`
			Engine     string `json:"engine"`
			KillSwitch bool   `json:"kill_switch"`
		}{Status: postgres.Status{RequiredVersion: postgres.LatestVersion}, Mode: "paper", Database: "unavailable", Engine: "not_implemented"}
		err = o.database(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			state, err := store.Status(ctx)
			if err != nil {
				return err
			}
			result.Status = state
			result.Database = "uninitialized"
			if state.Initialized {
				result.Database = "ready"
				on, err := store.KillSwitch(ctx)
				if err != nil {
					return err
				}
				result.KillSwitch = on
			}
			return nil
		})
		if errors.Is(err, postgres.ErrSchema) {
			result.Database = "incompatible"
		}
		if outputErr := o.write(result, fmt.Sprintf("Mode: paper\nDatabase: %s (schema %d/%d)\nEngine: not implemented\nKill switch: %t", result.Database, result.Version, result.RequiredVersion, result.KillSwitch)); outputErr != nil {
			return outputErr
		}
		if err != nil {
			return err
		}
		if !result.Initialized {
			return errors.New("database is not initialized; run millionaire init")
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
