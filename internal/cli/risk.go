package cli

import (
	"context"
	"fmt"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

func (o *options) addRiskCommands(root, group *cobra.Command) {
	var toggleID string
	toggle := &cobra.Command{Use: "kill-switch on|off|status", Short: "Persistently block new paper entries while preserving exits", Args: cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs), ValidArgs: []string{"on", "off", "status"}, RunE: func(cmd *cobra.Command, args []string) error {
		return o.paperDatabase(cmd.Context(), func(ctx context.Context, s *postgres.Store) error {
			if args[0] != "status" {
				if !o.offline {
					return o.controlMutation(ctx, toggleID, "kill-switch", map[string]any{"enabled": args[0] == "on"}, nil)
				}
				owner, err := s.AcquireEngineOwner(ctx)
				if err != nil {
					return err
				}
				defer owner.Close()
				if _, err := owner.SetControl(ctx, "kill-switch", "", "", args[0] == "on", paper.RiskPolicy{}); err != nil {
					return err
				}
			}
			on, err := s.KillSwitch(ctx)
			if err != nil {
				return err
			}
			return o.write(map[string]bool{"kill_switch": on}, fmt.Sprintf("Kill switch: %t", on))
		})
	}}
	toggle.Flags().StringVar(&toggleID, "request-id", "", "stable control request identity (required for owner mutation)")
	root.AddCommand(toggle)
	risk := &cobra.Command{Use: "risk", Short: "Inspect or replace account/strategy risk policy", Args: cobra.NoArgs}
	for _, action := range []string{"show", "set"} {
		var accountID, strategyID, requestID string
		var p paper.RiskPolicy
		cmd := &cobra.Command{Use: action, Short: action + " risk policy (set replaces all limits in this scope)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			if action == "set" {
				if err := p.Validate(); err != nil {
					return err
				}
			}
			return o.paperDatabase(cmd.Context(), func(ctx context.Context, s *postgres.Store) error {
				if _, err := s.PaperAccount(ctx, accountID); err != nil {
					return err
				}
				if action == "set" {
					if !o.offline {
						return o.controlMutation(ctx, requestID, "risk.set", map[string]any{"id": accountID, "strategy_id": strategyID, "policy": p}, nil)
					}
					owner, err := s.AcquireEngineOwner(ctx)
					if err != nil {
						return err
					}
					defer owner.Close()
					if _, err := owner.SetControl(ctx, "risk.set", accountID, strategyID, false, p); err != nil {
						return err
					}
				}
				policy, err := s.RiskPolicy(ctx, accountID, strategyID)
				if err != nil {
					return err
				}
				return o.write(policy, fmt.Sprintf("Risk account=%s strategy=%s: %+v", accountID, strategyID, policy))
			})
		}}
		cmd.Flags().StringVar(&accountID, "account", "", "paper account ID")
		cmd.Flags().StringVar(&strategyID, "strategy", "", "optional strategy definition ID; account limits also apply")
		_ = cmd.MarkFlagRequired("account")
		if action == "set" {
			cmd.Flags().StringVar(&requestID, "request-id", "", "stable owner control request identity")
			cmd.Flags().StringVar(&p.FixedRisk, "fixed-risk", "", "maximum entry price-distance risk in USDT")
			cmd.Flags().StringVar(&p.PercentRisk, "percent-risk", "", "maximum entry price-distance risk as percent of account equity")
			cmd.Flags().StringVar(&p.DailyLoss, "daily-loss", "", "maximum net realized loss plus fees per UTC day in USDT")
			cmd.Flags().IntVar(&p.MaxPositions, "max-positions", 0, "maximum open positions; 0 disables limit")
			cmd.Flags().StringVar(&p.SymbolExposure, "symbol-exposure", "", "maximum marked exposure per instrument in USDT")
			cmd.Flags().IntVar(&p.MaxLeverage, "max-leverage", 0, "maximum entry leverage; spot always requires 1")
			cmd.Flags().StringVar(&p.MarginPercent, "margin-percent", "", "maximum spot capital usage as percent of equity")
			cmd.Flags().IntVar(&p.CooldownSeconds, "cooldown-seconds", 0, "minimum seconds since last filled entry in scope")
			cmd.Flags().BoolVar(&p.Pyramiding, "pyramiding", false, "allow additional positions in an already held instrument")
		}
		risk.AddCommand(cmd)
	}
	group.AddCommand(risk)
}
