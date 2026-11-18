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

	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/spf13/cobra"
)

func (o *options) addFundingCommands(parent *cobra.Command) {
	group := &cobra.Command{Use: "funding", Short: "Apply and inspect explicit offline funding intervals and missing coverage"}
	var path string
	apply := &cobra.Command{Use: "apply", Short: "Apply one deterministic funding fixture before later prices", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !o.offline {
			return errors.New("funding apply requires explicit --offline; running owner owns funding")
		}
		f, err := os.Open(path)
		if err != nil {
			return errors.New("cannot open funding fixture")
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return errors.New("funding fixture unreadable or exceeds 1 MiB")
		}
		var event paper.FundingEvent
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("invalid funding fixture JSON")
		}
		if event.Instrument.Provider != "fixture" || event.Source != "fixture" {
			return errors.New("offline funding command requires fixture provider and source")
		}
		if err := event.Validate(); err != nil {
			return err
		}
		return o.paperDatabase(cmd.Context(), func(ctx context.Context, s *postgres.Store) error {
			owner, err := s.AcquirePaperOwner(ctx)
			if err != nil {
				return err
			}
			defer owner.Close()
			result, err := owner.ApplyFunding(ctx, event)
			if err != nil {
				return err
			}
			return o.write(result, fmt.Sprintf("Funding %s: %d positions, %d exits. %s", event.ID, len(result.Applications), len(result.Orders), result.Warning))
		})
	}}
	apply.Flags().StringVar(&path, "fixture", "", "explicit offline funding event JSON, rate null records missing coverage")
	_ = apply.MarkFlagRequired("fixture")
	list := &cobra.Command{Use: "list", Short: "Inspect persisted funding provenance, coverage warnings and financial outcomes", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return o.paperDatabase(cmd.Context(), func(ctx context.Context, s *postgres.Store) error {
			results, err := s.PaperFunding(ctx)
			if err != nil {
				return err
			}
			lines := []string{}
			for _, r := range results {
				lines = append(lines, fmt.Sprintf("%s %s %s", r.Event.ID, r.Event.EffectiveAt.Format("2006-01-02T15:04:05Z07:00"), r.Warning))
			}
			if len(lines) == 0 {
				lines = append(lines, "No funding intervals recorded; coverage is unknown")
			}
			return o.write(results, strings.Join(lines, "\n"))
		})
	}}
	group.AddCommand(apply, list)
	parent.AddCommand(group)
}
