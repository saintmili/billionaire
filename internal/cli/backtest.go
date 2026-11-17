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
	"github.com/saintmili/billionaire/internal/marketdata/bybit"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

func (o *options) addBacktestCommands(root *cobra.Command) {
	group := &cobra.Command{Use: "backtest", Short: "Run isolated deterministic backtests and inspect saved results", Args: cobra.NoArgs, RunE: func(c *cobra.Command, a []string) error { return c.Help() }}
	var selected marketFlags
	var cfg backtest.Config
	var version, frames, sourceRun string
	var cached, refresh bool
	run := &cobra.Command{Use: "run", Short: "Replay frozen public, fixture or cached historical candles", Long: "Run a spot long close-based strategy across its selected timeframes. Public history uses the shared cache/backfill boundary; --cached reads only persisted metadata and candles. Inputs, original provider provenance and results are retained. Signals execute at the next actual primary candle open. Missing warm-up always fails; --acknowledge-gaps records later gaps. OHLCV cannot reproduce update-mode strategies.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		app, e := o.load()
		if e != nil {
			return e
		}
		return o.withDatabase(cmd.Context(), app, func(ctx context.Context, s *postgres.Store) error {
			var inputs backtest.Inputs
			if sourceRun != "" {
				for _, name := range []string{"strategy", "provider", "market", "symbol", "fixture", "from", "to", "timeframes", "seed", "capital", "fee-rate", "slippage-bps", "fixed-risk", "percent-risk", "max-positions", "daily-loss", "symbol-exposure", "max-leverage", "margin-percent", "cooldown-seconds", "pyramiding", "cached", "refresh", "acknowledge-gaps", "clock-mode"} {
					if cmd.Flags().Changed(name) {
						return errors.New("--source-run cannot be combined with changed input flags")
					}
				}
				frozen, e := s.BacktestInputs(ctx, sourceRun)
				if e != nil {
					return e
				}
				inputs = frozen
			} else {
				if selected.provider != "fixture" && selected.provider != "bybit" {
					return errors.New("provider must be fixture or bybit")
				}
				if cached && (selected.fixture != "" || refresh) {
					return errors.New("--cached excludes --fixture and --refresh")
				}
				if selected.provider == "fixture" && !cached && selected.fixture == "" {
					return errors.New("fixture provider requires --fixture FILE")
				}
				if selected.provider != "fixture" && selected.fixture != "" {
					return errors.New("--fixture requires --provider fixture")
				}
				if cfg.ClockMode == "recorded-close" {
					return errors.New("historical selection supplies latest OHLCV snapshots, not recorded revisions; recorded-close requires an explicitly supplied complete closed-revision dataset")
				}
				cfg.Source = "public-history"
				if selected.provider == "fixture" {
					cfg.Source = "fixture-history"
				}
				if cached {
					cfg.Source = "cache-only"
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

				var provider marketdata.Provider
				if cached {
					selected.frame = string(cfg.Timeframes[0])
					identity, _, e := selected.identity(selected.provider)
					if e != nil {
						return e
					}
					cfg.Instrument, e = s.InstrumentInfo(ctx, identity)
					if e != nil {
						return e
					}
				} else {
					if selected.provider == "fixture" {
						provider, e = fixture.Open(selected.fixture)
					} else {
						provider, e = bybit.New(bybit.Options{RESTURL: app.MarketData.RESTURL, SpotWSURL: app.MarketData.SpotWSURL, LinearWSURL: app.MarketData.LinearWSURL})
					}
					if e != nil {
						return e
					}
					cfg.Instrument, e = provider.Instrument(ctx, domain.MarketType(selected.market), selected.symbol)
					if e != nil {
						return e
					}
				}
				inputs, e = s.SelectBacktestDataset(ctx, cfg, provider, refresh)
				if e != nil {
					return e
				}

			}
			result, e := s.RunFrozenBacktest(ctx, inputs)
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
	run.Flags().StringVar(&selected.provider, "provider", "fixture", "fixture or bybit (public read-only history)")
	run.Flags().StringVar(&selected.market, "market", "spot", "spot")
	run.Flags().StringVar(&selected.symbol, "symbol", "BTC/USDT", "instrument BASE/USDT")
	run.Flags().StringVar(&selected.fixture, "fixture", "", "offline candle fixture file")
	run.Flags().StringVar(&selected.from, "from", "", "inclusive aligned RFC3339 trading range start")
	run.Flags().StringVar(&selected.to, "to", "", "exclusive aligned RFC3339 trading range end")
	run.Flags().StringVar(&frames, "timeframes", "1m", "comma-separated intervals matching the immutable strategy version")
	run.Flags().Int64Var(&cfg.Seed, "seed", 0, "reproducibility seed; this model uses no randomness")
	run.Flags().StringVar(&cfg.InitialCapital, "capital", "10000", "initial USDT capital")
	run.Flags().StringVar(&cfg.FeeRate, "fee-rate", "0.001", "fee fraction per fill")
	run.Flags().StringVar(&cfg.SlippageBPS, "slippage-bps", "0", "adverse slippage in basis points")
	bindBacktestRisk(run, &cfg.Risk)
	run.Flags().BoolVar(&cached, "cached", false, "read cached metadata/candles only; no network")
	run.Flags().BoolVar(&refresh, "refresh", false, "refresh public/fixture history before freezing")
	run.Flags().BoolVar(&cfg.AcknowledgeGaps, "acknowledge-gaps", false, "record trading-range gaps; missing warm-up still fails")
	run.Flags().StringVar(&cfg.ClockMode, "clock-mode", "close", "close (latest OHLCV at modeled close); recorded-close requires supplied revision dataset")
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
