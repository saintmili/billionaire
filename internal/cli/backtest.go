package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/spf13/cobra"
)

func (o *options) addBacktestCommands(root *cobra.Command) {
	group := &cobra.Command{Use: "backtest", Short: "Run isolated deterministic backtests and inspect saved results", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error { return c.Help() }}
	var selected marketFlags
	var cfg backtest.Config
	var version, frames, sourceRun string
	run := &cobra.Command{Use: "run", Short: "Replay a saved strategy version on a frozen spot candle fixture", Long: "Run a fixture spot long close-based single-timeframe strategy. Inputs and results are persisted in PostgreSQL. Signals execute at the next available candle open. Open positions and pending intents remain at range end. No network access is used.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		app, e := o.load()
		if e != nil {
			return e
		}
		return o.withDatabase(cmd.Context(), app, func(ctx context.Context, s *postgres.Store) error {
			var bars []domain.Candle
			if sourceRun != "" {
				for _, name := range []string{"strategy", "provider", "market", "symbol", "fixture", "from", "to", "timeframes", "seed", "capital", "fee-rate", "slippage-bps", "fixed-risk", "percent-risk", "max-positions", "daily-loss", "symbol-exposure", "max-leverage", "margin-percent", "cooldown-seconds", "pyramiding"} {
					if cmd.Flags().Changed(name) {
						return errors.New("--source-run cannot be combined with changed input flags")
					}
				}
				frozen, e := s.BacktestInputs(ctx, sourceRun)
				if e != nil {
					return e
				}
				cfg = frozen.Config
				bars = frozen.Candles
			} else {
				if selected.provider != "fixture" || selected.fixture == "" {
					return errors.New("this slice requires --provider fixture and --fixture FILE")
				}
				cfg.VersionID = version
				cfg.From, e = time.Parse(time.RFC3339, selected.from)
				if e != nil {
					return errors.New("--from must be RFC3339")
				}
				cfg.To, e = time.Parse(time.RFC3339, selected.to)
				if e != nil {
					return errors.New("--to must be RFC3339")
				}
				cfg.Timeframes = nil
				for _, f := range strings.Split(frames, ",") {
					frame, e := domain.ParseTimeframe(f)
					if e != nil {
						return e
					}
					cfg.Timeframes = append(cfg.Timeframes, frame)
				}
				p, e := fixture.Open(selected.fixture)
				if e != nil {
					return e
				}
				cfg.Instrument, e = p.Instrument(ctx, domain.MarketType(selected.market), selected.symbol)
				if e != nil {
					return e
				}
				v, e := s.StrategyVersion(ctx, cfg.VersionID)
				if e != nil {
					return e
				}
				if e = cfg.Validate(v); e != nil {
					return e
				}
				start := cfg.From
				warm, e := strategy.WarmupQueries(v.Definition, cfg.Instrument.Instrument, cfg.From, strategy.DefaultRegistry())
				if e != nil {
					return e
				}
				for _, w := range warm {
					if w.Query.From.Before(start) {
						start = w.Query.From
					}
				}
				bars, e = p.History(ctx, marketdata.Query{Instrument: cfg.Instrument.Instrument, Timeframe: cfg.Timeframes[0], From: start, To: cfg.To})
				if e != nil {
					return e
				}
			}
			result, e := s.RunBacktest(ctx, cfg, bars)
			if e != nil {
				if result.ID != "" {
					if outputErr := o.writeBacktest(result); outputErr != nil {
						return outputErr
					}
				}
				return e
			}
			return o.writeBacktest(result)
		})
	}}
	run.Flags().StringVar(&version, "strategy", "", "immutable strategy version id")
	run.Flags().StringVar(&selected.provider, "provider", "fixture", "fixture (historical providers arrive in a later slice)")
	run.Flags().StringVar(&selected.market, "market", "spot", "spot")
	run.Flags().StringVar(&selected.symbol, "symbol", "BTC/USDT", "instrument BASE/USDT")
	run.Flags().StringVar(&selected.fixture, "fixture", "", "offline candle fixture file")
	run.Flags().StringVar(&selected.from, "from", "", "inclusive aligned RFC3339 trading range start")
	run.Flags().StringVar(&selected.to, "to", "", "exclusive aligned RFC3339 trading range end")
	run.Flags().StringVar(&frames, "timeframes", "1m", "comma-separated intervals (one in this slice)")
	run.Flags().Int64Var(&cfg.Seed, "seed", 0, "reproducibility seed; this model uses no randomness")
	run.Flags().StringVar(&cfg.InitialCapital, "capital", "10000", "initial USDT capital")
	run.Flags().StringVar(&cfg.FeeRate, "fee-rate", "0.001", "fee fraction per fill")
	run.Flags().StringVar(&cfg.SlippageBPS, "slippage-bps", "0", "adverse slippage in basis points")
	bindBacktestRisk(run, &cfg.Risk)
	run.Flags().StringVar(&sourceRun, "source-run", "", "rerun exact frozen inputs from an existing run; input flags excluded")
	report := &cobra.Command{Use: "report RUN-ID", Short: "Read saved manifest, status and execution results without replay", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, e := o.load()
		if e != nil {
			return e
		}
		return o.database(cmd.Context(), cfg, func(ctx context.Context, s *postgres.Store) error {
			r, e := s.BacktestReport(ctx, args[0])
			if e != nil {
				return e
			}
			return o.writeBacktest(r)
		})
	}}
	group.AddCommand(run, report)
	root.AddCommand(group)
}
func (o *options) writeBacktest(r backtest.Report) error {
	if o.output == "json" {
		return o.write(r, "")
	}
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	return o.write(r, fmt.Sprintf("Backtest %s: %s\n%s", r.ID, r.Status, b))
}
func bindBacktestRisk(cmd *cobra.Command, p *paper.RiskPolicy) {
	cmd.Flags().StringVar(&p.FixedRisk, "fixed-risk", "", "maximum USDT risk per entry")
	cmd.Flags().StringVar(&p.PercentRisk, "percent-risk", "", "maximum account equity percent risk per entry")
	cmd.Flags().StringVar(&p.DailyLoss, "daily-loss", "", "maximum daily USDT loss")
	cmd.Flags().IntVar(&p.MaxPositions, "max-positions", 0, "maximum open positions; zero unlimited")
	cmd.Flags().StringVar(&p.SymbolExposure, "symbol-exposure", "", "maximum symbol exposure percent")
	cmd.Flags().IntVar(&p.MaxLeverage, "max-leverage", 0, "maximum entry leverage; spot always 1")
	cmd.Flags().StringVar(&p.MarginPercent, "margin-percent", "", "maximum margin usage percent")
	cmd.Flags().IntVar(&p.CooldownSeconds, "cooldown-seconds", 0, "minimum seconds between entries")
	cmd.Flags().BoolVar(&p.Pyramiding, "pyramiding", false, "allow multiple entries for one instrument")
}
