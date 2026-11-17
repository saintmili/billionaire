package cli

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
	"time"
)

func (o *options) addSignalReportCommands(root *cobra.Command) {
	for _, group := range root.Commands() {
		if group.Name() == "report" {
			group.AddCommand(o.signalReportCommand(false))
		}
		if group.Name() == "backtest" {
			group.AddCommand(o.signalReportCommand(true))
		}
	}
}
func (o *options) signalReportCommand(backtest bool) *cobra.Command {
	var f analytics.SignalFilter
	var rule analytics.SuccessRule
	var scope, run, saved, provider, market, symbol, frame, from, to string
	cmd := &cobra.Command{Use: "signals", Short: "Persist signal behavior and hypothetical success under an explicit rule", Long: "Count distinct entry signals grouped by market-specific instrument, timeframe and immutable strategy version. Success is hypothetical: proposed price, percentage target/stop, and subsequent full closed OHLCV bars. Ambiguous, missing and incomplete observations remain unknown. Reports persist the selected rule and evidence hash without rerunning trading. Use --saved ID to read an existing immutable report.", Args: cobra.NoArgs}
	if backtest {
		cmd.Use = "signals RUN-ID"
		cmd.Args = cobra.ExactArgs(1)
		scope = "backtest"
	} else {
		cmd.Flags().StringVar(&scope, "scope", "", "required backtest or forward-paper scope")
		cmd.Flags().StringVar(&run, "run", "", "completed backtest run id")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if backtest {
			run = args[0]
		}
		if saved != "" {
			for _, name := range []string{"scope", "run", "account", "strategy-version", "provider", "market", "symbol", "timeframe", "from", "to", "threshold-percent", "stop-percent", "window-bars", "observation-basis"} {
				if cmd.Flags().Changed(name) {
					return errors.New("--saved excludes changed report selection and rule flags")
				}
			}
		} else {
			if err := validateReportScope(scope, run, f.AccountID); err != nil {
				return err
			}
			if err := rule.Validate(); err != nil {
				return err
			}
			var e error
			if f.From, e = optionalReportDate(from, "--from"); e != nil {
				return e
			}
			if f.To, e = optionalReportDate(to, "--to"); e != nil {
				return e
			}
			if frame != "" {
				f.Timeframe, e = domain.ParseTimeframe(frame)
				if e != nil {
					return e
				}
			}
			if f.Instrument, e = optionalReportInstrument(provider, market, symbol); e != nil {
				return e
			}
			if e = f.Validate(); e != nil {
				return e
			}
		}
		cfg, e := o.load()
		if e != nil {
			return e
		}
		return o.database(cmd.Context(), cfg, func(ctx context.Context, s *postgres.Store) error {
			var r analytics.SignalReport
			var e error
			if saved != "" {
				r, e = s.SignalReport(ctx, saved)
				if e == nil && backtest && r.RunID != run {
					return errors.New("saved report does not belong to requested run")
				}
			} else if scope == "backtest" {
				r, e = s.BacktestSignals(ctx, run, f, rule)
			} else {
				r, e = s.PaperSignals(ctx, f, rule, time.Now().UTC())
			}
			if e != nil {
				return e
			}
			b, e := json.MarshalIndent(r, "", "  ")
			if e != nil {
				return e
			}
			return o.write(r, string(b))
		})
	}
	cmd.Flags().StringVar(&saved, "saved", "", "read persisted report id without evaluating observations")
	cmd.Flags().StringVar(&f.AccountID, "account", "", "paper account; required for forward-paper")
	cmd.Flags().StringVar(&f.StrategyVersion, "strategy-version", "", "immutable strategy version")
	cmd.Flags().StringVar(&provider, "provider", "", "instrument provider")
	cmd.Flags().StringVar(&market, "market", "", "spot or linear")
	cmd.Flags().StringVar(&symbol, "symbol", "", "market-specific symbol")
	cmd.Flags().StringVar(&frame, "timeframe", "", "signal timeframe")
	cmd.Flags().StringVar(&from, "from", "", "inclusive RFC3339 signal time")
	cmd.Flags().StringVar(&to, "to", "", "exclusive RFC3339 signal time; observations may follow")
	cmd.Flags().StringVar(&rule.ThresholdPercent, "threshold-percent", "", "required hypothetical favorable move percent")
	cmd.Flags().StringVar(&rule.StopPercent, "stop-percent", "", "required hypothetical adverse move percent")
	cmd.Flags().IntVar(&rule.WindowBars, "window-bars", 0, "required number of subsequent full closed bars")
	cmd.Flags().StringVar(&rule.Basis, "observation-basis", "subsequent-closed-ohlcv", "observation model: subsequent-closed-ohlcv")
	return cmd
}
