package postgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
)

func TestFundingIsDurableAndConcurrentReplayDoesNotChargeTwice(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	r := spotRequest(account.ID, "funded-entry")
	r.Instrument = info.Instrument
	r.Leverage = 5
	order, err := owner.SubmitOrder(ctx, r, info, true)
	if err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	if _, err = owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	rate := "0.01"
	event := paper.FundingEvent{ID: "interval-one", Instrument: info.Instrument, EffectiveAt: obs.AvailableAt.Add(time.Second), AvailableAt: obs.AvailableAt.Add(time.Second), Source: "fixture", Provenance: "deterministic test rate", Rate: &rate, Price: spotDecimal("100")}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, e := owner.ApplyFunding(ctx, event); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	owner.Close()
	owner, err = store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err = owner.ApplyFunding(ctx, event); err != nil {
		t.Fatal(err)
	}
	p, err := store.PaperPosition(ctx, paper.Identity("position", order.ID))
	if err != nil || p.ReservedMargin.String() != "38" || p.FundingPnL.String() != "-2" {
		t.Fatalf("funding position %+v %v", p, err)
	}
	a, _ := store.PaperAccount(ctx, account.ID)
	if a.Balance.String() != "959.8" {
		t.Fatal(a)
	}
	history, err := store.PaperFunding(ctx)
	if err != nil || len(history) != 1 {
		t.Fatalf("history %+v %v", history, err)
	}
}

func TestFundingTriggersApproximateLiquidationAndMissingCoverageIsExplicit(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	r := spotRequest(account.ID, "funding-trigger")
	r.Instrument = info.Instrument
	r.Leverage = 5
	r.Liquidation = &paper.LiquidationModel{MaintenanceRate: "0.1", FeeRate: "0"}
	order, err := owner.SubmitOrder(ctx, r, info, true)
	if err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	if _, err = owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	rate := "0.1"
	event := paper.FundingEvent{ID: "funding-trigger-event", Instrument: info.Instrument, EffectiveAt: obs.AvailableAt.Add(time.Second), AvailableAt: obs.AvailableAt.Add(time.Second), Source: "fixture", Provenance: "fixture interval", Rate: &rate, Price: spotDecimal("100")}
	result, err := owner.ApplyFunding(ctx, event)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
		t.Fatalf("funding liquidation %+v %v", result, err)
	}
	p, err := store.PaperPosition(ctx, paper.Identity("position", order.ID))
	if err != nil || p.Status != "closed" || p.FundingPnL.String() != "-20" {
		t.Fatalf("position %+v %v", p, err)
	}
	event.ID = "missing-interval"
	event.EffectiveAt = event.EffectiveAt.Add(time.Hour)
	event.AvailableAt = event.EffectiveAt
	event.Rate = nil
	event.MissingReason = "public history unavailable"
	result, err = owner.ApplyFunding(ctx, event)
	if err != nil || result.Warning == "" || len(result.Applications) != 0 {
		t.Fatalf("coverage %+v %v", result, err)
	}
	ledger, err := store.PaperLedger(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	sum := spotDecimal("0")
	for _, e := range ledger {
		sum = sum.Add(e.Amount)
	}
	a, _ := store.PaperAccount(ctx, account.ID)
	if a.Balance.String() != "979.8" || !sum.Equal(a.Balance) {
		t.Fatalf("cash %+v ledger %s", a, sum)
	}
	alerts, e := store.Notifications(ctx, 10)
	if e != nil || len(alerts) != 2 {
		t.Fatalf("liquidation alerts %+v %v", alerts, e)
	}
	found := false
	for _, a := range alerts {
		if a.Event.Kind == "liquidation_approximate" && a.Event.Price == "100" {
			found = true
		}
	}
	if !found {
		t.Fatal("liquidation notification missing")
	}

}

