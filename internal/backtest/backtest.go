// Package backtest defines frozen replay inputs and persisted run results.
package backtest

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/strategy"
)

// BuildRevision may be set by release builds with -ldflags -X.
var BuildRevision = "unavailable"

const ExecutionModel = "spot-ohlcv-close-next-open-phase-v1"

type Config struct {
	VersionID      string                    `json:"strategy_version"`
	Instrument     marketdata.InstrumentInfo `json:"instrument_metadata"`
	From           time.Time                 `json:"from"`
	To             time.Time                 `json:"to"`
	Timeframes     []domain.Timeframe        `json:"timeframes"`
	Seed           int64                     `json:"seed"`
	InitialCapital string                    `json:"initial_capital"`
	FeeRate        string                    `json:"fee_rate"`
	SlippageBPS    string                    `json:"slippage_bps"`
	Risk           paper.RiskPolicy          `json:"risk"`
}
type Inputs struct {
	Config  Config           `json:"config"`
	Version strategy.Version `json:"version"`
	Candles []domain.Candle  `json:"candles"`
}
type Manifest struct {
	Build               string `json:"build"`
	StrategyHash        string `json:"strategy_hash"`
	ConfigurationHash   string `json:"configuration_hash"`
	DatasetHash         string `json:"dataset_hash"`
	ReproducibilityHash string `json:"reproducibility_hash"`
	Model               string `json:"execution_model"`
	Clock               string `json:"availability_model"`
	EndPolicy           string `json:"end_policy"`
}
type Report struct {
	Equity    []analytics.EquityPoint `json:"equity"`
	ID        string                  `json:"id"`
	AccountID string                  `json:"account_id,omitempty"`
	Scope     string                  `json:"scope"`
	Status    string                  `json:"status"`
	Error     string                  `json:"error,omitempty"`
	Manifest  Manifest                `json:"manifest"`
	Config    Config                  `json:"config"`
	Signals   []strategy.Signal       `json:"signals"`
	Decisions []paper.SignalDecision  `json:"decisions"`
	Orders    []paper.Order           `json:"orders"`
	Fills     []paper.Fill            `json:"fills"`
	Positions []paper.Position        `json:"positions"`
	Balance   domain.Decimal          `json:"final_cash_balance"`
	Ledger    []paper.LedgerEntry     `json:"ledger"`
	Warnings  []string                `json:"warnings"`
}

func Hash(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func NewManifest(in Inputs) Manifest {
	build := "revision=" + BuildRevision
	if b, ok := debug.ReadBuildInfo(); ok {
		revision, modified := BuildRevision, "unknown"
		for _, s := range b.Settings {
			if s.Key == "vcs.revision" && revision == "unavailable" {
				revision = s.Value
			}
			if s.Key == "vcs.modified" {
				modified = s.Value
			}
		}
		build = b.GoVersion + "/" + b.Main.Version + "/revision=" + revision + "/modified=" + modified
	}
	m := Manifest{Build: build, StrategyHash: in.Version.ID, ConfigurationHash: Hash(in.Config), DatasetHash: Hash(struct {
		Metadata marketdata.InstrumentInfo
		Candles  []domain.Candle
	}{in.Config.Instrument, in.Candles}), Model: ExecutionModel, Clock: "frozen OHLCV: close available at exclusive end; close phase before next open at equal boundary", EndPolicy: "retain open positions and pending intents; no synthetic final liquidation"}
	m.ReproducibilityHash = Hash(m)
	return m
}
func (c Config) Validate(v strategy.Version) error {
	if c.VersionID != v.ID || strategy.ContentHash(v.Definition) != v.ID {
		return errors.New("immutable strategy version mismatch")
	}
	if err := c.Instrument.Validate(); err != nil {
		return err
	}
	if c.Instrument.Instrument.Provider != "fixture" || c.Instrument.Instrument.Market != domain.Spot || v.Definition.Direction != strategy.Long || v.Definition.Evaluation != strategy.OnClose || len(c.Timeframes) != 1 || len(v.Definition.Timeframes) != 1 || c.Timeframes[0] != v.Definition.Primary {
		return errors.New("this slice supports fixture spot long close-based single-timeframe strategies only")
	}
	if err := v.Definition.ValidateInstance(strategy.Instance{ID: "validate", VersionID: v.ID, Account: "validate", Instrument: c.Instrument.Instrument, Action: strategy.AutoPaper}); err != nil {
		return err
	}
	if err := (marketdata.Query{Instrument: c.Instrument.Instrument, Timeframe: c.Timeframes[0], From: c.From, To: c.To}).Validate(); err != nil {
		return err
	}
	if _, err := paper.ExactValue(c.InitialCapital, true); err != nil {
		return err
	}
	if err := c.Risk.Validate(); err != nil {
		return err
	}
	_, err := paper.PrepareOrder(paper.OrderRequest{RequestID: "validate", AccountID: "validate", Action: "open", Instrument: c.Instrument.Instrument, Side: "long", Leverage: 1, Quantity: "risk", At: c.From, FeeRate: c.FeeRate, SlippageBPS: c.SlippageBPS}, c.Instrument)
	return err
}
