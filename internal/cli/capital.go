package cli

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
	"time"
)

func (o *options) addCapitalCommand(account *cobra.Command) {
	var r paper.CapitalAdjustment
	var id, at string
	cmd := &cobra.Command{Use: "adjust ACCOUNT-ID", Short: "Deposit or withdraw simulated USDT capital with an immutable receipt", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r.AccountID = args[0]
		var err error
		r.At, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return errors.New("at must be an explicit RFC3339 timestamp")
		}
		if _, err = r.Validate(); err != nil {
			return err
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		stale, _ := time.ParseDuration(cfg.MarketData.StaleAfter)
		var a paper.Account
		if !o.offline {
			if err = o.controlMutation(cmd.Context(), id, "account.adjust", r, &a); err != nil {
				return err
			}
			return o.write(a, accountText(a))
		}
		return o.paperDatabaseWithConfig(cmd.Context(), cfg, func(ctx context.Context, s *postgres.Store) error {
			owner, err := s.AcquirePaperOwner(ctx)
			if err != nil {
				return err
			}
			defer owner.Close()
			raw, err := json.Marshal(r)
			if err != nil {
				return err
			}
			outcome, err := app.NewControl(owner, nil, stale).Execute(ctx, app.Command{ID: id, Kind: "account.adjust", Payload: raw})
			if err != nil {
				return err
			}
			if outcome.Status == "rejected" {
				if err = o.write(outcome, outcome.Reason); err != nil {
					return err
				}
				return errors.New(outcome.Reason)
			}
			if err = json.Unmarshal(outcome.Result, &a); err != nil {
				return err
			}
			return o.write(a, accountText(a))
		})
	}}
	cmd.Flags().StringVar(&id, "request-id", "", "stable request identity for retries and outcome lookup")
	cmd.Flags().StringVar(&r.Direction, "direction", "", "deposit or withdraw")
	cmd.Flags().StringVar(&r.Amount, "amount", "", "positive exact amount; up to 18 decimal places")
	cmd.Flags().StringVar(&r.Currency, "currency", "USDT", "currency (USDT only)")
	cmd.Flags().StringVar(&r.Reason, "reason", "", "audit reason (1-500 bytes)")
	cmd.Flags().StringVar(&at, "at", "", "explicit RFC3339 audit timestamp; withdrawals use current mark freshness")
	for _, flag := range []string{"request-id", "direction", "amount", "reason", "at"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	account.AddCommand(cmd)
}
