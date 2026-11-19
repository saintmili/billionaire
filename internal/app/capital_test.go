package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/saintmili/billionaire/internal/analytics"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
	"testing"
	"time"
)

func TestCapitalDepositIsExactAuditableAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "capital", Name: "Capital", Currency: "USDT", InitialBalance: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"account_id": a.ID, "direction": "deposit", "amount": "0.125", "currency": "USDT", "reason": "fixture deposit", "at": "2026-11-19T12:00:00Z"})
	cmd := app.Command{ID: "deposit-1", Kind: "account.adjust", Payload: raw}
	first, err := app.NewControl(o, nil, time.Minute).Execute(ctx, cmd)
	if err != nil || first.Status != "accepted" {
		t.Fatalf("deposit=%+v %v", first, err)
	}
	o.Close()
	o, err = s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	retry, err := app.NewControl(o, nil, time.Minute).Execute(ctx, cmd)
	if err != nil || !sameJSON(first.Result, retry.Result) {
		t.Fatalf("retry=%+v %v", retry, err)
	}
	got, err := s.PaperAccount(ctx, a.ID)
	if err != nil || got.Balance.String() != "1000.125" {
		t.Fatalf("account=%+v %v", got, err)
	}
	ledger, err := s.PaperLedger(ctx, a.ID)
	if err != nil || len(ledger) != 2 || ledger[1].Kind != "capital_deposit" || ledger[1].Amount.String() != "0.125" {
		t.Fatalf("ledger=%+v %v", ledger, err)
	}
	report, err := s.PaperTrades(ctx, analytics.Filter{AccountID: a.ID}, time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC), time.Minute)
	if err != nil || report.Return != nil || report.CapitalDeposits.String() != "0.125" || report.CapitalWithdrawals.String() != "0" || !report.NetPnL.IsZero() || report.ReturnStatus != "unavailable_external_capital_flows" {
		t.Fatalf("deposit presented as return: %+v %v", report, err)
	}

	raw, _ = json.Marshal(paper.CapitalAdjustment{AccountID: a.ID, Direction: "withdraw", Amount: "0.125", Currency: "USDT", Reason: "fixture withdrawal", At: time.Date(2026, 11, 19, 12, 1, 0, 0, time.UTC)})
	outcome, err := app.NewControl(o, nil, time.Minute).Execute(ctx, app.Command{ID: "withdraw-after-deposit", Kind: "account.adjust", Payload: raw})
	if err != nil || outcome.Status != "accepted" {
		t.Fatalf("withdrawal %+v %v", outcome, err)
	}
	report, err = s.PaperTrades(ctx, analytics.Filter{AccountID: a.ID}, time.Now().UTC(), time.Minute)
	if err != nil || report.Return != nil || report.CapitalDeposits.String() != "0.125" || report.CapitalWithdrawals.String() != "0.125" || !report.NetPnL.IsZero() || report.LedgerCashReconciled == nil || !*report.LedgerCashReconciled {
		t.Fatalf("zero-net capital flows misreported %+v %v", report, err)
	}
}

func TestCapitalRejectsMalformedAndUnsupportedInputsWithoutChangingCash(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "validation", Name: "Validation", Currency: "USDT", InitialBalance: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	service := app.NewControl(o, nil, time.Minute)
	for i, patch := range []map[string]any{
		{"amount": "0"}, {"amount": "-1"}, {"amount": "1e2"}, {"amount": "NaN"}, {"amount": "0.0000000000000000001"}, {"amount": "1000000000000000001"}, {"amount": "1000000000000000000"}, {"currency": "USD"}, {"direction": "reverse"}, {"reason": " "}, {"at": ""}, {"at": nil}, {"account_id": "missing"}, {"unknown": true},
	} {
		fields := map[string]any{"account_id": a.ID, "direction": "deposit", "amount": "1", "currency": "USDT", "reason": "fixture", "at": "2026-11-19T12:00:00Z"}
		for k, v := range patch {
			fields[k] = v
		}
		raw, _ := json.Marshal(fields)
		r, e := service.Execute(ctx, app.Command{ID: fmt.Sprintf("invalid-%d", i), Kind: "account.adjust", Payload: raw})
		if e != nil || r.Status != "rejected" {
			t.Fatalf("patch %v: %+v %v", patch, r, e)
		}
	}
	got, err := s.PaperAccount(ctx, a.ID)
	if err != nil || got.Balance.String() != "1000" {
		t.Fatalf("cash %+v %v", got, err)
	}
	ledger, err := s.PaperLedger(ctx, a.ID)
	if err != nil || len(ledger) != 1 {
		t.Fatalf("ledger %+v %v", ledger, err)
	}
}
