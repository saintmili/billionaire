package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/control"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/marketdata/fixture"
	"github.com/saintmili/billionaire/internal/notify"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
)

// One foreground owner consumes real synchronized prices; every operator mutation
// crosses its private socket. Separate accounts make exit ownership unambiguous.
func TestM6RuntimeControlJourney(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx, postgres.Up))
	dec := func(v string) domain.Decimal { d, e := domain.ParseDecimal(v); must(e); return d }
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	i := domain.Instrument{Provider: "fixture", Market: domain.Spot, Base: "BTC", Quote: "USDT", ID: "BTCUSDT"}
	info := marketdata.InstrumentInfo{Instrument: i, PriceStep: dec("0.01"), QuantityStep: dec("0.001"), MinNotional: dec("1")}
	accounts := map[string]paper.Account{}
	for _, name := range []string{"manual", "approved", "auto"} {
		a, e := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: name, Name: name, Currency: "USDT", InitialBalance: "10000"})
		must(e)
		accounts[name] = a
	}
	threshold := "90"
	definition := strategy.Definition{SchemaVersion: 1, ID: "m6-cycle", Name: "M6 cycle", Implementation: "rules", Evaluation: strategy.OnClose, Emission: strategy.OnTransition,
		Entry: &strategy.Rule{Op: "gt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: &threshold}},
		Exit:  &strategy.Rule{Op: "lt", Left: strategy.Value{Field: "close"}, Right: strategy.Value{Constant: &threshold}}}
	version, e := s.AddStrategyVersion(ctx, definition, strategy.DefaultRegistry())
	must(e)
	for name, action := range map[string]strategy.Action{"approved": strategy.ManualPaper, "auto": strategy.AutoPaper} {
		must(s.AddStrategyInstance(ctx, strategy.Instance{ID: name, VersionID: version.ID, Account: accounts[name].ID, Instrument: i, Action: action}))
		must(s.SetStrategyEnabled(ctx, name, true))
	}
	must(s.SetNotificationPolicy(ctx, notify.Policy{Targets: []string{"terminal", "desktop"}}))
	// Force a portable headless failure through the real desktop adapter.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	bar := func(n int, price string) domain.Candle {
		return domain.Candle{Instrument: i, Timeframe: "1m", Source: "fixture", OpenTime: at.Add(time.Duration(n) * time.Minute), EndTime: at.Add(time.Duration(n+1) * time.Minute), ObservedAt: at.Add(time.Duration(n+1) * time.Minute), Open: dec(price), High: dec(price), Low: dec("10"), Close: dec(price), BaseVolume: dec("1"), Closed: true}
	}
	data := fixture.Dataset{Version: 1, Instruments: []marketdata.InstrumentInfo{info}, Candles: []domain.Candle{bar(-1, "80")}, Updates: []domain.Candle{bar(0, "100"), bar(1, "102"), bar(2, "102"), bar(3, "80"), bar(4, "81"), bar(5, "81"), bar(6, "81")}}
	raw, e := json.Marshal(data)
	must(e)
	dir := t.TempDir()
	must(os.Chmod(dir, 0700))
	path := filepath.Join(dir, "feed.json")
	socket := filepath.Join(dir, "owner.sock")
	must(os.WriteFile(path, raw, 0600))
	call := func(id, kind string, payload any) paper.ControlOutcome {
		t.Helper()
		b, e := json.Marshal(payload)
		must(e)
		command := app.Command{ID: id, Kind: kind, Payload: b}
		receipt, e := control.Call(ctx, socket, control.Request{Command: &command})
		must(e)
		return receipt
	}
	submit := func(id, account string, now time.Time) paper.ControlOutcome {
		return call(id, "order.submit", paper.OrderRequest{RequestID: id, AccountID: accounts[account].ID, Action: "open", Instrument: i, Side: "long", Quantity: "1", Leverage: 1, At: now, FeeRate: "0", SlippageBPS: "0"})
	}
	positions := func(account string) []paper.Position {
		rows, e := s.PaperPositions(ctx, accounts[account].ID)
		must(e)
		return rows
	}
	approve := func(signals []strategy.Signal, now time.Time) {
		for _, signal := range signals {
			if signal.InstanceID == "approved" {
				receipt := call("approve-"+signal.ID, "signal.approve", paper.SignalEntryRequest{SignalID: signal.ID, At: now, Quantity: "1", FeeRate: "0", SlippageBPS: "0"})
				if receipt.Status == "rejected" {
					t.Fatalf("approval: %+v", receipt)
				}
			}
		}
	}
	var original paper.ControlOutcome
	phase := 0
	run := func(first bool) {
		p, e := fixture.Open(path)
		must(e)
		opts := app.Options{QueueCapacity: 1, StaleAfter: time.Minute, Quantity: "1", OwnerHealthReady: func(owner *postgres.PaperOwner, health *app.Health) (func(), error) {
			server, e := control.Listen(socket, app.NewControl(owner, []marketdata.InstrumentInfo{info}, time.Minute, health))
			if e != nil {
				return nil, e
			}
			return func() { server.Close() }, nil
		}}
		must(app.Run(ctx, s, []app.Subscription{{Provider: p, Now: p.Now, Query: marketdata.Query{Instrument: i, Timeframe: "1m", From: at.Add(-time.Minute), To: at}}}, opts, func(result app.Result) error {
			if !first || result.Event.Candle == nil || result.Event.Kind == "recovered_candle" || !result.Event.Candle.ObservedAt.After(at) {
				return nil
			}
			phase++
			now := result.Event.Candle.ObservedAt
			switch phase {
			case 1:
				health, e := control.ReadHealth(ctx, socket)
				must(e)
				if !health.EntriesReady || len(health.Feeds) != 1 || health.Session == "" {
					t.Fatalf("owner readiness: %+v", health)
				}
				original = submit("manual-entry", "manual", now)
				if original.Status != "pending" {
					t.Fatalf("intent: %+v", original)
				}
				ds, e := s.SignalDecisions(ctx)
				must(e)
				awaiting := false
				for _, d := range ds {
					if d.Status == "awaiting_approval" && d.Order == nil {
						awaiting = true
					}
				}
				if !awaiting {
					t.Fatalf("manual signal bypassed approval: %+v", ds)
				}
				approve(result.Signals, now)
				for _, name := range []string{"manual", "approved", "auto"} {
					fills, e := s.PaperFills(ctx, accounts[name].ID)
					must(e)
					if len(fills) != 0 {
						t.Fatal("same-observation fill")
					}
				}
			case 2:
				for _, name := range []string{"manual", "approved", "auto"} {
					fills, e := s.PaperFills(ctx, accounts[name].ID)
					must(e)
					if len(fills) != 1 || fills[0].Price.String() != "102" {
						t.Fatalf("%s next-price fills: %+v", name, fills)
					}
				}
				call("limit", "risk.set", map[string]any{"id": accounts["auto"].ID, "policy": paper.RiskPolicy{MaxPositions: 1, Pyramiding: true}})
				risk := submit("risk-rejection", "auto", now)
				if risk.Status != "pending" {
					t.Fatalf("risk intent: %+v", risk)
				}
				pos := positions("manual")[0]
				call("partial", "order.submit", paper.OrderRequest{RequestID: "partial", AccountID: accounts["manual"].ID, PositionID: pos.ID, Action: "close", Instrument: i, Side: "long", Quantity: "0.5", Leverage: 1, At: now, FeeRate: "0", SlippageBPS: "0"})
			case 3:
				orders, e := s.PaperOrders(ctx, accounts["auto"].ID)
				must(e)
				found := false
				for _, order := range orders {
					if order.Request.RequestID == "risk-rejection" && order.Status == "rejected" && order.Reason == "maximum positions exceeded" {
						found = true
					}
				}
				if !found {
					t.Fatalf("risk limit not enforced: %+v", orders)
				}
				if positions("manual")[0].RemainingQuantity.String() != "0.5" {
					t.Fatal("partial close failed")
				}
				stop := "95"
				pos := positions("manual")[0]
				call("protect", "position.adjust", paper.ProtectionAdjustment{RequestID: "protect", PositionID: pos.ID, At: now, StopPrice: &stop})
				call("pause", "kill-switch", map[string]any{"enabled": true})
				rejected := submit("paused-entry", "manual", now)
				if rejected.Status != "rejected" {
					t.Fatalf("pause rejection: %+v", rejected)
				}
				var terminal bytes.Buffer
				n, e := notify.Dispatch(ctx, s, map[string]notify.Sender{"terminal": notify.Terminal{Writer: &terminal}, "desktop": notify.Desktop{}}, time.Now().UTC(), 100)
				must(e)
				if n == 0 || terminal.Len() == 0 {
					t.Fatal("terminal delivery absent")
				}
				decoder := json.NewDecoder(&terminal)
				for decoder.More() {
					var event notify.Event
					must(decoder.Decode(&event))
					if event.ID == "" {
						t.Fatal("invalid terminal JSON")
					}
				}
				notifications, e := s.Notifications(ctx, 1000)
				must(e)
				failed := false
				for _, d := range notifications {
					if d.Target == "desktop" && d.State == "failed" && d.Attempts == 1 {
						failed = true
					}
				}
				if !failed {
					t.Fatal("desktop failure not durable")
				}
			case 4:
				pos := positions("manual")[0]
				if pos.Status != "closed" {
					t.Fatalf("pause stopped protection: %+v", pos)
				}
				approve(result.Signals, now)
				for _, name := range []string{"approved", "auto"} {
					if positions(name)[0].Status != "open" {
						t.Fatal("configured exit used signal price")
					}
				}
			case 5:
				for _, name := range []string{"approved", "auto"} {
					pos := positions(name)[0]
					if pos.Status != "closed" {
						t.Fatalf("%s configured exit: %+v", name, pos)
					}
					fills, e := s.PaperFills(ctx, accounts[name].ID)
					must(e)
					expectedPnL := "-21"
					if name == "auto" {
						expectedPnL = "-21.183"
					}
					exitFound := false
					for _, fill := range fills {
						if fill.Price.String() == "81" && fill.RealizedPnL.String() == expectedPnL {
							exitFound = true
						}
					}
					if len(fills) != 2 || !exitFound {
						t.Fatalf("%s exit fills: %+v", name, fills)
					}
				}
				adjustment := paper.CapitalAdjustment{AccountID: accounts["manual"].ID, Direction: "deposit", Amount: "100.125", Currency: "USDT", Reason: "M6 acceptance capital", At: now}
				deposit := call("deposit", "account.adjust", adjustment)
				retry := call("deposit", "account.adjust", adjustment)
				if !sameM6Receipt(t, deposit, retry) {
					t.Fatal("capital retry changed receipt")
				}
				b, _ := json.Marshal(adjustment)
				var changed map[string]any
				must(json.Unmarshal(b, &changed))
				changed["amount"] = "101"
				b, _ = json.Marshal(changed)
				command := app.Command{ID: "deposit", Kind: "account.adjust", Payload: b}
				if _, e := control.Call(ctx, socket, control.Request{Command: &command}); e == nil {
					t.Fatal("conflicting capital identity accepted")
				}
				adjustment.Direction = "withdraw"
				adjustment.Amount = "25"
				call("withdraw", "account.adjust", adjustment)
				call("resume", "kill-switch", map[string]any{"enabled": false})
				call("pause", "kill-switch", map[string]any{"enabled": true})
				paused, e := s.KillSwitch(ctx)
				must(e)
				if paused {
					t.Fatal("historical pause replayed")
				}
				receipt := submit("resumed-entry", "manual", now)
				if receipt.Status != "pending" {
					t.Fatalf("resume entry: %+v", receipt)
				}
			case 6:
				if len(positions("manual")) != 2 {
					t.Fatal("resumed entry did not fill")
				}

			}
			return nil
		}))
	}
	run(true)
	if phase != 7 {
		t.Fatalf("processed %d phases", phase)
	}
	// Durable read models capture all identities, including receipts and delivery status.
	snapshot := func() []byte {
		orders, e := s.PaperOrders(ctx, "")
		must(e)
		fills, e := s.PaperFills(ctx, "")
		must(e)
		ledger := map[string][]paper.LedgerEntry{}
		for name, a := range accounts {
			rows, e := s.PaperLedger(ctx, a.ID)
			must(e)
			ledger[name] = rows
			sum := dec("0")
			for _, row := range rows {
				sum = sum.Add(row.Amount)
			}
			account, e := s.PaperAccount(ctx, a.ID)
			must(e)
			if !sum.Equal(account.Balance) {
				t.Fatalf("%s ledger/cash differ", name)
			}
		}
		signals, e := s.StrategySignals(ctx, strategy.SignalFilter{})
		must(e)
		decisions, e := s.SignalDecisions(ctx)
		must(e)
		notifications, e := s.Notifications(ctx, 1000)
		must(e)
		sources := []notify.Delivery{}
		for _, delivery := range notifications {
			// Freshness transitions describe each new monitoring session.
			// Signal/order/capital sources must retain their original identity.
			if !strings.HasPrefix(delivery.Event.ID, "freshness:") {
				sources = append(sources, delivery)
			}
		}
		b, e := json.Marshal([]any{orders, fills, ledger, signals, decisions, sources})
		must(e)
		return b
	}
	before := snapshot()
	run(false)
	if !bytes.Equal(before, snapshot()) {
		t.Fatal("restart replay changed durable financial or signal identities")
	}
	owner, e := s.AcquireEngineOwner(ctx)
	must(e)
	defer owner.Close()
	server, e := control.Listen(socket, app.NewControl(owner, []marketdata.InstrumentInfo{info}, time.Minute))
	must(e)
	defer server.Close()
	receipt, e := control.Call(ctx, socket, control.Request{Lookup: "manual-entry"})
	must(e)
	if !sameM6Receipt(t, original, receipt) {
		t.Fatal("pending receipt mutated after fill/restart")
	}
	a, e := s.PaperAccount(ctx, accounts["manual"].ID)
	must(e)
	if a.Balance.String() != "9983.125" {
		t.Fatalf("exact manual cash after stop, flows and resumed entry: %s", a.Balance)
	}
}

func sameM6Receipt(t *testing.T, a, b paper.ControlOutcome) bool {
	t.Helper()
	var ar, br any
	if err := json.Unmarshal(a.Result, &ar); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b.Result, &br); err != nil {
		t.Fatal(err)
	}
	return a.RequestID == b.RequestID && a.Status == b.Status && a.Reason == b.Reason && reflect.DeepEqual(ar, br)
}
