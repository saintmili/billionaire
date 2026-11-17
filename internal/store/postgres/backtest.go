package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/backtest"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

// RunBacktest freezes inputs before provisioning an execution namespace. Every
// financial mutation uses the same strategy/risk/execution services as paper mode.
func (s *Store) RunBacktest(ctx context.Context, cfg backtest.Config, bars []domain.Candle) (report backtest.Report, err error) {
	v, err := s.StrategyVersion(ctx, cfg.VersionID)
	if err != nil {
		return report, err
	}
	return s.RunFrozenBacktest(ctx, backtest.Inputs{Config: cfg, Version: v, Candles: bars})
}

// RunFrozenBacktest consumes the retained immutable version directly. It never
// resolves the version or history from mutable upstream registries.
func (s *Store) RunFrozenBacktest(ctx context.Context, inputs backtest.Inputs) (report backtest.Report, err error) {
	in, quality, err := backtest.Freeze(inputs.Config, inputs.Version, inputs.Candles)
	if err != nil {
		return report, err
	}
	cfg, v, frozen := in.Config, in.Version, in.Candles
	registry := strategy.DefaultRegistry()
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return report, err
	}
	id := hex.EncodeToString(random[:])
	schema := "bt_" + id
	report = backtest.Report{ID: id, Scope: "backtest", Status: "running", Manifest: backtest.NewManifest(in), Config: cfg, Signals: []strategy.Signal{}, Decisions: []paper.SignalDecision{}, Orders: []paper.Order{}, Fills: []paper.Fill{}, Positions: []paper.Position{}, Ledger: []paper.LedgerEntry{}, Warnings: []string{"OHLCV close model; no intrabar path is inferred", "Open positions and pending intents are retained at range end; final cash is not total equity", "Historical sample only; sample size and survivorship bias limit performance claims"}}
	report.CandidatesAvailable = true
	report.Candidates = []strategy.Candidate{}
	report.FundingCoverage = cfg.FundingCoverage()
	if cfg.Instrument.Instrument.Market == domain.Linear {
		report.Warnings = append(report.Warnings, "Approximate isolated liquidation uses frozen maintenance "+cfg.Liquidation.MaintenanceRate+" plus liquidation fee "+cfg.Liquidation.FeeRate+"; no venue tiers or mark/index basis", "Funding coverage: "+report.FundingCoverage.Status+"; missing funding is unknown, never an assumed zero rate")
	}
	report.Warnings = append(report.Warnings, quality...)
	report.Warnings = append(report.Warnings, "Original provider observation clocks/revisions are retained in frozen inputs; signal source_observed_at uses the explicitly modeled replay view")
	inputsJSON, _ := json.Marshal(in)
	manifestJSON, _ := json.Marshal(report.Manifest)
	resultJSON, _ := json.Marshal(report)
	if _, err = s.pool.Exec(ctx, `INSERT INTO millionaire.backtest_runs(id,execution_schema,status,inputs,manifest,report) VALUES($1,$2,$3,$4,$5,$6)`, id, schema, report.Status, inputsJSON, manifestJSON, resultJSON); err != nil {
		return report, safeError(ctx, ErrUnavailable)
	}
	// Cancellation cannot erase the run identity or masquerade as completion.
	defer func() {
		if err != nil {
			report.Status = "failed"
			report.Error = err.Error()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				report.Status = "cancelled"
			}
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		encoded, _ := json.Marshal(report)
		if _, saveErr := s.pool.Exec(cleanup, `UPDATE millionaire.backtest_runs SET status=$2,report=$3,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, id, report.Status, encoded); saveErr != nil {
			err = fmt.Errorf("run %s result persistence failed; status may remain running: %w", id, ErrUnavailable)
		}
	}()
	isolated := &Store{pool: &scopedPool{Pool: s.pool.Pool, schema: schema}}
	if err = isolated.Migrate(ctx, Up); err != nil {
		return report, err
	}
	if _, err = isolated.AddStrategyVersion(ctx, v.Definition, registry); err != nil {
		return report, err
	}
	account, err := isolated.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "backtest-" + id, Name: "Backtest", Currency: "USDT", InitialBalance: cfg.InitialCapital})
	if err != nil {
		return report, err
	}
	report.AccountID = account.ID
	if err = isolated.SetRiskPolicy(ctx, account.ID, "", cfg.Risk); err != nil {
		return report, err
	}
	binding := strategy.Instance{ID: "backtest-" + id, VersionID: v.ID, Instrument: cfg.Instrument.Instrument, Account: account.ID, Action: strategy.AutoPaper}
	if err = isolated.AddStrategyInstance(ctx, binding); err != nil {
		return report, err
	}
	if err = isolated.SetStrategyEnabled(ctx, binding.ID, true); err != nil {
		return report, err
	}
	owner, err := isolated.AcquirePaperOwner(ctx)
	if err != nil {
		return report, err
	}
	defer owner.Close()
	capture := func(at time.Time) error {
		point, e := isolated.CaptureBacktestEquity(ctx, account.ID, at)
		if e != nil {
			return e
		}
		report.Equity = append(report.Equity, point)
		return nil
	}
	if err = capture(cfg.From); err != nil {
		return report, err
	}

	type replayEvent struct {
		at      time.Time
		open    bool
		raw     domain.Candle
		funding *paper.FundingEvent
	}
	events := []replayEvent{}
	for _, raw := range frozen {
		if at := backtest.Availability(cfg, raw); !at.After(cfg.To) {
			events = append(events, replayEvent{at: at, raw: raw})
		} else {
			report.Warnings = append(report.Warnings, "Publication after range retained as provenance only: "+string(raw.Timeframe)+" "+raw.OpenTime.Format(time.RFC3339))
		}
		if raw.Timeframe == v.Definition.Primary && !raw.OpenTime.Before(cfg.From) {
			events = append(events, replayEvent{at: raw.OpenTime, open: true, raw: raw})
		}
	}
	for n := range cfg.Funding.Events {
		e := cfg.Funding.Events[n]
		events = append(events, replayEvent{at: e.AvailableAt, funding: &e})
	}
	sort.Slice(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if !a.at.Equal(b.at) {
			return a.at.Before(b.at)
		}
		if (a.funding != nil) != (b.funding != nil) {
			return a.funding != nil
		}
		if a.funding != nil {
			return a.funding.ID < b.funding.ID
		}
		if a.open != b.open {
			return !a.open
		}
		if a.open && !a.raw.ObservedAt.Equal(b.raw.ObservedAt) {
			return a.raw.ObservedAt.Before(b.raw.ObservedAt)
		}
		if a.raw.Timeframe != b.raw.Timeframe {
			return a.raw.Timeframe < b.raw.Timeframe
		}
		if !a.raw.OpenTime.Equal(b.raw.OpenTime) {
			return a.raw.OpenTime.Before(b.raw.OpenTime)
		}
		return a.raw.Revision < b.raw.Revision
	})
	settled := map[time.Time]bool{}
	evaluated := map[time.Time]bool{}
	opened := map[time.Time]bool{}
	for i := 0; i < len(events); {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		j := i + 1
		for j < len(events) && events[j].at.Equal(events[i].at) {
			j++
		}
		// Publication phase: every completed timeframe is visible before evaluation.
		for _, ev := range events[i:j] {
			if ev.open || ev.funding != nil {
				continue
			}
			c := ev.raw
			c.ObservedAt = ev.at
			if _, err = isolated.SaveCandles(ctx, []domain.Candle{c}); err != nil {
				return report, err
			}
		}
		for _, ev := range events[i:j] {
			if ev.open || ev.funding != nil || ev.raw.Timeframe != v.Definition.Primary || ev.raw.OpenTime.Before(cfg.From) || settled[ev.raw.OpenTime] {
				continue
			}
			settled[ev.raw.OpenTime] = true
			c := ev.raw
			c.ObservedAt = ev.at
			gate := paper.ExecutionGate{ReplayNextOpen: true, ReplayProtectionOnly: true, Now: ev.at, StaleAfter: 24 * time.Hour, Enabled: true}
			close := paper.Observation{Phase: "completed_interval", ID: "close-" + c.Revision, Instrument: c.Instrument, Source: c.Source, Price: c.Close, ObservedAt: ev.at, AvailableAt: ev.at, Ready: true, Bar: &paper.PriceBar{Start: c.OpenTime, End: c.EndTime, Open: c.Open, High: c.High, Low: c.Low, Close: c.Close}}
			if _, err = owner.Observe(ctx, close, gate); err != nil {
				return report, err
			}
		}
		// Funding points follow the interval ending here, before close decisions and opens.
		for _, ev := range events[i:j] {
			if ev.funding != nil {
				if _, err = owner.ApplyReplayFunding(ctx, *ev.funding); err != nil {
					return report, err
				}
				point := paper.Observation{ID: "funding-mark-" + ev.funding.ID, Instrument: ev.funding.Instrument, Source: ev.funding.Instrument.Provider, Price: ev.funding.Price, ObservedAt: ev.at, AvailableAt: ev.at, Ready: true}
				if ev.funding.Rate != nil {
					if _, err = owner.Observe(ctx, point, paper.ExecutionGate{ReplayNextOpen: true, ReplayProtectionOnly: true, Now: ev.at, StaleAfter: 24 * time.Hour, Enabled: true}); err != nil {
						return report, err
					}
				}
				if err = capture(ev.at); err != nil {
					return report, err
				}
			}
		}
		for _, ev := range events[i:j] {
			if ev.open || ev.funding != nil || ev.raw.Timeframe != v.Definition.Primary || ev.raw.OpenTime.Before(cfg.From) || evaluated[ev.raw.OpenTime] {
				continue
			}
			evaluated[ev.raw.OpenTime] = true
			c := ev.raw
			c.ObservedAt = ev.at
			closePoint := paper.Observation{ID: "close-point-" + c.Revision, Instrument: c.Instrument, Source: c.Source, Price: c.Close, ObservedAt: ev.at, AvailableAt: ev.at, Ready: true}
			if _, err = owner.Observe(ctx, closePoint, paper.ExecutionGate{ReplayNextOpen: true, ReplayProtectionOnly: true, Now: ev.at, StaleAfter: 24 * time.Hour, Enabled: true}); err != nil {
				return report, err
			}
			evaluation, e := isolated.EvaluateStrategyEvent(ctx, binding.ID, marketdata.Event{Kind: "candle", Candle: &c, AvailableAt: ev.at, AllowsEntries: true}, registry)
			if e != nil {
				return report, e
			}
			if err = capture(ev.at); err != nil {
				return report, err
			}
			if !evaluation.Ready {
				report.Warnings = append(report.Warnings, "Strategy unready at "+ev.at.Format(time.RFC3339)+": "+evaluation.Reason)
			}
			report.Candidates = append(report.Candidates, evaluation.Candidates...)
			report.Signals = append(report.Signals, evaluation.Signals...)
			for _, signal := range evaluation.Signals {
				var liquidation *paper.LiquidationModel
				if cfg.Instrument.Instrument.Market == domain.Linear {
					liquidation = &cfg.Liquidation
				}
				if _, err = owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: signal.ID, At: ev.at, Leverage: cfg.EffectiveLeverage(), Protection: &cfg.Protection, Liquidation: liquidation, FeeRate: cfg.FeeRate, SlippageBPS: cfg.SlippageBPS}, cfg.Instrument, true); err != nil {
					return report, err
				}
			}
		}
		// Only actual primary opens drive pending fills. Corrections never fabricate
		// another open, evaluate a past primary event or apply its interval twice.
		for _, ev := range events[i:j] {
			if !ev.open || opened[ev.raw.OpenTime] {
				continue
			}
			opened[ev.raw.OpenTime] = true
			c := ev.raw
			gate := paper.ExecutionGate{ReplayNextOpen: true, Now: ev.at, StaleAfter: 24 * time.Hour, Enabled: true}
			open := paper.Observation{Phase: "next_open", ID: "open-" + c.Revision, Instrument: c.Instrument, Source: c.Source, Price: c.Open, ObservedAt: ev.at, AvailableAt: ev.at, Ready: true}
			if _, err = owner.Observe(ctx, open, gate); err != nil {
				return report, err
			}
			if err = capture(ev.at); err != nil {
				return report, err
			}
		}
		i = j
	}
	if report.Funding, err = isolated.PaperFunding(ctx); err != nil {
		return report, err
	}
	if report.Orders, err = isolated.PaperOrders(ctx, account.ID); err != nil {
		return report, err
	}
	if report.Fills, err = isolated.PaperFills(ctx, account.ID); err != nil {
		return report, err
	}
	fillPhase := map[string]int{}
	for _, b := range frozen {
		fillPhase["close-"+b.Revision] = 0
		fillPhase["open-"+b.Revision] = 2
	}
	for _, e := range cfg.Funding.Events {
		fillPhase[paper.Identity("funding-price", e.ID)] = 1
	}
	sort.SliceStable(report.Fills, func(i, j int) bool {
		a, b := report.Fills[i], report.Fills[j]
		if !a.ExecutedAt.Equal(b.ExecutedAt) {
			return a.ExecutedAt.Before(b.ExecutedAt)
		}
		if fillPhase[a.ObservationID] != fillPhase[b.ObservationID] {
			return fillPhase[a.ObservationID] < fillPhase[b.ObservationID]
		}
		if !a.Price.Equal(b.Price) {
			return a.Price.LessThan(b.Price)
		}
		return a.Quantity.LessThan(b.Quantity)
	})
	if report.Positions, err = isolated.PaperPositions(ctx, account.ID); err != nil {
		return report, err
	}
	if report.Decisions, err = isolated.SignalDecisions(ctx); err != nil {
		return report, err
	}
	if report.Ledger, err = isolated.PaperLedger(ctx, account.ID); err != nil {
		return report, err
	}
	account, err = isolated.PaperAccount(ctx, account.ID)
	if err != nil {
		return report, err
	}
	report.Balance = account.Balance
	for _, o := range report.Orders {
		if o.Status == "pending" {
			report.Warnings = append(report.Warnings, "Unfilled end-of-range intent: "+o.ID)
		}
	}
	report.Status = "completed"
	return report, nil
}

// BacktestInputs returns exact frozen provenance/lookback without consulting the
// mutable provider store. Missing or corrupted inputs fail explicitly.
func (s *Store) BacktestInputs(ctx context.Context, id string) (backtest.Inputs, error) {
	in, e := decodePaperRow[backtest.Inputs](s.pool.QueryRow(ctx, "SELECT inputs FROM millionaire.backtest_runs WHERE id=$1", id))
	if errors.Is(e, pgx.ErrNoRows) {
		return in, errors.New("backtest run not found")
	}
	if e != nil {
		return in, safeError(ctx, ErrUnavailable)
	}
	saved, e := s.BacktestReport(ctx, id)
	if e != nil {
		return in, e
	}
	expected := backtest.NewManifest(in)
	if expected.DatasetHash != saved.Manifest.DatasetHash || expected.ConfigurationHash != saved.Manifest.ConfigurationHash || strategy.ContentHash(in.Version.Definition) != saved.Manifest.StrategyHash {
		return in, errors.New("frozen backtest inputs unavailable or corrupted")
	}
	return in, nil
}
func (s *Store) BacktestReport(ctx context.Context, id string) (backtest.Report, error) {
	report, e := decodePaperRow[backtest.Report](s.pool.QueryRow(ctx, "SELECT report FROM millionaire.backtest_runs WHERE id=$1", id))
	if errors.Is(e, pgx.ErrNoRows) {
		return report, errors.New("backtest run not found")
	}
	if e != nil {
		return report, safeError(ctx, ErrUnavailable)
	}
	return report, nil
}
