package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
	"sync"
	"testing"
	"time"
)

func capitalCommand(id, account, direction, amount string) app.Command {
	raw, _ := json.Marshal(paper.CapitalAdjustment{AccountID: account, Direction: direction, Amount: amount, Currency: "USDT", Reason: "fixture capital", At: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)})
	return app.Command{ID: id, Kind: "account.adjust", Payload: raw}
}
func TestCapitalWithdrawalsSerializeAndCannotOverspend(t *testing.T) {
	ctx := context.Background()
	s, o, a, _ := newSpotTest(t)
	control := app.NewControl(o, nil, time.Minute)
	var wg sync.WaitGroup
	results := make(chan paper.ControlOutcome, 2)
	for i := range 2 {
		wg.Go(func() {
			r, e := control.Execute(ctx, capitalCommand(fmt.Sprintf("withdraw-%d", i), a.ID, "withdraw", "600"))
			if e != nil {
				t.Error(e)
			}
			results <- r
		})
	}
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for r := range results {
		if r.Status == "accepted" {
			accepted++
		} else if r.Status == "rejected" {
			rejected++
		}
	}
	got, e := s.PaperAccount(ctx, a.ID)
	if e != nil || got.Balance.String() != "400" || accepted != 1 || rejected != 1 {
		t.Fatalf("account %+v outcomes %d/%d %v", got, accepted, rejected, e)
	}
	ledger, e := s.PaperLedger(ctx, a.ID)
	if e != nil || len(ledger) != 2 || ledger[1].Reason != "fixture capital" || ledger[1].RequestID == nil || ledger[1].OccurredAt == nil {
		t.Fatalf("ledger %+v %v", ledger, e)
	}
	// Reusing a receipt with different inputs cannot transfer money again.
	if _, e = control.Execute(ctx, capitalCommand("withdraw-0", a.ID, "withdraw", "1")); e == nil {
		t.Fatal("conflicting capital identity accepted")
	}
}
func TestCapitalWithOpenLinearPositionPreservesCollateralAndLossCoverage(t *testing.T) {
	for _, tc := range []struct{ name, price, withdraw, want string }{
		{"collateral already excluded", "100", "959.8", "0"},
		{"unrealized loss covered", "95", "949.8", "10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, o, a, _ := newSpotTest(t)
			info := spotInfo()
			info.Instrument.Market = domain.Linear
			r := spotRequest(a.ID, "capital-open")
			r.Instrument = info.Instrument
			r.Leverage = 5
			if _, e := o.SubmitOrder(ctx, r, info, true); e != nil {
				t.Fatal(e)
			}
			obs, gate := spotObservation(1)
			obs.Instrument = info.Instrument
			obs.ObservedAt = time.Now().UTC().Add(-2 * time.Second)
			obs.AvailableAt = obs.ObservedAt
			gate.Now = obs.AvailableAt
			if _, e := o.Observe(ctx, obs, gate); e != nil {
				t.Fatal(e)
			}
			obs.ID = "capital-mark"
			obs.Price = spotDecimal(tc.price)
			obs.ObservedAt = obs.ObservedAt.Add(time.Second)
			obs.AvailableAt = obs.ObservedAt
			gate.Now = obs.AvailableAt
			if _, e := o.Observe(ctx, obs, gate); e != nil {
				t.Fatal(e)
			}
			control := app.NewControl(o, nil, time.Minute)
			over := spotDecimal(tc.withdraw).Add(spotDecimal("0.001")).String()
			outcome, e := control.Execute(ctx, capitalCommand("too-much", a.ID, "withdraw", over))
			if e != nil || outcome.Status != "rejected" {
				t.Fatalf("unsafe withdrawal %+v %v", outcome, e)
			}
			outcome, e = control.Execute(ctx, capitalCommand("safe", a.ID, "withdraw", tc.withdraw))
			if e != nil || outcome.Status != "accepted" {
				t.Fatalf("safe withdrawal %+v %v", outcome, e)
			}
			got, e := s.PaperAccount(ctx, a.ID)
			if e != nil || got.Balance.String() != tc.want {
				t.Fatalf("cash %+v %v", got, e)
			}
			v, e := s.PaperAccountValuation(ctx, a.ID, time.Now().UTC(), time.Minute)
			if e != nil || v.ReservedMargin.String() != "40" || v.FreeMargin == nil || !v.FreeMargin.IsZero() {
				t.Fatalf("valuation %+v %v", v, e)
			}
			// A timestamp chosen by the caller cannot revive stale marks.
			stale := app.NewControl(o, nil, time.Nanosecond)
			outcome, e = stale.Execute(ctx, capitalCommand("stale", a.ID, "withdraw", "0.001"))
			if e != nil || outcome.Status != "rejected" {
				t.Fatalf("stale %+v %v", outcome, e)
			}
			outcome, e = stale.Execute(ctx, capitalCommand("deposit-open", a.ID, "deposit", "100"))
			if e != nil || outcome.Status != "accepted" {
				t.Fatalf("deposit with open exposure %+v %v", outcome, e)
			}
			ledger, e := s.PaperLedger(ctx, a.ID)
			if e != nil {
				t.Fatal(e)
			}
			sum := domain.Decimal{}
			for _, l := range ledger {
				sum = sum.Add(l.Amount)
			}
			got, e = s.PaperAccount(ctx, a.ID)
			if e != nil || !sum.Equal(got.Balance) {
				t.Fatalf("ledger %s cash %+v %v", sum, got, e)
			}
		})
	}
}
func TestCapitalReceiptAndOutboxFailuresRollbackTogether(t *testing.T) {
	for _, table := range []string{"control_outcomes", "journal_outbox", "paper_ledger_entries"} {
		t.Run(table, func(t *testing.T) {
			ctx := context.Background()
			s, o, a, dsn := newSpotTest(t)
			conn, e := pgx.Connect(ctx, dsn)
			if e != nil {
				t.Fatal(e)
			}
			defer conn.Close(ctx)
			_, e = conn.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION billionaire.fail_capital() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture failure'; END; $$; CREATE TRIGGER fail_capital BEFORE INSERT ON billionaire.%s FOR EACH ROW EXECUTE FUNCTION billionaire.fail_capital()`, table))
			if e != nil {
				t.Fatal(e)
			}
			control := app.NewControl(o, nil, time.Minute)
			cmd := capitalCommand("atomic-capital", a.ID, "deposit", "10")
			if _, e = control.Execute(ctx, cmd); e == nil {
				t.Fatal("injected failure accepted")
			}
			got, e := s.PaperAccount(ctx, a.ID)
			if e != nil || got.Balance.String() != "1000" {
				t.Fatalf("orphan cash %+v %v", got, e)
			}
			ledger, e := s.PaperLedger(ctx, a.ID)
			if e != nil || len(ledger) != 1 {
				t.Fatalf("orphan ledger %+v %v", ledger, e)
			}
			if _, e = control.Outcome(ctx, cmd.ID); e == nil {
				t.Fatal("orphan receipt")
			}
			if _, e = conn.Exec(ctx, "DROP TRIGGER fail_capital ON billionaire."+table); e != nil {
				t.Fatal(e)
			}
			if got, e := control.Execute(ctx, cmd); e != nil || got.Status != "accepted" {
				t.Fatalf("retry %+v %v", got, e)
			}
		})
	}
}

