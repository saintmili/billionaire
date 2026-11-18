package cli

import (
	"context"
	"fmt"
	"github.com/saintmili/billionaire/internal/config"
	"github.com/saintmili/billionaire/internal/notify"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
	"time"
)

func (o *options) addNotificationCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "notifications", Short: "Inspect durable alert deliveries or dispatch one bounded batch", Args: cobra.NoArgs}
	var limit int
	parent.PersistentFlags().IntVar(&limit, "limit", 100, "maximum deliveries (dispatch 1-100, status 1-1000)")
	parent.AddCommand(&cobra.Command{Use: "status", Short: "List delivery outcomes including failures and suppression", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		return o.withDatabase(cmd.Context(), cfg, func(ctx context.Context, s *postgres.Store) error {
			rows, err := s.Notifications(ctx, limit)
			if err != nil {
				return err
			}
			return o.write(rows, fmt.Sprintf("%d notification deliveries", len(rows)))
		})
	}})
	parent.AddCommand(&cobra.Command{Use: "dispatch", Short: "Dispatch one bounded batch; terminal alerts go to stderr", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := o.load()
		if err != nil {
			return err
		}
		return o.withDatabase(cmd.Context(), cfg, func(ctx context.Context, s *postgres.Store) error {
			n, err := notify.Dispatch(ctx, s, o.notificationSenders(cfg), time.Now().UTC(), limit)
			if err != nil {
				return err
			}
			return o.write(map[string]int{"attempted": n}, fmt.Sprintf("Attempted %d deliveries", n))
		})
	}})
	root.AddCommand(parent)
}

func (o *options) notificationSenders(cfg config.Config) map[string]notify.Sender {
	return map[string]notify.Sender{"terminal": notify.Terminal{Writer: o.diagnostics}, "desktop": notify.Desktop{}}
}
