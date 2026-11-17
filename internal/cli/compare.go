package cli

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

func (o *options) addComparisonCommands(root *cobra.Command) {
	for _, g := range root.Commands() {
		switch g.Name() {
		case "backtest":
			g.AddCommand(&cobra.Command{Use: "compare LEFT-RUN RIGHT-RUN", Short: "Compare retained runs, periods, evidence and simulation assumptions", Args: cobra.ExactArgs(2), RunE: func(c *cobra.Command, a []string) error {
				return o.readStore(c, func(ctx context.Context, s *postgres.Store) error {
					r, e := s.CompareBacktests(ctx, a[0], a[1])
					if e != nil {
						return e
					}
					return o.comparisonWrite(r)
				})
			}})
			var format, kind string
			export := &cobra.Command{Use: "export RUN-ID", Short: "Export retained report, trades or equity with exact context", Long: "Read-only export; never replay trading or generate signal reports. Includes all previously saved signal rules. CSV v1 columns: schema_version,kind,row_type,sequence,context_json,row_json. JSON cells preserve exact decimals, nulls and provenance. Empty collections emit one context row. Equity sequence preserves equal-time phases.", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, a []string) error {
				if format != "json" && format != "csv" {
					return errors.New("--format must be json or csv")
				}
				if kind != "report" && kind != "trades" && kind != "equity" {
					return errors.New("--kind must be report, trades or equity")
				}
				return o.readStore(c, func(ctx context.Context, s *postgres.Store) error {
					r, e := s.BacktestSnapshot(ctx, a[0])
					if e != nil {
						return e
					}
					return backtest.WriteExport(c.OutOrStdout(), format, kind, r)
				})
			}}
			export.Flags().StringVar(&format, "format", "json", "json or csv schema v1")
			export.Flags().StringVar(&kind, "kind", "report", "report, trades or equity")
			g.AddCommand(export)
		case "report":
			g.AddCommand(o.strategiesCommand())
		}
	}
}
func (o *options) readStore(c *cobra.Command, fn func(context.Context, *postgres.Store) error) error {
	cfg, e := o.load()
	if e != nil {
		return e
	}
	return o.database(c.Context(), cfg, fn)
}
func (o *options) comparisonWrite(r any) error {
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	return o.write(r, string(b))
}
func (o *options) strategiesCommand() *cobra.Command {
	var f postgres.StrategyReportFilter
	var scope, provider, market, symbol, frame, from, to string
	c := &cobra.Command{Use: "strategies", Short: "Read separate persisted backtest strategy results without pooling", Long: "Select explicit backtest scope. Each run retains immutable version, instrument, timeframe set, account and original date range. Date filters select overlapping runs, never truncate metrics. Returns include drawdown and closed trade sample sizes. No ranking or optimization is performed.", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error {
		if scope != "backtest" {
			return errors.New("--scope must be backtest; forward-paper strategy attribution is unavailable")
		}
		var e error
		if f.Range.From, e = optionalReportDate(from, "--from"); e != nil {
			return e
		}
		if f.Range.To, e = optionalReportDate(to, "--to"); e != nil {
			return e
		}
		if !f.Range.From.IsZero() && !f.Range.To.IsZero() && !f.Range.From.Before(f.Range.To) {
			return errors.New("--from must precede --to")
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
		return o.readStore(c, func(ctx context.Context, s *postgres.Store) error {
			r, e := s.BacktestStrategies(ctx, f)
			if e != nil {
				return e
			}
			return o.comparisonWrite(struct {
				SchemaVersion string                        `json:"schema_version"`
				Scope         string                        `json:"scope"`
				Filter        postgres.StrategyReportFilter `json:"filter"`
				Runs          []backtest.Snapshot           `json:"runs"`
				Warnings      []string                      `json:"warnings"`
			}{"1", scope, f, r, []string{"Separate full-run observations; overlapping dates do not recompute performance. Do not pool different datasets, assumptions or versions. Small samples and survivorship/look-ahead selection limit inference."}})
		})
	}}
	c.Flags().StringVar(&scope, "scope", "", "required backtest scope")
	c.Flags().StringSliceVar(&f.RunIDs, "run", nil, "saved run IDs; default all retained runs")
	c.Flags().StringVar(&f.AccountID, "account", "", "isolated account identity")
	c.Flags().StringVar(&f.StrategyVersion, "strategy-version", "", "immutable version identity")
	c.Flags().StringVar(&provider, "provider", "", "instrument provider")
	c.Flags().StringVar(&market, "market", "", "instrument market")
	c.Flags().StringVar(&symbol, "symbol", "", "instrument symbol")
	c.Flags().StringVar(&frame, "timeframe", "", "required member of run timeframe set")
	c.Flags().StringVar(&from, "from", "", "inclusive overlapping range start RFC3339")
	c.Flags().StringVar(&to, "to", "", "exclusive overlapping range end RFC3339")
	return c
}