func TestCapitalWithdrawalLeavesOpenSpotHoldingsUntouched(t *testing.T) {
	ctx := context.Background()
	s, o, a, _ := newSpotTest(t)
	order, err := o.SubmitOrder(ctx, spotRequest(a.ID, "capital-spot"), spotInfo(), true)
	if err != nil {
		t.Fatal(err)
	}
	obs, gate := spotObservation(1)
	obs.ObservedAt = time.Now().UTC().Add(-time.Second)
	obs.AvailableAt = obs.ObservedAt
	gate.Now = obs.AvailableAt
	if _, err = o.Observe(ctx, obs, gate); err != nil {
		t.Fatal(err)
	}
	outcome, err := app.NewControl(o, nil, time.Minute).Execute(ctx, capitalCommand("spot-cash-only", a.ID, "withdraw", "799.8"))
	if err != nil || outcome.Status != "accepted" {
		t.Fatalf("withdrawal %+v %v", outcome, err)
	}
	got, err := s.PaperAccount(ctx, a.ID)
	if err != nil || !got.Balance.IsZero() {
		t.Fatalf("cash %+v %v", got, err)
	}
	p, err := s.PaperPosition(ctx, paper.Identity("position", order.ID))
	if err != nil || p.Status != "open" || p.RemainingQuantity.String() != "2" {
		t.Fatalf("holdings %+v %v", p, err)
	}
}
