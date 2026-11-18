package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/saintmili/billionaire/internal/domain"
	"github.com/saintmili/billionaire/internal/paper"
)

var ErrPaperOwned = errors.New("offline paper execution is already owned by another process")

// PaperOwner pins ownership and all mutations to one session. Losing the session
// loses both the lock and the ability to write; no pool reconnect can bypass it.
type PaperOwner struct {
	namespace string
	mu        sync.Mutex
	conn      *scopedConn
}

func (s *Store) AcquirePaperOwner(ctx context.Context) (*PaperOwner, error) {
	raw, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	conn := &scopedConn{Conn: raw, schema: s.pool.schema}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(7341928602)").Scan(&acquired); err != nil {
		closePaperConnection(conn)
		return nil, safeError(ctx, ErrUnavailable)
	}
	if !acquired {
		closePaperConnection(conn)
		return nil, ErrPaperOwned
	}
	return &PaperOwner{conn: conn, namespace: s.pool.schema}, nil
}

func closePaperConnection(conn *scopedConn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}

func (o *PaperOwner) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn != nil {
		closePaperConnection(o.conn)
		o.conn = nil
	}
}

func (s *Store) CreatePaperAccount(ctx context.Context, request paper.CreateAccount) (paper.Account, error) {
	if _, err := request.Validate(); err != nil {
		return paper.Account{}, err
	}
	owner, err := s.AcquirePaperOwner(ctx)
	if err != nil {
		return paper.Account{}, err
	}
	defer owner.Close()
	return owner.CreateAccount(ctx, request)
}

func (o *PaperOwner) CreateAccount(ctx context.Context, request paper.CreateAccount) (paper.Account, error) {
	balance, err := request.Validate()
	if err != nil {
		return paper.Account{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.conn == nil {
		return paper.Account{}, errors.New("paper owner is closed")
	}
	tx, err := o.conn.Begin(ctx)
	if err != nil {
		return paper.Account{}, safeError(ctx, ErrUnavailable)
	}
	defer rollback(tx)
	account, err := scanPaperAccount(tx.QueryRow(ctx, `SELECT id,name,currency,balance::text,created_at FROM billionaire.paper_accounts WHERE request_id=$1`, request.RequestID))
	if err == nil {
		var initial string
		if err := tx.QueryRow(ctx, "SELECT initial_balance::text FROM billionaire.paper_accounts WHERE id=$1", account.ID).Scan(&initial); err != nil {
			return paper.Account{}, safeError(ctx, ErrUnavailable)
		}
		original, err := domain.ParseDecimal(initial)
		if err != nil {
			return paper.Account{}, ErrSchema
		}
		if account.Name != request.Name || account.Currency != request.Currency || !original.Equal(balance) {
			return paper.Account{}, errors.New("request ID already used with different account inputs")
		}
		return account, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return paper.Account{}, safeError(ctx, ErrUnavailable)
	}
	id := fmt.Sprintf("account_%x", sha256.Sum256([]byte(request.RequestID)))
	account, err = scanPaperAccount(tx.QueryRow(ctx, `INSERT INTO billionaire.paper_accounts(id,request_id,name,currency,initial_balance,balance) VALUES($1,$2,$3,$4,$5,$5) RETURNING id,name,currency,balance::text,created_at`, id, request.RequestID, request.Name, request.Currency, balance.String()))
	if err != nil {
		return paper.Account{}, safeError(ctx, ErrUnavailable)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billionaire.paper_ledger_entries(id,account_id,kind,amount) VALUES($1,$2,'opening',$3)`, id+":opening", id, balance.String()); err != nil {
		return paper.Account{}, safeError(ctx, ErrUnavailable)
	}
	payload, err := json.Marshal(account)
	if err != nil {
		return paper.Account{}, errors.New("cannot encode account event")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO billionaire.journal_outbox(event_id,kind,payload) VALUES($1,'account_opened',$2)`, id+":opening", payload); err != nil {
		return paper.Account{}, safeError(ctx, ErrUnavailable)
	}
	if err := tx.Commit(ctx); err != nil {
		return paper.Account{}, safeError(ctx, errors.New("account commit outcome unknown; retry with the same request ID"))
	}
	return account, nil
}

func scanPaperAccount(row pgx.Row) (paper.Account, error) {
	var a paper.Account
	var balance string
	if err := row.Scan(&a.ID, &a.Name, &a.Currency, &balance, &a.CreatedAt); err != nil {
		return a, err
	}
	var err error
	a.Balance, err = domain.ParseDecimal(balance)
	if err != nil {
		return a, ErrSchema
	}
	return a, nil
}

func (s *Store) PaperAccount(ctx context.Context, id string) (paper.Account, error) {
	a, err := scanPaperAccount(s.pool.QueryRow(ctx, `SELECT id,name,currency,balance::text,created_at FROM billionaire.paper_accounts WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return a, errors.New("paper account not found")
	}
	if err != nil {
		return a, safeError(ctx, ErrUnavailable)
	}
	return a, nil
}

func (s *Store) PaperAccounts(ctx context.Context) ([]paper.Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,name,currency,balance::text,created_at FROM billionaire.paper_accounts ORDER BY created_at,id`)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rows.Close()
	accounts := []paper.Account{}
	for rows.Next() {
		a, err := scanPaperAccount(rows)
		if err != nil {
			return nil, safeError(ctx, ErrSchema)
		}
		accounts = append(accounts, a)
	}
	if rows.Err() != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return accounts, nil
}

func (s *Store) PaperLedger(ctx context.Context, id string) ([]paper.LedgerEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,account_id,kind,amount::text,created_at FROM billionaire.paper_ledger_entries WHERE account_id=$1 ORDER BY created_at,id`, id)
	if err != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	defer rows.Close()
	entries := []paper.LedgerEntry{}
	for rows.Next() {
		var e paper.LedgerEntry
		var amount string
		if err := rows.Scan(&e.ID, &e.AccountID, &e.Kind, &amount, &e.CreatedAt); err != nil {
			return nil, safeError(ctx, ErrSchema)
		}
		e.Amount, err = domain.ParseDecimal(amount)
		if err != nil {
			return nil, ErrSchema
		}
		entries = append(entries, e)
	}
	if rows.Err() != nil {
		return nil, safeError(ctx, ErrUnavailable)
	}
	return entries, nil
}
