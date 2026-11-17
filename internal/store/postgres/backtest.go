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
	if err = cfg.Validate(v); err != nil {
		return report, err
	}
	registry := strategy.DefaultRegistry()
	warm, err := strategy.WarmupQueries(v.Definition, cfg.Instrument.Instrument, cfg.From, registry)
	if err != nil {
		return report, err
	}
	start := cfg.From
	for _, w := range warm {
		if w.Query.From.Before(start) {
			start = w.Query.From
		}
	}
	frozen := []domain.Candle{}
	for _, c := range bars {
		if c.Instrument == cfg.Instrument.Instrument && c.Timeframe == cfg.Timeframes[0] && !c.OpenTime.Before(start) && !c.EndTime.After(cfg.To) {
			frozen = append(frozen, c)
		}
	}
	sort.Slice(frozen, func(i, j int) bool { return frozen[i].OpenTime.Before(frozen[j].OpenTime) })
	if err = validateCandles(frozen); err != nil {
		return report, err
	}
	unique := []domain.Candle{}
	for _, c := range frozen {
		if len(unique) > 0 && unique[len(unique)-1].OpenTime.Equal(c.OpenTime) {
			if backtest.Hash(unique[len(unique)-1]) != backtest.Hash(c) {
				return report, errors.New("conflicting fixture candle revisions")
			}
			continue
		}
		unique = append(unique, c)
	}
	frozen = unique
	expected := start
	for _, c := range frozen {
		if !c.OpenTime.Equal(expected) {
			return report, strategy.ErrWarmup
		}
		expected = c.EndTime
	}
	if len(frozen) == 0 || !expected.Equal(cfg.To) {
		return report, strategy.ErrWarmup
	}
	in := backtest.Inputs{Config: cfg, Version: v, Candles: frozen}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return report, err
	}
	id := hex.EncodeToString(random[:])
	schema := "bt_" + id
	report = backtest.Report{ID: id, Scope: "backtest", Status: "running", Manifest: backtest.NewManifest(in), Config: cfg, Signals: []strategy.Signal{}, Decisions: []paper.SignalDecision{}, Orders: []paper.Order{}, Fills: []paper.Fill{}, Positions: []paper.Position{}, Ledger: []paper.LedgerEntry{}, Warnings: []string{"OHLCV close model; no intrabar path is inferred", "Open positions and pending intents are retained at range end; final cash is not total equity", "Fixture sample only; no statistical performance claim"}}
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
	for _, raw := range frozen {
		if err = ctx.Err(); err != nil {
			return report, err
		}
		c := raw
		c.ObservedAt = c.EndTime // virtual availability; original provenance is retained in Inputs
		if c.OpenTime.Before(cfg.From) {
			if _, err = isolated.SaveCandles(ctx, []domain.Candle{c}); err != nil {
				return report, err
			}
			continue
		}
		gate := paper.ExecutionGate{ReplayNextOpen: true, Now: c.OpenTime, StaleAfter: 24 * time.Hour, Enabled: true}
		open := paper.Observation{Phase: "next_open", ID: "open-" + c.Revision, Instrument: c.Instrument, Source: c.Source, Price: c.Open, ObservedAt: c.OpenTime, AvailableAt: c.OpenTime, Ready: true}
		if _, err = owner.Observe(ctx, open, gate); err != nil {
			return report, err
		}
		gate.Now = c.EndTime
		close := paper.Observation{ID: "close-" + c.Revision, Instrument: c.Instrument, Source: c.Source, Price: c.Close, ObservedAt: c.EndTime, AvailableAt: c.EndTime, Ready: true, Bar: &paper.PriceBar{Start: c.OpenTime, End: c.EndTime, Open: c.Open, High: c.High, Low: c.Low, Close: c.Close}}
		if _, err = owner.Observe(ctx, close, gate); err != nil {
			return report, err
		}
		if _, err = isolated.SaveCandles(ctx, []domain.Candle{c}); err != nil {
			return report, err
		}
		evaluation, e := isolated.EvaluateStrategyEvent(ctx, binding.ID, marketdata.Event{Kind: "candle", Candle: &c, AvailableAt: c.EndTime, AllowsEntries: true}, registry)
		if e != nil {
			return report, e
		}
		report.Signals = append(report.Signals, evaluation.Signals...)
		for _, signal := range evaluation.Signals {
			if _, err = owner.DecideSignal(ctx, paper.SignalEntryRequest{SignalID: signal.ID, At: c.EndTime, FeeRate: cfg.FeeRate, SlippageBPS: cfg.SlippageBPS}, cfg.Instrument, true); err != nil {
				return report, err
			}
		}
	}
	if report.Orders, err = isolated.PaperOrders(ctx, account.ID); err != nil {
		return report, err
	}
	if report.Fills, err = isolated.PaperFills(ctx, account.ID); err != nil {
		return report, err
	}
	sort.Slice(report.Fills, func(i, j int) bool { return report.Fills[i].ExecutedAt.Before(report.Fills[j].ExecutedAt) })
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
