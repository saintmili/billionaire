package postgres_test

import (
	"context"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"testing"
	"time"
)

func TestDailyLossIncludesFundingAndCapsWaivedIsolatedLoss(t *testing.T) {
	for _, scenario := range []struct{ name, rate, gap, limit, reason string }{
		{"funding debit", "0.01", "", "2.2", "daily loss limit reached"},
		{"funding credit", "-0.01", "", "0.2", ""},
		{"waived gap loss", "0", "50", "41", ""},
		{"actual gap loss", "0", "50", "40.2", "daily loss limit reached"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			info := spotInfo()
			info.Instrument.Market = domain.Linear
			r := spotRequest(account.ID, "first")
			r.Instrument = info.Instrument
			r.Leverage = 5
			if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			if _, err := owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			event := paper.FundingEvent{ID: "interval", Instrument: info.Instrument, EffectiveAt: r.At.Add(2 * time.Second), AvailableAt: r.At.Add(2 * time.Second), Source: "fixture", Provenance: "acceptance", Rate: &scenario.rate, Price: spotDecimal("100")}
			if _, err := owner.ApplyFunding(ctx, event); err != nil {
				t.Fatal(err)
			}
			if scenario.gap != "" {
				obs, gate = spotObservation(3)
				obs.Instrument = info.Instrument
				obs.Price = spotDecimal(scenario.gap)
				if _, err := owner.Observe(ctx, obs, gate); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SetRiskPolicy(ctx, account.ID, "", paper.RiskPolicy{DailyLoss: scenario.limit, Pyramiding: true}); err != nil {
				t.Fatal(err)
			}
			r.RequestID = "next"
			r.At = r.At.Add(4 * time.Second)
			if _, err := owner.SubmitOrder(ctx, r, info, true); err != nil {
				t.Fatal(err)
			}
			obs, gate = spotObservation(5)
			obs.Instrument = info.Instrument
			result, err := owner.Observe(ctx, obs, gate)
			if err != nil || len(result.Orders) != 1 || result.Orders[0].Reason != scenario.reason {
				t.Fatalf("daily realized economics: %+v %v", result, err)
			}
		})
	}
}