func TestLiquidationGapsCapLossAndBeatCompetingManualClose(t *testing.T) {
	for _, side := range []string{"long", "short"} {
		t.Run(side, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			info := spotInfo()
			info.Instrument.Market = domain.Linear
			r := spotRequest(account.ID, "liquidate")
			r.Instrument = info.Instrument
			r.Side = side
			r.Leverage = 5
			order, err := owner.SubmitOrder(ctx, r, info, true)
			if err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			r.RequestID = "manual"
			r.Action = "close"
			r.PositionID = paper.Identity("position", order.ID)
			r.At = r.At.Add(2 * time.Second)
			if _, err = owner.SubmitOrder(ctx, r, info, false); err != nil {
				t.Fatal(err)
			}
			obs, gate = spotObservation(3)
			obs.Instrument = info.Instrument
			price := "50"
			if side == "short" {
				price = "150"
			}
			obs.Price = spotDecimal(price)
			for range 2 {
				result, err := owner.Observe(ctx, obs, gate)
				if err != nil || len(result.Orders) != 2 {
					t.Fatalf("competing exits %+v %v", result, err)
				}
			}
			fills, _ := store.PaperFills(ctx, account.ID)
			if len(fills) != 2 {
				t.Fatalf("duplicate exits %+v", fills)
			}
			found := false
			for _, f := range fills {
				if f.ExitReason == "liquidation_approximate" {
					found = true
					if !f.Gap || f.Price.String() != price {
						t.Fatalf("gap %+v", f)
					}
				}
			}
			if !found {
				t.Fatal(fills)
			}
			a, _ := store.PaperAccount(ctx, account.ID)
			if a.Balance.String() != "959.8" {
				t.Fatalf("other cash consumed %+v", a)
			}
			p, _ := store.PaperPosition(ctx, r.PositionID)
			if p.RealizedPnL.String() != "-40.2" || p.Status != "closed" || !p.ReservedMargin.IsZero() {
				t.Fatalf("capped position %+v", p)
			}
		})
	}
}

func TestFundingRejectsConflictingAndRetroactiveInputsAndPriceRegression(t *testing.T) {
	ctx := context.Background()
	_, owner, _, _ := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	if _, err := owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	rate := "0"
	event := paper.FundingEvent{ID: "interval", Instrument: info.Instrument, EffectiveAt: obs.AvailableAt.Add(2 * time.Second), AvailableAt: obs.AvailableAt.Add(2 * time.Second), Source: "fixture", Provenance: "explicit zero", Rate: &rate, Price: spotDecimal("100")}
	if _, err := owner.ApplyFunding(ctx, event); err != nil {
		t.Fatal(err)
	}
	altered := event
	altered.Provenance = "changed"
	if _, err := owner.ApplyFunding(ctx, altered); err == nil {
		t.Fatal("conflicting event accepted")
	}
	altered = event
	altered.ID = "retroactive"
	altered.EffectiveAt = obs.AvailableAt
	altered.AvailableAt = obs.AvailableAt
	if _, err := owner.ApplyFunding(ctx, altered); err == nil {
		t.Fatal("retroactive event accepted")
	}
	obs, gate = spotObservation(2)
	obs.Instrument = info.Instrument
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || result.Reason == "" {
		t.Fatalf("funding clock regressed %+v %v", result, err)
	}
}

