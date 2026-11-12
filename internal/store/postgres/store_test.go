package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/saintmili/billionaire/internal/store/postgres"
	"github.com/saintmili/billionaire/internal/testdb"
)

func TestMigrationsRoundTripAndRepeatSafely(t *testing.T) {
	dsn := testdb.New(t)
	ctx := context.Background()
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.Status(ctx)
	if err != nil || state.Initialized || state.Version != 0 {
		t.Fatalf("fresh state: %+v %v", state, err)
	}
	for range 2 {
		if err := store.Migrate(ctx, postgres.Up); err != nil {
			t.Fatal(err)
		}
	}
	state, err = store.Status(ctx)
	if err != nil || !state.Initialized || state.Version != postgres.LatestVersion {
		t.Fatalf("initialized state: %+v %v", state, err)
	}
	store.Close()
	store, err = postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err = store.Status(ctx)
	if err != nil || !state.Initialized {
		t.Fatalf("state lost after reopen: %+v %v", state, err)
	}
	for range postgres.LatestVersion + 1 {
		if err := store.Migrate(ctx, postgres.Down); err != nil {
			t.Fatal(err)
		}
	}
	state, err = store.Status(ctx)
	if err != nil || state.Initialized || state.Version != 0 {
		t.Fatalf("rolled-back state: %+v %v", state, err)
	}
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
}

func TestFailedMigrationLeavesDatabaseUninitialized(t *testing.T) {
	dsn := testdb.New(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("cannot inject database fault")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA millionaire; CREATE TABLE millionaire.app_metadata(id integer)"); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx, postgres.Up); !errors.Is(err, postgres.ErrMigration) {
		t.Fatalf("expected migration failure, got %v", err)
	}
	state, err := store.Status(ctx)
	if err != nil || state.Initialized || state.Version != 0 {
		t.Fatalf("failed migration advanced state: %+v %v", state, err)
	}
	if _, err := conn.Exec(ctx, "DROP TABLE millionaire.app_metadata"); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, postgres.Up); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentMigrationAndCancellation(t *testing.T) {
	dsn := testdb.New(t)
	ctx := context.Background()
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var group sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		group.Go(func() { results <- store.Migrate(ctx, postgres.Up) })
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.Status(ctx)
	if err != nil || !state.Initialized {
		t.Fatalf("concurrent state: %+v %v", state, err)
	}
	lock, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close(ctx)
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_lock(7341928601)"); err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := store.Migrate(waiting, postgres.Down); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("migration ignored cancellation: %v", err)
	}
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_unlock(7341928601)"); err != nil {
		t.Fatal(err)
	}
	state, err = store.Status(ctx)
	if err != nil || !state.Initialized {
		t.Fatalf("cancellation changed state: %+v %v", state, err)
	}
}

func TestIncompatibleSchemaIsRejected(t *testing.T) {
	dsn := testdb.New(t)
	ctx := context.Background()
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
	if _, err := conn.Exec(ctx, "UPDATE millionaire.schema_migrations SET checksum='changed'"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Status(ctx); !errors.Is(err, postgres.ErrSchema) {
		t.Fatalf("corrupt status accepted: %v", err)
	}
	if err := store.Migrate(ctx, postgres.Down); !errors.Is(err, postgres.ErrSchema) {
		t.Fatalf("corrupt migration accepted: %v", err)
	}
}

func TestOpenRejectsBadDSNAndHonorsCancellation(t *testing.T) {
	if _, err := postgres.Open(context.Background(), "postgres://sensitive-marker:%invalid@host/db"); !errors.Is(err, postgres.ErrUnavailable) {
		t.Fatalf("unsafe DSN error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := postgres.Open(ctx, "postgres://127.0.0.1:1/db?sslmode=disable"); !errors.Is(err, context.Canceled) {
		t.Fatalf("connection ignored cancellation: %v", err)
	}
}
