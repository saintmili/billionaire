package postgres_test

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"sync"
	"testing"
	"time"
)

func TestProtectionPartialTargetPersistsAndStopClosesRemainder(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	r := spotRequest(account.ID, "protected")
	r.StopPrice = "90"
	r.TargetPrice = "120"
	r.TargetQuantity = "0.5"
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	o, g := spotObservation(1)
	if _, err := owner.Observe(ctx, o, g); err != nil {
		t.Fatal(err)
	}
	o, g = spotObservation(2)
	o.Price = spotDecimal("120")
	for range 2 {
		result, err := owner.Observe(ctx, o, g)
		if err != nil || len(result.Orders) != 1 || result.Orders[0].Status != "filled" {
			t.Fatalf("target: %+v %v", result, err)
		}
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || len(positions) != 1 || positions[0].RemainingQuantity.String() != "1.5" || !positions[0].Protection.TargetDone {
		t.Fatalf("partial: %+v %v", positions, err)
	}
	owner.Close()
	owner, err = store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	o, g = spotObservation(3)
	o.Price = spotDecimal("125")
	if _, err := owner.Observe(ctx, o, g); err != nil {
		t.Fatal(err)
	}
	o, g = spotObservation(4)
	o.Price = spotDecimal("80")
	g.Enabled = false
	if err := store.SetKillSwitch(ctx, true); err != nil {
		t.Fatal(err)
	}
	result, err := owner.Observe(ctx, o, g)
	if err != nil || len(result.Orders) != 1 {
		t.Fatalf("stop: %+v %v", result, err)
	}
	positions, err = store.PaperPositions(ctx, account.ID)
	if err != nil || positions[0].Status != "closed" {
		t.Fatalf("closed: %+v %v", positions, err)
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 3 {
		t.Fatalf("fills: %+v %v", fills, err)
	}
	saved, err := store.PaperAccount(ctx, account.ID)
	if err != nil || saved.Balance.String() != "979.62" {
		t.Fatalf("balance: %+v %v", saved, err)
	}
}

func TestProtectionRollbackAndConcurrentManualCloseCannotOverclose(t *testing.T) {
	ctx := context.Background()
	store, owner, account, dsn := newSpotTest(t)
	r := spotRequest(account.ID, "protected")
	r.StopPrice = "90"
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	o, g := spotObservation(1)
	if _, err := owner.Observe(ctx, o, g); err != nil {
		t.Fatal(err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := positions[0]
	r.RequestID = "manual"
	r.Action = "close"
	r.PositionID = p.ID
	r.At = o.AvailableAt
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	o, g = spotObservation(2)
	o.Price = spotDecimal("85")
	for _, fault := range []struct{ table, operation string }{{"paper_orders", "INSERT"}, {"paper_positions", "INSERT"}, {"paper_fills", "INSERT"}, {"paper_accounts", "UPDATE"}, {"paper_ledger_entries", "INSERT"}, {"journal_outbox", "INSERT"}, {"paper_price_events", "INSERT"}} {
		_, err := conn.Exec(ctx, `CREATE FUNCTION millionaire.fail_protection() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END; $$; CREATE TRIGGER fail_protection BEFORE `+fault.operation+` ON millionaire.`+fault.table+` FOR EACH ROW EXECUTE FUNCTION millionaire.fail_protection()`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.Observe(ctx, o, g); err == nil {
			t.Fatalf("%s failure ignored", fault.table)
		}
		saved, err := store.PaperPosition(ctx, p.ID)
		if err != nil || saved.RemainingQuantity.String() != "2" || saved.Protection.StopPrice != "90" {
			t.Fatalf("rollback: %+v %v", saved, err)
		}
		fills, err := store.PaperFills(ctx, account.ID)
		if err != nil || len(fills) != 1 {
			t.Fatalf("fill rollback: %+v %v", fills, err)
		}
		a, err := store.PaperAccount(ctx, account.ID)
		if err != nil || a.Balance.String() != "799.8" {
			t.Fatalf("cash rollback: %+v %v", a, err)
		}
		if _, err := conn.Exec(ctx, `DROP TRIGGER fail_protection ON millionaire.`+fault.table+`; DROP FUNCTION millionaire.fail_protection()`); err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Go(func() { _, err := owner.Observe(ctx, o, g); errs <- err })
	wg.Go(func() {
		r.RequestID = "competing"
		_, err := owner.SubmitOrder(ctx, r, spotInfo(), true)
		errs <- err
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	fills, err := store.PaperFills(ctx, account.ID)
	if err != nil || len(fills) != 2 {
		t.Fatalf("overclose: %+v %v", fills, err)
	}
	orders, err := store.PaperOrders(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range orders {
		if order.Request.RequestID == "manual" && order.Status != "rejected" {
			t.Fatalf("manual close not rejected: %+v", order)
		}
	}
	ledger, err := store.PaperLedger(ctx, account.ID)
	if err != nil || len(ledger) != 5 {
		t.Fatalf("ledger: %+v %v", ledger, err)
	}
}

func TestAdjustmentPersistsConflictsAndRollsBackWithOutbox(t *testing.T) {
	ctx := context.Background()
	store, owner, account, dsn := newSpotTest(t)
	r := spotRequest(account.ID, "entry")
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	o, g := spotObservation(1)
	if _, err := owner.Observe(ctx, o, g); err != nil {
		t.Fatal(err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := positions[0].ID
	stop := "95"
	a := paper.ProtectionAdjustment{RequestID: "adjust", PositionID: id, At: o.AvailableAt.Add(time.Second), StaleAfter: time.Minute, StopPrice: &stop}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE FUNCTION millionaire.fail_adjustment() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END; $$; CREATE TRIGGER fail_adjustment BEFORE INSERT ON millionaire.journal_outbox FOR EACH ROW EXECUTE FUNCTION millionaire.fail_adjustment()`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.AdjustProtection(ctx, a); err == nil {
		t.Fatal("outbox failure ignored")
	}
	p, err := store.PaperPosition(ctx, id)
	if err != nil || p.Protection.StopPrice != "" {
		t.Fatalf("adjustment leaked: %+v %v", p, err)
	}
	if _, err := conn.Exec(ctx, `DROP TRIGGER fail_adjustment ON millionaire.journal_outbox; DROP FUNCTION millionaire.fail_adjustment()`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		p, err = owner.AdjustProtection(ctx, a)
		if err != nil || p.Protection.StopPrice != "95" {
			t.Fatalf("adjustment: %+v %v", p, err)
		}
	}
	stop = "96"
	if _, err := owner.AdjustProtection(ctx, a); err == nil {
		t.Fatal("changed request accepted")
	}
}

func TestProtectionCandleCollisionAndTrailingRestart(t *testing.T) {
	for _, scenario := range []string{"collision", "trailing"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			store, owner, account, _ := newSpotTest(t)
			r := spotRequest(account.ID, "entry")
			r.StopPrice = "90"
			if scenario == "collision" {
				r.TargetPrice = "120"
				r.SlippageBPS = "100"
			} else {
				r.TrailActivation = "110"
				r.TrailDistance = "10"
			}
			if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
				t.Fatal(err)
			}
			o, g := spotObservation(1)
			if _, err := owner.Observe(ctx, o, g); err != nil {
				t.Fatal(err)
			}
			o, g = spotObservation(3)
			low := "95"
			if scenario == "collision" {
				low = "85"
			}
			o.Price = spotDecimal("110")
			o.Bar = &paper.PriceBar{Start: r.At.Add(2 * time.Second), End: o.ObservedAt, Open: spotDecimal("100"), High: spotDecimal("125"), Low: spotDecimal(low), Close: o.Price}
			result, err := owner.Observe(ctx, o, g)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "collision" {
				if len(result.Orders) != 1 {
					t.Fatalf("collision result: %+v", result)
				}
				fills, err := store.PaperFills(ctx, account.ID)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, f := range fills {
					if f.ExitReason == "stop_loss" && f.Ambiguous && f.Price.String() == "89.1" {
						found = true
					}
				}
				if !found {
					t.Fatalf("collision fill: %+v", fills)
				}
			} else {
				if len(result.Orders) != 0 {
					t.Fatalf("same-bar trailing exit: %+v", result)
				}
				positions, err := store.PaperPositions(ctx, account.ID)
				if err != nil || positions[0].Protection.StopPrice != "115" {
					t.Fatalf("trailing state: %+v %v", positions, err)
				}
				owner.Close()
				owner, err = store.AcquirePaperOwner(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer owner.Close()
				o, g = spotObservation(4)
				o.Price = spotDecimal("114")
				if _, err := owner.Observe(ctx, o, g); err != nil {
					t.Fatal(err)
				}
				fills, err := store.PaperFills(ctx, account.ID)
				if err != nil || len(fills) != 2 {
					t.Fatalf("trailing fills: %+v %v", fills, err)
				}
				found := false
				for _, f := range fills {
					if f.ExitReason == "trailing_stop" && f.Gap && f.Price.String() == "114" {
						found = true
					}
				}
				if !found {
					t.Fatalf("restart trailing fill absent: %+v", fills)
				}
			}
		})
	}
}

func TestProtectionUpgradeAllowsAdjustingLegacyPosition(t *testing.T) {
	ctx := context.Background()
	store, owner, account, dsn := newSpotTest(t)
	r := spotRequest(account.ID, "entry")
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	o, g := spotObservation(1)
	if _, err := owner.Observe(ctx, o, g); err != nil {
		t.Fatal(err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := positions[0].ID
	owner.Close()
	if err := store.Migrate(ctx, postgres.Down); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `UPDATE millionaire.paper_positions SET payload=payload-'protection'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	owner, err = store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	stop := "95"
	a := paper.ProtectionAdjustment{RequestID: "upgrade-adjust", PositionID: id, At: o.AvailableAt.Add(time.Second), StaleAfter: time.Minute, StopPrice: &stop}
	p, err := owner.AdjustProtection(ctx, a)
	if err != nil || p.Protection.FeeRate != "0.001" || p.Protection.StopPrice != "95" {
		t.Fatalf("legacy adjustment: %+v %v", p, err)
	}
}

func TestTrailingUpdateRollsBackWhenJournalOutboxFails(t *testing.T) {
	ctx := context.Background()
	store, owner, account, dsn := newSpotTest(t)
	r := spotRequest(account.ID, "entry")
	r.TrailActivation = "110"
	r.TrailDistance = "10"
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	o, g := spotObservation(1)
	if _, err := owner.Observe(ctx, o, g); err != nil {
		t.Fatal(err)
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := positions[0].ID
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE FUNCTION millionaire.fail_trailing() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END; $$; CREATE TRIGGER fail_trailing BEFORE INSERT ON millionaire.journal_outbox FOR EACH ROW EXECUTE FUNCTION millionaire.fail_trailing()`); err != nil {
		t.Fatal(err)
	}
	o, g = spotObservation(2)
	o.Price = spotDecimal("120")
	if _, err := owner.Observe(ctx, o, g); err == nil {
		t.Fatal("trailing outbox failure did not abort")
	}
	p, err := store.PaperPosition(ctx, id)
	if err != nil || p.Protection.StopPrice != "" {
		t.Fatalf("trailing update leaked: %+v %v", p, err)
	}
	if _, err := conn.Exec(ctx, `DROP TRIGGER fail_trailing ON millionaire.journal_outbox; DROP FUNCTION millionaire.fail_trailing()`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := owner.Observe(ctx, o, g); err != nil {
			t.Fatal(err)
		}
	}
	p, err = store.PaperPosition(ctx, id)
	if err != nil || p.Protection.StopPrice != "110" {
		t.Fatalf("trailing retry: %+v %v", p, err)
	}
}

func TestProtectionRejectsUnusableObservationsWithoutLosingStop(t *testing.T) {
	ctx := context.Background()
	store, owner, account, _ := newSpotTest(t)
	r := spotRequest(account.ID, "entry")
	r.StopPrice = "90"
	if _, err := owner.SubmitOrder(ctx, r, spotInfo(), true); err != nil {
		t.Fatal(err)
	}
	o, g := spotObservation(1)
	if _, err := owner.Observe(ctx, o, g); err != nil {
		t.Fatal(err)
	}
	for _, second := range []int{2, 3} {
		o, g = spotObservation(second)
		o.Price = spotDecimal("80")
		if second == 2 {
			o.Ready = false
		} else {
			g.Now = g.Now.Add(2 * time.Minute)
		}
		result, err := owner.Observe(ctx, o, g)
		if err != nil || len(result.Orders) != 0 {
			t.Fatalf("unusable: %+v %v", result, err)
		}
	}
	o, g = spotObservation(4)
	o.Price = spotDecimal("0")
	if _, err := owner.Observe(ctx, o, g); err == nil {
		t.Fatal("invalid price accepted")
	}
	positions, err := store.PaperPositions(ctx, account.ID)
	if err != nil || positions[0].RemainingQuantity.String() != "2" || positions[0].Protection.StopPrice != "90" {
		t.Fatalf("unusable observation changed holdings: %+v %v", positions, err)
	}
	o.Price = spotDecimal("85")
	if _, err := owner.Observe(ctx, o, g); err != nil {
		t.Fatal(err)
	}
	positions, err = store.PaperPositions(ctx, account.ID)
	if err != nil || positions[0].Status != "closed" {
		t.Fatalf("valid observation failed: %+v %v", positions, err)
	}
}
