// Package cli translates command inputs into configuration and storage operations.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/saintmili/billionaire/internal/config"
)

type options struct {
	configPath  string
	output      string
	out         io.Writer
	diagnostics io.Writer
	level       slog.LevelVar
}

// Execute keeps structured logs on diagnostics and command results on out.
func Execute(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	diagnostics = &synchronizedWriter{Writer: diagnostics}
	opts := &options{out: out, diagnostics: diagnostics}
	logger := slog.New(slog.NewJSONHandler(diagnostics, &slog.HandlerOptions{Level: &opts.level, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if len(groups) == 0 && a.Key == slog.TimeKey {
			return slog.Time(a.Key, a.Value.Time().In(time.UTC))
		}
		return a
	}}))
	root := &cobra.Command{Use: "billionaire", Short: "Public market monitoring and paper trading", SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs}
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(diagnostics)
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&opts.configPath, "config", "billionaire.yaml", "configuration file")
	root.PersistentFlags().StringVar(&opts.output, "output", "text", "result format: text or json")
	invoked := false
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		invoked = true
		if opts.output != "text" && opts.output != "json" {
			return errors.New("output must be text or json")
		}
		return cmd.Context().Err()
	}
	configuration := &cobra.Command{Use: "config", Short: "Manage application configuration", Args: cobra.NoArgs}
	validate := &cobra.Command{Use: "validate", Short: "Validate configuration offline", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := opts.load()
		if err != nil {
			return err
		}
		return opts.write(map[string]any{"valid": true, "mode": cfg.App.Mode, "schema_version": cfg.SchemaVersion}, "Configuration valid (paper mode)")
	}}
	configuration.AddCommand(validate)
	root.AddCommand(configuration)
	root.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
	configuration.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
	opts.addNotificationCommands(root)
	opts.addStorageCommands(root)
	root.AddCommand(opts.startCommand())
	opts.addMarketCommands(root)
	opts.addStrategyCommands(root)
	opts.addPaperCommands(root)
	opts.addBacktestCommands(root)
	opts.addTradeCommands(root)
	opts.addSignalReportCommands(root)
	opts.addComparisonCommands(root)
	err := root.ExecuteContext(ctx)
	if err != nil {
		logger.Error("command failed", "error", err.Error())
	} else if invoked {
		logger.Info("command completed")
	}
	return err
}

func (o *options) load() (config.Config, error) {
	file, err := os.Open(o.configPath)
	if err != nil {
		return config.Config{}, errors.New("config: cannot open file; run init or select a file with --config")
	}
	defer file.Close()
	cfg, err := config.Load(file, os.Getenv)
	if err != nil {
		return cfg, err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.App.LogLevel)); err != nil {
		return cfg, errors.New("app.log_level is invalid")
	}
	o.level.Set(level)
	return cfg, nil
}
func (o *options) write(result any, text string) error {
	if o.output == "json" {
		return json.NewEncoder(o.out).Encode(result)
	}
	_, err := fmt.Fprintln(o.out, text)
	return err
}

// Background terminal alerts and foreground diagnostics share a writer safely.
type synchronizedWriter struct {
	sync.Mutex
	io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.Lock()
	defer w.Unlock()
	return w.Writer.Write(p)
}
