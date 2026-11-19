package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
)

// Locks are fault injection only: all financial outcomes are read through the
// application/store public boundaries. The exact clock advances only after
// PostgreSQL proves the owner is waiting, so machine speed cannot decide freshness.
func TestForegroundPricesExpireWhileFinancialPreparationWaits(t *testing.T) {
	for _, scenario := range []string{"entry account", "pending order", "risk control", "protection position", "protection account", "later order"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			dsn := testdb.New(t)
			s, err := postgres.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.Migrate(ctx, postgres.Up); err != nil {
				t.Fatal(err)
			}
			dec := func(v string) domain.Decimal {
				d, e := domain.ParseDecimal(v)
				if e != nil {
					t.Fatal(e)
				}
				return d
			}
			at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
			i := domain.Instrument{Provider: "fixture", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
			info := marketdata.InstrumentInfo{Instrument: i, PriceStep: dec("0.01"), QuantityStep: dec("0.001"), MinNotional: dec("1")}
			account, err := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "waiting", Name: "Waiting", Currency: "USDT", InitialBalance: "1000"})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := s.AcquirePaperOwner(ctx)
			if err != nil {
				t.Fatal(err)
			}
			protective := scenario == "protection position" || scenario == "protection account"
			count := 1
			if scenario == "later order" {
				count = 2
			}
			for n := 0; n < count; n++ {
				entryAccount := account.ID
				if scenario == "later order" && n == 1 {
					other, e := owner.CreateAccount(ctx, paper.CreateAccount{RequestID: "other", Name: "Other", Currency: "USDT", InitialBalance: "1000"})
					if e != nil {
						t.Fatal(e)
					}
					entryAccount = other.ID
				}
				request := paper.OrderRequest{RequestID: fmt.Sprint("entry", n), AccountID: entryAccount, Action: "open", Instrument: i, Side: "long", Quantity: "1", Leverage: 1, At: at.Add(time.Duration(n-5) * time.Second), FeeRate: "0", SlippageBPS: "0"}
				if protective {
					request.StopPrice = "90"
				}
				if _, err = owner.SubmitOrder(ctx, request, info, true); err != nil {
					t.Fatal(err)
				}
			}
			if protective {
				if _, err = owner.Observe(ctx, paper.Observation{ID: "seed", Instrument: i, Source: "fixture", Price: dec("100"), ObservedAt: at.Add(-time.Second), AvailableAt: at.Add(-time.Second), Ready: true}, paper.ExecutionGate{Now: at.Add(-time.Second), StaleAfter: time.Minute, Enabled: true}); err != nil {
					t.Fatal(err)
				}
			}
			owner.Close()
			bar := domain.Candle{Instrument: i, Timeframe: "1m", Source: "fixture", OpenTime: at.Add(-time.Minute), EndTime: at, ObservedAt: at, Open: dec("80"), High: dec("80"), Low: dec("80"), Close: dec("80"), BaseVolume: dec("1"), Closed: true}
			update := bar
			update.OpenTime = at
			update.EndTime = at.Add(time.Minute)
			update.Closed = false
			raw, _ := json.Marshal(fixture.Dataset{Version: 1, Instruments: []marketdata.InstrumentInfo{info}, Candles: []domain.Candle{bar}, Updates: []domain.Candle{update}})
			path := filepath.Join(t.TempDir(), "wait.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			p, err := fixture.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			blocker, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Close(context.Background())
			observer, err := pgx.Connect(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close(context.Background())
			release := func() error { _, e := blocker.Exec(ctx, "ROLLBACK"); return e }
			if scenario == "later order" {
				if _, err = blocker.Exec(ctx, `SELECT pg_advisory_lock(91276); CREATE FUNCTION billionaire.wait_first_fill() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='filled' THEN PERFORM pg_advisory_xact_lock(91276); END IF; RETURN NEW; END; $$; CREATE TRIGGER wait_first_fill BEFORE UPDATE ON billionaire.paper_orders FOR EACH ROW EXECUTE FUNCTION billionaire.wait_first_fill()`); err != nil {
					t.Fatal(err)
				}
				release = func() error { _, e := blocker.Exec(ctx, "SELECT pg_advisory_unlock(91276)"); return e }
			} else {
				table := "paper_accounts"
				if scenario == "pending order" {
					table = "paper_orders"
				}
				if scenario == "risk control" {
					table = "paper_controls"
				}
				if scenario == "protection position" {
					table = "paper_positions"
				}
				if _, err = blocker.Exec(ctx, "BEGIN; SELECT id FROM billionaire."+table+" FOR UPDATE"); err != nil {
					t.Fatal(err)
				}
			}
			var offset atomic.Int64
			clock := func() time.Time { return at.Add(time.Duration(offset.Load())) }
			unblocked := make(chan error, 1)
			go func() {
				for {
					var waiting bool
					e := observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND pid<>pg_backend_pid())`).Scan(&waiting)
					if e != nil {
						unblocked <- e
						return
					}
					if waiting {
						offset.Store(int64(2 * time.Minute))
						unblocked <- release()
						return
					}
					select {
					case <-ctx.Done():
						unblocked <- ctx.Err()
						return
					case <-time.After(5 * time.Millisecond):
					}
				}
			}()
			seen := false
			err = app.Run(ctx, s, []app.Subscription{{Provider: p, Now: clock, Query: marketdata.Query{Instrument: i, Timeframe: "1m", From: at.Add(-time.Minute), To: at}}}, app.Options{QueueCapacity: 1, StaleAfter: time.Minute}, func(r app.Result) error {
				if r.Event.Candle == nil {
					return nil
				}
				seen = true
				if r.Observation == nil || r.Observation.PriceAccepted {
					t.Errorf("expired delivery certified as an accepted price: %+v", r.Observation)
				}
				fills, e := s.PaperFills(ctx, "")
				if e != nil {
					return e
				}
				expected := 0
				if protective || scenario == "later order" {
					expected = 1
				}
				if len(fills) != expected {
					t.Errorf("expired price executed: got %d fills, want %d", len(fills), expected)
				}
				positions, e := s.PaperPositions(ctx, account.ID)
				if e != nil {
					return e
				}
				if protective && (len(positions) != 1 || positions[0].Status != "open" || positions[0].Protection.StopPrice != "90") {
					t.Errorf("expired price exited/adjusted protection: %+v", positions)
				}
				return nil
			})
			if err != nil || !seen {
				t.Fatalf("engine result: %v, seen=%v", err, seen)
			}
			if e := <-unblocked; e != nil {
				t.Fatal(e)
			}
		})
	}
}
