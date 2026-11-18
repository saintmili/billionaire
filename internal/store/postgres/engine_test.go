package postgres_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/paper"
	"testing"

	"github.com/saintmili/billionaire/internal/marketdata"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/strategy"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestEngineExcludesOtherEvaluatorsAndPaperOwners(t *testing.T) {
	ctx := context.Background()
	s, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	owner, err := s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquirePaperOwner(ctx); !errors.Is(err, postgres.ErrPaperOwned) {
		t.Fatalf("competing writer: %v", err)
	}
	if _, err = s.EvaluateStrategyEvent(ctx, "missing", marketdata.Event{}, strategy.DefaultRegistry()); !errors.Is(err, postgres.ErrPaperOwned) {
		t.Fatalf("competing evaluator: %v", err)
	}
	owner.Close()
	next, err := s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}

func TestEngineSessionLossCannotReconnectAndWrite(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	owner, err := s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	injection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer injection.Close(ctx)
	var killed bool
	err = injection.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND classid=1 AND objid=(7341928602::bigint % 4294967296)::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&killed)
	if err != nil || !killed {
		t.Fatalf("inject ownership loss: %v %v", killed, err)
	}
	if err = owner.Check(ctx); err == nil {
		t.Fatal("lost owner reported healthy")
	}
	if _, err = owner.CreateAccount(ctx, paper.CreateAccount{RequestID: "lost-owner", Name: "Lost owner", Currency: "USDT", InitialBalance: "100"}); err == nil {
		t.Fatal("lost owner reconnected and wrote")
	}
	next, err := s.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
	accounts, err := s.PaperAccounts(ctx)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("lost owner financial writes: %+v %v", accounts, err)
	}
}
