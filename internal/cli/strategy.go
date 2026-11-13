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

	"github.com/spf13/cobra"

	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
)

func readStrategy(path string, registry *strategy.Registry) (strategy.Definition, error) {
	file, err := os.Open(path)
	if err != nil {
		return strategy.Definition{}, errors.New("cannot open strategy YAML")
	}
	defer file.Close()
	return strategy.Parse(file, registry)
}
func (o *options) addStrategyCommands(root *cobra.Command) {
	registry := strategy.DefaultRegistry()
	group := &cobra.Command{Use: "strategy", Short: "Manage immutable strategy versions and instances", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() }}
	group.AddCommand(&cobra.Command{Use: "validate FILE", Short: "Validate strategy YAML offline", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		definition, err := readStrategy(args[0], registry)
		if err != nil {
			return err
		}
		return o.write(map[string]any{"valid": true, "strategy_id": definition.ID, "content_hash": strategy.ContentHash(definition)}, "Strategy valid: "+definition.ID)
	}})
	var instanceID, provider, market, symbol, account, action string
	add := &cobra.Command{Use: "add FILE", Short: "Add an immutable version; optionally create a disabled instance", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		definition, err := readStrategy(args[0], registry)
		if err != nil {
			return err
		}
		if provider != "bybit" && provider != "fixture" {
			return errors.New("provider must be bybit or fixture")
		}
		var binding strategy.Instance
		if instanceID != "" {
			selected := marketFlags{market: market, symbol: symbol, frame: string(definition.Primary)}
			instrument, _, err := selected.identity(provider)
			if err != nil {
				return err
			}
			selectedAction := strategy.Action(action)
			if selectedAction == "" {
				selectedAction = definition.Action
			}
			binding = strategy.Instance{ID: instanceID, VersionID: strategy.ContentHash(definition), Instrument: instrument, Account: account, Action: selectedAction}
			if err := definition.ValidateInstance(binding); err != nil {
				return err
			}
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		return o.database(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			version, err := store.AddStrategyVersion(ctx, definition, registry)
			if err != nil {
				return err
			}
			result := struct {
				Version  strategy.Version   `json:"version"`
				Instance *strategy.Instance `json:"instance,omitempty"`
			}{Version: version}
			if instanceID != "" {
				binding.VersionID = version.ID
				if err := store.AddStrategyInstance(ctx, binding); err != nil {
					return err
				}
				actual, err := store.StrategyInstance(ctx, binding.ID)
				if err != nil {
					return err
				}
				result.Instance = &actual
			}
			return o.write(result, fmt.Sprintf("Registered %s version %d (%s)", definition.ID, version.Number, version.ID))
		})
	}}
	add.Flags().StringVar(&instanceID, "instance", "", "optional new instance id (disabled until enable)")
	add.Flags().StringVar(&provider, "provider", "bybit", "bybit or fixture")
	add.Flags().StringVar(&market, "market", "spot", "spot or linear")
	add.Flags().StringVar(&symbol, "symbol", "BTC/USDT", "instrument BASE/USDT")
	add.Flags().StringVar(&account, "account", "", "paper-account reference; required with --instance")
	add.Flags().StringVar(&action, "action", "", "instance action override: alert_only, manual_paper, auto_paper")
	group.AddCommand(add)
	group.AddCommand(&cobra.Command{Use: "list", Short: "List versions and instance bindings", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		return o.database(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			versions, err := store.StrategyVersions(ctx)
			if err != nil {
				return err
			}
			instances, err := store.StrategyInstances(ctx)
			if err != nil {
				return err
			}
			var text strings.Builder
			for _, version := range versions {
				fmt.Fprintf(&text, "%s v%d %s\n", version.Definition.ID, version.Number, version.ID)
			}
			for _, instance := range instances {
				fmt.Fprintf(&text, "%s enabled=%v version=%s account=%s\n", instance.ID, instance.Enabled, instance.VersionID, instance.Account)
			}
			return o.write(map[string]any{"versions": versions, "instances": instances}, strings.TrimSpace(text.String()))
		})
	}})
	for _, enabled := range []bool{true, false} {
		name := "disable"
		if enabled {
			name = "enable"
		}
		group.AddCommand(&cobra.Command{Use: name + " INSTANCE", Short: name + " a strategy instance", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := o.load()
			if err != nil {
				return err
			}
			return o.database(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
				if err := store.SetStrategyEnabled(ctx, args[0], enabled); err != nil {
					return err
				}
				return o.write(map[string]any{"instance_id": args[0], "enabled": enabled}, fmt.Sprintf("%s enabled=%v", args[0], enabled))
			})
		}})
	}
	var eventPath string
	var sourceReady bool
	evaluate := &cobra.Command{Use: "evaluate INSTANCE", Short: "Evaluate one explicit offline candle event; records signals only", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		candle, err := readStrategyEvent(eventPath)
		if err != nil {
			return err
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		if sourceReady && candle.Instrument.Provider != "fixture" {
			return errors.New("source-ready offline evaluation requires fixture provider")
		}
		return o.database(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			result, err := store.EvaluateStrategyEvent(ctx, args[0], marketdata.Event{Kind: "offline_evaluation", Candle: &candle, AllowsEntries: sourceReady}, registry)
			if err != nil {
				return err
			}
			return o.write(result, fmt.Sprintf("Ready: %v; signals: %d; reason: %s", result.Ready, len(result.Signals), result.Reason))
		})
	}}
	evaluate.Flags().BoolVar(&sourceReady, "source-ready", false, "explicitly mark a fixture evaluation ready for offline paper entry")
	evaluate.Flags().StringVar(&eventPath, "event", "", "canonical candle JSON file")
	evaluate.MarkFlagRequired("event")
	group.AddCommand(evaluate)
	root.AddCommand(group)
	var signalInstance, signalStrategy string
	signals := &cobra.Command{Use: "signals", Short: "Inspect recorded strategy observations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() }}
	list := &cobra.Command{Use: "list", Short: "List up to 10000 signals in deterministic order", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		return o.database(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			found, err := store.StrategySignals(ctx, strategy.SignalFilter{InstanceID: signalInstance, StrategyID: signalStrategy})
			if err != nil {
				return err
			}
			selected := []strategy.Signal{}
			var text strings.Builder
			for _, signal := range found {

				selected = append(selected, signal)
				fmt.Fprintf(&text, "%s %s %s %s %s @ %s\n", signal.ID, signal.Kind, signal.Direction, signal.Instrument.Base+"/"+signal.Instrument.Quote, signal.Timeframe, signal.ProposedEntry.String())
			}
			return o.write(selected, strings.TrimSpace(text.String()))
		})
	}}
	list.Flags().StringVar(&signalInstance, "instance", "", "instance filter")
	list.Flags().StringVar(&signalStrategy, "strategy", "", "strategy-definition filter")
	signals.AddCommand(list)
	root.AddCommand(signals)
}
func readStrategyEvent(path string) (domain.Candle, error) {
	var candle domain.Candle
	file, err := os.Open(path)
	if err != nil {
		return candle, errors.New("cannot open strategy event JSON")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return candle, errors.New("strategy event is unreadable or exceeds 1 MiB")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return candle, errors.New("strategy event must be a JSON object")
	}
	for _, field := range []string{"open", "high", "low", "close", "base_volume"} {
		var text string
		if json.Unmarshal(fields[field], &text) != nil {
			return candle, fmt.Errorf("strategy event.%s must be a plain decimal string", field)
		}
		if _, err := domain.ParseDecimal(text); err != nil {
			return candle, fmt.Errorf("strategy event.%s must be a bounded plain decimal string", field)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&candle) != nil {
		return candle, errors.New("strategy event: malformed JSON or invalid field/decimal")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return candle, errors.New("strategy event must contain one JSON document")
	}
	if candle.Source != candle.Instrument.Provider {
		return candle, errors.New("strategy event source/provider mismatch")
	}
	candle.ObservedAt = candle.ObservedAt.UTC()
	candle = marketdata.Stamp(candle)
	return candle, candle.Validate()
}
