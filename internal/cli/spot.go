package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/saintmili/billionaire/internal/config"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

type spotFixture struct {
	Metadata    marketdata.InstrumentInfo `json:"metadata"`
	Observation paper.Observation         `json:"observation"`
}

func readSpotFixture(path string) (spotFixture, error) {
	var fixture spotFixture
	file, err := os.Open(path)
	if err != nil {
		return fixture, errors.New("cannot open paper fixture")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return fixture, errors.New("paper fixture is unreadable or exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fixture) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return fixture, errors.New("invalid paper fixture JSON")
	}
	if fixture.Metadata.Instrument.Provider != "fixture" {
		return fixture, errors.New("offline paper fixtures require fixture provider metadata")
	}
	if err := fixture.Metadata.Validate(); err != nil {
		return fixture, err
	}
	return fixture, nil
}

func spotEnabled(cfg config.Config, info marketdata.InstrumentInfo) bool {
	for _, instrument := range cfg.MarketData.Instruments {
		if instrument.Market == string(info.Instrument.Market) && instrument.Symbol == info.Instrument.Base+"/"+info.Instrument.Quote {
			return true
		}
	}
	return false
}

func (o *options) addSpotCommands(group *cobra.Command) {
	o.addProtectionCommand(group)
	for _, action := range []string{"open", "close"} {
		var r paper.OrderRequest
		var fixturePath, at, market, symbol string
		use := action
		args := cobra.NoArgs
		if action == "close" {
			use = "close <position-id>"
			args = cobra.ExactArgs(1)
		}
		cmd := &cobra.Command{Use: use, Short: "Submit an offline spot market intent (waits for a later observation)", Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			timestamp, err := time.Parse(time.RFC3339Nano, at)
			if err != nil {
				return errors.New("at must be an explicit RFC3339 intent timestamp")
			}
			r.At = timestamp
			r.Action = action
			cfg, err := o.load()
			if err != nil {
				return err
			}
			return o.paperDatabaseWithConfig(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
				var info marketdata.InstrumentInfo
				if action == "open" {
					fixture, err := readSpotFixture(fixturePath)
					if err != nil {
						return err
					}
					info = fixture.Metadata
					if string(info.Instrument.Market) != market || info.Instrument.Base+"/"+info.Instrument.Quote != symbol {
						return errors.New("requested market/symbol does not match fixture metadata")
					}
				} else {
					position, err := store.PaperPosition(ctx, args[0])
					if err != nil {
						return err
					}
					r.PositionID = position.ID
					r.AccountID = position.AccountID
					info = position.Metadata
				}
				r.Instrument = info.Instrument
				owner, err := store.AcquirePaperOwner(ctx)
				if err != nil {
					return err
				}
				defer owner.Close()
				order, err := owner.SubmitSpotOrder(ctx, r, info, spotEnabled(cfg, info))
				if err != nil {
					return err
				}
				if err := o.write(order, fmt.Sprintf("Order %s: %s %s", order.ID, order.Status, order.Reason)); err != nil {
					return err
				}
				if order.Status == "rejected" {
					return errors.New(order.Reason)
				}
				return nil
			})
		}}
		cmd.Flags().StringVar(&r.RequestID, "request-id", "", "stable order request ID; reuse with identical inputs and intent time")
		cmd.Flags().StringVar(&r.Quantity, "quantity", "", "positive base quantity, or risk for policy sizing on entry")
		cmd.Flags().StringVar(&at, "at", "", "explicit RFC3339 intent timestamp for offline execution")
		cmd.Flags().StringVar(&r.FeeRate, "fee-rate", "0.001", "taker fee fraction, from 0 to 1, frozen with the order")
		cmd.Flags().StringVar(&r.SlippageBPS, "slippage-bps", "0", "adverse slippage basis points, from 0 to below 10000")
		r.Side = "long"
		r.Leverage = 1
		for _, flag := range []string{"request-id", "quantity", "at"} {
			_ = cmd.MarkFlagRequired(flag)
		}
		if action == "open" {
			cmd.Flags().StringVar(&r.TargetPrice, "take-profit", "", "initial long target above fill price")
			cmd.Flags().StringVar(&r.TargetQuantity, "target-quantity", "", "one-time partial target quantity; default all remaining holdings")
			cmd.Flags().StringVar(&r.TrailActivation, "trail-activation", "", "absolute trailing activation price")
			cmd.Flags().StringVar(&r.TrailDistance, "trail-distance", "", "absolute trailing price distance")
			cmd.Flags().StringVar(&r.StopPrice, "stop-price", "", "initial protective stop below fill price; also used for risk sizing")
			cmd.Flags().StringVar(&r.StrategyID, "strategy", "", "optional strategy definition scope")
			cmd.Flags().StringVar(&r.SignalID, "signal", "", "optional originating signal ID")
			cmd.Flags().StringVar(&r.AccountID, "account", "", "existing paper account ID")
			cmd.Flags().StringVar(&r.Side, "side", "long", "spot holdings direction (long only)")
			cmd.Flags().IntVar(&r.Leverage, "leverage", 1, "spot leverage (1 only)")
			cmd.Flags().StringVar(&market, "market", "spot", "market (spot only in this slice)")
			cmd.Flags().StringVar(&symbol, "symbol", "BTC/USDT", "BASE/USDT instrument")
			cmd.Flags().StringVar(&fixturePath, "fixture", "", "offline JSON instrument metadata; observations are applied separately")
			_ = cmd.MarkFlagRequired("account")
			_ = cmd.MarkFlagRequired("fixture")
		}
		group.AddCommand(cmd)
	}
	var fixturePath, at string
	observe := &cobra.Command{Use: "observe", Short: "Apply one explicit offline price observation atomically", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		fixture, err := readSpotFixture(fixturePath)
		if err != nil {
			return err
		}
		if fixture.Observation.Instrument != fixture.Metadata.Instrument {
			return errors.New("observation does not match fixture metadata")
		}
		now, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return errors.New("at must be an explicit RFC3339 processing timestamp")
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		staleAfter, _ := time.ParseDuration(cfg.MarketData.StaleAfter)
		return o.paperDatabaseWithConfig(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			owner, err := store.AcquirePaperOwner(ctx)
			if err != nil {
				return err
			}
			defer owner.Close()
			result, err := owner.ObserveSpot(ctx, fixture.Observation, paper.ExecutionGate{Now: now, StaleAfter: staleAfter, Enabled: spotEnabled(cfg, fixture.Metadata)})
			if err != nil {
				return err
			}
			lines := []string{fmt.Sprintf("Observation %s: %s", result.ObservationID, result.Reason)}
			rejected := false
			for _, order := range result.Orders {
				lines = append(lines, fmt.Sprintf("%s: %s %s", order.ID, order.Status, order.Reason))
				rejected = rejected || order.Status == "rejected"
			}
			if err := o.write(result, strings.Join(lines, "\n")); err != nil {
				return err
			}
			if rejected {
				return errors.New("one or more paper orders rejected; inspect recorded reasons")
			}
			return nil
		})
	}}
	observe.Flags().StringVar(&fixturePath, "fixture", "", "offline metadata and price observation JSON")
	observe.Flags().StringVar(&at, "at", "", "explicit RFC3339 processing timestamp")
	_ = observe.MarkFlagRequired("fixture")
	_ = observe.MarkFlagRequired("at")
	group.AddCommand(observe)
	for _, kind := range []string{"positions", "orders", "fills"} {
		var accountID string
		cmd := &cobra.Command{Use: kind, Short: "Inspect paper " + kind + " (including completed records)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			return o.paperDatabase(cmd.Context(), func(ctx context.Context, store *postgres.Store) error {
				var result any
				var lines []string
				switch kind {
				case "positions":
					positions, err := store.PaperPositions(ctx, accountID)
					if err != nil {
						return err
					}
					result = positions
					for _, p := range positions {
						lines = append(lines, fmt.Sprintf("%s %s %s remaining=%s realized_pnl=%s fees=%s", p.ID, p.Instrument.Base+"/"+p.Instrument.Quote, p.Status, p.RemainingQuantity.String(), p.RealizedPnL.String(), p.FeesPaid.String()))
					}
				case "orders":
					orders, err := store.PaperOrders(ctx, accountID)
					if err != nil {
						return err
					}
					result = orders
					for _, order := range orders {
						lines = append(lines, fmt.Sprintf("%s %s %s %s", order.ID, order.Request.Action, order.Status, order.Reason))
					}
				case "fills":
					fills, err := store.PaperFills(ctx, accountID)
					if err != nil {
						return err
					}
					result = fills
					for _, f := range fills {
						lines = append(lines, fmt.Sprintf("%s quantity=%s price=%s fee=%s realized_pnl=%s", f.ID, f.Quantity.String(), f.Price.String(), f.Fee.String(), f.RealizedPnL.String()))
					}
				}
				if len(lines) == 0 {
					lines = append(lines, "No paper "+kind)
				}
				return o.write(result, strings.Join(lines, "\n"))
			})
		}}
		cmd.Flags().StringVar(&accountID, "account", "", "optional account filter")
		group.AddCommand(cmd)
	}
}
