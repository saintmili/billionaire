package app_test

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/app"
	"github.com/saintmili/billionaire/internal/paper"
	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
	"reflect"
	"testing"
	"time"
)

func TestControlOriginalOutcomeSurvivesRestartAndRejectsConflict(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	service := app.NewControl(owner, nil, 0)
	payload, _ := json.Marshal(paper.CreateAccount{RequestID: "create-one", Name: "Original", Currency: "USDT", InitialBalance: "1000"})
	command := app.Command{ID: "control-one", Kind: "account.create", Payload: payload}
	first, err := service.Execute(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	owner.Close()
	owner, err = store.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	service = app.NewControl(owner, nil, 0)
	recovered, err := service.Outcome(ctx, command.ID)
	if err != nil || !sameJSON(first.Result, recovered.Result) {
		t.Fatalf("recovered %s %v", recovered.Result, err)
	}
	retry, err := service.Execute(ctx, command)
	if err != nil || !sameJSON(first.Result, retry.Result) {
		t.Fatalf("retry %s %v", retry.Result, err)
	}
	command.Kind = "kill-switch"
	command.Payload = json.RawMessage(`{"enabled":true}`)
	if _, err = service.Execute(ctx, command); err == nil {
		t.Fatal("conflicting identity accepted")
	}
	accounts, err := store.PaperAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%v err=%v", accounts, err)
	}
}

func sameJSON(a, b []byte) bool {
	var x, y any
	json.Unmarshal(a, &x)
	json.Unmarshal(b, &y)
	return reflect.DeepEqual(x, y)
}

func TestControlRejectionsRemainOriginalAfterLaterStateChanges(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	service := app.NewControl(owner, nil, time.Minute)
	cmd := app.Command{ID: "missing-instance", Kind: "strategy.enabled", Payload: json.RawMessage(`{"id":"missing","enabled":true}`)}
	result, err := service.Execute(ctx, cmd)
	if err != nil || result.Status != "rejected" {
		t.Fatalf("rejection=%+v %v", result, err)
	}
	recovered, err := service.Outcome(ctx, cmd.ID)
	if err != nil || recovered.Status != "rejected" || recovered.Reason == "" {
		t.Fatalf("recovery=%+v %v", recovered, err)
	}
}

func TestControlReceiptFailureRollsBackFinancialMutation(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	// Inject a database failure exactly after the financial subtransaction succeeds.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `CREATE FUNCTION billionaire.fail_control_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected receipt failure'; END; $$; CREATE TRIGGER fail_control_receipt BEFORE INSERT ON billionaire.control_outcomes FOR EACH ROW EXECUTE FUNCTION billionaire.fail_control_receipt()`)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	service := app.NewControl(owner, nil, time.Minute)
	raw, _ := json.Marshal(paper.CreateAccount{RequestID: "atomic-account", Name: "Atomic", Currency: "USDT", InitialBalance: "1000"})
	cmd := app.Command{ID: "atomic-control", Kind: "account.create", Payload: raw}
	if _, err = service.Execute(ctx, cmd); err == nil {
		t.Fatal("receipt failure accepted")
	}
	accounts, err := store.PaperAccounts(ctx)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("orphan financial mutation: %v %v", accounts, err)
	}
	if _, err = conn.Exec(ctx, "DROP TRIGGER fail_control_receipt ON billionaire.control_outcomes"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Execute(ctx, cmd); err != nil {
		t.Fatal(err)
	}
	accounts, err = store.PaperAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("retry %v %v", accounts, err)
	}
}

func TestControlCannotReconnectAfterOwningSessionIsKilled(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
	owner, err := store.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	health := app.NewHealth(store, owner, nil, time.Minute)
	service := app.NewControl(owner, nil, time.Minute, health)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND classid=(7341928602::bigint>>32)::oid AND objid=(7341928602::bigint&4294967295)::oid AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, healthErr := service.Health(ctx)
	if healthErr == nil || snapshot.Ownership != "lost" || snapshot.Database != "ready" || snapshot.EntriesReady {
		t.Fatalf("health did not distinguish ownership loss from DB availability: %+v %v", snapshot, healthErr)
	}
	payload, _ := json.Marshal(paper.CreateAccount{RequestID: "killed-account", Name: "Killed", Currency: "USDT", InitialBalance: "1000"})
	cmd := app.Command{ID: "killed-control", Kind: "account.create", Payload: payload}
	if _, err = service.Execute(ctx, cmd); err == nil {
		t.Fatal("killed owner reconnected")
	}
	accounts, err := store.PaperAccounts(ctx)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("dead owner mutated %v %v", accounts, err)
	}
	next, err := store.AcquireEngineOwner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if _, err = app.NewControl(next, nil, time.Minute).Execute(ctx, cmd); err != nil {
		t.Fatal(err)
	}
}
