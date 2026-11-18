package postgres_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestPaperAccountOpeningPersistsAndReconciles(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	request := paper.CreateAccount{RequestID: "opening-demo", Name: "demo", Currency: "USDT", InitialBalance: "10000.125"}
	account, err := store.CreatePaperAccount(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if account.ID == "" || account.Balance.String() != "10000.125" {
		t.Fatalf("account: %+v", account)
	}
	retry, err := store.CreatePaperAccount(ctx, request)
	if err != nil || retry.ID != account.ID {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	store.Close()
	store, err = postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accounts, err := store.PaperAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts: %+v %v", accounts, err)
	}
	saved, err := store.PaperAccount(ctx, account.ID)
	if err != nil || saved.Balance.String() != "10000.125" {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	ledger, err := store.PaperLedger(ctx, account.ID)
	if err != nil || len(ledger) != 1 || ledger[0].Amount.String() != "10000.125" || ledger[0].Kind != "opening" {
		t.Fatalf("ledger: %+v %v", ledger, err)
	}
}

func TestPaperAccountRejectsChangedRequestsAndInvalidInputs(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	request := paper.CreateAccount{RequestID: "request", Name: "demo", Currency: "USDT", InitialBalance: "1"}
	if _, err := store.CreatePaperAccount(ctx, request); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.InitialBalance = "2"
	if _, err := store.CreatePaperAccount(ctx, changed); err == nil {
		t.Fatal("changed request accepted")
	}
	for _, balance := range []string{"0", "-1", "NaN", "Infinity", "1e3", "1.2.3", "0.0000000000000000001", "1000000000000000001"} {
		invalid := request
		invalid.RequestID = "invalid"
		invalid.InitialBalance = balance
		if _, err := store.CreatePaperAccount(ctx, invalid); err == nil {
			t.Fatalf("accepted balance %q", balance)
		}
	}
	for _, change := range []func(*paper.CreateAccount){
		func(r *paper.CreateAccount) { r.Name = " " },
		func(r *paper.CreateAccount) { r.Name = "a\n" },
		func(r *paper.CreateAccount) { r.Currency = "USD" },
		func(r *paper.CreateAccount) { r.RequestID = "" },
	} {
		invalid := request
		change(&invalid)
		if _, err := store.CreatePaperAccount(ctx, invalid); err == nil {
			t.Fatalf("invalid account accepted: %+v", invalid)
		}
	}
	accounts, err := store.PaperAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("invalid requests changed accounts: %+v %v", accounts, err)
	}
}

func TestPaperAccountRollbackAndLedgerImmutability(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	request := paper.CreateAccount{RequestID: "rollback", Name: "rollback", Currency: "USDT", InitialBalance: "12.5"}
	for _, table := range []string{"paper_ledger_entries", "journal_outbox"} {
		_, err := conn.Exec(ctx, `CREATE FUNCTION billionaire.fail_account_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END; $$; CREATE TRIGGER fail_write BEFORE INSERT ON billionaire.`+table+` FOR EACH ROW EXECUTE FUNCTION billionaire.fail_account_write()`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreatePaperAccount(ctx, request); err == nil {
			t.Fatalf("%s failure did not abort account creation", table)
		}
		accounts, err := store.PaperAccounts(ctx)
		if err != nil || len(accounts) != 0 {
			t.Fatalf("rollback left accounts: %+v %v", accounts, err)
		}
		if _, err := conn.Exec(ctx, `DROP TRIGGER fail_write ON billionaire.`+table+`; DROP FUNCTION billionaire.fail_account_write()`); err != nil {
			t.Fatal(err)
		}
	}
	a, err := store.CreatePaperAccount(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE billionaire.paper_ledger_entries SET amount=0`,
		`DELETE FROM billionaire.paper_ledger_entries`,
		`TRUNCATE billionaire.paper_ledger_entries`,
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Fatalf("mutable ledger: %s", sql)
		}
	}
	entries, err := store.PaperLedger(ctx, a.ID)
	if err != nil || len(entries) != 1 || entries[0].Amount.String() != "12.5" {
		t.Fatalf("ledger: %+v %v", entries, err)
	}
}

func TestPaperOwnershipRejectsCompetitorAndReleasesAfterProcessDeath(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestPaperOwnerChild$")
	child.Env = append(os.Environ(), "BILLIONAIRE_OWNER_CHILD="+dsn)
	pipe, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(pipe).ReadString('\n'); ready <- line }()
	select {
	case line := <-ready:
		if line != "owned\n" {
			t.Fatalf("child: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child never acquired ownership")
	}
	if _, err := store.AcquirePaperOwner(ctx); !errors.Is(err, postgres.ErrPaperOwned) {
		t.Fatalf("competing owner: %v", err)
	}
	if _, err := store.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "blocked", Name: "blocked", Currency: "USDT", InitialBalance: "1"}); !errors.Is(err, postgres.ErrPaperOwned) {
		t.Fatalf("mutation bypassed ownership: %v", err)
	}
	if _, err := store.PaperAccounts(ctx); err != nil {
		t.Fatalf("read blocked: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	deadline := time.Now().Add(5 * time.Second)
	var owner *postgres.PaperOwner
	for {
		owner, err = store.AcquirePaperOwner(ctx)
		if err == nil {
			break
		}
		if !errors.Is(err, postgres.ErrPaperOwned) || time.Now().After(deadline) {
			t.Fatalf("ownership not released: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	owner.Close()
	owner.Close()
	if _, err := owner.CreateAccount(ctx, paper.CreateAccount{RequestID: "closed", Name: "closed", Currency: "USDT", InitialBalance: "1"}); err == nil {
		t.Fatal("closed owner wrote")
	}
	if _, err := store.CreatePaperAccount(ctx, paper.CreateAccount{RequestID: "recovered", Name: "recovered", Currency: "USDT", InitialBalance: "1"}); err != nil {
		t.Fatal(err)
	}
}

// A separate test process holds the actual session lock until forcibly killed.
func TestPaperOwnerChild(t *testing.T) {
	dsn := os.Getenv("BILLIONAIRE_OWNER_CHILD")
	if dsn == "" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	fmt.Println("owned")
	select {}
}

func TestPaperOwnerLosingSessionCannotWriteThroughNewConnection(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	// Snapshot this isolated database's existing sessions before acquiring the
	// owner. Terminate the new session without depending on its lock key.
	var existing []int32
	if err := admin.QueryRow(ctx, `SELECT array_agg(pid) FROM pg_stat_activity WHERE datname=current_database()`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquirePaperOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	rows, err := admin.Query(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=current_database() AND pid <> ALL($1::int[])`, existing)
	if err != nil {
		t.Fatal(err)
	}
	terminated, err := pgx.CollectRows(rows, pgx.RowTo[bool])
	if err != nil || len(terminated) != 1 || !terminated[0] {
		t.Fatalf("terminate owner session: %v %v", terminated, err)
	}
	request := paper.CreateAccount{RequestID: "lost", Name: "lost", Currency: "USDT", InitialBalance: "1"}
	if _, err := owner.CreateAccount(ctx, request); err == nil {
		t.Fatal("lost owner wrote through a replacement connection")
	}
	owner.Close()
	if _, err := store.CreatePaperAccount(ctx, request); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.CreatePaperAccount(canceled, paper.CreateAccount{RequestID: "cancel", Name: "cancel", Currency: "USDT", InitialBalance: "1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	accounts, err := store.PaperAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("canceled request created account: %+v %v", accounts, err)
	}
}
