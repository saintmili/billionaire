// Package postgres owns application storage. StopLore uses a separate schema/pool.
package postgres

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUnavailable = errors.New("database unavailable; check the configured environment variable and PostgreSQL service")
	ErrSchema      = errors.New("database schema is incompatible or corrupted")
	ErrMigration   = errors.New("database migration failed; no changes were committed")
)

type Direction string

const (
	Up            Direction = "up"
	Down          Direction = "down"
	LatestVersion           = 8
)

//go:embed migrations/0001_bootstrap.up.sql
var bootstrapUp string

//go:embed migrations/0001_bootstrap.down.sql
var bootstrapDown string

type migration struct {
	version  int
	up, down string
}

//go:embed migrations/0002_market_data.up.sql
var marketUp string

//go:embed migrations/0002_market_data.down.sql
var marketDown string

//go:embed migrations/0003_strategies.up.sql
var strategiesUp string

//go:embed migrations/0003_strategies.down.sql
var strategiesDown string

//go:embed migrations/0004_paper_accounts.up.sql
var accountsUp string

//go:embed migrations/0004_paper_accounts.down.sql
var accountsDown string

//go:embed migrations/0005_spot_execution.up.sql
var spotUp string

//go:embed migrations/0005_spot_execution.down.sql
var spotDown string

//go:embed migrations/0006_risk.up.sql
var riskUp string

//go:embed migrations/0006_risk.down.sql
var riskDown string

//go:embed migrations/0007_protection.up.sql
var protectionUp string

//go:embed migrations/0007_protection.down.sql
var protectionDown string

//go:embed migrations/0008_signal_decisions.up.sql
var decisionsUp string

//go:embed migrations/0008_signal_decisions.down.sql
var decisionsDown string

var migrations = []migration{{1, bootstrapUp, bootstrapDown}, {2, marketUp, marketDown}, {3, strategiesUp, strategiesDown}, {4, accountsUp, accountsDown}, {5, spotUp, spotDown}, {6, riskUp, riskDown}, {7, protectionUp, protectionDown}, {8, decisionsUp, decisionsDown}}

func (m migration) checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(m.up+"\n--DOWN--\n"+m.down)))
}

type Store struct{ pool *pgxpool.Pool }
type Status struct {
	Initialized     bool `json:"initialized"`
	Version         int  `json:"schema_version"`
	RequiredVersion int  `json:"required_schema_version"`
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, ErrUnavailable
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, ErrUnavailable
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["application_name"] = "millionaire"
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, safeError(ctx, ErrUnavailable)
	}
	return &Store{pool: pool}, nil
}
func (s *Store) Close() { s.pool.Close() }
func safeError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}

// Status is read-only and never initializes or migrates a database.
func (s *Store) Status(ctx context.Context) (Status, error) {
	state := Status{RequiredVersion: LatestVersion}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return state, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('millionaire.schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return state, safeError(ctx, ErrUnavailable)
	}
	if !exists {
		return state, nil
	}
	version, err := readVersion(ctx, tx)
	if err != nil {
		return state, err
	}
	state.Version = version
	if version == LatestVersion {
		var mode string
		if err := tx.QueryRow(ctx, "SELECT mode FROM millionaire.app_metadata WHERE id=TRUE").Scan(&mode); err != nil || mode != "paper" {
			return state, safeError(ctx, ErrSchema)
		}
		state.Initialized = true
	}
	return state, nil
}

func readVersion(ctx context.Context, tx pgx.Tx) (int, error) {
	rows, err := tx.Query(ctx, "SELECT version,checksum FROM millionaire.schema_migrations ORDER BY version")
	if err != nil {
		return 0, safeError(ctx, ErrSchema)
	}
	defer rows.Close()
	version := 0
	for rows.Next() {
		var n int
		var checksum string
		if err := rows.Scan(&n, &checksum); err != nil {
			return 0, safeError(ctx, ErrSchema)
		}
		if n != version+1 || n > len(migrations) || checksum != migrations[n-1].checksum() {
			return 0, ErrSchema
		}
		version = n
	}
	if rows.Err() != nil {
		return 0, safeError(ctx, ErrSchema)
	}
	return version, nil
}

// Migrate applies all pending up migrations or rolls back the last version.
// Each invocation is atomic, serialized by a transaction-scoped advisory lock.
// Down at version zero and Up at the latest version are safe no-ops.
func (s *Store) Migrate(ctx context.Context, direction Direction) error {
	if direction != Up && direction != Down {
		return errors.New("migration direction must be up or down")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7341928601)"); err != nil {
		return safeError(ctx, ErrMigration)
	}
	if _, err := tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS millionaire;
 CREATE TABLE IF NOT EXISTS millionaire.schema_migrations (
 version INTEGER PRIMARY KEY CHECK(version>0), checksum TEXT NOT NULL
 )`); err != nil {
		return safeError(ctx, ErrMigration)
	}
	version, err := readVersion(ctx, tx)
	if err != nil {
		return err
	}
	if direction == Up {
		for _, m := range migrations[version:] {
			if _, err := tx.Exec(ctx, m.up); err != nil {
				return safeError(ctx, ErrMigration)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO millionaire.schema_migrations(version,checksum) VALUES($1,$2)", m.version, m.checksum()); err != nil {
				return safeError(ctx, ErrMigration)
			}
		}
	} else if version > 0 {
		m := migrations[version-1]
		if _, err := tx.Exec(ctx, m.down); err != nil {
			return safeError(ctx, ErrMigration)
		}
		if _, err := tx.Exec(ctx, "DELETE FROM millionaire.schema_migrations WHERE version=$1", version); err != nil {
			return safeError(ctx, ErrMigration)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return safeError(ctx, errors.New("migration commit outcome is unknown; check status before retrying"))
	}
	return nil
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
