package postgres_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/notify"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
	"strings"
	"testing"
	"time"
)

func TestNotificationsDurableDedupCooldownAndTerminal(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	s, e := postgres.Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx, postgres.Up); e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	event := notify.Event{ID: "failure-1", Kind: "engine_failure", At: at, Reason: "bad\x1b[31m\nline"}
	for _, id := range []string{"failure-1", "failure-1", "failure-2"} {
		event.ID = id
		if e = s.RecordNotification(ctx, event, notify.Policy{Cooldown: time.Minute, Key: "engine"}); e != nil {
			t.Fatal(e)
		}
	}
	s.Close()
	s, e = postgres.Open(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	var out bytes.Buffer
	n, e := notify.Dispatch(ctx, s, map[string]notify.Sender{"terminal": notify.Terminal{Writer: &out}}, at, 10)
	if e != nil || n != 1 {
		t.Fatalf("dispatch %d %v", n, e)
	}
	rows, e := s.Notifications(ctx, 10)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 || !strings.Contains(out.String(), `\u001b`) {
		t.Fatalf("rows/output %v %q", rows, out.String())
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.State]++
	}
	if counts["delivered"] != 1 || counts["suppressed"] != 1 {
		t.Fatal(counts)
	}
}

func TestNotificationClaimsRecoverAndRetriesExhaustWithoutSensitiveErrors(t *testing.T) {
	ctx := context.Background()
	s, e := postgres.Open(ctx, testdb.New(t))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx, postgres.Up); e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if e = s.RecordEngineFailure(ctx, "one", at, "engine failed"); e != nil {
		t.Fatal(e)
	}
	first, e := s.ClaimNotifications(ctx, at, 1)
	if e != nil || len(first) != 1 {
		t.Fatalf("first claim %+v %v", first, e)
	}
	duplicate, e := s.ClaimNotifications(ctx, at, 1)
	if e != nil || len(duplicate) != 0 {
		t.Fatalf("concurrent claim %+v %v", duplicate, e)
	}
	recovered, e := s.ClaimNotifications(ctx, at.Add(6*time.Minute), 1)
	if e != nil || len(recovered) != 1 || recovered[0].Attempts != 2 {
		t.Fatalf("recovery %+v %v", recovered, e)
	}
	if e = s.FinishNotification(ctx, first[0], "delivered", "", at); e != nil {
		t.Fatal(e)
	}
	rows, _ := s.Notifications(ctx, 10)
	if rows[0].State != "inflight" {
		t.Fatal("stale acknowledgement took newer lease")
	}
	if e = s.FinishNotification(ctx, recovered[0], "pending", "", at); e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 4; i++ {
		_, e = notify.Dispatch(ctx, s, map[string]notify.Sender{"terminal": failingTerminal{}}, at.Add(time.Duration(i)*time.Hour), 1)
		if e != nil {
			t.Fatal(e)
		}
	}
	rows, e = s.Notifications(ctx, 10)
	if e != nil || rows[0].State != "failed" || rows[0].Attempts != 5 || rows[0].Error != "delivery_failed" {
		t.Fatalf("exhaustion %+v %v", rows, e)
	}
}

type failingTerminal struct{}

func (failingTerminal) Send(context.Context, notify.Event) error {
	return fmt.Errorf("secret://TOKEN@example.test")
}

func TestNotificationFailureRollsBackOrderAdmission(t *testing.T) {
	s, owner, account, dsn := newSpotTest(t)
	ctx := context.Background()
	conn, e := pgx.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close(ctx)
	_, e = conn.Exec(ctx, `CREATE FUNCTION billionaire.fail_notification() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END; $$;CREATE TRIGGER fail_notification BEFORE INSERT ON billionaire.notification_outbox FOR EACH ROW EXECUTE FUNCTION billionaire.fail_notification()`)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := spotInfo()
	request := paper.OrderRequest{RequestID: "rejected", AccountID: account.ID, Action: "open", Instrument: info.Instrument, Side: "long", Leverage: 1, Quantity: "1", At: at, FeeRate: "0", SlippageBPS: "0"}
	if _, e = owner.SubmitOrder(ctx, request, info, false); e == nil {
		t.Fatal("notification failure was ignored")
	}
	orders, e := s.PaperOrders(ctx, account.ID)
	if e != nil || len(orders) != 0 {
		t.Fatalf("rollback orders %+v %v", orders, e)
	}
	if _, e = conn.Exec(ctx, `DROP TRIGGER fail_notification ON billionaire.notification_outbox`); e != nil {
		t.Fatal(e)
	}
	if _, e = owner.SubmitOrder(ctx, request, info, false); e != nil {
		t.Fatal(e)
	}
	rows, e := s.Notifications(ctx, 10)
	if e != nil || len(rows) != 1 || rows[0].Event.Kind != "order_rejected" {
		t.Fatalf("alerts %+v %v", rows, e)
	}
}
func TestFreshnessTransitionsIgnoreCheckpointsAndRecover(t *testing.T) {
	ctx := context.Background()
	s, e := postgres.Open(ctx, testdb.New(t))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx, postgres.Up); e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, ready := range []bool{true, true, false, false, true, true} {
		if e = s.ObserveFreshness(ctx, "feed", "BTCUSDT", "1m", ready, at.Add(time.Duration(i)*time.Minute), "readiness transition"); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.ObserveFreshness(ctx, "feed", "BTCUSDT", "1m", false, at.Add(4*time.Minute+30*time.Second), "old checkpoint"); e != nil {
		t.Fatal(e)
	}
	rows, e := s.Notifications(ctx, 10)
	if e != nil || len(rows) != 2 || rows[0].Event.Kind != "data_stale" || rows[1].Event.Kind != "data_recovered" {
		t.Fatalf("transitions %+v %v", rows, e)
	}
}

func TestBlockedNotificationSenderDoesNotHoldFinancialOwnership(t *testing.T) {
	s, owner, account, _ := newSpotTest(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.RecordEngineFailure(ctx, "blocked", at, "fixture"); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := notify.Dispatch(ctx, s, map[string]notify.Sender{"terminal": blockingTerminal{started, release}}, at, 1)
		done <- err
	}()
	<-started
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request := paper.OrderRequest{RequestID: "unblocked", AccountID: account.ID, Action: "open", Instrument: spotInfo().Instrument, Side: "long", Leverage: 1, Quantity: "1", At: at, FeeRate: "0", SlippageBPS: "0"}
	_, err := owner.SubmitOrder(bounded, request, spotInfo(), true)
	close(release)
	if dispatchErr := <-done; dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if err != nil {
		t.Fatalf("delivery held financial processing: %v", err)
	}
}

type blockingTerminal struct{ started, release chan struct{} }

func (s blockingTerminal) Send(ctx context.Context, _ notify.Event) error {
	close(s.started)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
