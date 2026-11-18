package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

func (o *options) addSignalEntryCommands(group *cobra.Command) {
	for _, approve := range []bool{false, true} {
		var request paper.SignalEntryRequest
		var path, at, requestID string
		limit := 100
		use := "consume"
		args := cobra.NoArgs
		if approve {
			use = "approve <signal-id>"
			args = cobra.ExactArgs(1)
		}
		cmd := &cobra.Command{Use: use, Short: "Decide persisted fixture signals; entries wait for a later paper observe", Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			if !approve && !o.offline {
				return errors.New("paper consume requires explicit --offline; running owner owns automatic decisions")
			}
			var fixture spotFixture
			var err error
			if o.offline {
				fixture, err = readSpotFixture(path)
				if err != nil {
					return err
				}
			}
			request.At, err = time.Parse(time.RFC3339Nano, at)
			if err != nil {
				return errors.New("at must be an explicit RFC3339 decision timestamp")
			}
			request.Approve = approve
			if approve {
				request.SignalID = args[0]
			}
			cfg, err := o.load()
			if err != nil {
				return err
			}
			return o.paperDatabaseWithConfig(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
				if !o.offline {
					var d paper.SignalDecision
					err := o.controlMutation(ctx, requestID, "signal.approve", request, &d)
					if err != nil {
						return err
					}
					if err = o.write(d, decisionText(d)); err != nil {
						return err
					}
					if d.Status == "rejected" || (d.Order != nil && d.Order.Status == "rejected") {
						return errors.New("signal approval rejected; inspect recorded reason")
					}
					return nil
				}
				owner, err := store.AcquirePaperOwner(ctx)
				if err != nil {
					return err
				}
				defer owner.Close()
				ids := []string{request.SignalID}
				if !approve {
					signals, err := store.PendingSignalEntries(ctx, fixture.Metadata, limit)
					if err != nil {
						return err
					}
					ids = []string{}
					for _, s := range signals {
						ids = append(ids, s.ID)
					}
				}
				decisions := []paper.SignalDecision{}
				lines := []string{}
				rejected := false
				for _, id := range ids {
					request.SignalID = id
					d, err := owner.DecideSignal(ctx, request, fixture.Metadata, spotEnabled(cfg, fixture.Metadata))
					if err != nil {
						return err
					}
					decisions = append(decisions, d)
					lines = append(lines, decisionText(d))
					rejected = rejected || d.Status == "rejected" || (d.Order != nil && d.Order.Status == "rejected")
				}
				if len(lines) == 0 {
					lines = append(lines, "No pending signals for fixture instrument")
				}
				if err := o.write(decisions, strings.Join(lines, "\n")); err != nil {
					return err
				}
				if rejected {
					return errors.New("one or more signal entries rejected; inspect recorded decisions")
				}
				return nil
			})
		}}
		cmd.Flags().StringVar(&requestID, "request-id", "", "stable owner control request identity")
		cmd.Flags().StringVar(&path, "fixture", "", "offline instrument metadata JSON")
		cmd.Flags().StringVar(&at, "at", "", "explicit RFC3339 decision timestamp; reuse on approval retry")
		cmd.Flags().StringVar(&request.Quantity, "quantity", "risk", "base quantity or risk for execution-time policy sizing")
		cmd.Flags().IntVar(&request.Leverage, "leverage", 1, "isolated linear leverage; spot requires 1")
		cmd.Flags().StringVar(&request.FeeRate, "fee-rate", "0.001", "frozen simulated taker fee fraction")
		cmd.Flags().StringVar(&request.SlippageBPS, "slippage-bps", "0", "frozen adverse slippage basis points")
		if !approve {
			cmd.Flags().IntVar(&limit, "limit", 100, "maximum signals consumed, from 1 to 1000")
		}
		if !approve {
			_ = cmd.MarkFlagRequired("fixture")
		}
		_ = cmd.MarkFlagRequired("at")
		group.AddCommand(cmd)
	}
	group.AddCommand(&cobra.Command{Use: "decisions", Short: "Inspect recorded signal decisions and current order outcomes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return o.paperDatabase(cmd.Context(), func(ctx context.Context, store *postgres.Store) error {
			decisions, err := store.SignalDecisions(ctx)
			if err != nil {
				return err
			}
			lines := []string{}
			for _, d := range decisions {
				lines = append(lines, decisionText(d))
			}
			if len(lines) == 0 {
				lines = append(lines, "No signal decisions")
			}
			return o.write(decisions, strings.Join(lines, "\n"))
		})
	}})
}

func decisionText(d paper.SignalDecision) string {
	text := fmt.Sprintf("%s: %s %s", d.SignalID, d.Status, d.Reason)
	if d.Order != nil {
		text += fmt.Sprintf("; order %s: %s %s", d.Order.ID, d.Order.Status, d.Order.Reason)
	}
	return text
}
