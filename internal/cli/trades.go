package cli

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
	"time"
)

func (o *options) addTradeCommands(root *cobra.Command) {
	report := &cobra.Command{Use: "report", Short: "Read financial and strategy reports", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error { return c.Help() }}
	root.AddCommand(report)
	report.AddCommand(o.tradeCommand(false))
	for _, group := range root.Commands() {
		if group.Name() == "backtest" {
			group.AddCommand(o.tradeCommand(true))
		}
	}
}
func (o *options) tradeCommand(backtest bool) *cobra.Command {
	var f analytics.Filter
	var scope, run, provider, market, symbol, from, to string
	var stale time.Duration
	cmd := &cobra.Command{Use: "trades", Short: "Read position lifecycle performance, costs and observed equity", Long: "Read-only trade performance. Partial exits count as one closed position lifecycle. Date filters select position opening instants [from,to). Account equity/ledger are not attributed to strategy or instrument subsets. Missing valuations and unsupported metrics are null with labels; no trading is replayed.", Args: cobra.NoArgs}
	if backtest {
		cmd.Use = "trades RUN-ID"
		cmd.Args = cobra.ExactArgs(1)
		scope = "backtest"
	} else {
		cmd.Flags().StringVar(&scope, "scope", "", "required backtest or forward-paper scope")
		cmd.Flags().StringVar(&run, "run", "", "persisted completed backtest run id")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if backtest {
			run = args[0]
		}
		if scope != "backtest" && scope != "forward-paper" {
			return errors.New("--scope must explicitly select backtest or forward-paper")
		}
		if scope == "backtest" && run == "" {
			return errors.New("backtest scope requires --run")
		}
		if scope == "forward-paper" && (run != "" || f.AccountID == "") {
			return errors.New("forward-paper scope requires --account and excludes --run")
		}
		var err error
		if from != "" {
			f.From, err = time.Parse(time.RFC3339, from)
			if err != nil {
				return errors.New("--from must be RFC3339")
			}
		}
		if to != "" {
			f.To, err = time.Parse(time.RFC3339, to)
			if err != nil {
				return errors.New("--to must be RFC3339")
			}
		}
		if provider != "" || market != "" || symbol != "" {
			identity, _, e := (marketFlags{provider: provider, market: market, symbol: symbol, frame: "1m"}).identity(provider)
			if e != nil {
				return e
			}
			f.Instrument = &identity
		}
		if err = f.Validate(); err != nil {
			return err
		}
		if stale <= 0 {
			return errors.New("--stale-after must be positive")
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		return o.database(cmd.Context(), cfg, func(ctx context.Context, s *postgres.Store) error {
			var r analytics.Report
			var e error
			if scope == "backtest" {
				r, e = s.BacktestTrades(ctx, run, f)
			} else {
				r, e = s.PaperTrades(ctx, f, time.Now().UTC(), stale)
			}
			if e != nil {
				return e
			}
			if o.output == "json" {
				return o.write(r, "")
			}
			b, e := json.MarshalIndent(r, "", "  ")
			if e != nil {
				return e
			}
			return o.write(r, string(b))
		})
	}
	cmd.Flags().StringVar(&f.AccountID, "account", "", "paper account id; required for forward-paper")
	cmd.Flags().StringVar(&f.StrategyVersion, "strategy-version", "", "immutable signal strategy version id (manual orders have no version)")
	cmd.Flags().StringVar(&provider, "provider", "", "instrument provider; supply with --market and --symbol")
	cmd.Flags().StringVar(&market, "market", "", "spot or linear; supply with --provider and --symbol")
	cmd.Flags().StringVar(&symbol, "symbol", "", "market-specific symbol; supply with --provider and --market")
	cmd.Flags().StringVar(&from, "from", "", "inclusive RFC3339 position opening instant")
	cmd.Flags().StringVar(&to, "to", "", "exclusive RFC3339 position opening instant")
	cmd.Flags().DurationVar(&stale, "stale-after", time.Minute, "maximum current forward-paper valuation source age")
	return cmd
}