func TestFundingAndLiquidationRollbackWhenOutboxFails(t *testing.T) {
	for _, kind := range []string{"funding", "liquidation"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, dsn := newSpotTest(t)
			info := spotInfo()
			info.Instrument.Market = domain.Linear
			r := spotRequest(account.ID, "rollback-entry")
			r.Instrument = info.Instrument
			r.Leverage = 5
			order, err := owner.SubmitOrder(ctx, r, info, true)
			if err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			conn, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close(ctx)
			if _, err = conn.Exec(ctx, `CREATE FUNCTION billionaire.fail_finance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected-private-detail'; END; $$; CREATE TRIGGER fail_finance BEFORE INSERT ON billionaire.journal_outbox FOR EACH ROW EXECUTE FUNCTION billionaire.fail_finance()`); err != nil {
				t.Fatal(err)
			}
			obs, gate = spotObservation(2)
			obs.Instrument = info.Instrument
			obs.Price = spotDecimal("50")
			rate := "0.01"
			event := paper.FundingEvent{ID: "rollback-funding", Instrument: info.Instrument, EffectiveAt: obs.AvailableAt, AvailableAt: obs.AvailableAt, Source: "fixture", Provenance: "rollback fixture", Rate: &rate, Price: spotDecimal("100")}
			apply := func() error {
				if kind == "funding" {
					_, err := owner.ApplyFunding(ctx, event)
					return err
				}
				_, err := owner.Observe(ctx, obs, gate)
				return err
			}
			if err = apply(); err == nil || strings.Contains(err.Error(), "injected-private-detail") {
				t.Fatalf("unsafe/missing failure: %v", err)
			}
			p, _ := store.PaperPosition(ctx, paper.Identity("position", order.ID))
			if p.Status != "open" || p.ReservedMargin.String() != "40" || !p.FundingPnL.IsZero() {
				t.Fatalf("position leaked %+v", p)
			}
			fills, _ := store.PaperFills(ctx, account.ID)
			if len(fills) != 1 {
				t.Fatalf("fill leaked %+v", fills)
			}
			ledger, _ := store.PaperLedger(ctx, account.ID)
			sum := spotDecimal("0")
			for _, e := range ledger {
				sum = sum.Add(e.Amount)
			}
			if sum.String() != "959.8" {
				t.Fatalf("ledger leaked %s", sum)
			}
			history, _ := store.PaperFunding(ctx)
			if len(history) != 0 {
				t.Fatalf("outcome leaked %+v", history)
			}
			if _, err = conn.Exec(ctx, `DROP TRIGGER fail_finance ON billionaire.journal_outbox; DROP FUNCTION billionaire.fail_finance()`); err != nil {
				t.Fatal(err)
			}
			if err = apply(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFundingSignsReconcileThroughFinalClose(t *testing.T) {
	for _, tc := range []struct{ side, rate, margin, pnl, balance string }{{"long", "0.01", "38", "-2.4", "997.6"}, {"short", "0.01", "42", "1.6", "1001.6"}, {"long", "-0.01", "42", "1.6", "1001.6"}, {"short", "-0.01", "38", "-2.4", "997.6"}} {
		t.Run(tc.side+tc.rate, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			info := spotInfo()
			info.Instrument.Market = domain.Linear
			r := spotRequest(account.ID, "signed-funding")
			r.Instrument = info.Instrument
			r.Side = tc.side
			r.Leverage = 5
			order, err := owner.SubmitOrder(ctx, r, info, true)
			if err != nil {
				t.Fatal(err)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			event := paper.FundingEvent{ID: "signed-interval", Instrument: info.Instrument, EffectiveAt: obs.AvailableAt.Add(time.Second), AvailableAt: obs.AvailableAt.Add(time.Second), Source: "fixture", Provenance: "signed rate fixture", Rate: &tc.rate, Price: spotDecimal("100")}
			if _, err = owner.ApplyFunding(ctx, event); err != nil {
				t.Fatal(err)
			}
			id := paper.Identity("position", order.ID)
			p, _ := store.PaperPosition(ctx, id)
			if p.ReservedMargin.String() != tc.margin {
				t.Fatalf("margin %+v", p)
			}
			r.RequestID = "signed-close"
			r.Action = "close"
			r.PositionID = id
			r.At = r.At.Add(3 * time.Second)
			if _, err = owner.SubmitOrder(ctx, r, info, false); err != nil {
				t.Fatal(err)
			}
			obs, gate = spotObservation(4)
			obs.Instrument = info.Instrument
			if _, err = owner.Observe(ctx, obs, gate); err != nil {
				t.Fatal(err)
			}
			p, _ = store.PaperPosition(ctx, id)
			a, _ := store.PaperAccount(ctx, account.ID)
			if p.Status != "closed" || p.RealizedPnL.String() != tc.pnl || a.Balance.String() != tc.balance {
				t.Fatalf("reconciliation %+v %+v", p, a)
			}
			ledger, _ := store.PaperLedger(ctx, account.ID)
			sum := spotDecimal("0")
			for _, e := range ledger {
				sum = sum.Add(e.Amount)
			}
			if !sum.Equal(a.Balance) {
				t.Fatalf("ledger %s balance %s", sum, a.Balance)
			}
		})
	}
}

func TestFundingUpgradeRecoversEntryClockAndApproximateModel(t *testing.T) {
	ctx := context.Background()
	store, owner, account, dsn := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	r := spotRequest(account.ID, "legacy-funded-entry")
	r.Instrument = info.Instrument
	r.Leverage = 5
	order, err := owner.SubmitOrder(ctx, r, info, true)
	if err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	obs.Instrument = info.Instrument
	if _, err = owner.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	owner.Close()
	// Exercise the funding upgrade from its actual predecessor as migrations advance.
	for version := postgres.LatestVersion; version > 8; version-- {
		if err = store.Migrate(ctx, postgres.Down); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `UPDATE billionaire.paper_positions SET payload=payload-'opened_at'-'liquidation'`); err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	p, err := store.PaperPosition(ctx, paper.Identity("position", order.ID))
	if err != nil || !p.OpenedAt.Equal(obs.AvailableAt) || p.Liquidation == nil || p.Liquidation.MaintenanceRate != "0.005" {
		t.Fatalf("upgrade %+v %v", p, err)
	}
}

func TestLiquidationClosesExhaustedCollateralBelowEntryMinimumNotional(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	info := spotInfo()
	info.Instrument.Market = domain.Linear
	r := spotRequest(account.ID, "dust-liquidation")
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
	obs, gate = spotObservation(2)
	obs.Instrument = info.Instrument
	obs.Price = spotDecimal("0.01")
	result, err := owner.Observe(ctx, obs, gate)
	if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
		t.Fatalf("dust liquidation %+v %v", result, err)
	}
	p, _ := store.PaperPositions(ctx, account.ID)
	if len(p) != 1 || p[0].Status != "closed" {
		t.Fatalf("dust exposure %+v", p)
	}
}
