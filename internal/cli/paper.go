package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/saintmili/billionaire/internal/config"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

func (o *options) addPaperCommands(root *cobra.Command) {
	group := &cobra.Command{Use: "paper", Short: "Manage offline paper accounts", Args: cobra.NoArgs}
	account := &cobra.Command{Use: "account", Short: "Create and inspect paper accounts", Args: cobra.NoArgs}
	var request paper.CreateAccount
	create := &cobra.Command{Use: "create", Short: "Create an account; reuse request ID for safe retries", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		// Validate explicit inputs before configuration or database access.
		validation := request
		if !cmd.Flags().Changed("balance") {
			validation.InitialBalance = "1"
		}
		if _, err := validation.Validate(); err != nil {
			return err
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("balance") {
			request.InitialBalance = cfg.Paper.InitialBalance
		}
		if _, err := request.Validate(); err != nil {
			return err
		}
		return o.paperDatabaseWithConfig(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			var a paper.Account
			var err error
			if !o.offline {
				err = o.controlMutation(ctx, request.RequestID, "account.create", request, &a)
			} else {
				a, err = store.CreatePaperAccount(ctx, request)
			}
			if err != nil {
				return err
			}
			return o.write(a, accountText(a))
		})
	}}
	create.Flags().StringVar(&request.Name, "name", "", "account display name (1-80 ASCII characters)")
	create.Flags().StringVar(&request.RequestID, "request-id", "", "stable creation request ID; required for safe retries")
	create.Flags().StringVar(&request.InitialBalance, "balance", "", "initial USDT balance (defaults to paper.initial_balance)")
	create.Flags().StringVar(&request.Currency, "currency", "USDT", "account currency (USDT only)")
	_ = create.MarkFlagRequired("name")
	_ = create.MarkFlagRequired("request-id")
	account.AddCommand(create)
	o.addCapitalCommand(account)
	var valuationAt string
	show := &cobra.Command{Use: "show <account-id>", Short: "Inspect balance and immutable ledger", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		now := time.Now().UTC()
		if valuationAt != "" {
			var err error
			now, err = time.Parse(time.RFC3339Nano, valuationAt)
			if err != nil {
				return errors.New("at must be an RFC3339 valuation timestamp")
			}
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		staleAfter, _ := time.ParseDuration(cfg.MarketData.StaleAfter)
		return o.paperDatabaseWithConfig(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			a, err := store.PaperAccount(ctx, args[0])
			if err != nil {
				return err
			}
			ledger, err := store.PaperLedger(ctx, a.ID)
			if err != nil {
				return err
			}
			valuation, err := store.PaperAccountValuation(ctx, a.ID, now, staleAfter)
			if err != nil {
				return err
			}
			result := struct {
				Account   paper.Account          `json:"account"`
				Ledger    []paper.LedgerEntry    `json:"ledger"`
				Valuation paper.AccountValuation `json:"valuation"`
			}{a, ledger, valuation}
			text := accountText(a)
			text += fmt.Sprintf("\navailable=%s reserved_margin=%s realized_pnl=%s", valuation.AvailableBalance, valuation.ReservedMargin, valuation.RealizedPnL)
			if valuation.Equity != nil {
				text += fmt.Sprintf("\nunrealized_pnl=%s equity=%s free_margin=%s", valuation.UnrealizedPnL, valuation.Equity, valuation.FreeMargin)
			} else {
				text += "\nvaluation unavailable: " + valuation.Reason
			}
			for _, e := range ledger {
				text += fmt.Sprintf("\n%s: %s %s", e.Kind, e.Amount.String(), a.Currency)
			}
			return o.write(result, text)
		})
	}}
	show.Flags().StringVar(&valuationAt, "at", "", "RFC3339 valuation clock; defaults to current time")
	account.AddCommand(show)
	group.AddCommand(account)
	group.AddCommand(&cobra.Command{Use: "accounts", Short: "List paper accounts and balances", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		return o.paperDatabase(cmd.Context(), func(ctx context.Context, store *postgres.Store) error {
			accounts, err := store.PaperAccounts(ctx)
			if err != nil {
				return err
			}
			lines := []string{}
			for _, a := range accounts {
				lines = append(lines, accountText(a))
			}
			if len(lines) == 0 {
				lines = append(lines, "No paper accounts")
			}
			return o.write(accounts, strings.Join(lines, "\n"))
		})
	}})
	group.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
	account.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
	o.addSignalEntryCommands(group)
	o.addSpotCommands(group)
	o.addRiskCommands(root, group)
	root.AddCommand(group)
}

func accountText(a paper.Account) string {
	return fmt.Sprintf("%s (%s): %s %s", a.Name, a.ID, a.Balance.String(), a.Currency)
}

func (o *options) paperDatabase(ctx context.Context, operation func(context.Context, *postgres.Store) error) error {
	cfg, err := o.load()
	if err != nil {
		return err
	}
	return o.paperDatabaseWithConfig(ctx, cfg, operation)
}

// paperDatabaseWithConfig keeps policy, storage and timeouts on the same snapshot.
func (o *options) paperDatabaseWithConfig(ctx context.Context, cfg config.Config, operation func(context.Context, *postgres.Store) error) error {
	return o.database(ctx, cfg, func(ctx context.Context, store *postgres.Store) error {
		state, err := store.Status(ctx)
		if err != nil {
			return err
		}
		if !state.Initialized {
			return errors.New("database is not initialized; run billionaire init")
		}
		return operation(ctx, store)
	})
}
