package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

func (o *options) addProtectionCommand(group *cobra.Command) {
	var r paper.ProtectionAdjustment
	var at, stop, target, quantity, activation, distance string
	cmd := &cobra.Command{Use: "adjust <position-id>", Short: "Adjust paper protection using the latest fresh fixture price", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		r.PositionID = args[0]
		timestamp, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return errors.New("at must be an explicit RFC3339 adjustment timestamp")
		}
		r.At = timestamp.UTC()
		for _, field := range []struct {
			name   string
			value  *string
			target **string
		}{{"stop-loss", &stop, &r.StopPrice}, {"take-profit", &target, &r.TargetPrice}, {"target-quantity", &quantity, &r.TargetQuantity}, {"trail-activation", &activation, &r.TrailActivation}, {"trail-distance", &distance, &r.TrailDistance}} {
			if cmd.Flags().Changed(field.name) {
				*field.target = field.value
			}
		}
		cfg, err := o.load()
		if err != nil {
			return err
		}
		r.StaleAfter, _ = time.ParseDuration(cfg.MarketData.StaleAfter)
		if err := r.Validate(); err != nil {
			return err
		}
		return o.paperDatabaseWithConfig(cmd.Context(), cfg, func(ctx context.Context, store *postgres.Store) error {
			var p paper.Position
			var err error
			if !o.offline {
				err = o.controlMutation(ctx, r.RequestID, "position.adjust", r, &p)
			} else {
				owner, e := store.AcquirePaperOwner(ctx)
				if e != nil {
					return e
				}
				defer owner.Close()
				p, err = owner.AdjustProtection(ctx, r)
			}
			if err != nil {
				return err
			}
			return o.write(p, fmt.Sprintf("Position %s: stop=%s target=%s trailing_activation=%s trailing_distance=%s", p.ID, p.Protection.StopPrice, p.Protection.TargetPrice, p.Protection.TrailActivation, p.Protection.TrailDistance))
		})
	}}
	cmd.Flags().StringVar(&r.RequestID, "request-id", "", "stable adjustment request ID; retry with identical inputs")
	cmd.Flags().StringVar(&at, "at", "", "explicit RFC3339 adjustment timestamp")
	cmd.Flags().StringVar(&stop, "stop-loss", "", "long stop below current price; empty disables")
	cmd.Flags().StringVar(&target, "take-profit", "", "long target above current price; empty disables")
	cmd.Flags().StringVar(&quantity, "target-quantity", "", "one-time target base quantity; empty closes remaining holdings")
	cmd.Flags().StringVar(&activation, "trail-activation", "", "absolute price activating trailing; empty disables with distance")
	cmd.Flags().StringVar(&distance, "trail-distance", "", "absolute trailing price distance; empty disables with activation")
	_ = cmd.MarkFlagRequired("request-id")
	_ = cmd.MarkFlagRequired("at")
	group.AddCommand(cmd)
}
